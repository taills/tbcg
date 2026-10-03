package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/tbcg/server/internal/persistence"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestUnitOfWorkCommitsGameCreation(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	game := newTestGame(t)

	err := store.UnitOfWork().WithinTx(ctx, func(repositories persistence.TxRepositories) error {
		return repositories.Games().Create(ctx, game)
	})
	if err != nil {
		t.Fatalf("create game within transaction: %v", err)
	}

	var got persistence.Game
	err = store.UnitOfWork().WithinTx(ctx, func(repositories persistence.TxRepositories) error {
		var err error
		got, err = repositories.Games().ByID(ctx, game.ID)
		return err
	})
	if err != nil {
		t.Fatalf("read committed game: %v", err)
	}
	if got.ID != game.ID || got.DisplayName != game.DisplayName {
		t.Fatalf("read game = {ID: %q, DisplayName: %q}, want {ID: %q, DisplayName: %q}", got.ID, got.DisplayName, game.ID, game.DisplayName)
	}
	if got.CreatedAt.IsZero() {
		t.Error("committed game has zero CreatedAt")
	}
}

func TestUnitOfWorkRollsBackGameCreationOnCallbackError(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	game := newTestGame(t)
	callbackErr := errors.New("abort transaction")

	err := store.UnitOfWork().WithinTx(ctx, func(repositories persistence.TxRepositories) error {
		if err := repositories.Games().Create(ctx, game); err != nil {
			return err
		}
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("WithinTx error = %v, want wrapped callback error %v", err, callbackErr)
	}

	err = store.UnitOfWork().WithinTx(ctx, func(repositories persistence.TxRepositories) error {
		_, err := repositories.Games().ByID(ctx, game.ID)
		return err
	})
	if !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("ByID after rollback error = %v, want ErrNotFound", err)
	}
}

func TestGameRepositoryDuplicateIDReturnsConflictWithoutChangingOriginal(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	original := newTestGame(t)
	if err := store.UnitOfWork().WithinTx(ctx, func(repositories persistence.TxRepositories) error {
		return repositories.Games().Create(ctx, original)
	}); err != nil {
		t.Fatalf("create original game: %v", err)
	}

	duplicate := persistence.Game{
		ID:          original.ID,
		DisplayName: original.DisplayName + " updated",
	}
	err := store.UnitOfWork().WithinTx(ctx, func(repositories persistence.TxRepositories) error {
		return repositories.Games().Create(ctx, duplicate)
	})
	if !errors.Is(err, persistence.ErrConflict) {
		t.Fatalf("duplicate Create error = %v, want ErrConflict", err)
	}

	var got persistence.Game
	err = store.UnitOfWork().WithinTx(ctx, func(repositories persistence.TxRepositories) error {
		var err error
		got, err = repositories.Games().ByID(ctx, original.ID)
		return err
	})
	if err != nil {
		t.Fatalf("read original game after duplicate conflict: %v", err)
	}
	if got.ID != original.ID || got.DisplayName != original.DisplayName {
		t.Fatalf("game after duplicate conflict = {ID: %q, DisplayName: %q}, want original {ID: %q, DisplayName: %q}", got.ID, got.DisplayName, original.ID, original.DisplayName)
	}
}

func openUnitOfWorkTestStore(t *testing.T) (context.Context, *Store) {
	t.Helper()
	maintenanceURL := os.Getenv("TBCG_TEST_DATABASE_URL")
	if maintenanceURL == "" {
		if os.Getenv("TBCG_REQUIRE_TEST_DATABASE") == "1" {
			t.Fatal("CI requires TBCG_TEST_DATABASE_URL")
		}
		t.Skip("set TBCG_TEST_DATABASE_URL to a disposable PostgreSQL test database URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	ternPath, err := ternTestBinary()
	if err != nil {
		t.Fatalf("find Tern v2.4.3 CLI: %v", err)
	}
	versionOutput, err := exec.CommandContext(ctx, ternPath, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("run tern version: %v\n%s", err, versionOutput)
	}
	if !strings.Contains(string(versionOutput), "v2.4.3") {
		t.Fatalf("require tern v2.4.3, got %q", strings.TrimSpace(string(versionOutput)))
	}

	config := parseIsolatedTestDatabase(t, maintenanceURL)
	maintenanceDB := stdlib.OpenDB(*config)
	t.Cleanup(func() {
		if err := maintenanceDB.Close(); err != nil {
			t.Errorf("close PostgreSQL maintenance connection: %v", err)
		}
	})
	if err := maintenanceDB.PingContext(ctx); err != nil {
		t.Fatalf("connect to TBCG_TEST_DATABASE_URL maintenance database: %v", err)
	}
	assertPostgreSQL18(t, ctx, maintenanceDB)

	databaseName, err := randomTestDatabaseName()
	if err != nil {
		t.Fatalf("create isolated database name: %v", err)
	}
	if _, err := maintenanceDB.ExecContext(ctx, "CREATE DATABASE \""+databaseName+"\""); err != nil {
		t.Fatalf("create isolated UnitOfWork test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := maintenanceDB.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS \""+databaseName+"\""); err != nil {
			t.Errorf("drop isolated UnitOfWork test database %q: %v", databaseName, err)
		}
	})

	testURL, err := databaseURLForTestDatabase(maintenanceURL, databaseName)
	if err != nil {
		t.Fatalf("prepare isolated UnitOfWork test database URL: %v", err)
	}
	serverRoot := findServerRoot(t)
	migrationDir := filepath.Join(serverRoot, "migrations")
	env, err := ternEnvironment(config, databaseName, migrationDir)
	if err != nil {
		t.Fatalf("prepare tern environment: %v", err)
	}
	command := exec.CommandContext(ctx, ternPath, "migrate")
	command.Dir = serverRoot
	command.Env = env
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run tern migrate against isolated UnitOfWork test database: %v\n%s", err, output)
	}

	store, err := Open(ctx, testURL)
	if err != nil {
		t.Fatalf("open isolated PostgreSQL test store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close PostgreSQL store: %v", err)
		}
	})
	return ctx, store
}

func ternTestBinary() (string, error) {
	return exec.LookPath("tern")
}

func newTestGame(t *testing.T) persistence.Game {
	t.Helper()
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate random game ID: %v", err)
	}
	return persistence.Game{
		ID:          "test-" + hex.EncodeToString(suffix[:]),
		DisplayName: "Integration test game",
	}
}

func databaseURLForTestDatabase(databaseURL, databaseName string) (string, error) {
	parsedURL, err := url.Parse(databaseURL)
	if err != nil {
		return "", err
	}
	parsedURL.Path = "/" + databaseName
	return parsedURL.String(), nil
}
