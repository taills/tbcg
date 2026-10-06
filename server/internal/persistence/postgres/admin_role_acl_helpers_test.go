package postgres

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type adminACLFixture struct {
	ctx        context.Context
	db         *sql.DB
	roles      *adminRoleFixture
	appDSN     string
	workerDSN  string
	ternPath   string
	ternEnv    []string
	serverRoot string
}

func newAdminACLFixture(t *testing.T, version string) *adminACLFixture {
	t.Helper()
	maintenanceURL := os.Getenv("TBCG_TEST_DATABASE_URL")
	if maintenanceURL == "" {
		if os.Getenv("TBCG_REQUIRE_TEST_DATABASE") == "1" {
			t.Fatal("真实 PostgreSQL 权限验证需要测试数据库环境")
		}
		t.Skip("未配置独占的本地 PostgreSQL 测试数据库")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	config := parseIsolatedTestDatabase(t, maintenanceURL)
	maintenanceDB := stdlib.OpenDB(*config)
	t.Cleanup(func() { closeACLTestDatabase(t, maintenanceDB) })
	assertPostgreSQL18(t, ctx, maintenanceDB)
	roles := prepareAdminRoleFixture(t, ctx, maintenanceDB, config)
	databaseName, err := randomTestDatabaseName()
	if err != nil {
		t.Fatal("生成权限测试专属数据库名失败")
	}
	execACLStatement(t, ctx, maintenanceDB, "CREATE DATABASE "+pgx.Identifier{databaseName}.Sanitize())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if _, err := maintenanceDB.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
			t.Error("清理权限测试专属数据库失败")
		}
	})
	fixture := &adminACLFixture{ctx: ctx, roles: roles, serverRoot: findServerRoot(t)}
	fixture.ternPath, err = exec.LookPath("tern")
	if err != nil {
		t.Fatal("未找到 tern 迁移工具")
	}
	fixture.ternEnv, err = ternEnvironment(config, databaseName, filepath.Join(fixture.serverRoot, "migrations"))
	if err != nil {
		t.Fatal("准备权限测试迁移环境失败")
	}
	fixture.migrate(t, version)
	dbConfig := config.Copy()
	dbConfig.Database = databaseName
	fixture.db = stdlib.OpenDB(*dbConfig)
	t.Cleanup(func() { closeACLTestDatabase(t, fixture.db) })
	appURL, err := url.Parse(maintenanceURL)
	if err != nil {
		t.Fatal("准备权限测试应用连接失败")
	}
	appURL.Path = "/" + databaseName
	appURL.User = url.UserPassword(roles.appRole, roles.appPassword)
	fixture.appDSN = appURL.String()
	workerURL := *appURL
	workerURL.User = url.UserPassword(roles.workerRole, roles.workerPassword)
	fixture.workerDSN = workerURL.String()
	return fixture
}

func (f *adminACLFixture) migrate(t *testing.T, version string) {
	t.Helper()
	if _, err := f.migrationResult(version); err != nil {
		t.Fatalf("权限测试迁移到版本 %s 失败", version)
	}
}

func (f *adminACLFixture) migrationResult(version string) ([]byte, error) {
	command := exec.CommandContext(f.ctx, f.ternPath, "migrate", "--destination", version)
	command.Dir = f.serverRoot
	command.Env = f.ternEnv
	return command.CombinedOutput()
}

func (f *adminACLFixture) assertMigrationRejected(t *testing.T, reason string) {
	t.Helper()
	principalBefore := adminPrincipalSnapshot(t, f.ctx, f.db)
	output, err := f.migrationResult("3")
	if err == nil {
		t.Error("003 迁移接受了越权或重叠的既存列权限")
		f.migrate(t, "2")
		return
	}
	if !strings.Contains(string(output), reason) {
		t.Fatal("003 迁移未因预期的角色权限预检拒绝；详细输出已隐藏")
	}
	if after := adminPrincipalSnapshot(t, f.ctx, f.db); after != principalBefore {
		t.Error("003 迁移失败时改变了外部 principal 属性或成员关系")
	}
	var version int
	var table sql.NullString
	if err := f.db.QueryRowContext(f.ctx, `SELECT version, to_regclass('public.admin_users') FROM schema_version`).Scan(&version, &table); err != nil {
		t.Fatal("检查失败迁移的原子性失败")
	}
	if version != 2 || table.Valid {
		t.Errorf("失败迁移未保持版本 2：version=%d admin_users_exists=%t", version, table.Valid)
	}
}

func adminPrincipalSnapshot(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var snapshot string
	if err := db.QueryRowContext(ctx, `
SELECT jsonb_build_object(
  'attributes', (SELECT to_jsonb(r) - 'rolpassword' FROM pg_roles r WHERE rolname = 'tbcg_admin_control_plane'),
  'memberships', (SELECT COALESCE(jsonb_agg(to_jsonb(m) ORDER BY m.roleid, m.member, m.grantor), '[]'::jsonb)
                  FROM pg_auth_members m WHERE m.roleid = 'tbcg_admin_control_plane'::regrole
                     OR m.member = 'tbcg_admin_control_plane'::regrole))::text`).Scan(&snapshot); err != nil {
		t.Fatal("读取外部 principal 非敏感快照失败")
	}
	return snapshot
}

func (f *adminACLFixture) grantColumn(t *testing.T, table, column, privilege string, inherited bool) {
	t.Helper()
	grantee := adminControlRole
	if inherited {
		grantee = randomTestIdentifier(t, "tbcg_acl_group_")
		execACLStatement(t, f.ctx, f.db, "CREATE ROLE "+pgx.Identifier{grantee}.Sanitize()+" NOLOGIN")
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for _, statement := range []string{
				"REVOKE " + pgx.Identifier{grantee}.Sanitize() + " FROM " + adminControlRole,
				"DROP ROLE IF EXISTS " + pgx.Identifier{grantee}.Sanitize(),
			} {
				if _, err := f.db.ExecContext(cleanupCtx, statement); err != nil {
					t.Error("清理专属继承权限角色失败")
				}
			}
		})
		execACLStatement(t, f.ctx, f.db, "GRANT "+pgx.Identifier{grantee}.Sanitize()+" TO "+adminControlRole)
	}
	object := pgx.Identifier{"public", table}.Sanitize()
	columnID := pgx.Identifier{column}.Sanitize()
	roleID := pgx.Identifier{grantee}.Sanitize()
	execACLStatement(t, f.ctx, f.db, "GRANT "+privilege+" ("+columnID+") ON "+object+" TO "+roleID)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := f.db.ExecContext(cleanupCtx, "REVOKE "+privilege+" ("+columnID+") ON "+object+" FROM "+roleID); err != nil {
			t.Error("清理测试创建的精确列 ACL 失败")
		}
	})
	assertColumnOnlyGrant(t, f.ctx, f.db, table, column, privilege)
}

func assertColumnOnlyGrant(t *testing.T, ctx context.Context, db *sql.DB, table, column, privilege string) {
	t.Helper()
	var tableGrant, columnGrant bool
	if err := db.QueryRowContext(ctx, `
SELECT has_table_privilege('tbcg_admin_control_plane', 'public.' || $1, $3),
       has_column_privilege('tbcg_admin_control_plane', 'public.' || $1, $2, $3)`, table, column, privilege).Scan(&tableGrant, &columnGrant); err != nil {
		t.Fatal("检查列级 ACL 失败")
	}
	if tableGrant || !columnGrant {
		t.Fatalf("列级反例不成立：table=%t column=%t", tableGrant, columnGrant)
	}
}

func execACLStatement(t *testing.T, ctx context.Context, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, statement); err != nil {
		t.Fatal("执行专属权限 fixture SQL 失败；未输出连接或 SQL 敏感信息")
	}
}

func cleanupACLStatement(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, statement); err != nil {
		t.Error("清理测试专属权限 fixture 失败；未输出连接或 SQL 敏感信息")
	}
}

func closeACLTestDatabase(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Error("关闭权限测试数据库连接失败")
	}
}
