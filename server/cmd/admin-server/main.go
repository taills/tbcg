package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/taills/tbcg/server/internal/admin"
	"github.com/taills/tbcg/server/internal/content/mapdata"
	"github.com/taills/tbcg/server/internal/content/schema"
	"github.com/taills/tbcg/server/internal/persistence/postgres"
	battlepreview "github.com/taills/tbcg/server/modules/battle/preview"
)

var adminEnvironmentKeys = []string{
	"TBCG_ADMIN_DATABASE_URL",
	"TBCG_ADMIN_BIND",
	"TBCG_ADMIN_ORIGIN",
	"TBCG_ADMIN_TLS_CERT",
	"TBCG_ADMIN_TLS_KEY",
	"TBCG_ADMIN_SESSION_TTL",
	"TBCG_ADMIN_IDLE_TTL",
	"TBCG_ADMIN_MAX_BODY_BYTES",
	"TBCG_PROJECT_ROOT",
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.LookupEnv); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

const (
	sessionCleanupBatchSize  = 100
	sessionCleanupMaxBatches = 10
	sessionCleanupBudget     = 5 * time.Second
)

type sessionCleaner interface {
	CleanupExpiredSessions(context.Context, int) (int64, error)
}

func startSessionCleanup(parent context.Context, repository sessionCleaner) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSessionCleanup(ctx, repository, time.Minute)
	}()
	return func() {
		cancel()
		<-done
	}
}

func runSessionCleanup(ctx context.Context, repository sessionCleaner, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanupCtx, cancel := context.WithTimeout(ctx, sessionCleanupBudget)
			deleted, limitReached, err := cleanupExpiredSessionBatches(cleanupCtx, repository)
			cancel()
			if err != nil && ctx.Err() == nil {
				log.Printf("admin session cleanup failed deleted_count=%d: %v", deleted, err)
			} else if deleted > 0 {
				log.Printf("admin session cleanup deleted_count=%d batch_limit_reached=%t", deleted, limitReached)
			}
		}
	}
}

func cleanupExpiredSessionBatches(ctx context.Context, repository sessionCleaner) (int64, bool, error) {
	var deleted int64
	for range sessionCleanupMaxBatches {
		if err := ctx.Err(); err != nil {
			return deleted, false, fmt.Errorf("stop expired session cleanup: %w", err)
		}
		count, err := repository.CleanupExpiredSessions(ctx, sessionCleanupBatchSize)
		if err != nil {
			return deleted, false, fmt.Errorf("cleanup expired session batch: %w", err)
		}
		if count < 0 || count > sessionCleanupBatchSize {
			return deleted, false, errors.New("session cleanup returned an invalid bounded row count")
		}
		deleted += count
		if count < sessionCleanupBatchSize {
			return deleted, false, nil
		}
	}
	return deleted, true, nil
}

func run(ctx context.Context, lookupEnv func(string) (string, bool)) error {
	values := make(map[string]string, len(adminEnvironmentKeys))
	for _, key := range adminEnvironmentKeys {
		if value, ok := lookupEnv(key); ok {
			values[key] = value
		}
	}
	config, err := parseServerConfig(values)
	if err != nil {
		return fmt.Errorf("load admin server configuration: %w", err)
	}
	projectRoot, err := filepath.Abs(config.ProjectRoot)
	if err != nil {
		return fmt.Errorf("resolve admin project root: %w", err)
	}

	schemaRoot, err := os.OpenRoot(filepath.Join(projectRoot, "contracts"))
	if err != nil {
		return fmt.Errorf("initialize trusted item schemas: open trusted schema root: %w", err)
	}
	defer func() {
		if err := schemaRoot.Close(); err != nil {
			log.Printf("admin schema root close failed: %v", err)
		}
	}()
	registry, err := schema.NewRegistry(schemaRoot.FS())
	if err != nil {
		return fmt.Errorf("initialize trusted content schemas: %w", err)
	}
	geometry, err := mapdata.NewValidator(schemaRoot.FS())
	if err != nil {
		return fmt.Errorf("initialize trusted map geometry schema: %w", err)
	}
	assetRoot, err := os.OpenRoot(filepath.Join(projectRoot, "games"))
	if err != nil {
		return fmt.Errorf("open trusted Game assets root: %w", err)
	}
	defer func() {
		if err := assetRoot.Close(); err != nil {
			log.Printf("admin asset root close failed: %v", err)
		}
	}()
	assets := assetRoot.FS()
	repository, err := postgres.OpenAdminRepository(ctx, config.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open restricted admin database repository: %w", err)
	}
	defer func() {
		if err := repository.Close(); err != nil {
			log.Printf("admin database close failed: %v", err)
		}
	}()
	stopSessionCleanup := startSessionCleanup(ctx, repository)
	defer stopSessionCleanup()
	api := admin.NewContentHandler(repository, admin.Config{
		ExpectedOrigin: config.Origin,
		SessionTTL:     config.SessionTTL,
		IdleTTL:        config.IdleTTL,
		MaxBodyBytes:   config.MaxBodyBytes,
	}, registry, assets, admin.Extensions{Geometry: geometry, Preview: battlepreview.Runner{}})
	web, err := newWebHandler(api, projectRoot)
	if err != nil {
		return fmt.Errorf("initialize built admin application: %w", err)
	}
	defer func() {
		if err := web.Close(); err != nil {
			log.Printf("admin web root close failed: %v", err)
		}
	}()

	listener, err := net.Listen("tcp", config.BindAddress)
	if err != nil {
		return fmt.Errorf("listen for admin requests: %w", err)
	}
	server := &http.Server{
		Handler:           web,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	if config.TLSCert != "" {
		certificate, err := tls.LoadX509KeyPair(config.TLSCert, config.TLSKey)
		if err != nil {
			return fmt.Errorf("load admin TLS key pair: %w", err)
		}
		server.TLSConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{certificate},
		}
		listener = tls.NewListener(listener, server.TLSConfig)
	}

	serveError := make(chan error, 1)
	go func() {
		log.Printf("admin server listening on %s", listener.Addr())
		serveError <- server.Serve(listener)
	}()
	select {
	case err := <-serveError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve admin HTTP requests: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			shutdownErr := fmt.Errorf("gracefully stop admin server: %w", err)
			if closeErr := server.Close(); closeErr != nil {
				return errors.Join(shutdownErr, fmt.Errorf("force close admin server: %w", closeErr))
			}
			return shutdownErr
		}
		if err := <-serveError; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("admin server shutdown: %w", err)
		}
		return nil
	}
}
