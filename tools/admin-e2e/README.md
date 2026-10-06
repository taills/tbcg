# Admin Firefox 业务 E2E

本 harness 只运行真实 admin server、生产构建的 React 静态页面和 PostgreSQL 管理 API；不 mock API/数据库，不复用已有服务。它在每次运行中创建独立临时 OpenSSL CA/localhost leaf 证书和六个全新 NSS profiles，五个 profile 信任该 CA，一个 profile 不导入 CA，用于验证 Firefox 明确报告 `SEC_ERROR_UNKNOWN_ISSUER`。它不会修改系统/全局 CA、Keychain 或用户 Firefox profile，也不会关闭 TLS 验证。

## 前提

- 已安装 Node/pnpm 项目依赖、Playwright Firefox 155/build 1543、`/opt/homebrew/bin/certutil` 和 `/usr/bin/openssl`。
- Backend 已确认 0008 E2E 服务端实现 GREEN；main-owned PostgreSQL 18 测试集群仅用于本机 disposable fixtures。输入是 main 提供的 `0600` maintenance env JSON，必须指向 `127.0.0.1:55432/postgres`，并包含 Tern 迁移需要的 TLS `verify-full` 与 CA 路径。Harness 只把拆分后的 `PG*` 参数送给受控 setup/cleanup 子进程，不输出或传递原始 DSN，也不使用 JSON 中非环境字段 `root`。
- Harness 为每次运行创建随机 appDB/受限 login、执行 `/tmp/tbcg-tern-bin/tern` v2.4.3 迁移和受控 bootstrap，绑定 `demo` 与字典序较后的 `zz-e2e-scope` 两个 Game，再在临时目录生成 `0600` 业务 JSON：`TBCG_ADMIN_DATABASE_URL` 与四角色各自的 username/password 字符串。Author 在两个 Game 都有 Author role，默认 scope 为 `demo`；第二个 Game 不需要内容或发布数据。不得使用受限 app login 提权。Harness 测试结束后只清理本次创建的随机 DB/login/NOLOGIN group（仅当由本次创建），不停止集群。
- Go server 读取 harness 设置的 `TBCG_ADMIN_BIND`、`TBCG_ADMIN_ORIGIN`、`TBCG_ADMIN_TLS_CERT`、`TBCG_ADMIN_TLS_KEY` 和 `TBCG_PROJECT_ROOT`；数据库 URL 由隔离的 server launcher 从临时 `0600` 文件传给 Go 子进程，不进入 Playwright Node 环境。Launcher 固定启动 `go run ./cmd/admin-server`，不接受自定义 shell 命令。

- 阶段2起，fixture 还会准备 `tbcg_content_build_owner` / `tbcg_content_build_worker` 两个 NOLOGIN 角色（只在本次创建时由本次清理）和一个随机 worker 登录，并在迁移后为 `demo` 写入受控 build profile（manifest SHA-256 由仓库原创 `games/demo/assets/manifest.json` 原始字节计算）。worker 的 verify-full DSN 只写入单独的 `0600` 文件，由 `start-worker.mjs` 仅注入 `go run ./cmd/content-build-worker` 子进程；Playwright Node、Firefox 与 admin-server 均不接收该 DSN，日志按值遮盖。私有产物目录位于临时目录（`0700`，不在 Web 根下），运行结束后 harness 检查至少生成一个 Release manifest 且所有产物为仅属主权限，并随临时目录删除。

## 运行

在 `admin/` 目录运行：

```sh
TBCG_ADMIN_E2E_MAINTENANCE_ENV_FILE=/absolute/path/to/private-maintenance-env.json \
TBCG_ADMIN_E2E_PG_CA_CERT=/absolute/path/to/public-postgres-ca.crt \
pnpm e2e
```

Harness 先基于 verify-full maintenance env 创建本次独占 appDB/login与fixture，写出临时 `0600` 业务 env，再构建 `admin/dist`。Go server 使用仅含受限 app DB URL 的临时 `0600` 文件；server launcher 只把 app URL 传入 Go 子进程。Playwright Node 只接收测试角色凭据，Firefox 环境使用系统 allowlist（不含 `TBCG_*`/数据库/CA变量），Vite build 不接收 E2E 凭据。Node Playwright 启动前设置 `NODE_EXTRA_CA_CERTS`。服务 URL 必须是 `https://localhost:<port>` 或 `https://127.0.0.1:<port>`。`reuseExistingServer` 关闭；若端口占用，测试失败而不会接入已有服务。测试结束后等待 server/浏览器退出，再 drop 自己的随机 appDB/login 与只在本次创建的 NOLOGIN role，不停止 main-owned PostgreSQL 集群。

业务旅程验证 Author 两个隔离 Firefox profile 的草稿 CAS 冲突及保留本地输入、明确 reapply、提交不可变 revision、Reviewer 对确切 revision/hash 审批，以及 Reader/Publisher 的 UI 和 API 写权限拒绝。随后独立 Publisher 在构建面板选择该确切 revision，真实 worker 进程完成后界面显示“已构建，尚未激活”与公开包 hash；私有包不能通过静态路径读取，Author/Reviewer/Reader 读取候选与提交构建均为 403。Reviewer 完成审批后，Author 在 `demo` 中输入未保存编辑，验证切换到 `zz-e2e-scope` 时取消保留当前 scope/输入、确认后切换并清除该 scope 的编辑，随后回到 `demo` 确认此前持久化草稿仍可恢复。还会检查 Secure/HttpOnly/SameSite Cookie 属性、跨 Origin 拒绝和错误 CSRF token 拒绝。另有一个未信任 CA 的 Firefox profile，要求浏览器返回指定证书颁发者错误。所有上下文由测试关闭；Harness 只清理自己创建的临时 CA 密钥/证书和 NSS profiles，不触碰 PostgreSQL 集群。

## 数据与产物

- E2E 角色账号 secret 只进入 Playwright Node 测试进程；app DB URL 写入 `0600` 临时 server 配置，由 launcher 仅注入 Go server 子进程，Playwright Node 与 Firefox 不接收 DB URL。Firefox 使用系统环境变量 allowlist，Vite build 过滤 TBCG/数据库/密码类变量。Harness 只保留首次加载页面且尚未输入任何凭据时的安全截图（`0600`）；不启用自动 screenshot、video 或 trace，避免持久化凭据和会话数据。输出及保存日志会遮盖业务 JSON 中的值。
- 经过去敏的运行日志保存在 `admin/test-results/secure-e2e/`，路径会在运行结束时打印。该目录可能含本地业务测试细节，不应上传为公开 artifact。
- 失败状态以 Playwright 退出码报告；TLS 负向检查只有在错误文本包含 `SEC_ERROR_UNKNOWN_ISSUER` 时才通过，不以任意导航失败作为成功。
- Harness 仅创建/删除自己随机命名的 appDB、app login 和必要的测试 NOLOGIN group；绝不初始化、停止或 drop main-owned PostgreSQL cluster，不删除共享 Playwright browser cache，也不代表整个 task 0008、runtime 或 Godot 流程验收完成。
