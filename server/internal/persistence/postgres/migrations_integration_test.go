package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestMigrationsInstallAndRemainIdempotent(t *testing.T) {
	maintenanceURL := os.Getenv("TBCG_TEST_DATABASE_URL")
	if maintenanceURL == "" {
		if os.Getenv("TBCG_REQUIRE_TEST_DATABASE") == "1" {
			t.Fatal("CI requires TBCG_TEST_DATABASE_URL")
		}
		t.Skip("set TBCG_TEST_DATABASE_URL to a disposable PostgreSQL maintenance database URL")
	}

	ternPath, err := exec.LookPath("tern")
	if err != nil {
		t.Fatalf("find standalone tern v2.4.3 CLI on PATH: %v", err)
	}
	versionOutput, err := exec.Command(ternPath, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("run tern version: %v\n%s", err, versionOutput)
	}
	if !strings.Contains(string(versionOutput), "v2.4.3") {
		t.Fatalf("require tern v2.4.3, got %q", strings.TrimSpace(string(versionOutput)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	maintenanceConfig := parseIsolatedTestDatabase(t, maintenanceURL)
	maintenanceDB := stdlib.OpenDB(*maintenanceConfig)
	t.Cleanup(func() { _ = maintenanceDB.Close() })
	if err := maintenanceDB.PingContext(ctx); err != nil {
		t.Fatalf("connect to TBCG_TEST_DATABASE_URL maintenance database: %v", err)
	}
	assertPostgreSQL18(t, ctx, maintenanceDB)

	databaseName, err := randomTestDatabaseName()
	if err != nil {
		t.Fatalf("create isolated database name: %v", err)
	}
	if _, err := maintenanceDB.ExecContext(ctx, "CREATE DATABASE \""+databaseName+"\""); err != nil {
		t.Fatalf("create isolated test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := maintenanceDB.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS \""+databaseName+"\""); err != nil {
			t.Errorf("drop isolated test database %q: %v", databaseName, err)
		}
	})

	migrationDir := filepath.Join(findServerRoot(t), "migrations")
	if info, err := os.Stat(migrationDir); err != nil || !info.IsDir() {
		t.Fatalf("locate server migrations directory %q: %v", migrationDir, err)
	}
	env, err := ternEnvironment(maintenanceConfig, databaseName, migrationDir)
	if err != nil {
		t.Fatalf("prepare tern environment: %v", err)
	}
	for run := 1; run <= 2; run++ {
		runTernMigration(t, ctx, ternPath, findServerRoot(t), env, "migrate")
	}

	testConfig := maintenanceConfig.Copy()
	testConfig.Database = databaseName
	db := stdlib.OpenDB(*testConfig)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to isolated migrated database: %v", err)
	}

	assertRequiredTables(t, ctx, db)
	assertMigrationVersionRecorded(t, ctx, db)
	assertSchemaConstraints(t, ctx, db)
	assertSchemaVersionColumns(t, ctx, db)
	assertOwnerGuardEpochConstraints(t, ctx, db)
	assertCharacterStateVersionConflict(t, ctx, db)

	runTernMigration(t, ctx, ternPath, findServerRoot(t), env, "migrate", "--destination", "0")
	var gamesTable sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('public.games')").Scan(&gamesTable); err != nil {
		t.Fatalf("check games after migrating down to version 0: %v", err)
	}
	if gamesTable.Valid {
		t.Fatalf("games table still exists after migrating down to version 0: %q", gamesTable.String)
	}

	runTernMigration(t, ctx, ternPath, findServerRoot(t), env, "migrate")
	assertRequiredTables(t, ctx, db)
	assertMigrationVersionRecorded(t, ctx, db)
	assertSchemaConstraints(t, ctx, db)
	assertSchemaVersionColumns(t, ctx, db)
}

func runTernMigration(t *testing.T, ctx context.Context, ternPath, serverRoot string, env []string, args ...string) {
	t.Helper()
	commandCtx, commandCancel := context.WithTimeout(ctx, 90*time.Second)
	defer commandCancel()
	command := exec.CommandContext(commandCtx, ternPath, args...)
	command.Dir = serverRoot
	command.Env = env
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s: %v\\n%s", strings.Join(args, " "), err, output)
	}
}

const (
	disposableTestDatabasePort = 55432
	disposableTestDatabaseAck  = "I_UNDERSTAND_TEST_DB_IS_DISPOSABLE"
)

func TestIsolatedTestDatabaseURLRejectsUnsafeTargets(t *testing.T) {
	const safeURL = "postgresql://test:test@127.0.0.1:55432/postgres?sslmode=disable"
	tests := []struct {
		name         string
		databaseURL  string
		confirmation string
		envName      string
		envValue     string
		wantError    bool
	}{
		{name: "approved loopback test database", databaseURL: safeURL, confirmation: disposableTestDatabaseAck},
		{name: "default shared port", databaseURL: "postgresql://test:test@127.0.0.1:5432/postgres", confirmation: disposableTestDatabaseAck, wantError: true},
		{name: "non-loopback host", databaseURL: "postgresql://test:test@db.internal:55432/postgres", confirmation: disposableTestDatabaseAck, wantError: true},
		{name: "non-maintenance database", databaseURL: "postgresql://test:test@127.0.0.1:55432/production", confirmation: disposableTestDatabaseAck, wantError: true},
		{name: "inherited hostaddr override", databaseURL: safeURL, confirmation: disposableTestDatabaseAck, envName: "PGHOSTADDR", envValue: "192.0.2.10", wantError: true},
		{name: "inherited service override", databaseURL: safeURL, confirmation: disposableTestDatabaseAck, envName: "PGSERVICE", envValue: "production", wantError: true},
		{name: "missing destructive-test confirmation", databaseURL: safeURL, wantError: true},
	}
	for _, variable := range []string{"PGHOSTADDR", "PGSERVICE", "PGSERVICEFILE"} {
		t.Setenv(variable, "")
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.envName != "" {
				t.Setenv(test.envName, test.envValue)
			}
			config, err := validateIsolatedTestDatabaseURL(test.databaseURL, test.confirmation)
			if test.wantError {
				if err == nil {
					t.Fatal("expected unsafe test database URL to be rejected")
				}
				if config != nil {
					t.Fatal("rejected URL returned a connection config")
				}
				return
			}
			if err != nil {
				t.Fatalf("validate approved test URL: %v", err)
			}
			if config.Host != "127.0.0.1" || config.Port != disposableTestDatabasePort || config.Database != "postgres" {
				t.Fatalf("validated config points outside isolated test target: host=%q port=%d database=%q", config.Host, config.Port, config.Database)
			}
		})
	}
}

func parseIsolatedTestDatabase(t *testing.T, databaseURL string) *pgx.ConnConfig {
	t.Helper()
	config, err := validateIsolatedTestDatabaseURL(databaseURL, os.Getenv("TBCG_TEST_DATABASE_CONFIRM"))
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func validateIsolatedTestDatabaseURL(databaseURL, confirmation string) (*pgx.ConnConfig, error) {
	if confirmation != disposableTestDatabaseAck {
		return nil, errors.New("set TBCG_TEST_DATABASE_CONFIRM only for an isolated disposable PostgreSQL test server")
	}
	for _, variable := range []string{"PGHOSTADDR", "PGSERVICE", "PGSERVICEFILE"} {
		if value := strings.TrimSpace(os.Getenv(variable)); value != "" {
			return nil, fmt.Errorf("refuse inherited %s override %q for disposable PostgreSQL tests", variable, value)
		}
	}
	parsedURL, err := url.Parse(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse TBCG_TEST_DATABASE_URL: %w", err)
	}
	if parsedURL.Scheme != "postgres" && parsedURL.Scheme != "postgresql" {
		return nil, errors.New("TBCG_TEST_DATABASE_URL must use a PostgreSQL URL scheme")
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse TBCG_TEST_DATABASE_URL: %w", err)
	}
	if ip := net.ParseIP(config.Host); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("refuse non-loopback PostgreSQL test host %q", config.Host)
	}
	if config.Port != disposableTestDatabasePort {
		return nil, fmt.Errorf("refuse PostgreSQL test port %d; expected isolated port %d", config.Port, disposableTestDatabasePort)
	}
	if config.Database != "postgres" {
		return nil, errors.New("TBCG_TEST_DATABASE_URL must target the disposable maintenance database 'postgres'")
	}
	return config, nil
}

func assertPostgreSQL18(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var versionNumber int
	if err := db.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&versionNumber); err != nil {
		t.Fatalf("read PostgreSQL server version: %v", err)
	}
	if versionNumber < 180000 || versionNumber >= 190000 {
		t.Fatalf("PostgreSQL version number %d is outside the required major version 18", versionNumber)
	}
}

func randomTestDatabaseName() (string, error) {
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generate random suffix: %w", err)
	}
	return "tbcg_test_" + hex.EncodeToString(suffix[:]), nil
}

func randomTestIdentifier(t *testing.T, prefix string) string {
	t.Helper()
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate random test identifier: %v", err)
	}
	return prefix + hex.EncodeToString(suffix[:])
}

func findServerRoot(t *testing.T) string {
	t.Helper()
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for dir := currentDir; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("find server root containing go.mod from %q", currentDir)
		}
	}
}

func ternEnvironment(maintenanceConfig *pgx.ConnConfig, databaseName, migrationDir string) ([]string, error) {
	parsedURL, err := url.Parse(os.Getenv("TBCG_TEST_DATABASE_URL"))
	if err != nil {
		return nil, fmt.Errorf("parse maintenance database URL: %w", err)
	}
	env := os.Environ()
	for _, variable := range []string{"PGHOSTADDR", "PGSERVICE", "PGSERVICEFILE"} {
		env = removeEnvironmentValue(env, variable)
	}
	settings := map[string]string{
		"PGHOST":          maintenanceConfig.Host,
		"PGPORT":          fmt.Sprintf("%d", maintenanceConfig.Port),
		"PGUSER":          maintenanceConfig.User,
		"PGPASSWORD":      maintenanceConfig.Password,
		"PGDATABASE":      databaseName,
		"TERN_MIGRATIONS": migrationDir,
	}
	libpqParameters := map[string]string{
		"sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT",
		"sslkey": "PGSSLKEY", "connect_timeout": "PGCONNECT_TIMEOUT", "application_name": "PGAPPNAME",
		"target_session_attrs": "PGTARGETSESSIONATTRS", "gssencmode": "PGGSSENCMODE",
	}
	for parameter, variable := range libpqParameters {
		if value := parsedURL.Query().Get(parameter); value != "" {
			settings[variable] = value
		}
	}
	for key, value := range settings {
		env = setEnvironmentValue(env, key, value)
	}
	return env, nil
}

func removeEnvironmentValue(env []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func setEnvironmentValue(env []string, key, value string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, prefix+value)
}

func assertRequiredTables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	const query = `
SELECT table_name
FROM information_schema.tables
WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("query public base tables: %v", err)
	}
	defer rows.Close()
	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan public table name: %v", err)
		}
		tables[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate public table names: %v", err)
	}
	for _, name := range []string{
		"games", "realms", "content_revisions", "releases", "owner_guards",
		"characters", "operation_results", "wallets", "wallet_ledger", "outbox",
	} {
		if !tables[name] {
			t.Errorf("required PostgreSQL table %q is missing", name)
		}
	}
}

func assertMigrationVersionRecorded(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var version int64
	if err := db.QueryRowContext(ctx, "SELECT version FROM public.schema_version").Scan(&version); err != nil {
		t.Fatalf("read Tern migration version from public.schema_version: %v", err)
	}
	if version < 1 {
		t.Errorf("expected at least one recorded migration, got version %d", version)
	}
}

func assertSchemaConstraints(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	assertCatalogCount(t, ctx, db, "foreign-key constraints", `
SELECT count(*) FROM pg_constraint
WHERE connamespace = 'public'::regnamespace AND contype = 'f'`, 1)
	assertCatalogCount(t, ctx, db, "unique constraints", `
SELECT count(*) FROM pg_constraint
WHERE connamespace = 'public'::regnamespace AND contype = 'u'`, 1)
	assertCatalogCount(t, ctx, db, "check constraints", `
SELECT count(*) FROM pg_constraint
WHERE connamespace = 'public'::regnamespace AND contype = 'c'`, 1)
	assertCatalogCount(t, ctx, db, "JSONB columns", `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'public' AND data_type = 'jsonb'`, 1)

	var stateVersionExists bool
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM information_schema.columns
  WHERE table_schema = 'public' AND table_name = 'characters' AND column_name = 'state_version'
)`).Scan(&stateVersionExists); err != nil {
		t.Fatalf("inspect characters.state_version: %v", err)
	}
	if !stateVersionExists {
		t.Error("characters.state_version column is missing")
	}

	var ownerEpochExists bool
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM information_schema.columns
  WHERE table_schema = 'public' AND table_name = 'owner_guards' AND column_name = 'owner_epoch'
)`).Scan(&ownerEpochExists); err != nil {
		t.Fatalf("inspect owner_guards.owner_epoch: %v", err)
	}
	if !ownerEpochExists {
		t.Error("owner_guards.owner_epoch column is missing")
	}

	assertCatalogCount(t, ctx, db, "game/realm-scoped composite foreign keys", `
SELECT count(*) FROM pg_constraint
WHERE connamespace = 'public'::regnamespace AND contype = 'f'
  AND position('game_id' IN pg_get_constraintdef(oid)) > 0
  AND position('realm_id' IN pg_get_constraintdef(oid)) > 0`, 1)
	assertCatalogCount(t, ctx, db, "game/realm-scoped composite primary or unique constraints", `
SELECT count(*) FROM pg_constraint
WHERE connamespace = 'public'::regnamespace AND contype IN ('p', 'u')
  AND position('game_id' IN pg_get_constraintdef(oid)) > 0
  AND position('realm_id' IN pg_get_constraintdef(oid)) > 0`, 1)
}

func assertSchemaVersionColumns(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	const query = `
SELECT count(*)
FROM information_schema.columns
WHERE table_schema = 'public'
  AND (table_name, column_name) IN (
    ('content_revisions', 'schema_version'),
    ('releases', 'content_schema_version'),
    ('releases', 'instance_schema_version'),
    ('releases', 'ruleset_version'),
    ('releases', 'protocol_version'),
    ('releases', 'client_min_build')
  )`
	var count int
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		t.Fatalf("inspect content and release version columns: %v", err)
	}
	if count != 6 {
		t.Errorf("expected all 6 content/release version columns, found %d", count)
	}
}

func assertOwnerGuardEpochConstraints(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	gameID := randomTestIdentifier(t, "owner-guard-game-")
	realmID := randomTestIdentifier(t, "owner-guard-realm-")
	if _, err := db.ExecContext(ctx, `INSERT INTO games (game_id, display_name) VALUES ($1, $2)`, gameID, "Owner guard test"); err != nil {
		t.Fatalf("insert owner guard test game: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO realms (game_id, realm_id, display_name) VALUES ($1, $2, $3)`, gameID, realmID, "Owner guard test realm"); err != nil {
		t.Fatalf("insert owner guard test realm: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO owner_guards (game_id, realm_id, owner_epoch) VALUES ($1, $2, 0)`, gameID, realmID); err != nil {
		t.Fatalf("insert owner guard at epoch 0: %v", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE owner_guards SET owner_epoch = 1 WHERE game_id = $1 AND realm_id = $2`, gameID, realmID); err != nil {
		t.Fatalf("advance owner epoch from 0 to 1: %v", err)
	}
	assertExecRejected(t, ctx, db, "same owner epoch", `UPDATE owner_guards SET owner_epoch = 1 WHERE game_id = $1 AND realm_id = $2`, gameID, realmID)
	assertExecRejected(t, ctx, db, "decreasing owner epoch", `UPDATE owner_guards SET owner_epoch = 0 WHERE game_id = $1 AND realm_id = $2`, gameID, realmID)

	targetGameID := randomTestIdentifier(t, "owner-guard-target-game-")
	targetRealmID := randomTestIdentifier(t, "owner-guard-target-realm-")
	if _, err := db.ExecContext(ctx, `INSERT INTO games (game_id, display_name) VALUES ($1, $2)`, targetGameID, "Owner guard target"); err != nil {
		t.Fatalf("insert owner guard target game: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO realms (game_id, realm_id, display_name) VALUES ($1, $2, $3)`, targetGameID, targetRealmID, "Owner guard target realm"); err != nil {
		t.Fatalf("insert owner guard target realm: %v", err)
	}
	assertExecRejected(t, ctx, db, "moving owner guard scope", `UPDATE owner_guards SET game_id = $3, realm_id = $4, owner_epoch = 2 WHERE game_id = $1 AND realm_id = $2`, gameID, realmID, targetGameID, targetRealmID)
	assertExecRejected(t, ctx, db, "deleting owner guard", `DELETE FROM owner_guards WHERE game_id = $1 AND realm_id = $2`, gameID, realmID)
	assertExecRejected(t, ctx, db, "truncating owner guards", `TRUNCATE owner_guards`)

	var epoch int64
	if err := db.QueryRowContext(ctx, `SELECT owner_epoch FROM owner_guards WHERE game_id = $1 AND realm_id = $2`, gameID, realmID).Scan(&epoch); err != nil {
		t.Fatalf("read owner epoch after rejected mutations: %v", err)
	}
	if epoch != 1 {
		t.Errorf("owner epoch after rejected mutations = %d, want 1", epoch)
	}
}

func assertCharacterStateVersionConflict(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	gameID := randomTestIdentifier(t, "state-version-game-")
	realmID := randomTestIdentifier(t, "state-version-realm-")
	characterID := randomTestIdentifier(t, "state-version-character-")
	if _, err := db.ExecContext(ctx, `INSERT INTO games (game_id, display_name) VALUES ($1, $2)`, gameID, "State version test"); err != nil {
		t.Fatalf("insert state version test game: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO realms (game_id, realm_id, display_name) VALUES ($1, $2, $3)`, gameID, realmID, "State version test realm"); err != nil {
		t.Fatalf("insert state version test realm: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO characters (game_id, realm_id, character_id, owner_subject, instance_schema_version) VALUES ($1, $2, $3, $4, 1)`, gameID, realmID, characterID, "test-subject"); err != nil {
		t.Fatalf("insert state version test character: %v", err)
	}

	type updateResult struct {
		rows int64
		err  error
	}
	start := make(chan struct{})
	results := make(chan updateResult, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Go(func() {
			<-start
			result, err := db.ExecContext(ctx, `UPDATE characters SET state_version = state_version + 1 WHERE game_id = $1 AND realm_id = $2 AND character_id = $3 AND state_version = 1`, gameID, realmID, characterID)
			if err != nil {
				results <- updateResult{err: err}
				return
			}
			rows, err := result.RowsAffected()
			results <- updateResult{rows: rows, err: err}
		})
	}
	close(start)
	wait.Wait()
	close(results)

	updated, stale := 0, 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("conditional state_version update: %v", result.err)
		}
		switch result.rows {
		case 1:
			updated++
		case 0:
			stale++
		default:
			t.Errorf("conditional update affected %d rows, want 0 or 1", result.rows)
		}
	}
	if updated != 1 || stale != 1 {
		t.Fatalf("concurrent expected-version updates: updated=%d stale=%d, want one of each", updated, stale)
	}

	var version int64
	if err := db.QueryRowContext(ctx, `SELECT state_version FROM characters WHERE game_id = $1 AND realm_id = $2 AND character_id = $3`, gameID, realmID, characterID).Scan(&version); err != nil {
		t.Fatalf("read character state_version after concurrent updates: %v", err)
	}
	if version != 2 {
		t.Errorf("state_version after concurrent updates = %d, want 2", version)
	}
}

func assertExecRejected(t *testing.T, ctx context.Context, db *sql.DB, description, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query, args...); err == nil {
		t.Errorf("expected %s to fail", description)
	}
}

func assertCatalogCount(t *testing.T, ctx context.Context, db *sql.DB, description, query string, minimum int64) {
	t.Helper()
	var count int64
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		t.Fatalf("inspect %s in PostgreSQL catalogs: %v", description, err)
	}
	if count < minimum {
		t.Errorf("expected at least %d %s, found %d", minimum, description, count)
	}
}
