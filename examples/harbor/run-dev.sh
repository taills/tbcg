#!/usr/bin/env bash
# 不用 Docker、以开发模式直接运行「雾港奇谭」：game-server 加载 examples/harbor/content，Release 标记为 dev-local。
# 框架按约定读取 <项目根>/games/<id>/content 与 <项目根>/contracts；这里用符号链接拼出一个临时项目根，不改动框架。
# 前置：TBCG_GAME_DATABASE_URL 指向已执行 server/migrations 全部迁移的 PostgreSQL（loopback 可不启用 TLS）。
# 完整的管理后台与发布流程请用同目录的 docker-compose.yml。
set -euo pipefail

: "${TBCG_GAME_DATABASE_URL:?请设置 TBCG_GAME_DATABASE_URL（已迁移的 PostgreSQL 连接串）}"
command -v psql >/dev/null || { echo "需要 psql 来登记 Game 身份" >&2; exit 1; }

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
stage="$(mktemp -d "${TMPDIR:-/tmp}/tbcg-harbor.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/games"
ln -s "$root/contracts" "$stage/contracts"
ln -s "$here" "$stage/games/harbor"

# realms 外键引用 games；首次运行登记 Game 身份，重复运行无副作用。
psql "$TBCG_GAME_DATABASE_URL" -v ON_ERROR_STOP=1 -q \
  -c "INSERT INTO games (game_id, display_name) VALUES ('harbor', '雾港奇谭') ON CONFLICT (game_id) DO NOTHING"

cd "$root/server"
TBCG_GAME_ID=harbor \
TBCG_GAME_DEV_CONTENT=1 \
TBCG_GAME_BOOTSTRAP_REALM=1 \
TBCG_PROJECT_ROOT="$stage" \
  go run ./cmd/game-server
