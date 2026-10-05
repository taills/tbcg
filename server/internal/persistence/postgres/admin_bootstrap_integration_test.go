package postgres

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"testing"
)

func TestAdminBootstrapCreatesAccountBindingAndAuditAtomically(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID := randomTestIdentifier(t, "bootstrap_")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO games (game_id,display_name) VALUES ($1,'测试游戏')`, gameID); err != nil {
		t.Fatalf("准备 bootstrap 游戏：%v", err)
	}
	var databaseName string
	if err := store.db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("读取本次隔离数据库名称：%v", err)
	}
	dsn, err := databaseURLForTestDatabase(os.Getenv("TBCG_TEST_DATABASE_URL"), databaseName)
	if err != nil {
		t.Fatal("无法准备 bootstrap 测试连接配置")
	}
	var secret [24]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal("无法创建一次性测试密码")
	}
	username := randomTestIdentifier(t, "bootstrap_user_")
	command := exec.CommandContext(ctx, "go", "run", "./cmd/admin-bootstrap")
	command.Dir = findServerRoot(t)
	command.Env = append(os.Environ(),
		"GOTOOLCHAIN=go1.26.4",
		"TBCG_ADMIN_BOOTSTRAP_CONFIRM=I_UNDERSTAND_LOCAL_ADMIN_BOOTSTRAP",
		"TBCG_ADMIN_BOOTSTRAP_DATABASE_URL="+dsn,
		"TBCG_ADMIN_BOOTSTRAP_USERNAME="+username,
		"TBCG_ADMIN_BOOTSTRAP_PASSWORD="+hex.EncodeToString(secret[:]),
		"TBCG_ADMIN_BOOTSTRAP_GAME_ID="+gameID,
		"TBCG_ADMIN_BOOTSTRAP_ROLES=Reader",
	)
	// 不输出子进程环境或原始错误命令，防止维护凭据进入测试日志。
	if _, err := command.CombinedOutput(); err != nil {
		t.Fatal("受控 bootstrap 命令失败，账号、绑定和审计应在同一事务中成功")
	}
	var accounts, bindings, audits int
	if err := store.db.QueryRowContext(ctx, `
SELECT
 (SELECT count(*) FROM admin_users WHERE username=$1),
 (SELECT count(*) FROM admin_role_bindings b JOIN admin_users u ON u.admin_id=b.admin_id WHERE u.username=$1 AND b.game_id=$2 AND b.role='Reader'),
 (SELECT count(*) FROM audit_logs a JOIN admin_users u ON u.admin_id=a.actor_id WHERE u.username=$1 AND a.game_id=$2 AND a.action='admin.bootstrap' AND a.resource_id=u.admin_id::text)`, username, gameID).Scan(&accounts, &bindings, &audits); err != nil {
		t.Fatalf("核对 bootstrap 原子结果：%v", err)
	}
	if accounts != 1 || bindings != 1 || audits != 1 {
		t.Fatalf("bootstrap 结果数量为账号 %d、绑定 %d、审计 %d，期望各一条", accounts, bindings, audits)
	}
}
