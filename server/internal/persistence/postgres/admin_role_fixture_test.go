package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestMain(m *testing.M) {
	code, err := runPostgresPackageTests(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "prepare isolated PostgreSQL control-plane role fixture failed")
		os.Exit(1)
	}
	os.Exit(code)
}

func runPostgresPackageTests(m *testing.M) (int, error) {
	maintenanceURL := os.Getenv("TBCG_TEST_DATABASE_URL")
	if maintenanceURL == "" {
		return m.Run(), nil
	}
	config, err := validateIsolatedTestDatabaseURL(maintenanceURL, os.Getenv("TBCG_TEST_DATABASE_CONFIRM"))
	if err != nil {
		return 0, err
	}
	db := stdlib.OpenDB(*config)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return 0, fmt.Errorf("connect isolated maintenance database for role fixture: %w", err)
	}
	var roleExists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tbcg_admin_control_plane')`).Scan(&roleExists); err != nil {
		return 0, fmt.Errorf("inspect control-plane role fixture: %w", err)
	}
	createdRole := false
	if !roleExists {
		if _, err := db.ExecContext(ctx, `CREATE ROLE tbcg_admin_control_plane NOLOGIN`); err != nil {
			return 0, fmt.Errorf("create isolated test control-plane role: %w", err)
		}
		createdRole = true
	}
	code := m.Run()
	if createdRole {
		if _, err := db.ExecContext(context.Background(), `DROP ROLE IF EXISTS tbcg_admin_control_plane`); err != nil {
			return code, fmt.Errorf("drop test-owned control-plane role: %w", err)
		}
	}
	return code, nil
}

type adminRoleFixture struct {
	appRole      string
	appPassword  string
	externalDB   string
	createdGroup bool
	createdApp   bool
	createdExtDB bool
}

func prepareAdminRoleFixture(t *testing.T, ctx context.Context, maintenanceDB *sql.DB, maintenanceConfig *pgx.ConnConfig) *adminRoleFixture {
	t.Helper()
	fixture := &adminRoleFixture{appRole: randomTestIdentifier(t, "tbcg_admin_app_")}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if fixture.createdExtDB {
			if _, err := maintenanceDB.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS "+pgx.Identifier{fixture.externalDB}.Sanitize()); err != nil {
				t.Errorf("drop isolated external-ACL database: %v", err)
			}
		}
		if fixture.createdApp {
			if _, err := maintenanceDB.ExecContext(cleanupCtx, "DROP ROLE IF EXISTS "+pgx.Identifier{fixture.appRole}.Sanitize()); err != nil {
				t.Errorf("drop temporary admin application role: %v", err)
			}
		}
		if fixture.createdGroup {
			if _, err := maintenanceDB.ExecContext(cleanupCtx, "DROP ROLE IF EXISTS tbcg_admin_control_plane"); err != nil {
				t.Errorf("drop test-provisioned control-plane role: %v", err)
			}
		}
	})

	var groupExists bool
	if err := maintenanceDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tbcg_admin_control_plane')`).Scan(&groupExists); err != nil {
		t.Fatalf("inspect provisioned control-plane role: %v", err)
	}
	if !groupExists {
		if _, err := maintenanceDB.ExecContext(ctx, `CREATE ROLE tbcg_admin_control_plane NOLOGIN`); err != nil {
			t.Fatalf("provision disposable NOLOGIN control-plane role: %v", err)
		}
		fixture.createdGroup = true
	}
	var canLogin bool
	if err := maintenanceDB.QueryRowContext(ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname = 'tbcg_admin_control_plane'`).Scan(&canLogin); err != nil {
		t.Fatalf("read control-plane role login property: %v", err)
	}
	if canLogin {
		t.Fatal("provisioned tbcg_admin_control_plane role must be NOLOGIN")
	}

	var password [32]byte
	if _, err := rand.Read(password[:]); err != nil {
		t.Fatalf("generate temporary admin database password: %v", err)
	}
	fixture.appPassword = hex.EncodeToString(password[:])
	appIdentifier := pgx.Identifier{fixture.appRole}.Sanitize()
	if _, err := maintenanceDB.ExecContext(ctx, "CREATE ROLE "+appIdentifier+" LOGIN PASSWORD '"+fixture.appPassword+"'"); err != nil {
		t.Fatalf("create temporary admin application login: %v", err)
	}
	fixture.createdApp = true
	if _, err := maintenanceDB.ExecContext(ctx, "GRANT tbcg_admin_control_plane TO "+appIdentifier); err != nil {
		t.Fatalf("grant NOLOGIN control-plane role to temporary app login: %v", err)
	}

	externalDBName, err := randomTestDatabaseName()
	if err != nil {
		t.Fatalf("create external ACL database name: %v", err)
	}
	fixture.externalDB = externalDBName
	if _, err := maintenanceDB.ExecContext(ctx, "CREATE DATABASE "+pgx.Identifier{fixture.externalDB}.Sanitize()); err != nil {
		t.Fatalf("create isolated external-ACL database: %v", err)
	}
	fixture.createdExtDB = true
	externalConfig := maintenanceConfig.Copy()
	externalConfig.Database = fixture.externalDB
	externalDB := stdlib.OpenDB(*externalConfig)
	t.Cleanup(func() {
		if err := externalDB.Close(); err != nil {
			t.Errorf("close external ACL database connection: %v", err)
		}
	})
	if _, err := externalDB.ExecContext(ctx, `CREATE TABLE public.external_acl_fixture (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("create external database ACL fixture: %v", err)
	}
	if _, err := externalDB.ExecContext(ctx, `GRANT SELECT ON public.external_acl_fixture TO tbcg_admin_control_plane`); err != nil {
		t.Fatalf("grant external database fixture access to control-plane role: %v", err)
	}
	return fixture
}

func assertAdminRolePermissions(t *testing.T, ctx context.Context, maintenanceConfig *pgx.ConnConfig, databaseName string, fixture *adminRoleFixture) {
	t.Helper()
	appConfig := maintenanceConfig.Copy()
	appConfig.Database = databaseName
	appConfig.User = fixture.appRole
	appConfig.Password = fixture.appPassword
	appDB := stdlib.OpenDB(*appConfig)
	t.Cleanup(func() {
		if err := appDB.Close(); err != nil {
			t.Errorf("close restricted admin app database: %v", err)
		}
	})
	conn, err := appDB.Conn(ctx)
	if err != nil {
		t.Fatalf("connect with restricted admin app login: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close restricted admin app connection: %v", err)
		}
	})
	if _, err := conn.ExecContext(ctx, `SET ROLE tbcg_admin_control_plane`); err != nil {
		t.Fatalf("assume NOLOGIN control-plane role: %v", err)
	}
	var sessionUser, currentUser string
	if err := conn.QueryRowContext(ctx, `SELECT session_user, current_user`).Scan(&sessionUser, &currentUser); err != nil {
		t.Fatalf("inspect restricted database role identity: %v", err)
	}
	if sessionUser != fixture.appRole || currentUser != "tbcg_admin_control_plane" {
		t.Fatalf("database identities = session %q/current %q, want temporary login/control role", sessionUser, currentUser)
	}
	checks := []struct {
		table string
		priv  string
		want  bool
	}{
		{table: "games", priv: "SELECT", want: false},
		{table: "content_revisions", priv: "SELECT", want: true},
		{table: "content_revisions", priv: "INSERT", want: false},
		{table: "admin_sessions", priv: "SELECT", want: false},
		{table: "admin_sessions", priv: "INSERT", want: false},
		{table: "admin_sessions", priv: "UPDATE", want: false},
		{table: "admin_sessions", priv: "DELETE", want: true},
		{table: "content_drafts", priv: "SELECT", want: false},
		{table: "content_drafts", priv: "INSERT", want: false},
		{table: "content_drafts", priv: "UPDATE", want: false},
		{table: "content_drafts", priv: "DELETE", want: false},
		{table: "admin_users", priv: "SELECT", want: false},
		{table: "admin_users", priv: "UPDATE", want: false},
		{table: "admin_role_bindings", priv: "SELECT", want: false},
		{table: "admin_role_bindings", priv: "INSERT", want: false},
		{table: "audit_logs", priv: "SELECT", want: false},
		{table: "audit_logs", priv: "INSERT", want: false},
		{table: "audit_logs", priv: "UPDATE", want: false},
		{table: "revision_approvals", priv: "SELECT", want: true},
		{table: "revision_approvals", priv: "INSERT", want: true},
		{table: "characters", priv: "UPDATE", want: false},
		{table: "wallets", priv: "UPDATE", want: false},
		{table: "realms", priv: "UPDATE", want: false},
		{table: "owner_guards", priv: "UPDATE", want: false},
		{table: "releases", priv: "UPDATE", want: false},
		{table: "outbox", priv: "INSERT", want: false},
	}
	for _, check := range checks {
		var allowed bool
		if err := conn.QueryRowContext(ctx, `SELECT has_table_privilege(current_user, $1, $2)`, "public."+check.table, check.priv).Scan(&allowed); err != nil {
			t.Fatalf("inspect %s privilege on %s: %v", check.priv, check.table, err)
		}
		if allowed != check.want {
			t.Errorf("control-plane role table-level %s privilege on %s = %t, want %t", check.priv, check.table, allowed, check.want)
		}
	}
	columnChecks := []struct {
		table  string
		column string
		priv   string
		want   bool
	}{
		{table: "games", column: "game_id", priv: "SELECT", want: true},
		{table: "games", column: "display_name", priv: "SELECT", want: false},
		{table: "admin_users", column: "admin_id", priv: "SELECT", want: true},
		{table: "admin_users", column: "username", priv: "SELECT", want: true},
		{table: "admin_users", column: "password_hash", priv: "SELECT", want: true},
		{table: "admin_users", column: "active", priv: "SELECT", want: true},
		{table: "admin_users", column: "created_at", priv: "SELECT", want: false},
		{table: "admin_sessions", column: "token_hash", priv: "SELECT", want: true},
		{table: "admin_sessions", column: "csrf_hash", priv: "SELECT", want: true},
		{table: "admin_sessions", column: "expires_at", priv: "SELECT", want: true},
		{table: "admin_sessions", column: "idle_expires_at", priv: "SELECT", want: true},
		{table: "admin_sessions", column: "admin_id", priv: "INSERT", want: true},
		{table: "admin_sessions", column: "token_hash", priv: "INSERT", want: true},
		{table: "admin_sessions", column: "csrf_hash", priv: "INSERT", want: true},
		{table: "admin_sessions", column: "expires_at", priv: "INSERT", want: true},
		{table: "admin_sessions", column: "idle_expires_at", priv: "INSERT", want: true},
		{table: "admin_sessions", column: "session_id", priv: "INSERT", want: false},
		{table: "admin_sessions", column: "csrf_hash", priv: "UPDATE", want: true},
		{table: "admin_sessions", column: "idle_expires_at", priv: "UPDATE", want: true},
		{table: "admin_sessions", column: "last_seen_at", priv: "UPDATE", want: true},
		{table: "admin_sessions", column: "admin_id", priv: "UPDATE", want: false},
		{table: "admin_role_bindings", column: "game_id", priv: "SELECT", want: true},
		{table: "admin_role_bindings", column: "admin_id", priv: "SELECT", want: true},
		{table: "admin_role_bindings", column: "role", priv: "SELECT", want: true},
		{table: "admin_role_bindings", column: "created_at", priv: "SELECT", want: false},
		{table: "content_drafts", column: "draft_id", priv: "SELECT", want: true},
		{table: "content_drafts", column: "game_id", priv: "SELECT", want: true},
		{table: "content_drafts", column: "content_id", priv: "SELECT", want: true},
		{table: "content_drafts", column: "draft_version", priv: "SELECT", want: true},
		{table: "content_drafts", column: "submitted_draft_version", priv: "SELECT", want: true},
		{table: "content_drafts", column: "definition", priv: "SELECT", want: true},
		{table: "content_drafts", column: "updated_at", priv: "SELECT", want: true},
		{table: "content_drafts", column: "updated_by", priv: "SELECT", want: false},
		{table: "content_drafts", column: "game_id", priv: "INSERT", want: true},
		{table: "content_drafts", column: "content_id", priv: "INSERT", want: true},
		{table: "content_drafts", column: "definition", priv: "INSERT", want: true},
		{table: "content_drafts", column: "updated_by", priv: "INSERT", want: true},
		{table: "content_drafts", column: "draft_id", priv: "INSERT", want: false},
		{table: "content_drafts", column: "draft_version", priv: "UPDATE", want: true},
		{table: "content_drafts", column: "submitted_draft_version", priv: "UPDATE", want: true},
		{table: "content_drafts", column: "definition", priv: "UPDATE", want: true},
		{table: "content_drafts", column: "updated_by", priv: "UPDATE", want: true},
		{table: "content_drafts", column: "updated_at", priv: "UPDATE", want: true},
		{table: "content_drafts", column: "content_id", priv: "UPDATE", want: false},
		{table: "audit_logs", column: "game_id", priv: "INSERT", want: true},
		{table: "audit_logs", column: "actor_id", priv: "INSERT", want: true},
		{table: "audit_logs", column: "action", priv: "INSERT", want: true},
		{table: "audit_logs", column: "resource_type", priv: "INSERT", want: true},
		{table: "audit_logs", column: "resource_id", priv: "INSERT", want: true},
		{table: "audit_logs", column: "outcome", priv: "INSERT", want: true},
		{table: "audit_logs", column: "details", priv: "INSERT", want: true},
		{table: "audit_logs", column: "audit_id", priv: "INSERT", want: false},
	}
	for _, check := range columnChecks {
		var allowed bool
		if err := conn.QueryRowContext(ctx, `SELECT has_column_privilege(current_user, 'public.' || $1, $2, $3)`, check.table, check.column, check.priv).Scan(&allowed); err != nil {
			t.Fatalf("inspect %s privilege on %s.%s: %v", check.priv, check.table, check.column, err)
		}
		if allowed != check.want {
			t.Errorf("control-plane role column-level %s privilege on %s.%s = %t, want %t", check.priv, check.table, check.column, allowed, check.want)
		}
	}
	if _, err := conn.ExecContext(ctx, `SELECT audit_id FROM audit_logs LIMIT 1`); err == nil {
		t.Error("restricted application unexpectedly read private audit records")
	}
	var auditSequenceSelect, auditSequenceUsage bool
	if err := conn.QueryRowContext(ctx, `
SELECT has_sequence_privilege(current_user, 'public.audit_logs_audit_id_seq', 'SELECT'),
       has_sequence_privilege(current_user, 'public.audit_logs_audit_id_seq', 'USAGE')`).Scan(&auditSequenceSelect, &auditSequenceUsage); err != nil {
		t.Fatalf("inspect audit sequence permissions: %v", err)
	}
	if auditSequenceSelect || !auditSequenceUsage {
		t.Errorf("audit sequence SELECT/USAGE = %t/%t, want false/true", auditSequenceSelect, auditSequenceUsage)
	}
	for _, column := range []string{"game_id", "content_id", "revision", "schema_version", "content_type", "body"} {
		var allowed bool
		if err := conn.QueryRowContext(ctx, `SELECT has_column_privilege(current_user, 'public.content_revisions', $1, 'INSERT')`, column).Scan(&allowed); err != nil {
			t.Fatalf("inspect content_revisions.%s INSERT privilege: %v", column, err)
		}
		if !allowed {
			t.Errorf("control-plane role cannot INSERT content_revisions.%s", column)
		}
	}
	var canInsertCreatedAt bool
	if err := conn.QueryRowContext(ctx, `SELECT has_column_privilege(current_user, 'public.content_revisions', 'created_at', 'INSERT')`).Scan(&canInsertCreatedAt); err != nil {
		t.Fatalf("inspect content_revisions.created_at INSERT privilege: %v", err)
	}
	if canInsertCreatedAt {
		t.Error("control-plane role must not override content_revisions server-assigned created_at")
	}
	for _, statement := range []struct {
		name string
		sql  string
	}{
		{name: "write game registry", sql: `INSERT INTO games (game_id, display_name) VALUES ('blocked-admin-write', 'blocked')`},
		{name: "write realm active pointer", sql: `UPDATE realms SET active_release_id = NULL WHERE false`},
		{name: "write runtime owner guard", sql: `UPDATE owner_guards SET owner_epoch = owner_epoch + 1 WHERE false`},
		{name: "write player character", sql: `UPDATE characters SET attributes = '{}'::jsonb WHERE false`},
	} {
		if _, err := conn.ExecContext(ctx, statement.sql); err == nil {
			t.Errorf("restricted application unexpectedly could %s", statement.name)
		}
	}
}

func assertAdminRoleSurvivesDown(t *testing.T, ctx context.Context, maintenanceDB *sql.DB, maintenanceConfig *pgx.ConnConfig, fixture *adminRoleFixture) {
	t.Helper()
	var roleExists, canLogin, member bool
	if err := maintenanceDB.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tbcg_admin_control_plane'),
       (SELECT rolcanlogin FROM pg_roles WHERE rolname = 'tbcg_admin_control_plane'),
       pg_has_role($1, 'tbcg_admin_control_plane', 'member')`, fixture.appRole).Scan(&roleExists, &canLogin, &member); err != nil {
		t.Fatalf("verify database principal survives migration down: %v", err)
	}
	if !roleExists || canLogin || !member {
		t.Errorf("control-plane role down-migration state: exists=%t can_login=%t app_member=%t", roleExists, canLogin, member)
	}
	externalConfig := maintenanceConfig.Copy()
	externalConfig.Database = fixture.externalDB
	externalDB := stdlib.OpenDB(*externalConfig)
	defer func() {
		if err := externalDB.Close(); err != nil {
			t.Errorf("close external ACL verification database: %v", err)
		}
	}()
	var externalSelect bool
	if err := externalDB.QueryRowContext(ctx, `SELECT has_table_privilege('tbcg_admin_control_plane', 'public.external_acl_fixture', 'SELECT')`).Scan(&externalSelect); err != nil {
		t.Fatalf("verify external database grant survives migration down: %v", err)
	}
	if !externalSelect {
		t.Error("down migration revoked the control-plane role's unrelated external database grant")
	}
}
