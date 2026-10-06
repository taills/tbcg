# 0010.2 指定遭遇胜利推进与幂等奖励

**状态：**开发完成（待评审）——开发与本机验证见 [运行面验证记录](../../docs/verification/0008-0015-runtime.md)；独立评审、80% 覆盖率与远端 CI 未完成，不能标记“已完成”
**依赖：**0010.1、0011
**目标：**在 0011 战斗引擎产出可信胜利事实后，为任务接入指定遭遇目标推进、参与归属和幂等奖励结算，消除 0010 与 0011 的循环依赖。

## 范围

- 扩展任务目标定义 `target_ref`、`encounter_ref`、`count`、`progress_scope`、`repeat_policy`，并在发布时校验引用和执行能力。
- Go 只消费 0011 产生的胜利、参与者、Release 和 encounter 事实；校验接取时间、参与范围、指定目标和任务状态后推进 Quest Instance。
- ClaimReward 使用 0010.1 的背包/货币/账本/operation_results/outbox 事务入口，保证重复胜利事件和断线重试幂等。

## 不做范围

- 不新增通用战斗结算器、技能系统或副本阶段；这些属于 0011 和 0012。
- 不把任意 FlowEvent 动画或客户端事件当成任务进度来源。

## 验收标准

- [x] 玩家击败任务指定 encounter 后才推进目标；公共怪、错误 encounter、未参与战斗和未接取任务都不推进。
- [x] 个人/队伍 progress_scope 明确且被测试覆盖，同一胜利事实重复投递不重复计数或发奖。
- [ ] 奖励领取与背包/货币账本同事务提交；失败不发成功消息，未知提交结果可用同幂等键核验。（已实现，故障注入测试待补）

## 代表文件

- `contracts/content/quest*.schema.json`
- `contracts/content/encounter*.schema.json`
- `server/modules/quest/**`
- `server/modules/encounter/**`
- `tests/e2e/**/quest_encounter*`

## 验证

以下为原计划的验收命令形态；实际已执行的验证见运行面验证记录（Go 包路径为 `server/modules/<domain>`，Godot 测试以 `--script res://tests/<path>.gd` 运行）：

```bash
go test ./server/modules/quest/... ./server/modules/encounter/... -cover
pnpm --dir admin test -- content quest encounter
/Applications/Godot.app/Contents/MacOS/Godot --headless --path client --run-tests quest_encounter_reward
```
