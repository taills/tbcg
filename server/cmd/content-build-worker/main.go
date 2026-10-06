// Command content-build-worker runs trusted single-item builds from the durable admin job queue.
// It uses a dedicated restricted database role and a private artifact directory; it is a process
// boundary for trusted code, not a sandbox for untrusted inputs.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	contentbuild "github.com/taills/tbcg/server/internal/content/build"
	"github.com/taills/tbcg/server/internal/content/buildworker"
	"github.com/taills/tbcg/server/internal/content/mapdata"
	"github.com/taills/tbcg/server/internal/content/schema"
	"github.com/taills/tbcg/server/internal/persistence/postgres"
)

const (
	defaultPollInterval     = 5 * time.Second
	minPollInterval         = 100 * time.Millisecond
	maxPollInterval         = time.Minute
	defaultLeaseSeconds     = 30
	maxConsecutiveFailures  = 5
	workerIdentityEnvKey    = "TBCG_BUILD_WORKER_ID"
	workerDatabaseEnvKey    = "TBCG_BUILD_WORKER_DATABASE_URL"
	artifactDirectoryEnvKey = "TBCG_BUILD_ARTIFACT_DIR"
	projectRootEnvKey       = "TBCG_PROJECT_ROOT"
	pollIntervalEnvKey      = "TBCG_BUILD_WORKER_POLL_INTERVAL"
	leaseSecondsEnvKey      = "TBCG_BUILD_WORKER_LEASE_SECONDS"
)

var workerIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type config struct {
	DatabaseURL  string
	WorkerID     string
	ArtifactDir  string
	ProjectRoot  string
	PollInterval time.Duration
	LeaseSeconds int
}

type runner interface {
	RunOnce(context.Context) (bool, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.LookupEnv); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func loadConfig(lookupEnv func(string) (string, bool)) (config, error) {
	value := func(key string) string {
		v, _ := lookupEnv(key)
		return v
	}
	cfg := config{
		DatabaseURL:  value(workerDatabaseEnvKey),
		WorkerID:     value(workerIdentityEnvKey),
		ArtifactDir:  value(artifactDirectoryEnvKey),
		ProjectRoot:  value(projectRootEnvKey),
		PollInterval: defaultPollInterval,
		LeaseSeconds: defaultLeaseSeconds,
	}
	if cfg.DatabaseURL == "" || cfg.ProjectRoot == "" {
		return config{}, fmt.Errorf("%s and %s are required", workerDatabaseEnvKey, projectRootEnvKey)
	}
	if !workerIDPattern.MatchString(cfg.WorkerID) {
		return config{}, fmt.Errorf("%s must be a stable lowercase UUID", workerIdentityEnvKey)
	}
	if !filepath.IsAbs(cfg.ArtifactDir) {
		return config{}, fmt.Errorf("%s must be an absolute private directory path", artifactDirectoryEnvKey)
	}
	if raw, ok := lookupEnv(pollIntervalEnvKey); ok {
		interval, err := time.ParseDuration(raw)
		if err != nil || interval < minPollInterval || interval > maxPollInterval {
			return config{}, fmt.Errorf("%s must be between %v and %v", pollIntervalEnvKey, minPollInterval, maxPollInterval)
		}
		cfg.PollInterval = interval
	}
	if raw, ok := lookupEnv(leaseSecondsEnvKey); ok {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 5 || seconds > 300 {
			return config{}, fmt.Errorf("%s must be an integer between 5 and 300", leaseSecondsEnvKey)
		}
		cfg.LeaseSeconds = seconds
	}
	return cfg, nil
}

func run(ctx context.Context, lookupEnv func(string) (string, bool)) (resultErr error) {
	cfg, err := loadConfig(lookupEnv)
	if err != nil {
		return fmt.Errorf("load content build worker configuration: %w", err)
	}
	projectRoot, err := filepath.Abs(cfg.ProjectRoot)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	schemaRoot, err := os.OpenRoot(filepath.Join(projectRoot, "contracts"))
	if err != nil {
		return fmt.Errorf("open trusted schema root: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, closeWith("trusted schema root", schemaRoot.Close)) }()
	validator, err := schema.New(schemaRoot.FS())
	if err != nil {
		return fmt.Errorf("compile trusted item schemas: %w", err)
	}
	store, err := contentbuild.OpenArtifactStore(cfg.ArtifactDir)
	if err != nil {
		return fmt.Errorf("open private artifact store: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, closeWith("private artifact store", store.Close)) }()
	repository, err := postgres.OpenBuildWorkerRepository(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open restricted build worker database: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, closeWith("build worker database", repository.Close)) }()
	worker, err := buildworker.New(cfg.WorkerID, cfg.LeaseSeconds, repository.WorkerQueue(), store, validator)
	if err != nil {
		return err
	}
	registry, err := schema.NewRegistry(schemaRoot.FS())
	if err != nil {
		return fmt.Errorf("compile trusted content schemas: %w", err)
	}
	geometry, err := mapdata.NewValidator(schemaRoot.FS())
	if err != nil {
		return fmt.Errorf("compile trusted map geometry schema: %w", err)
	}
	worker, err = worker.WithSnapshots(repository.SnapshotQueue(), registry, geometry)
	if err != nil {
		return err
	}
	log.Printf("content build worker started worker_id=%s schema_fingerprint=%s builder=%s", cfg.WorkerID, validator.Fingerprint(), contentbuild.BuilderVersion)
	return runLoop(ctx, worker, cfg.PollInterval, maxConsecutiveFailures)
}

// runLoop processes one job at a time. Lease loss is logged and skipped; repeated other failures
// stop the process so an external supervisor restarts it with fresh connections.
func runLoop(ctx context.Context, worker runner, pollInterval time.Duration, maxFailures int) error {
	consecutiveFailures := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		claimed, err := worker.RunOnce(ctx)
		switch {
		case err == nil:
			consecutiveFailures = 0
		case errors.Is(err, buildworker.ErrLeaseLost):
			consecutiveFailures = 0
			log.Printf("content build worker lost a claim lease; completion was not written")
		case errors.Is(err, context.Canceled):
			return err
		default:
			consecutiveFailures++
			log.Printf("content build worker iteration failed consecutive_failures=%d: %v", consecutiveFailures, err)
			if consecutiveFailures >= maxFailures {
				return fmt.Errorf("content build worker stopping after %d consecutive failures: %w", consecutiveFailures, err)
			}
		}
		if claimed && err == nil {
			continue
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func closeWith(name string, closeFn func() error) error {
	if err := closeFn(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	return nil
}
