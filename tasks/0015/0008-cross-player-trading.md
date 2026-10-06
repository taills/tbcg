# 0015.8 跨玩家交易

**状态：**开发完成（待评审）——开发与本机验证见 [运行面验证记录](../../docs/verification/0008-0015-runtime.md)；独立评审、80% 覆盖率与远端 CI 未完成，不能标记“已完成”
**依赖：**0012、0013、0015.5（必须先确认双人副本 MVP、创作/发布体验和基础经济/商店事务成熟）
**目标：**交付最低可玩的双人确认交易闭环，确保跨玩家资产转移有明确 owner、锁定、审计和恢复语义。

## 最低可玩范围

- 网页定义并发布交易规则、可交易物品/货币限制、税费和风控开关；提供审计查询。
- Go 实现 Trade owner、双方报价、锁定、双确认、取消、超时和提交事务；两个 Character/Inventory 资产变更、账本、operation_results/outbox 保持一致。
- Godot 展示交易邀请、报价、锁定、双确认、取消/失败原因和最终资产变化。

## 不做范围

- 不实现拍卖行、跨服交易、离线邮件交易、现金支付或复杂撮合市场。
- 不使用泛化分布式事务口号掩盖 PostgreSQL 内的明确锁顺序和恢复测试。

## 验收标准

- [x] 两名玩家可在同一交易会话中各自放入物品或货币，双确认后资产原子转移。
- [ ] 取消、超时、断线、重复确认、状态版本冲突和未知提交结果不会复制或丢失资产。（已实现，故障注入测试待补）
- [ ] 网页定义/发布、Go 权威事务/owner、Godot 表现、真实 PostgreSQL、三端测试、覆盖率 80%+、安全评审和代码评审均完成后才可标记 supported。

## 代表文件

- `contracts/content/trade*.schema.json`
- `server/modules/trade/**`
- `server/modules/economy/**`
- `admin/src/features/trade/**`
- `client/framework/presentation/trade/**`

## 验证

以下为原计划的验收命令形态；实际已执行的验证见运行面验证记录（Go 包路径为 `server/modules/<domain>`，Godot 测试以 `--script res://tests/<path>.gd` 运行）：

```bash
go test ./server/modules/trade/... ./server/modules/economy/... -race -cover
pnpm --dir admin test -- trade
/Applications/Godot.app/Contents/MacOS/Godot --headless --path client --run-tests trade
```
