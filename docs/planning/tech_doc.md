# TBCG 技术基线与开发入口

**状态：**工具链任务 0002 的当前锁定记录。以下区分“版本已锁定”“本机实际验证”和“尚未验证”；锁定不等于整个框架已经实现。

## 1. 唯一数据库决策

- **业务数据库：PostgreSQL ONLY**，开发、CI、测试和生产使用 PostgreSQL；不提供 SQLite/MySQL adapter，也不以 SQLite 替代集成测试。
- PostgreSQL 主版本锁定 **18**；本机 Homebrew client/server 为 **18.3**。`psql --version` / `postgres --version` 已确认 18.3；Unix socket 默认检查 `/tmp:5432` 无响应，但 TCP 5432 已被一个无关 Docker 服务占用，禁止复用或停止。任务 0003 的本机集成测试要求显式设置 disposable 确认标记，只接受 loopback `127.0.0.1:55432` 上的 `postgres` 维护库，拒绝默认5432/非loopback并清除/拒绝继承的 `PGHOSTADDR`、`PGSERVICE` 路由覆盖；测试使用独立临时数据目录与随机凭据，结束后仅关闭并清理自己创建的集群。
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

`go.mod` 不设置指向 xyq、本机绝对目录或 Leaf 工作树的 `replace`。Leaf 使用 `/v2` 模块路径。Go 模块校验与依赖图在 Go 1.27.0 和 Go 1.26.4 下均成功；当前只实现 PostgreSQL persistence 基础切片，游戏网络/领域模块仍未实现。

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

当前 CI 检查 Go modules、Go 源码存在时的 gofmt/goimports/race/coverage/vet、管理前端依赖审计/格式/typecheck/test/build 和 Godot headless 项目导入。Go 源码存在后会安装固定的 `golang.org/x/tools/cmd/goimports v0.51.0`；无 Go 源码时输出 notice 且只验证模块。Go job 使用版本固定的 PostgreSQL 18 service 和一次性 trust-auth 测试容器（仅loopback映射、无生产数据/秘密），并强制设置 URL、confirmation token、required 标记；测试端拒绝非loopback、非55432或非 `postgres` maintenance DB，防止误操作共享数据库。Tern v2.4.3 在 CI 显式安装。Admin 的 Playwright E2E 尚未配置，后续入口计划为 `pnpm exec playwright test`。

本次没有触发 GitHub 远端 workflow；本地成功结果不等于远程 CI 已通过。

## 6. 当前可用命令与尚未验证项

| 检查 | 本机结果 | 说明 |
|---|---|---|
| `GOTOOLCHAIN=go1.26.4 go version` | 通过，输出 Go 1.26.4 | 工具链由 Go 自动下载至本机工具链缓存 |
| `go mod download all` / `go mod verify` / `go list -m all` | Go 1.27.0 和指定 1.26.4 下通过 | Go 模块图与校验和已固定 |
| Go `gofmt` / `goimports` | 本机对当前 Go 源码已格式化；CI 固定 `golang.org/x/tools/cmd/goimports v0.51.0` | Go module/persistence tests 使用 Go 1.26.4 |
| Go `go test -race -cover ./...` / `go vet ./...` | 真实 PostgreSQL 隔离测试通过；Postgres persistence statements 覆盖率 98.1%；race 与 vet 通过 | 测试临时初始化 PG 18.3 私有集群，使用 loopback 55432、随机凭据与显式确认标记并清理；拒绝共享5432、非loopback和 PGHOSTADDR/PGSERVICE 覆盖 |
| Admin `pnpm audit/format:check/typecheck/test --coverage/build` | 全部通过，16 项测试；语句 94.2%、分支 81.63%；audit 无已知漏洞 | 包含共享内容 Schema、公开投影、身份匹配及原型链属性拒绝 |
| Playwright E2E | 尚未配置 | 后续入口计划 `pnpm exec playwright test`，目前不可运行 |
| Godot headless 与 WSS probe | 通过，Godot 4.7.1 | 协议/内容脚本与真实 Leaf WSS 联调通过；尚无游戏玩法 E2E |
| PostgreSQL 客户端/服务端版本 | 18.3 已安装 | task0003测试使用独立临时PG18.3集群和55432 loopback端口，结束后已停止并清理；共享5432未触碰 |
| Tern schema migration / PostgreSQL UoW | v2.4.3 通过真实 PostgreSQL 空库测试 | 两次up、down到0、up重放、scope约束/字段版本/owner guard保护及Game Repo提交/回滚/唯一冲突均通过；state_version并发CAS通过 |
| GitHub Actions workflow | 文件已锁版本/Actions SHA | 尚未在 GitHub 远端执行 |
| Godot 导出 | 未验证 | 尚无 export preset；版本匹配的模板已安装 |

任务 0003 已完成 PostgreSQL schema/migration 与基础 repository/UoW 切片，Go integration 使用临时隔离 PostgreSQL 18.3 完成 race、迁移 down/up、幂等冲突、owner_guard 防回退/删除/跨域迁移及 state_version 并发 CAS 测试；package statement coverage 98.1%，go vet 通过。Realm 接管与运行事务两连接竞态、业务 commit unknown 恢复及真实奖励 outbox 故障注入仍由任务 0007 验证，不因当前 Schema/基础 UnitOfWork 测试通过而宣称已完成。任务 0004–0006 分别负责 Leaf/Godot 协议、共享 Schema 和地图几何导出。

## 7. 内容 Schema 投影验证（任务 0005）

- 单一来源：`contracts/content/item.schema.json` 与 `item-public.schema.json`。字段约定、错误码和受限投影说明见 `contracts/content/README.md`。
- Go 使用 `jsonschema/v6 v6.0.3`，管理端使用 `ajv 8.20.0` 的 Draft 2020-12 + strict/ownProperties；不动态加载远程 Schema。Go Schema 包的 race 测试通过，语句覆盖率 88.2%。
- Godot 接收可信 Schema Dictionary，构造仅含公开字段的 Resource，生产适配器不读取工作区父目录。headless fixture 校验通过，尚无 GDScript 覆盖率统计。
- Vite 仅允许 `admin/` 和 `contracts/content/` 两个明确目录；已验证本地开发页、应用入口和 Schema 模块可加载。
- 管理端使用 `@vitest/coverage-v8 5.0.3`，四项覆盖指标门槛均为 80%。统计包含手写业务代码；排除测试文件、测试 setup 和 DOM 挂载入口 `main.tsx`，不排除内容校验逻辑。
- 当前选择手写受限适配器而不是通用三语言代码生成器。变更必须运行同一组 canonical fixtures；公开字段与顶层身份不一致会拒绝，金额保持十进制字符串。

## 8. Leaf/Godot WSS 协议验证（任务 0004）

- `server/internal/transport/leaf` 提供受限协议适配器；`cmd/protocol-probe` 只绑定 loopback，要求显式临时令牌，不是正式账号认证服务。
- 当前消息为 Hello/HelloAck/Ping/Pong；默认响应 data/meta 为对象，拒绝未知字段/类型/版本、重复请求 ID 和超额请求。生产心跳、限流、账号生命周期与重连快照留在后续任务。
- Go 协议包 race 测试通过，覆盖率 87.6%；probe 包 84.6%。Godot 状态机 headless 测试通过；不把 GDScript 覆盖率写成已测量。
- 真实跨端验证：在仓库根目录执行 `python3 tools/verify_protocol.py --godot /path/to/godot`，或通过 `GODOT` 指定 4.7.1 编辑器。脚本使用系统 OpenSSL、Go 1.26.4 和 Python 3.9+，创建并清理自己的临时证书、令牌、二进制及进程。
- 正向联调验证可信 TLS、HelloAck 与 Ping/Pong；负向用例分别断言认证阶段拒绝和 TLS 握手失败，避免任意非零退出造成假阳性。禁止 `client_unsafe`。
- 工具测试依赖固定在 `tools/requirements-test.txt`。pytest 7 项通过，Black/isort/Ruff 检查通过；单元测试加真实 WSS 执行后，联调脚本自身覆盖率 90%。
- CI 已加入两个 Godot headless 脚本、真正 WSS 联调及管理端覆盖率门禁；Godot 步骤同时检查退出码和 `SCRIPT ERROR`，避免引擎运行错误仍返回 0。CI 配置仅本地语法验证，尚未向远端推送/运行。
- 尚未完成 Godot 导出/PCK 资源打包，当前也没有可玩的地图、战斗、内容管理界面或生产游戏服务。

## 9. 地图空间创作与导出（任务 0006）

- 地图空间 Schema 为 `contracts/content/map_geometry.schema.json`，约定详见同目录 `map_geometry.md`；最大 256×256 格、1024 锚点，0 可走/1 阻挡，拒绝旋转/缩放/非整数原点等不支持的空间布局。
- Godot 已启用 `client/addons/tbcg_authoring/plugin.cfg`。在原创 `client/games/demo/maps/training-ground.tscn` 中选择地图根，通过“项目 → 工具 → 校验/导出当前地图”生成确定性 JSON。
- `spatial_grid.gd` 为生产空间规则，Exporter 与共享向量测试均直接使用；首个 spawn 的可达集合只计算一次。导出器不会改写输入场景，锚点按稳定 ID 排序，与节点顺序无关。
- Go `internal/content/mapdata` 从受信 Schema 编译不可变几何快照，拒绝坏尺寸、重复/越界/阻挡/不可达锚点；支持边界、像素/格转换、禁止对角切墙及有界 BFS。
- 本机 Go 1.26.4 race：mapdata 覆盖率94.4%，map-validate CLI92.3%；build/vet通过。Godot4.7.1 headless editor、场景/写入测试通过；真实挂载 SceneTree 的地图也能导出。
- `python3 tools/verify_map_export.py --godot <4.7.1编辑器>` 重复从真实场景导出，校验字节确定性和golden语义，再调用Go CLI；真实跨端验证通过，工具覆盖率91%。与WSS工具合并运行11项Python单测通过，Black/isort/Ruff通过。
- 写入失败会检查 store/flush 错误并报告失败；不冒充完整资源包发布或原子持久化。未测量GDScript覆盖率；未执行真实人工菜单点击及导出包验收，后续创作体验与发布任务继续补足。