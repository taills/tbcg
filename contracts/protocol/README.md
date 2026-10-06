# 协议 spike v1

本目录记录任务 0004 的最小协议，不代表账号系统或完整生产网关已经交付。

## 数据包与握手

- 客户端使用 Godot `WebSocketPeer`，服务端使用 Leaf `network.Processor` 适配器。
- 传输以 WSS WebSocket **binary message** 承载 UTF-8 JSON，不添加 TCP 长度头；WebSocket 分片由底层网络库重组。
- 请求支持 `system.hello`、`system.ping`；响应支持 `system.hello_ack`、`system.pong`。
- 只在凭证验证、游戏/分服/客户端构建/Release/能力协商通过并收到匹配 HelloAck 后进入 ready。
- `request_id` 是最多 128 个 Unicode 字符的非空字符串；它用于请求关联，不代替经济操作的持久化幂等键。
- `server_seq`、非空 `state_version` 用规范非负十进制字符串表示；无聚合版本时 `state_version` 显式为 `null`。
- `data`、`meta` 必须是对象，空值写作 `{}`；`error` 是对象或 `null`。
- 总消息上限为 65,536 字节；额外 envelope 字段、错误 payload 类型、未知消息或不兼容版本均拒绝。

## 可信契约来源

`message_catalog.json` 是稳定消息名称及阶段的目录，两个 envelope Schema 描述载荷形态。Go 的小型类型映射必须由契约测试核对，不使用 Leaf 的类型注册顺序作为消息 ID。Godot 适配器通过依赖注入接收可信目录数据；测试可从仓库目录加载，运行时资源打包不能依赖工作区父目录。

## Probe 安全边界

`protocol-probe` 只绑定 loopback，用环境变量中的临时令牌验证握手；不提供真实玩家账号、令牌颁发、刷新或撤销服务。正式身份验证在后续玩家会话任务中接入。

端到端验证命令 `python3 tools/verify_protocol.py` 构建临时 Go probe，生成一天有效期的 loopback 测试证书及随机令牌，启动 WSS 后调用 Godot headless probe。客户端只信任本次临时证书，保留证书和主机名验证；错误令牌和无受信证书连接必须失败。工具只停止自己启动的进程，临时私钥/令牌不入库。

协议 spike 不等于完整网关：持久化会话恢复、账号/角色授权、运行态快照和正式限流策略由后续任务交付。Leaf 的连接层 API 对消息帧元数据和读 deadline 的暴露范围须在网关深化时单独验证，不能以 Hello/Ping 联调成功冒充生产容量或防护验收。

## 运行面 v1（任务 0009–0015）

`message_catalog.json` 现在登记全部运行面消息：每个客户端请求声明 `required_payload_fields`（可选字段在
`optional_payload_fields`）与 `response_type`（统一为 `<请求类型>_result`）；`phase: "push"` 的服务端消息是推送。

- **握手**：`system.hello` 的 `session_token` 由 game-server 的 `POST /api/v1/sessions` 签发（只存 SHA-256），
  `release_id` 必须等于当前服务中的 Release；不一致返回 `release_mismatch` 并附当前 `release_id`，客户端须先下载新公开包。
- **推送**：`request_id` 以 `push-` 开头、按连接递增；客户端按 `type` 合并到本地镜像，绝不据此本地推进权威状态。
- **幂等**：涉及资产/进度的请求携带 `idempotency_key`（8–128 个安全字符），服务端以 `operation_results` 去重；
  `outcome_unknown` 时必须用同一键重试核验，不能换键。
- **权威**：移动只接受逐步合法的网格路径与当前 `state_version`；战斗只接受本回合授权单位的意图，事件以 `event_seq` 严格排序，
  客户端回放时 HP 只取 `hp_after`。
- 服务端严格解码各载荷（拒绝未知字段）；错误为 `{code, message}`，常见码：`invalid_path`、`too_fast`、`state_conflict`、
  `forbidden`、`not_leader`、`party_busy`、`already_claimed`、`not_participant`、`insufficient_items`、`insufficient_currency`、
  `idempotency_conflict`、`outcome_unknown`、`owner_lost`。

game-server 另提供 HTTP（非管理）接口：`POST /api/v1/accounts`、`POST /api/v1/sessions`、`GET /api/v1/realm`、
`GET /api/v1/releases/{releaseId}/public`（仅当前服务中 Release 的公开白名单包，响应头带 `X-TBCG-Bundle-SHA256`）。私有包没有任何 HTTP 路由。
