package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	release "github.com/taills/tbcg/server/internal/content/release"
	"github.com/taills/tbcg/server/internal/persistence"
)

func TestConcurrentIdenticalReleasePublishesAreIdempotent(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	definition := releaseDefinition(gameID, realmID, "release-concurrent-retry", 2)
	definition.OwnerEpoch = 0
	publisher := release.NewPublisher(store.db, filepath.Join(t.TempDir(), "artifacts"))
	var entered atomic.Int32
	ready := make(chan struct{})
	build := func(ctx context.Context, _ release.Definition) (release.Artifacts, error) {
		// builder 在读取活动指针后执行；双方会合后才开始产物安装与注册。
		if entered.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready:
			return release.Artifacts{Public: []byte("concurrent-public"), Private: []byte("concurrent-private")}, nil
		case <-ctx.Done():
			return release.Artifacts{}, ctx.Err()
		}
	}
	validate := func(context.Context, release.Definition) error { return nil }

	start := make(chan struct{})
	type publishResult struct {
		published release.Published
		err       error
	}
	results := make(chan publishResult, 2)
	for range 2 {
		go func() {
			<-start
			published, err := publisher.Publish(ctx, definition, build, validate)
			results <- publishResult{published: published, err: err}
		}()
	}
	close(start)

	published := make([]release.Published, 0, 2)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent identical publish: %v", result.err)
		}
		published = append(published, result.published)
	}
	if published[0].ReleaseID != definition.ReleaseID || published[1].ReleaseID != definition.ReleaseID ||
		published[0].PreviousReleaseID != oldRelease || published[1].PreviousReleaseID != oldRelease ||
		published[0].PublicHash == "" || published[0].PublicHash != published[1].PublicHash ||
		published[0].PrivateHash == "" || published[0].PrivateHash != published[1].PrivateHash {
		t.Fatalf("concurrent publish results = %+v, want matching release ID and hashes", published)
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, definition.ReleaseID)
	assertReleaseStatus(t, ctx, store, gameID, definition.ReleaseID, "activated")
}

func TestReleaseRetryAfterBuilderAndValidatorFailures(t *testing.T) {
	for _, failure := range []string{"builder", "validator"} {
		t.Run(failure, func(t *testing.T) {
			ctx, store := openUnitOfWorkTestStore(t)
			gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
			definition := releaseDefinition(gameID, realmID, "release-retry-"+failure, 2)
			definition.OwnerEpoch = 0
			publisher := release.NewPublisher(store.db, filepath.Join(t.TempDir(), "artifacts"))
			var calls atomic.Int32
			build := func(context.Context, release.Definition) (release.Artifacts, error) {
				if failure == "builder" && calls.Add(1) == 1 {
					return release.Artifacts{}, errors.New("injected first build failure")
				}
				return release.Artifacts{Public: []byte("retry-public"), Private: []byte("retry-private")}, nil
			}
			validate := func(context.Context, release.Definition) error {
				if failure == "validator" && calls.Add(1) == 1 {
					return errors.New("injected first validation failure")
				}
				return nil
			}

			if _, err := publisher.Publish(ctx, definition, build, validate); err == nil {
				t.Fatal("first publish unexpectedly succeeded despite injected failure")
			}
			assertActiveRelease(t, ctx, store, gameID, realmID, oldRelease)
			if failure == "builder" {
				assertReleaseStatusMissing(t, ctx, store, gameID, definition.ReleaseID)
			} else {
				assertReleaseStatus(t, ctx, store, gameID, definition.ReleaseID, "built")
			}

			published, err := publisher.Publish(ctx, definition, build, validate)
			if err != nil {
				t.Fatalf("retry publish after %s failure: %v", failure, err)
			}
			if published.ReleaseID != definition.ReleaseID || published.PublicHash == "" || published.PrivateHash == "" {
				t.Fatalf("retry result = %+v, want release ID and artifact hashes", published)
			}
			assertActiveRelease(t, ctx, store, gameID, realmID, definition.ReleaseID)
			assertReleaseStatus(t, ctx, store, gameID, definition.ReleaseID, "activated")
		})
	}
}

func TestReleaseCommitAcknowledgmentLossIsUnknownAndSameContentRetryRecovers(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	definition := releaseDefinition(gameID, realmID, "release-commit-ack-lost", 2)
	definition.OwnerEpoch = 0
	var databaseName string
	if err := store.db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read isolated release database name: %v", err)
	}
	dsn, err := databaseURLForTestDatabase(os.Getenv("TBCG_TEST_DATABASE_URL"), databaseName)
	if err != nil {
		t.Fatalf("build isolated release database URL: %v", err)
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse isolated PostgreSQL test database URL: %v", err)
	}
	var loseAcknowledgment atomic.Bool
	loseAcknowledgment.Store(true)
	db := sql.OpenDB(&commitAcknowledgmentLossConnector{
		base: stdlib.GetDefaultDriver(), dsn: config.ConnString(), loseAcknowledgment: &loseAcknowledgment,
	})
	defer db.Close()

	artifactRoot := filepath.Join(t.TempDir(), "artifacts")
	publisherWithLostAck := release.NewPublisher(db, artifactRoot)
	build := func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("ack-public"), Private: []byte("ack-private")}, nil
	}
	validate := func(context.Context, release.Definition) error { return nil }
	_, err = publisherWithLostAck.Publish(ctx, definition, build, validate)
	if !errors.Is(err, release.ErrActivationOutcomeUnknown) {
		t.Fatalf("commit acknowledgment loss error = %v, want ErrActivationOutcomeUnknown", err)
	}
	if !errors.Is(err, persistence.ErrUnknown) {
		t.Fatalf("commit acknowledgment loss error = %v, want persistence.ErrUnknown", err)
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, definition.ReleaseID)
	assertReleaseStatus(t, ctx, store, gameID, definition.ReleaseID, "activated")
	if definition.ReleaseID == oldRelease {
		t.Fatal("test release ID unexpectedly matches the previous release")
	}

	publisher := release.NewPublisher(store.db, artifactRoot)
	if _, err := publisher.Publish(ctx, definition, build, validate); err != nil {
		t.Fatalf("retry same release after lost commit acknowledgment: %v", err)
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, definition.ReleaseID)
	assertReleaseStatus(t, ctx, store, gameID, definition.ReleaseID, "activated")
}
