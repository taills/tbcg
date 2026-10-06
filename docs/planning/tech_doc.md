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
| 模块路径 | `github.com/taills/tbcg/server`（仓库 `git@github.com:taills/tbcg.git`，Go module 位于 `server/` 子目录）。 |

`go.mod` 不设置指向 xyq、本机绝对目录或 Leaf 工作树的 `replace`。Leaf 使用 `/v2` 模块路径。Go 模块校验与依赖图在 Go 1.27.0 和 Go 1.26.4 下均成功；游戏运行面位于 `server/modules/`，入口为 `server/cmd/game-server`。

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
| 质量工具 | Prettier `3.9.9`、Vitest `5.0.3`、Testing Library、jsdom、`@playwright/test 1.63.0` 均精确锁定 |

前端基础校验：

```bash
cd admin
pnpm install --frozen-lockfile
pnpm audit --audit-level high
pnpm format:check
pnpm typecheck
pnpm typecheck:e2e
pnpm test --coverage
pnpm build
pnpm e2e
```

任务 0002 基线结果（当时）：依赖 audit 未发现已知高危公告；format/typecheck、1 项 Vitest 和 Vite production build 通过，当时无管理内容界面及 Playwright。任务 0008 已添加受控登录、item 编辑/预览、版本冲突对比/显式重应用和 Reviewer 队列；Playwright 入口固定在 `@playwright/test 1.63.0`，命令为 `pnpm e2e`，与 Vitest 80% 覆盖门禁分离。2026-10-04 控制面初验为 64 项 Vitest；2026-10-05 最后响应身份校验及 fixture 补强后，全量 75 项通过，typecheck/typecheck:e2e/format/check:item-validators/build/audit 通过，最终生产 bundle 无 `Function`/`eval`/`require` 调用。

## 5. CI 与供应链

`.github/workflows/ci.yml` 在 Pull Request 和 push 至 `main` 时运行，最小权限为 `contents: read`；所有 action 以完整 40 位 commit SHA 固定，并保留对应上游 tag 注释：

- `actions/checkout` v7.0.1
- `actions/setup-go` v7.0.0
- `actions/setup-node` v7.0.0
- `chickensoft-games/setup-godot` v2.4.3

当前 CI 检查 Go modules、Go 源码存在时的 gofmt/goimports/race/coverage/vet、管理前端依赖审计/格式/typecheck/test/build 和 Godot headless 项目导入。Go 源码存在后会安装固定的 `golang.org/x/tools/cmd/goimports v0.51.0`；无 Go 源码时输出 notice 且只验证模块。Go job 使用版本固定的 PostgreSQL 18 service 和一次性 trust-auth 测试容器（仅loopback映射、无生产数据/秘密），并强制设置 URL、confirmation token、required 标记；测试端拒绝非loopback、非55432或非 `postgres` maintenance DB，防止误操作共享数据库。Tern v2.4.3 在 CI 显式安装。Admin 本地 Playwright Firefox E2E 与 `webServer` 配置已固定，但 `.github/workflows/ci.yml` 尚未增加浏览器 E2E job；CI runner 还需提供受信测试 CA、隔离数据库和临时角色/账号 fixture。

本次没有触发 GitHub 远端 workflow；本地成功结果不等于远程 CI 已通过。

## 6. 当前可用命令与尚未验证项

| 检查 | 本机结果 | 说明 |
|---|---|---|
| `GOTOOLCHAIN=go1.26.4 go version` | 通过，输出 Go 1.26.4 | 工具链由 Go 自动下载至本机工具链缓存 |
| `go mod download all` / `go mod verify` / `go list -m all` | Go 1.27.0 和指定 1.26.4 下通过 | Go 模块图与校验和已固定 |
| Go `gofmt` / `goimports` | 全仓格式/import 检查通过 | Go 1.26.4；工具 `goimports v0.51.0` |
| Go `go test -race` / `go build` / `go vet` / `staticcheck` | 控制面与 ACL 验证通过，含隔离 PostgreSQL 18.3 migration、最小权限、TLS 管理 API 用户旅程、受控 bootstrap RED→GREEN；2026-10-05 最终主流程 `resume-final-tests/build/vet/staticcheck/format/mod/gosec` 门禁 status 0（Go 1.26.4、12 包、0 Skip） | RequireDB=1，所有真实 PostgreSQL 测试无 Skip；只使用 loopback 55432、随机凭据与 disposable 确认；拒绝共享 5432/非 loopback/PGHOSTADDR/PGSERVICE 覆盖 |
| 管理 API/PG 覆盖 | `resume-final.coverage` weighted OR：overall 1971/2414 = 81.65%；核心 admin+postgres 961/1174 = 81.86%；admin 596/705 = 84.54%；postgres 365/469 = 77.83%；cmd/admin-server 145/204 = 71.08%；cmd/admin-bootstrap 26/74 = 35.14% | 覆盖匿名/登录、CSRF、CAS、RBAC、不可变 revision/hash、审批、审计回滚、账户凭据哈希、列级 ACL/runtime 写拒绝和 session cleanup 边界；不声明每个 package 都达到 80%，CLI 正确性由真实 workflow E2E 与 bootstrap PG CLI 测试补充证明 |
| Go gosec | 全仓 0 issues（有说明的 G124/G115 suppressions） | CSRF double-submit cookie 值仅绑定 session 内 SHA-256；revision int32 转换前显式检查范围 |
| Admin `pnpm install --frozen-lockfile` / `audit` / `format:check` / `typecheck` / `typecheck:e2e` / `test --coverage` / `check:item-validators` / `build` | 通过；2026-10-05 最终 75 项 Vitest；语句 86.52%、分支 83.02%、函数 90.24%、行 86.31%；audit 无已知高危；bundle 无 `Function`/`eval`/`require` 调用 | 含受控登录、字段式物品编辑/公开预览、code point Unicode 边界、AJV standalone 生成校验器、版本冲突显式重应用、Reviewer 队列和权限恢复 |
| Admin OpenAPI | Redocly CLI `1.34.4 lint` 结构验证通过；语义断言通过 | 1 条 info-license 建议 warning；未加法律许可证声明 |
| Playwright | 历史三轮 Firefox 各 2 passed（6/6）；2026-10-05 最后生产源码修复后追加一轮，2/2 passed | 使用真实 admin server、生产构建 React、PostgreSQL 管理 API、受信临时 CA/NSS profile；未信任 CA profile 必须返回 `SEC_ERROR_UNKNOWN_ISSUER`；未关闭 TLS 验证、未降级 HTTP、未修改系统 Keychain |
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

## 8. 任务 0008 控制面实施阶段边界

任务 0008 分为契约、控制面、隔离构建、真实 runtime owner 激活、Godot 公开包读取五阶段。控制面第一纵切片已验证；阶段2已实现单物品隔离构建纵切片：确定性 builder、逐 Game 受控 build profile、durable job 与受限 worker 进程，build candidates/start/job 三个 HTTP 路由在 OpenAPI 中标记 supported；activation、公共 bundle 与 Godot 读取仍为 planned。任何局部结果都不代表 0008 完成。控制面完整 API 轮廓与错误 envelope 以 `contracts/admin/openapi.yaml` 为准；创建使用 `POST /games/{gameId}/item-drafts`，读写/校验/提交通过 opaque `draftId` 定址；`content_id` 放请求体/查询参数，不占资源路径。只有 `draft_version` 使用十进制字符串并限制为 PostgreSQL signed bigint `0..9223372036854775807`；所有 Definition、public projection、Revision、Approval revision 均为 1..2147483647 整数。build/activate/runtime ack/公开读取标记 planned，不创建伪接口。

本阶段 migration 003 追加 admin_users、admin_sessions、admin_role_bindings、content_drafts、revision_approvals、audit_logs，并继续复用 001 的 content_revisions；草稿含 opaque `draft_id`、absolute/idle session expiry 以及 `submitted_draft_version`，阻止同一 CAS 快照被重复固化。迁移前由部署/隔离测试集群预置 `NOLOGIN tbcg_admin_control_plane` 权限组和应用登录成员；迁移只授予/撤销当前数据库对象权限，down 不删除集群级角色或触碰其他数据库 ACL。应用连接在每个连接建立后 `SET ROLE tbcg_admin_control_plane`。权限组用列级 grants 只开放控制面所需字段，不得写 runtime/player 数据、Realm active pointer、owner guards 或角色绑定；`admin_users`/`admin_role_bindings` 只读，draft 不授 DELETE，revision/approval/audit 不授 UPDATE，账号和角色通过受控 bootstrap 维护。Reviewer 专属审批，Publisher 未来构建/激活；角色不隐式继承，活动指针只能由真实 runtime owner 确认。提交 revision、审批及成功审计同事务；失败/拒绝操作审计独立提交。revision 插入触发器强制 DB 主键与顶层/public game/content/integer revision identity 一致，数据库阻止已提交 revision 的 UPDATE/DELETE/TRUNCATE。阶段2 migration 004 新增逐 Game 受控 `game_build_profiles`（含递增 profile_version 和 asset manifest SHA-256）、`admin_build_jobs` 与 release version counter；build profile/job 快照及 version allocation 不对 admin/worker 直接授权。独立 `tbcg_content_build_owner` 和 `tbcg_content_build_worker` 均由部署预置且为 NOLOGIN；migration 预检角色继承/现存 ACL。admin 角色只获 candidate/read/find/get/enqueue 五个 SECURITY DEFINER 函数的 EXECUTE，worker 角色只获 claim/renew/fail/finish 四个函数的 EXECUTE，两者均无 build、Release 或 runtime 表的直接权限；owner 角色除迁移执行者外不得有成员，worker 登录不得同时属于 admin/owner。函数固定 `search_path`，由函数体推导 Release 身份/版本，只能登记 `built` 且 `activated_at` 为空的 Release；`release_entries` 插入对父 Release 加共享锁以与激活串行化，已登记 entry 不可更新/删除/截断。

管理进程入口为 `server/cmd/admin-server`，默认只监听 `127.0.0.1:8443`，无 TLS 证书时拒绝启动。浏览器管理面按 HTTPS-only 验证，cookie 为 Secure、HttpOnly、SameSite、无 Domain；文档不再承诺 HTTP loopback 浏览器例外。运行需提供 `TBCG_ADMIN_DATABASE_URL`（登录成员继承部署预置 `tbcg_admin_control_plane` 后连接钩子 `SET ROLE`）、TLS cert/key、`TBCG_ADMIN_ORIGIN`，并在项目根构建 `admin/dist`。本地账号通过 `server/cmd/admin-bootstrap` 一次性创建，输入使用 env 和确认标记；bootstrap/业务 E2E 使用受控维护文件、PostgreSQL `verify-full` CA 与受限 app login，密码不打印，不存在账号 CRUD/HTTP route。

内容构建进程入口为 `server/cmd/content-build-worker`，与 admin-server 分离部署、使用独立 OS 身份（生产要求，本地测试不自动创建）与独立数据库登录。需要 `TBCG_BUILD_WORKER_DATABASE_URL`（登录成员只继承预置 `tbcg_content_build_worker`，连接后 `SET ROLE` 并在每个连接上校验无 DDL/表权限/跨角色成员资格，强制 verify-full TLS）、稳定的小写 UUID `TBCG_BUILD_WORKER_ID`（重启后用于取回本 worker 未过期的认领）、绝对路径 `TBCG_BUILD_ARTIFACT_DIR`（必须是非符号链接的 0700 目录，位于 Web 根之外）与 `TBCG_PROJECT_ROOT`（只读取 `contracts` 可信 Schema）。可选 `TBCG_BUILD_WORKER_POLL_INTERVAL`（100ms–1m，默认 5s）与 `TBCG_BUILD_WORKER_LEASE_SECONDS`（5–300，默认 30）。单执行槽：每个作业在租约一半时间内完成，失去租约时不写完成状态；连续 5 次非租约失败后退出交由外部 supervisor 重启。该进程只运行仓库内可信构建代码，不是恶意输入沙箱。

### 0008 控制面验证记录

- 真正 PostgreSQL 18.3/Tern 2.4.3 测试使用 loopback `127.0.0.1:55432` 的专属临时集群；数据库 fixture 创建受限登录成员，真实执行 `SET ROLE` 后验证允许的控制面权限、拒绝玩家/Realm/owner/Release/outbox写入，并验证 migration down 不删除 NOLOGIN 组、应用 membership 或另一数据库 ACL。DB-owned 隔离测试可使用 NoTLS 本地专属集群；生产和业务 E2E 使用 `verify-full`。
- 控制面 Go 测试覆盖匿名/登录会话、token hash、Author 草稿、两个独立 cookie contexts 的大整数 CAS 冲突、未授权 Reader/Publisher 操作、许可图标校验、成功审计失败回滚、不可变 revision/hash 审批与审批后草稿编辑。补充验证包含 main bootstrap UUID actor_id/text resource_id 参数冲突 RED→生产 cast+CLI race GREEN、匿名 roles `nil`→`[]` JSON/HTTP RED→race GREEN、bcrypt known-wrong 期望修正为 1 次错误记录 RED→GREEN。
- 2026-10-05 管理端最终全量为 75 个 Vitest 用例通过，包含 11 个新增 API 响应身份错配反例与合法旧 scope 回包的异步完成检查；Node 24 / pnpm 10.21 下 typecheck、typecheck:e2e、format、AJV standalone validator fingerprint check、生产构建与 audit 均通过；AJV 8.20 是唯一 contracts 静态 standalone ESM 校验器。浏览器 bundle 检查无 `Function`/`eval`/`require`，不需要 `unsafe-eval`；OpenAPI Redocly 结构验证通过，仍有 1 条 `info-license` 建议 warning。
- Playwright 1.63.0 Firefox 真实浏览器 E2E 历史三轮各 2 passed（6/6），2026-10-05 最后生产源码修复后又通过一轮 2/2。Harness 启动真实 admin server、生产构建 React 与 PostgreSQL 管理 API；每轮创建并清理自己的 appDB、受限 login、随机角色、NSS profile 与 server，不停止 main-owned PostgreSQL cluster，不修改系统 CA/Keychain，不复用已有服务。
- 真实浏览器旅程覆盖 Author 两个 context 的 CAS 冲突与 dirty 保留、显式 reapply、提交、Reviewer exact hash 审批、Reader/Publisher 拒绝、Origin/CSRF 拒绝、Secure cookie、双 Game confirm/cancel/accept、回访已保存草稿，以及第三个未信任 CA profile 的 `SEC_ERROR_UNKNOWN_ISSUER`。安全截图只保留未输入账号密码的登录页；不声明 E2E 汇总了 console errors = 0，预期的未信任 TLS handshake 日志不按失败记录。
- 即使控制面 API/PG/TLS/浏览器 E2E 通过，0008 仍进行中；隔离 build、Release manifest/package、真实 runtime owner activation receipt、公共白名单包和 Godot 读取均未实现或验收。详细证据与限制见 [`../verification/0008-stage1.md`](../verification/0008-stage1.md)。

## 9. Leaf/Godot WSS 协议验证（任务 0004）

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

## 10. PostgreSQL 结算与发布故障验证（任务 0007）

- `internal/economy` 以单事务提交钱包、账本、operation result 和 outbox；按 game/realm/actor/operation 作用域核验载荷 hash，事件类型参与 hash 且经白名单校验。消费者凭据与数据库副作用同事务提交，过期租约不能冒领。
- `internal/persistence/commit.go` 统一提交结果分类，使用 PostgreSQL 18 的 `SeverityUnlocalized`，不比较本地化错误文本。明确拒绝/回滚与 FATAL、传输错误、超时造成的 unknown 分离；恢复缺记录不视为回滚。
- `Settle` 重放同样要求当前 owner；接管后的旧命令通过只读 `Recover` 核验，新 owner 可用当前 epoch、原键和原业务载荷重放。真实 PG 测试确认资产不重复。
- Release 激活按 owner guard → realm → Release 锁序，在事务中验证 epoch 并持 guard 共享锁到提交。不可变产物先完整暂存、同步，再原子 no-replace 安装；同内容重试复用，不同内容/不完整目标/符号链接拒绝。
- Release 身份和定义按 game 共享，realm 持各自活动指针；跨 realm 复用不降级已激活状态、不重置首次激活时间。注册和激活均核对全部版本、manifest 与公开/私有产物 hash。
- 2026-10-04 最终真实 PG 18.3 全量 Go race、build、vet、staticcheck 通过；全部 Go 语句覆盖 87.8%，经济模块 83.1%、Release 84.9%。gosec 扫描生产代码 0 条问题。奖励和 Release 均验证真实 COMMIT 成功、测试 driver 丢弃确认后的 pending/恢复路径。
- 本切片不代表管理平台或真实运行进程发布确认已经完成；artifact root 是运维控制的私有目录，尚无公共 HTTP 路由。任务 0008 必须交付真实运行进程加载/确认、管理权限及公开白名单路由。详细故障、复审与未运行项见 [`../verification/0007.md`](../verification/0007.md)。

## 游戏运行进程（2026-10-07）

```sh
cd server
# 开发联调（loopback、无 TLS、直接加载 games/demo/content，标记 dev-local）
TBCG_GAME_DATABASE_URL='postgresql://…@127.0.0.1:55432/<db>?sslmode=…' \
TBCG_GAME_DEV_CONTENT=1 TBCG_GAME_BOOTSTRAP_REALM=1 TBCG_PROJECT_ROOT=.. \
go run ./cmd/game-server
# 生产形态：TLS 证书 + 私有产物目录（只读使用 worker 写入的 0700/0600 产物）
TBCG_GAME_TLS_CERT=… TBCG_GAME_TLS_KEY=… TBCG_BUILD_ARTIFACT_DIR=/abs/private/artifacts …
```

环境变量：`TBCG_GAME_ID`（默认 demo）、`TBCG_REALM_ID`（默认 main）、`TBCG_GAME_WS_BIND`（127.0.0.1:7443）、
`TBCG_GAME_HTTP_BIND`（127.0.0.1:7080）、`TBCG_GAME_CLIENT_MIN_BUILD`、`TBCG_GAME_ALLOWED_ORIGINS`。无 TLS 时只允许 loopback
绑定；非 loopback 的 PostgreSQL 必须 TLS。数据库角色：迁移 005 在存在 `tbcg_game_runtime` NOLOGIN 组时授予运行面所需的
最小表/列权限，生产应为 game-server 使用该组的专用登录。

Godot 客户端：`/Applications/Godot.app/Contents/MacOS/Godot --path client` 运行主场景（登录 → 角色 → 地图/HUD/战斗）。
headless 测试：`--headless --path client --script res://tests/<path>.gd`。真实客户端联调由 Go 测试
`TestGodotClientAgainstGameServer` 启动服务端后调用 Godot 执行 `tests/network/game_client_probe.gd`。

内容构建 worker 现在同时处理单物品与完整 Game 快照作业（同一受限进程与权限组）。
