# 0004 Leaf 与 Godot 游戏协议 spike

**状态：**进行中（实现及本地联调已通过，等待 Godot 最终审查收口）
**目标：**在真实 Leaf 与 Godot `WebSocketPeer` 之间证明版本化 WSS binary JSON 契约可用，再进入游戏业务。

## 范围

- 在共享协议目录定义 Hello/HelloAck、命令/响应、推送 envelope、稳定消息 ID、协议版本及长度上限。
- 实现 Leaf `network.Processor` Adapter，避免直接使用默认类型名信封；Godot 接收 binary frame 后校验 UTF-8 JSON。
- 游戏层只有收到 HelloAck 并确认协议/内容/能力兼容之后才进入 ready。
- 为未知消息、坏版本、超大载荷、重复请求、未握手消息、断线与有界写队列补充测试。

## 验收标准

- [ ] 真实 Leaf WSS 服务和 Godot headless 客户端完成 Hello/HelloAck 与一条回显命令双向测试。
- [ ] HelloAck 前 ready 信号不会触发；未知 ID/坏版本/超限输入被明确拒绝并留下安全错误。
- [ ] 同一 golden envelope 在 Go 与 GDScript 中解码一致，ID/64 位数值使用明确字符串规则。

## 验证

- Go 执行针对 Processor 的单测与 `go test -race`；Godot 执行约定的 headless 协议测试。
- 真实浏览器或客户端核实 WebSocket frame 语义与重连/关闭流程；任何环境缺失都标阻塞。

### 本地验证记录

- Go 1.26.4 的协议包 race 测试通过，语句覆盖率 87.6%；probe 命令包 84.6%；`go vet ./...` 通过。
- Godot 4.7.1 editor 导入、headless 协议状态机测试通过：HelloAck 前不得 ready、错误包与文本帧拒绝、真实 handshaking timeout、断线关闭和缓冲消息先处理。
- `python3 tools/verify_protocol.py` 已在本机真实运行 Leaf WSS ↔ Godot：使用临时证书与随机令牌完成 HelloAck/Ping/Pong；错误令牌必须在握手阶段失败，不受信 CA 必须出现 TLS 握手错误，且服务器仍存活。不是 mock 替代的 E2E。
- 联调工具 pytest 7 项通过，Black/isort/Ruff 检查通过；结合真实 E2E 执行，脚本自身语句覆盖率 90%（不以测试代码覆盖率冒充工具覆盖率）。临时私钥和测试服务均由工具清理。
- 明确边界：本任务仅实现协议 spike 与受控 loopback probe，不交付生产账号/令牌颁发、重连快照、持续心跳及按时间窗口限流。当前有消息字节上限、连接数/写队列上限、每连接请求数上限与重复 request_id 拒绝；这些不等同于正式生产网关验收。

## 依赖与文件

**依赖：**0001、0002。
**预计文件：**`contracts/protocol/message_catalog.json`、`server/internal/transport/leaf/`、`client/framework/network/`、`tests/contracts/`。
