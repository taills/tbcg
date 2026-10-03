# 0010 NPC、任务与背包结算

**状态：**未开始
**目标：**完成网页定义 NPC/任务、Godot 放置锚点、玩家对话接取/交付和有事务保护的任务奖励。

## 范围

- 定义 NPC 锚点到内容 ID 的绑定、对话选项动作、任务定义/状态/指定目标、奖励与物品实例。
- 服务端校验地图、距离、可见性、权限、任务前置与目标参与归属；定义个人/队伍进度语义。
- 以 OptionChosen→AcceptQuest→可信目标事件→ClaimReward 推进任务；持久化状态、物品/货币账本、幂等结果与 outbox 同一 PostgreSQL 事务。
- Godot 负责交互展示，不本地推进任务或发奖。

## 验收标准

- [ ] 玩家可在指定 NPC 选项中接取任务，在野外击败指定遭遇后领取目标奖励。
- [ ] 不匹配目标、未接取任务、非参与者、重复领取和伪造 NPC 距离均不能推进/发奖。
- [ ] 任务奖励事务失败不发成功消息；成功后断线/重试只产生一份有效货币/物品。

## 验证

- Go 任务/背包/交易边界单测，真实 PostgreSQL 故障测试和跨端 Godot + API E2E。
- Playwright 创建/发布任务与 NPC，游戏内两客户端验证接取和交付归属。

## 依赖与文件

**依赖：**0008、0009。
**预计文件：**`server/modules/quest/`、`server/modules/inventory/`、`server/modules/economy/`、相关 Schema、`admin/src/features/content/`、`client/framework/presentation/`。
