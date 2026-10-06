#!/bin/sh
# 一次性初始化（可重复运行）：执行框架迁移 → 登记 Game 与构建配置 → 创建受限数据库登录 → 创建管理员。
# 连接参数来自 PG* 环境变量（数据库超级用户，verify-full TLS）。
set -eu

until pg_isready -q; do
  echo "waiting for PostgreSQL..."
  sleep 1
done

TERN_MIGRATIONS=/app/migrations tern migrate >/tmp/tern.log 2>&1 || { cat /tmp/tern.log; exit 1; }
echo "migrations applied"

manifest_sha=$(sha256sum /app/games/harbor/assets/manifest.json | cut -d' ' -f1)
psql -v ON_ERROR_STOP=1 -q \
  -v manifest_sha="$manifest_sha" \
  -v admin_db_password="$HARBOR_ADMIN_DB_PASSWORD" \
  -v worker_db_password="$HARBOR_WORKER_DB_PASSWORD" \
  -f /app/docker/setup.sql

exists=$(psql -At -v username="$HARBOR_ADMIN_USERNAME" <<'SQL'
SELECT count(*) FROM admin_users WHERE username = :'username';
SQL
)
if [ "$exists" = "0" ]; then
  TBCG_ADMIN_BOOTSTRAP_CONFIRM=I_UNDERSTAND_LOCAL_ADMIN_BOOTSTRAP \
  TBCG_ADMIN_BOOTSTRAP_DATABASE_URL="postgresql://${PGUSER}@${PGHOST}:${PGPORT}/${PGDATABASE}?sslmode=verify-full&sslrootcert=${PGSSLROOTCERT}" \
  TBCG_ADMIN_BOOTSTRAP_USERNAME="$HARBOR_ADMIN_USERNAME" \
  TBCG_ADMIN_BOOTSTRAP_PASSWORD="$HARBOR_ADMIN_PASSWORD" \
  TBCG_ADMIN_BOOTSTRAP_GAME_ID=harbor \
  TBCG_ADMIN_BOOTSTRAP_ROLES=Reader,Author,Reviewer,Publisher \
  admin-bootstrap
  echo "created admin account $HARBOR_ADMIN_USERNAME"
fi
echo "setup complete"
