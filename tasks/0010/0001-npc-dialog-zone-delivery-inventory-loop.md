# 0010.1 NPC 对话、区域/交付任务与背包闭环

**状态：**开发完成（待评审）——开发与本机验证见 [运行面验证记录](../../docs/verification/0008-0015-runtime.md)；独立评审、80% 覆盖率与远端 CI 未完成，不能标记“已完成”
**依赖：**0008、0009
**目标：**先交付不依赖战斗引擎的任务闭环：网页定义并发布 NPC/对话/区域或交付目标，Godot 展示交互，Go 权威校验接取、目标推进和背包/货币奖励事务。

## 范围

- 网页定义 NPC 绑定、对话选项、AcceptQuest、区域进入目标、物品交付目标、奖励和重复策略，并通过 Release 发布。
- Go 校验地图、距离、可见性、角色状态、任务前置、目标归属、交付物品所有权和幂等键；背包、货币账本、operation_results 与 outbox 在同一 PostgreSQL 事务提交。
- Godot 在 NPC 附近展示可选对话、任务状态和交付结果，只提交玩家意图，不本地推进任务或发奖。

## 不做范围

- 不实现指定遭遇胜利或击杀计数推进；该能力在 0010.2 接入 0011 的战斗事实。
- 不实现组队共享任务、副本奖励、商店购买或跨玩家交易。

## 验收标准

- [x] 玩家可通过指定 NPC 选项接取任务，并通过进入指定区域或交付指定物品完成目标。
- [x] 伪造距离、错误地图、未接取任务、错误物品所有者、重复交付和重复领取均不能推进或发奖。
- [ ] PostgreSQL 提交未知、断线重试和 outbox 重投只产生一份有效货币/物品奖励。（已实现，故障注入测试待补）

## 代表文件

- `contracts/content/npc*.schema.json`
- `contracts/content/quest*.schema.json`
- `server/modules/quest/**`
- `server/modules/inventory/**`
- `admin/src/features/content/**`、`client/framework/presentation/npc/**`

## 验证

以下为原计划的验收命令形态；实际已执行的验证见运行面验证记录（Go 包路径为 `server/modules/<domain>`，Godot 测试以 `--script res://tests/<path>.gd` 运行）：

```bash
go test ./server/modules/quest/... ./server/modules/inventory/... -cover
pnpm --dir admin test -- content quest npc
/Applications/Godot.app/Contents/MacOS/Godot --headless --path client --run-tests npc_quest_inventory
```
