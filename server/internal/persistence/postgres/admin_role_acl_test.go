package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestAdminRepositoryRejectsColumnOnlyPrivileges(t *testing.T) {
	fixture := newAdminACLFixture(t, "3")
	for _, check := range []struct {
		table     string
		column    string
		privilege string
	}{
		{table: "wallets", column: "balance", privilege: "UPDATE"},
		{table: "characters", column: "attributes", privilege: "UPDATE"},
		{table: "games", column: "game_id", privilege: "INSERT"},
		{table: "content_revisions", column: "body", privilege: "UPDATE"},
		{table: "audit_logs", column: "audit_id", privilege: "SELECT"},
	} {
		for _, inherited := range []bool{false, true} {
			name := check.table + "/" + check.privilege + "/direct"
			if inherited {
				name = check.table + "/" + check.privilege + "/inherited"
			}
			t.Run(name, func(t *testing.T) {
				fixture.grantColumn(t, check.table, check.column, check.privilege, inherited)
				assertAdminStartupRejected(t, fixture)
				assertColumnOnlyGrant(t, fixture.ctx, fixture.db, check.table, check.column, check.privilege)
			})
		}
	}
	t.Run("runtime column SELECT is not a write", func(t *testing.T) {
		fixture.grantColumn(t, "wallets", "balance", "SELECT", false)
		assertAdminStartupAllowed(t, fixture)
	})
	assertAdminStartupAllowed(t, fixture)
}

func TestAdminRepositoryRejectsLoginColumnOnlyPrivileges(t *testing.T) {
	fixture := newAdminACLFixture(t, "3")
	for _, check := range []struct {
		table     string
		column    string
		privilege string
	}{
		{table: "wallets", column: "balance", privilege: "UPDATE"},
		{table: "characters", column: "attributes", privilege: "UPDATE"},
		{table: "games", column: "game_id", privilege: "INSERT"},
		{table: "content_revisions", column: "body", privilege: "UPDATE"},
		{table: "audit_logs", column: "audit_id", privilege: "SELECT"},
	} {
		t.Run(check.table+"/"+check.privilege, func(t *testing.T) {
			grant := check.privilege + " (" + pgx.Identifier{check.column}.Sanitize() + ") ON " + pgx.Identifier{"public", check.table}.Sanitize()
			roleID := pgx.Identifier{fixture.roles.appRole}.Sanitize()
			execACLStatement(t, fixture.ctx, fixture.db, "GRANT "+grant+" TO "+roleID)
			t.Cleanup(func() { cleanupACLStatement(t, fixture.db, "REVOKE "+grant+" FROM "+roleID) })
			assertAdminStartupRejected(t, fixture)
		})
	}
	t.Run("LOGIN direct schema CREATE", func(t *testing.T) {
		roleID := pgx.Identifier{fixture.roles.appRole}.Sanitize()
		execACLStatement(t, fixture.ctx, fixture.db, "GRANT CREATE ON SCHEMA public TO "+roleID)
		t.Cleanup(func() { cleanupACLStatement(t, fixture.db, "REVOKE CREATE ON SCHEMA public FROM "+roleID) })
		assertAdminStartupRejected(t, fixture)
	})
	assertAdminStartupAllowed(t, fixture)
}

func TestAdminMigrationRejectsColumnOnlyPrivileges(t *testing.T) {
	fixture := newAdminACLFixture(t, "2")
	for _, check := range []struct {
		table     string
		column    string
		privilege string
		reason    string
	}{
		{table: "wallets", column: "balance", privilege: "UPDATE", reason: "runtime write privileges"},
		{table: "characters", column: "attributes", privilege: "UPDATE", reason: "runtime write privileges"},
		{table: "games", column: "game_id", privilege: "INSERT", reason: "runtime write privileges"},
		{table: "content_revisions", column: "body", privilege: "INSERT", reason: "overlapping control-plane table privileges"},
		{table: "content_revisions", column: "created_at", privilege: "SELECT", reason: "overlapping control-plane table privileges"},
	} {
		for _, inherited := range []bool{false, true} {
			name := check.table + "/" + check.privilege + "/direct"
			if inherited {
				name = check.table + "/" + check.privilege + "/inherited"
			}
			t.Run(name, func(t *testing.T) {
				fixture.grantColumn(t, check.table, check.column, check.privilege, inherited)
				fixture.assertMigrationRejected(t, check.reason)
				assertColumnOnlyGrant(t, fixture.ctx, fixture.db, check.table, check.column, check.privilege)
				fixture.migrate(t, "2")
				assertColumnOnlyGrant(t, fixture.ctx, fixture.db, check.table, check.column, check.privilege)
			})
		}
	}
	fixture.migrate(t, "3")
	assertAdminStartupAllowed(t, fixture)
}

func TestAdminMigrationPreservesPriorNonoverlappingColumnACL(t *testing.T) {
	fixture := newAdminACLFixture(t, "2")
	fixture.grantColumn(t, "games", "display_name", "SELECT", false)
	fixture.grantColumn(t, "content_revisions", "created_at", "INSERT", false)
	principalBefore := adminPrincipalSnapshot(t, fixture.ctx, fixture.db)
	for range 2 {
		fixture.migrate(t, "3")
		fixture.migrate(t, "2")
		assertColumnOnlyGrant(t, fixture.ctx, fixture.db, "games", "display_name", "SELECT")
		assertColumnOnlyGrant(t, fixture.ctx, fixture.db, "content_revisions", "created_at", "INSERT")
		if after := adminPrincipalSnapshot(t, fixture.ctx, fixture.db); after != principalBefore {
			t.Error("up/down 改变了外部 principal 属性或成员关系")
		}
	}
}

func TestAdminRepositoryRejectsReplicationPrincipals(t *testing.T) {
	fixture := newAdminACLFixture(t, "3")
	t.Run("LOGIN replication principal", func(t *testing.T) {
		roleID := pgx.Identifier{fixture.roles.appRole}.Sanitize()
		execACLStatement(t, fixture.ctx, fixture.db, "ALTER ROLE "+roleID+" REPLICATION")
		t.Cleanup(func() { cleanupACLStatement(t, fixture.db, "ALTER ROLE "+roleID+" NOREPLICATION") })
		assertAdminStartupRejected(t, fixture)
		var replication bool
		if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT rolreplication FROM pg_roles WHERE rolname = $1`, fixture.roles.appRole).Scan(&replication); err != nil || !replication {
			t.Fatal("启动预检改变了测试专属 LOGIN principal 的 REPLICATION 属性")
		}
	})
	for _, member := range []string{fixture.roles.appRole, adminControlRole} {
		t.Run("inherited replication/"+member, func(t *testing.T) {
			fixture.grantReplicationGroup(t, member)
			assertAdminStartupRejected(t, fixture)
		})
	}
	assertAdminStartupAllowed(t, fixture)
}

func TestAdminMigrationRejectsInheritedReplicationGroup(t *testing.T) {
	fixture := newAdminACLFixture(t, "2")
	t.Run("NOLOGIN replication ancestor", func(t *testing.T) {
		fixture.grantReplicationGroup(t, adminControlRole)
		fixture.assertMigrationRejected(t, "inherited administrative or DDL privileges")
	})
	fixture.migrate(t, "3")
	assertAdminStartupAllowed(t, fixture)
}

func (f *adminACLFixture) grantReplicationGroup(t *testing.T, member string) {
	t.Helper()
	role := randomTestIdentifier(t, "tbcg_repl_group_")
	roleID := pgx.Identifier{role}.Sanitize()
	memberID := pgx.Identifier{member}.Sanitize()
	execACLStatement(t, f.ctx, f.db, "CREATE ROLE "+roleID+" NOLOGIN REPLICATION")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, statement := range []string{"REVOKE " + roleID + " FROM " + memberID, "DROP ROLE IF EXISTS " + roleID} {
			if _, err := f.db.ExecContext(cleanupCtx, statement); err != nil {
				t.Error("清理专属 REPLICATION group fixture 失败")
			}
		}
	})
	execACLStatement(t, f.ctx, f.db, "GRANT "+roleID+" TO "+memberID)
}

func assertAdminStartupRejected(t *testing.T, fixture *adminACLFixture) {
	t.Helper()
	principalBefore := adminPrincipalSnapshot(t, fixture.ctx, fixture.db)
	repository, err := openAdminRepository(fixture.ctx, fixture.appDSN, true)
	if err == nil {
		if closeErr := repository.Close(); closeErr != nil {
			t.Error("关闭错误接受的越权 repository 失败")
		}
		t.Error("admin repository 接受了越权数据库 principal")
	} else if !strings.Contains(err.Error(), "admin service database login or effective control-plane privileges are not restricted") {
		t.Error("admin repository 未因权限守卫拒绝越权 principal")
	}
	if after := adminPrincipalSnapshot(t, fixture.ctx, fixture.db); after != principalBefore {
		t.Error("启动拒绝时改变了外部 principal 属性或成员关系")
	}
}

func assertAdminStartupAllowed(t *testing.T, fixture *adminACLFixture) {
	t.Helper()
	repository, err := openAdminRepository(fixture.ctx, fixture.appDSN, true)
	if err != nil {
		t.Fatal("admin repository 拒绝了合法的最小权限 principal；连接细节已隐藏")
	}
	if err := repository.Close(); err != nil {
		t.Error("关闭合法最小权限 repository 失败")
	}
}
