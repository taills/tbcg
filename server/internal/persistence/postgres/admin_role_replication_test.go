package postgres

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestAdminMigrationRejectsDirectReplicationGroupWithoutChangingPrincipal(t *testing.T) {
	fixture := newAdminACLFixture(t, "2")
	var replication bool
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT rolreplication FROM pg_roles WHERE rolname = 'tbcg_admin_control_plane'`).Scan(&replication); err != nil || replication {
		t.Fatal("专属 control group 初始 REPLICATION 属性不满足安全 fixture 前提")
	}
	before := adminPrincipalSnapshot(t, fixture.ctx, fixture.db)
	migration, err := os.ReadFile(filepath.Join(fixture.serverRoot, "migrations", "003_admin_control_plane.sql"))
	if err != nil {
		t.Fatal("读取真实 003 迁移失败")
	}
	up, _, found := strings.Cut(string(migration), "---- create above / drop below ----")
	if !found {
		t.Fatal("003 迁移缺少 up/down 边界")
	}
	assertTransactionalReplicationPreflight(t, fixture, up)
	if after := adminPrincipalSnapshot(t, fixture.ctx, fixture.db); after != before {
		t.Fatal("事务回滚后外部 principal 属性或成员关系发生改变")
	}
	var version int
	var table sql.NullString
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT version, to_regclass('public.admin_users') FROM schema_version`).Scan(&version, &table); err != nil {
		t.Fatal("验证事务回滚后的数据库状态失败")
	}
	if version != 2 || table.Valid {
		t.Fatalf("事务回滚未恢复版本 2：version=%d admin_users_exists=%t", version, table.Valid)
	}
}

func assertTransactionalReplicationPreflight(t *testing.T, fixture *adminACLFixture, up string) {
	t.Helper()
	tx, err := fixture.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatal("创建专属 REPLICATION preflight 事务失败")
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error("REPLICATION fixture 最终回滚失败")
		}
	}()
	// 只修改本会话预置测试组，属性始终不提交，也不执行复制操作。
	if _, err := tx.ExecContext(fixture.ctx, `ALTER ROLE tbcg_admin_control_plane REPLICATION`); err != nil {
		t.Fatal("在专属事务内设置测试组 REPLICATION 属性失败")
	}
	var replication bool
	if err := tx.QueryRowContext(fixture.ctx, `SELECT rolreplication FROM pg_roles WHERE rolname = 'tbcg_admin_control_plane'`).Scan(&replication); err != nil || !replication {
		t.Fatal("同一事务未看到 REPLICATION 反例属性")
	}
	_, err = tx.ExecContext(fixture.ctx, up)
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok || pgErr.Code != "P0001" || !strings.Contains(pgErr.Message, "restricted NOLOGIN role") {
		t.Fatal("真实 003 preflight 未拒绝同事务内的直接 REPLICATION group；错误细节已隐藏")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal("回滚 REPLICATION 属性和失败迁移失败")
	}
}
