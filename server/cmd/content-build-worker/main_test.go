package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/taills/tbcg/server/internal/content/buildworker"
)

func validEnvironment(t *testing.T) map[string]string {
	t.Helper()
	store := filepath.Join(t.TempDir(), "artifacts")
	if err := os.Mkdir(store, 0o700); err != nil {
		t.Fatalf("create artifact dir: %v", err)
	}
	return map[string]string{
		"TBCG_BUILD_WORKER_DATABASE_URL": "postgres://worker@db.example.test/tbcg?sslmode=verify-full",
		"TBCG_BUILD_WORKER_ID":           "00000000-0000-4000-8000-0000000000d1",
		"TBCG_BUILD_ARTIFACT_DIR":        store,
		"TBCG_PROJECT_ROOT":              "../../..",
	}
}

func lookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadConfigRequiresExplicitBoundedWorkerSettings(t *testing.T) {
	values := validEnvironment(t)
	config, err := loadConfig(lookup(values))
	if err != nil {
		t.Fatalf("load valid worker config: %v", err)
	}
	if config.PollInterval != defaultPollInterval || config.LeaseSeconds != defaultLeaseSeconds {
		t.Fatalf("defaults poll/lease = %v/%d", config.PollInterval, config.LeaseSeconds)
	}
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "missing database", key: "TBCG_BUILD_WORKER_DATABASE_URL", value: ""},
		{name: "non UUID worker", key: "TBCG_BUILD_WORKER_ID", value: "worker-1"},
		{name: "uppercase UUID", key: "TBCG_BUILD_WORKER_ID", value: "00000000-0000-4000-8000-0000000000D1"},
		{name: "relative artifact dir", key: "TBCG_BUILD_ARTIFACT_DIR", value: "artifacts"},
		{name: "fast poll", key: "TBCG_BUILD_WORKER_POLL_INTERVAL", value: "10ms"},
		{name: "slow poll", key: "TBCG_BUILD_WORKER_POLL_INTERVAL", value: "2h"},
		{name: "long lease", key: "TBCG_BUILD_WORKER_LEASE_SECONDS", value: "301"},
		{name: "bad lease", key: "TBCG_BUILD_WORKER_LEASE_SECONDS", value: "abc"},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := validEnvironment(t)
			changed[test.key] = test.value
			if _, err := loadConfig(lookup(changed)); err == nil {
				t.Fatalf("loadConfig accepted %s=%q", test.key, test.value)
			}
		})
	}
}

type scriptedRunner struct {
	results []runResult
	calls   int
}

type runResult struct {
	claimed bool
	err     error
}

func (r *scriptedRunner) RunOnce(context.Context) (bool, error) {
	if r.calls >= len(r.results) {
		return false, context.Canceled
	}
	result := r.results[r.calls]
	r.calls++
	return result.claimed, result.err
}

func TestRunLoopDrainsJobsAndStopsAfterRepeatedDatabaseFailures(t *testing.T) {
	databaseDown := errors.New("claim build job: connection refused")
	runner := &scriptedRunner{results: []runResult{
		{claimed: true},
		{claimed: true, err: buildworker.ErrLeaseLost},
		{claimed: false},
		{err: databaseDown},
		{err: databaseDown},
		{err: databaseDown},
	}}
	err := runLoop(context.Background(), runner, time.Millisecond, 3)
	if !errors.Is(err, databaseDown) || runner.calls != 6 {
		t.Fatalf("runLoop() = %v after %d calls, want database failure after 6", err, runner.calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	idle := &scriptedRunner{results: []runResult{{claimed: false}}}
	if err := runLoop(ctx, idle, time.Hour, 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("runLoop() with canceled context = %v", err)
	}
}

func TestRunFailsClosedBeforeTouchingTheDatabase(t *testing.T) {
	invalid := validEnvironment(t)
	delete(invalid, "TBCG_BUILD_WORKER_ID")
	if err := run(context.Background(), lookup(invalid)); err == nil {
		t.Fatal("run accepted a worker without a stable identity")
	}
	missingSchemas := validEnvironment(t)
	missingSchemas["TBCG_PROJECT_ROOT"] = t.TempDir()
	if err := run(context.Background(), lookup(missingSchemas)); err == nil {
		t.Fatal("run started without the trusted schema root")
	}
	openStore := validEnvironment(t)
	if err := os.Chmod(openStore["TBCG_BUILD_ARTIFACT_DIR"], 0o755); err != nil {
		t.Fatalf("widen artifact dir: %v", err)
	}
	if err := run(context.Background(), lookup(openStore)); err == nil {
		t.Fatal("run accepted a group/world readable artifact directory")
	}
}
