# 0011 权威回合战斗与怪物技能

**状态：**未开始
**目标：**交付由 Go 权威结算并由 Godot 精确回放的多人回合战斗与基础怪物/技能定义。

## 范围

- 定义战斗聚合状态、命令授权、回合超时、固定 RNG 接口、技能/Buff/AI 与遭遇 Definition。
- 领域计算产生隔离候选状态；Unit/Buff/切片/map 无共享可变引用，事务提交前不替换权威状态。
- 每次攻击输出逐目标、逐击权威事件，包括 event_seq、actor/target、damage、HP before/after、crit 事实、死亡与状态事件。
- Godot 消费事件回放，不按目标数均摊伤害、不在客户端再算暴击或结果。

## 验收标准

- [ ] 多名玩家各自授权提交本回合命令；超时使用服务器默认行为，非法命令不会进入结算。
- [ ] 固定 seed 回合可重放一致；旧快照不变，计算/持久化失败不会泄漏候选状态。
- [ ] 每目标 damage/HP/crit/event_seq 与内核结果一一对应，胜负与奖励只由 Go 确定。

## 验证

- Go 表驱动、fuzz、状态隔离与 race 测试；真实 PostgreSQL 中断恢复和幂等奖励测试。
- Godot 端 golden replay 核对事件顺序、逐目标数值和动画，不执行权威计算。

## 依赖与文件

**依赖：**0004、0007、0010。
**预计文件：**`server/modules/battle/`、`server/modules/encounter/`、技能/怪物 Schema、`client/framework/presentation/battle/`、协议 fixtures。
