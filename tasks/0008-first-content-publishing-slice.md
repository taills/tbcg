# 0008 第一个内容编辑及发布纵切片

**状态：**开发完成（待评审）——控制面（2026-10-04）、单物品隔离构建（2026-10-07）、完整 Game 快照构建、runtime owner 激活与 Godot 公开包读取（2026-10-07）均已实现并本机验证；独立评审与 Firefox E2E 扩展未完成
**目标：**完成一个物品 Definition 从网页草稿、校验、审批到 Go/Godot 可读取 Release 的端到端内容路径。按“契约 → 控制面 → 隔离构建 → 真实 runtime owner 激活 → Godot 公开包读取”分阶段验收。

## 范围

- 实现管理员身份、最小 RBAC、审计事件与乐观锁草稿 revision。
- React 物品编辑/预览调用 Go 管理 API；字段错误回传到表单，非法引用不能发布。
- 构建不可变 Release manifest/hash，生成服务端私有与客户端公开投影并经运行进程确认激活。
- staging/激活失败保留旧 release_id，公开接口不得读取服务端私有字段。

## 验收标准

- [x] 控制面阶段：Game 绑定 Reader/Author/Reviewer/Publisher 可分离或兼任；Reviewer 才能审批；Publisher 不隐式获得 Reviewer 权限。
- [x] 后续阶段：只有 Publisher 可请求构建/激活，且 runtime owner 独占活动指针确认。
- [x] 控制面阶段：同一草稿 revision 冲突返回可操作错误，浏览器保留本地输入并要求用户显式 reapply；审批后 revision 数据不可原地改写。
- [x] 后续阶段：Release manifest/hash 可重现，并由隔离构建产物证明。
- [x] 后续阶段：Godot 客户端只获取公开数据；私有 drop/reward 字段通过 API/静态路径均不可访问。

## 验证

- [x] 控制面：Go API/PostgreSQL 集成测试、ACL/迁移/受控 bootstrap 测试、React 单测与 Firefox 真实浏览器 E2E 覆盖登录、编辑、CAS 冲突、提交、审批、权限拒绝、Origin/CSRF 和 TLS 负向路径。
- [x] 后续阶段：staging 运行中修改草稿，确认旧激活版本持续服务到新版本被 runtime owner 确认。
- [x] 后续阶段：公开白名单 bundle/Godot adapter 读取验证，确认私有字段不可达。

## 阶段与本轮范围

1. **契约冻结**：以 `contracts/admin/openapi.yaml` 维护完整 API 轮廓并标明 `supported`/`planned`。当前支持 session/login/logout、`POST /games/{gameId}/item-drafts`、`GET/PUT /games/{gameId}/item-drafts/{draftId}`、draftId 下的 validate/submit、review queue 与 body 指定 `content_id + integer revision + body_hash` 的审核；content_id 不作资源路径段。build/activation/runtime owner 确认/public bundle/Godot 读取仅为 planned。除 CAS `draft_version` 使用十进制字符串外，所有 revision（Definition/public/Approval）均为 1..2147483647 的整数。
2. **控制面（已验证）**：003 迁移、受控 NOLOGIN PostgreSQL 权限组、认证/CSRF/RBAC、Game 绑定、opaque draftId/CAS 草稿、schema/许可 manifest 引用校验、服务器分配并由数据库身份触发器保护的不可变 revision、Reviewer exact hash 审批、成功事务审计与拒绝独立审计。migration 用 submitted_draft_version CAS 防止同一草稿快照被重复固化。Go/真实 PG/可信 TLS 管理 API、React 单测与 Firefox 浏览器用例已分层验收；记录见 [`../docs/verification/0008-stage1.md`](../docs/verification/0008-stage1.md)。
3. **隔离构建（单物品纵切片已验证）**：Publisher 在 `GET /games/{gameId}/build-candidates` 选择已批准的 exact `content_id + revision + body_hash`，`POST /games/{gameId}/builds` 以 Game/actor/key 作用域的 `Idempotency-Key` 入队；同一请求重放返回同一 job/Release，不同请求 409，提交确认丢失返回 503 并要求同 key 重试。入队在同一事务内冻结 canonical Definition、逐 Game 受控 build profile（`profile_version` 行锁线性化，固定原始资产 manifest SHA-256）、Schema fingerprint、代码固定 builder 版本与原创资产字节，并在 Game 计数器行锁内预留 Release 版本（允许空洞，不重编号）。独立 `content-build-worker` 进程以 NOLOGIN worker 角色、verify-full TLS 和四个 SECURITY DEFINER 函数认领（claim token/lease/有界 attempt）、构建确定性 `single_item` public/private 包，原子 no-replace 写入 Web 根之外的 0700/0600 私有目录，再在同一事务登记 `built` Release、单条 entry 与审计；不激活、不改 Realm 指针。profile 不由浏览器传入；无 profile、身份/hash/许可不一致均 fail-closed；capabilities 为空，不称完整 Game 快照。Publisher UI 显示“已构建，尚未激活”。验证记录见 [`../docs/verification/0008-stage2.md`](../docs/verification/0008-stage2.md)。
4. **runtime 激活（已实现）**：Publisher 经 `POST /games/{gameId}/realms/{realmId}/activations`（Idempotency-Key）登记激活请求；持有 owner_epoch 的 game-server 读取私有产物、复核哈希/Schema/跨引用/版本后在同一事务切换 `realms.active_release_id`；失败记 `rejected`（如 `artifact_missing`），旧指针与内存内容继续服务。完整 Game 快照由 005 迁移的 `admin_snapshot_jobs` 与同一受限 worker 构建。
5. **Godot 读取（已实现）**：game-server 只提供当前服务中 Release 的公开包 HTTP 路由；Godot `framework/content/public_bundle.gd` 校验 SHA-256、scope 与条目白名单形状，真实 headless 客户端联调确认私有字段与私有包均不可达。验证见 [`../docs/verification/0008-0015-runtime.md`](../docs/verification/0008-0015-runtime.md)。

控制面 partial 不得将任务 0008 标记完成，也不得宣称完整发布链路 E2E。首次资产闭包限定原创占位 icon，manifest 记录来源、SHA-256 与许可；缺失或无许可的 `public.icon_id` 必须校验失败。不得引入任意上传或 xyq 素材。

## 控制面第一纵切片验证摘要（2026-10-04）

- 真实 PostgreSQL 18.3/Tern 2.4.3、受限 `tbcg_admin_control_plane`、列级 ACL、runtime/player 写拒绝、migration down 角色保留、role replication/重叠权限拒绝、owner guard 与 content revision 不可变保护已验证。
- `admin-bootstrap` 为受控一次性入口；验证包含 main bootstrap UUID actor_id/text resource_id 参数冲突 RED→生产 cast+CLI race GREEN。
- session/login 覆盖随机 token hash、CSRF hash、absolute/idle expiry、匿名 roles `nil`→`[]` JSON/HTTP RED→race GREEN、bcrypt known-wrong 期望修正 RED→GREEN。bootstrap 的所有连接 fallback 均验证 loopback；数据库连接拒绝账号/RBAC 表的额外直接/继承列写权限，最后反例已由真实 PG RED 转 race GREEN。
- 2026-10-05 React 最终复验：7 个文件、75 项 Vitest 通过，Statements 86.52%、Branches 83.02%、Functions 90.24%、Lines 86.31%；typecheck、typecheck:e2e、format、AJV standalone validator fingerprint check、build、audit 通过。新增 11 个 API 响应身份错配反例 RED→GREEN，拒绝跨 Game/内外 revision identity/审批请求对应关系错配；合法 fixture 和旧 scope 完整审批回包的异步断言同步补强。
- Firefox 真实浏览器 E2E：2026-10-04 历史三轮各 2 passed（共 6/6）；2026-10-05 最后生产源码修复后另跑一轮，2/2 passed，本次专属 appDB/role 已清理。覆盖 Author 双 context CAS/dirty/reapply/submit，Reviewer exact hash 审批，Reader/Publisher 拒绝，Origin/CSRF 拒绝，Secure cookie，双 Game confirm/cancel/accept，回访已保存草稿，未信任 CA profile 返回 `SEC_ERROR_UNKNOWN_ISSUER`。
- 浏览器管理面按 HTTPS-only 记录；旧 HTTP loopback 浏览器例外不再是任务承诺。DB-owned 隔离测试的 NoTLS 只限专属本地测试集群；生产和业务 E2E 使用 PostgreSQL `verify-full`。
- 2026-10-05 最终主流程 Go gate status 0：Go 1.26.4、RequireDB=1、`-race -count=1`，12 个包通过且 0 Skip；包括审计/审批表篡改 ACL 与稳定权限拒绝原因断言。weighted OR overall 1971/2414 = 81.65%，核心 admin+postgres 961/1174 = 81.86%，admin 84.54%，postgres 77.83%，cmd/admin-server 71.08%，cmd/admin-bootstrap 35.14%。该数字不声明每个 package 都达到 80%；CLI 正确性由真实 workflow E2E 与 bootstrap PG CLI 测试补充证明。
- 已知 LOW 限制仍保留：session cleanup 有界批处理（超过 1000/min 可能积压），新 source/key 在容量满时有界扫描并 fail-closed 返回 429，可能拒绝合法新来源。登录限制是 IP 与 IP+账号组合额度，不是全局账号预算/MFA。最终定点复审无新增阻断项。OpenAPI 仍有 1 条 `info-license` 建议 warning。GitHub Actions 远端 workflow 未由本记录触发；本轮未重跑未修改的 Python/Godot 验证。

## 依赖与文件

**依赖：**0003、0005、0007。
**预计文件：**`server/internal/admin/`、`server/internal/persistence/postgres/admin*.go`、`server/migrations/003_admin_control_plane.sql`、`server/cmd/admin-server/`、`contracts/admin/openapi.yaml`、`admin/src/features/content/`、`docs/verification/0008-stage1.md`。
