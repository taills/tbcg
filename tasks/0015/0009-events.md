# 0015.9 活动

**状态：**开发完成（待评审）——开发与本机验证见 [运行面验证记录](../../docs/verification/0008-0015-runtime.md)；独立评审、80% 覆盖率与远端 CI 未完成，不能标记“已完成”
**依赖：**0012、0013（必须先确认双人副本 MVP 与创作/发布体验成熟）
**目标：**交付最低可玩的活动日历入口和一个可完成活动；可选加入简单排行榜，但必须明确范围并保持可信时钟与奖励事务。

## 最低可玩范围

- 网页定义并发布活动日历、开放时间、入口、奖励、参与限制和可选排行榜规则。
- Go 使用可信时钟校验开放窗口、参与次数、任务/副本/战斗组合事件和奖励幂等；排行榜若启用，只覆盖单 Realm 简单分数排名。
- Godot 展示活动入口、时间状态、参与进度、奖励和可选排行榜。

## 不做范围

- 不实现跨服活动、拍卖、复杂实时竞赛、大规模分片排行榜或外部支付活动。
- 不允许客户端本地时间决定活动开放或排名结果。

## 验收标准

- [x] 玩家可在开放时间内参与一个活动并领取一次奖励，关闭时间外不能参与或领奖。
- [x] 可选排行榜如启用，仅为单 Realm 简单排行，分数写入、奖励和重复提交有真实 PostgreSQL 测试覆盖。
- [ ] 网页定义/发布、Go 权威事务/owner、Godot 表现、真实 PostgreSQL、三端测试、覆盖率 80%+ 和评审均完成后才可标记 supported。

## 代表文件

- `contracts/content/event*.schema.json`
- `server/modules/event/**`
- `server/modules/ranking/**`
- `admin/src/features/content/events/**`
- `client/framework/presentation/events/**`

## 验证

以下为原计划的验收命令形态；实际已执行的验证见运行面验证记录（Go 包路径为 `server/modules/<domain>`，Godot 测试以 `--script res://tests/<path>.gd` 运行）：

```bash
go test ./server/modules/event/... ./server/modules/ranking/... -race -cover
pnpm --dir admin test -- events
/Applications/Godot.app/Contents/MacOS/Godot --headless --path client --run-tests events
```
