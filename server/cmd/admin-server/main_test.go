package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type cleanupLifecycleStub struct {
	called    chan struct{}
	cancelled chan struct{}
}

func (s *cleanupLifecycleStub) CleanupExpiredSessions(ctx context.Context, _ int) (int64, error) {
	close(s.called)
	<-ctx.Done()
	close(s.cancelled)
	return 0, ctx.Err()
}

func TestRunStopsBeforeDatabaseForMissingTrustedSchema(t *testing.T) {
	values := map[string]string{
		"TBCG_ADMIN_DATABASE_URL": "postgresql://user:pass@127.0.0.1:55432/test_db?sslmode=verify-full",
		"TBCG_ADMIN_TLS_CERT":     "/tmp/not-loaded-before-schema.crt",
		"TBCG_ADMIN_TLS_KEY":      "/tmp/not-loaded-before-schema.key",
		"TBCG_PROJECT_ROOT":       t.TempDir(),
	}
	err := run(context.Background(), func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err == nil || !strings.Contains(err.Error(), "initialize trusted item schemas") {
		t.Fatalf("run with missing trusted schema error = %v", err)
	}
}

func TestRunRejectsPlaintextDatabaseAfterPreparingTrustedRoots(t *testing.T) {
	projectRoot := t.TempDir()
	for _, name := range []string{"item.schema.json", "item-public.schema.json"} {
		data, err := os.ReadFile(filepath.Join("../../../contracts/content", name))
		if err != nil {
			t.Fatalf("read trusted test schema %s: %v", name, err)
		}
		path := filepath.Join(projectRoot, "contracts", "content", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create temporary trusted schema root: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("copy trusted test schema %s: %v", name, err)
		}
	}
	assetRoot := filepath.Join(projectRoot, "games", "demo", "assets")
	if err := os.MkdirAll(assetRoot, 0o700); err != nil {
		t.Fatalf("create temporary asset root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assetRoot, "manifest.json"), []byte(`{"schema_version":1,"assets":[]}`), 0o600); err != nil {
		t.Fatalf("write temporary asset manifest: %v", err)
	}
	values := map[string]string{
		"TBCG_ADMIN_DATABASE_URL": "postgresql://user:pass@127.0.0.1:55432/test_db?sslmode=disable",
		"TBCG_ADMIN_TLS_CERT":     "/tmp/not-loaded-before-db-policy.crt",
		"TBCG_ADMIN_TLS_KEY":      "/tmp/not-loaded-before-db-policy.key",
		"TBCG_PROJECT_ROOT":       projectRoot,
	}
	err := run(context.Background(), func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err == nil || !strings.Contains(err.Error(), "open restricted admin database repository") {
		t.Fatalf("run with plaintext PostgreSQL error = %v", err)
	}
}

func TestStartSessionCleanupJoinsAlreadyCancelledContext(t *testing.T) {
	stub := &cleanupLifecycleStub{called: make(chan struct{}), cancelled: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stop := startSessionCleanup(ctx, stub)
	stop()
	select {
	case <-stub.called:
		t.Fatal("cancelled cleanup worker unexpectedly ran a database operation")
	default:
	}
}

func TestSessionCleanupLoopUsesBoundedContextAndStopsOnCancellation(t *testing.T) {
	stub := &cleanupLifecycleStub{called: make(chan struct{}), cancelled: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runSessionCleanup(ctx, stub, time.Millisecond)
		close(done)
	}()
	select {
	case <-stub.called:
	case <-time.After(time.Second):
		t.Fatal("session cleanup loop did not invoke the repository")
	}
	cancel()
	select {
	case <-stub.cancelled:
	case <-time.After(time.Second):
		t.Fatal("session cleanup did not receive cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session cleanup goroutine did not stop")
	}
}
