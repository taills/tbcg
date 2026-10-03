# TBCG 技术基线与开发入口

**状态：**工具链任务 0002 的当前锁定记录。以下区分“版本已锁定”“本机实际验证”和“尚未验证”；锁定不等于整个框架已经实现。

## 1. 唯一数据库决策

- **业务数据库：PostgreSQL ONLY**，开发、CI、测试和生产使用 PostgreSQL；不提供 SQLite/MySQL adapter，也不以 SQLite 替代集成测试。
- PostgreSQL 主版本锁定 **18**；本机 Homebrew client/server 为 **18.3**。`psql --version` / `postgres --version` 已确认 18.3；本机 `pg_isready` 对默认 `/tmp:5432` 返回无响应，表示目前没有可用服务，未运行数据库集成测试。数据库容器和测试库由任务 0003 建立。
- PostgreSQL 驱动依赖锁定 `github.com/jackc/pgx/v5 v5.10.0`；运行时实现应使用 `pgx/v5/stdlib` 的 `database/sql` 适配器，并由 Go 存储模块管理事务与连接生命周期。
- 数据库迁移采用 PostgreSQL 专用 standalone CLI `github.com/jackc/tern/v2 v2.4.3`（Tern 使用 PostgreSQL/pgx，Go 1.25+，MIT）；通过 `go install github.com/jackc/tern/v2@v2.4.3` 固定安装，不加入游戏服务的运行时 `go.mod`。任务 0003 将验证空库、重复迁移和升级行为；连接由 `PG*` 环境变量提供，不在仓库保存 DSN/密码。
- **Leaf 传递依赖说明：**`github.com/taills/leaf/v2 v2.0.3` 自身的 `go.mod` 声明可选 SQLite 实现依赖，因此 `go list -m all`/`go.sum` 会出现 `modernc.org/sqlite`。TBCG 没有直接 SQLite 依赖、driver 注册或 SQLite 打开逻辑，不使用 Leaf SQLite Adapter；这不构成 SQLite 数据库存储支持。若将来要求连依赖图也不得包含 SQLite，需先重新评估 Leaf，而不是偷偷删除校验和或 fork。

## 2. Go 服务端

| 项目 | 锁定值 |
|---|---|
| Go 指令基线 | `go 1.26.4` |
| 本机默认 Go | 1.27.0；同时使用工具链选择验证了 Go 1.26.4 |
| Leaf | `github.com/taills/leaf/v2 v2.0.3` |
| PostgreSQL | `github.com/jackc/pgx/v5 v5.10.0` |
| JSON Schema | `github.com/santhosh-tekuri/jsonschema/v6 v6.0.3` |
| PostgreSQL 迁移 CLI | `github.com/jackc/tern/v2 v2.4.3`；独立安装，不作为游戏运行时依赖 |
| Go 格式化工具 | `gofmt`（与 Go 工具链同版本）+ `golang.org/x/tools/cmd/goimports v0.51.0` |
| 模块路径 | 暂用 `example.com/tbcg/server`；确认代码托管组织/远程仓库后、向外发布前需替换为正式模块路径。 |

`go.mod` 不设置指向 xyq、本机绝对目录或 Leaf 工作树的 `replace`。Leaf 使用 `/v2` 模块路径。Go 模块校验与依赖图在 Go 1.27.0 和 Go 1.26.4 下均成功；任务 0002 尚无应用 Go package，因此不能宣称 `go test ./...` 有业务代码可测。

## 3. Godot 客户端/创作工具

| 项目 | 锁定/检测值 |
|---|---|
| Godot 编辑器 | **4.7.1-stable**；本机实际输出 `4.7.1.stable.official.a13da4feb` |
| 导出模板 | 本机存在 `~/Library/Application Support/Godot/export_templates/4.7.1.stable/`；与编辑器版本一致 |
| 项目目录 | `client/` |
| 图形渲染 | Godot project 配置 `gl_compatibility`，为 2D/桌面兼容基线 |

已在本机执行并通过：

```bash
/Applications/Godot.app/Contents/MacOS/Godot --version
/Applications/Godot.app/Contents/MacOS/Godot --headless --editor --path client --quit
```

CI 使用 `chickensoft-games/setup-godot` 的完整 commit SHA `ac93246ea68518a384c1c280a496e5989341f2f6`（上游 tag `v2.4.3`），固定 Godot `4.7.1`，安装匹配 export templates。当前尚无 export preset，不宣称完成桌面/Web 导出。

## 4. React 管理前端

| 项目 | 锁定值 |
|---|---|
| Node.js | 本机 `v24.20.0`；项目 engine 约束 `^22.22.2 || ^24.15.0 || >=26.0.0` |
| pnpm | `10.21.0`，由 `package.json` packageManager 与 `pnpm-lock.yaml` 锁定 |
| React / React DOM | `19.3.0` |
| Vite | `8.3.2`；`@vitejs/plugin-react 6.1.1` |
| TypeScript | `6.0.3`；本版 `typescript-eslint 8.71.0` peer 上限 `<6.1.0`，当前脚手架暂不引入 ESLint TS 插件 |
| Tailwind CSS | `tailwindcss` / `@tailwindcss/vite` `4.3.3`；官方 Vite plugin 与 Vite 8 peer 范围兼容 |
| 质量工具 | Prettier `3.9.9`（`format:check`）、Vitest `5.0.3`、Testing Library、jsdom 均精确锁定 |

前端基础校验：

```bash
cd admin
pnpm install --frozen-lockfile
pnpm audit --audit-level high
pnpm format:check
pnpm typecheck
pnpm test
pnpm build
```

任务 0002 的本机验证结果：依赖 audit 未发现已知高危公告；format:check 与 typecheck 通过；1 个测试文件、1 个测试通过；Vite production build 通过。Playwright/E2E 入口尚未安装或配置；后续加入 `@playwright/test` 后统一采用 `pnpm exec playwright test`。管理员页面目前只含最小脚手架，不代表内容管理功能已完成。

## 5. CI 与供应链

`.github/workflows/ci.yml` 在 Pull Request 和 push 至 `main` 时运行，最小权限为 `contents: read`；所有 action 以完整 40 位 commit SHA 固定，并保留对应上游 tag 注释：

- `actions/checkout` v7.0.1
- `actions/setup-go` v7.0.0
- `actions/setup-node` v7.0.0
- `chickensoft-games/setup-godot` v2.4.3

当前 CI 检查 Go modules、Go 源码出现后的 gofmt/goimports/race/coverage/vet、管理前端依赖审计/格式/typecheck/test/build 和 Godot headless 项目导入。Go 源码尚不存在时，CI 会明确输出 notice，仅验证依赖，不声称 Go tests 已通过；首次添加 Go package 后自动安装固定的 `golang.org/x/tools/cmd/goimports v0.51.0`，再运行格式检查、`go test -race -cover ./...` 和 `go vet ./...`。Admin 的 Playwright E2E 尚未配置，后续入口计划为 `pnpm exec playwright test`。需要 PostgreSQL 的 migration/集成故障测试将在任务 0003/0007 中使用版本固定的 PostgreSQL 18 service 补入，不在当前工具链任务启动 CI 服务。

本次没有触发 GitHub 远端 workflow；本地成功结果不等于远程 CI 已通过。

## 6. 当前可用命令与尚未验证项

| 检查 | 本机结果 | 说明 |
|---|---|---|
| `GOTOOLCHAIN=go1.26.4 go version` | 通过，输出 Go 1.26.4 | 工具链由 Go 自动下载至本机工具链缓存 |
| `go mod download all` / `go mod verify` / `go list -m all` | Go 1.27.0 和指定 1.26.4 下通过 | 仅检查依赖，无业务包构建 |
| Go `gofmt` / `goimports` | CI 已锁定命令；本机尚无 Go source packages | `goimports` 固定为 `golang.org/x/tools/cmd/goimports v0.51.0`，首次出现 Go 文件后运行 |
| Admin `pnpm audit/format:check/typecheck/test/build` | 全部通过；audit 未发现高危公告 | 测试为初始脚手架 smoke test |
| Playwright E2E | 尚未配置 | 后续入口计划 `pnpm exec playwright test`，目前不可运行 |
| Godot `--version` / headless editor import | 通过，Godot 4.7.1 | 项目场景有效；还没有真实游戏/E2E |
| PostgreSQL 客户端/服务端版本 | 18.3 已安装 | 默认本机服务未运行；没有做迁移、事务或故障测试 |
| GitHub Actions workflow | 文件已锁版本/Actions SHA | 尚未在 GitHub 远端执行 |
| Godot 导出 | 未验证 | 尚无 export preset；版本匹配的模板已安装 |

任务 0003 将补 PostgreSQL 服务配置、migration 工具/版本和真实集成测试入口；任务 0004–0006 分别验证 Leaf/Godot 协议、Schema 和地图几何导出。