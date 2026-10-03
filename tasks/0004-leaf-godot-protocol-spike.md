# 0004 Leaf 与 Godot 游戏协议 spike

**状态：**进行中
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

## 依赖与文件

**依赖：**0001、0002。
**预计文件：**`contracts/protocol/message_catalog.json`、`server/internal/transport/leaf/`、`client/framework/network/`、`tests/contracts/`。
