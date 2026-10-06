# 0012.3 两阶段双人副本闭环

**状态：**开发完成（待评审）——开发与本机验证见 [运行面验证记录](../../docs/verification/0008-0015-runtime.md)；独立评审、80% 覆盖率与远端 CI 未完成，不能标记“已完成”
**依赖：**0012.1、0012.2、0011
**目标：**基于角色成长/装备、Party owner 和权威战斗，交付一个版本固定的两阶段双人副本闭环。

## 范围

- 网页定义并发布 Dungeon Definition：准入条件、参与人数、阶段图、两个 encounter、限次、奖励和 Release 闭包。
- Go 实现 Dungeon Instance owner、阶段转换、战斗挂接、断线重连、失败/投降/解散处理、限次和每位合格参与者的幂等奖励入口。
- Godot 展示副本入口、准备、阶段进度、战斗回放、结算和失败原因；客户端不决定胜负或奖励。

## 不做范围

- 不实现复杂随机地图、跨服匹配、公会副本或实时活动。
- 不把战斗结束回调直接写入他人背包；奖励必须经过 Character/Inventory 结算入口。

## 验收标准

- [x] 两名玩家满足准入后共同进入副本，连续完成两个阶段 encounter 并分别领取一次奖励。
- [x] 进行中发布新 Release 不改变已创建 Dungeon Instance 的 Definition、奖励或 encounter 引用。
- [ ] 断线重连、崩溃恢复、重复提交、已离队成员和非参与者领奖都有真实 PostgreSQL 集成测试覆盖。（已实现，故障注入测试待补）
- [x] 与父 0012 最终 gate 一起验收全部原创样例：2 张地图、1 个角色原型 + 1 个门派、2 个 NPC、2 个怪物、3 个技能、3–5 个物品、1 条三阶段任务、1 个双人多阶段副本。

## 代表文件

- `contracts/content/dungeon*.schema.json`
- `server/modules/dungeon/**`
- `server/modules/party/**`
- `admin/src/features/content/dungeon/**`
- `client/framework/presentation/dungeon/**`

## 验证

以下为原计划的验收命令形态；实际已执行的验证见运行面验证记录（Go 包路径为 `server/modules/<domain>`，Godot 测试以 `--script res://tests/<path>.gd` 运行）：

```bash
go test ./server/modules/dungeon/... ./server/modules/party/... -race -cover
pnpm --dir admin test -- content dungeon
/Applications/Godot.app/Contents/MacOS/Godot --headless --path client --run-tests two_player_dungeon
```
