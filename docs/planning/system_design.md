# TBCG 系统设计

本文聚焦首版可执行契约：协议、空间、规则、战斗、事务、Release 和安全边界。产品范围以 `docs/planning/prd.md` 为准。

## 1. 游戏协议契约

首版游戏连接使用 WSS，WebSocket binary frame 中承载 UTF-8 JSON。Godot 使用 `WebSocketPeer`；Go 侧通过 Leaf Adapter 接入。Leaf 不进入领域模型，领域代码只接收已解析、已校验的命令。

所有消息使用稳定 envelope：

```json
{
  "protocol_version": "1",
  "request_id": "client-request-id",
  "type": "Character.MoveRequested",
  "payload": {},
  "client_state": {
    "release_id": "release-id",
    "last_server_seq": "42"
  }
}
```

响应和推送包含 `success`、`request_id`、`server_seq`、`state_version`、`data`、`error`、`meta`。未知 `type`、过期协议、消息超限、频率超限、凭证失效或 Release 不兼容必须拒绝。大整数以十进制字符串传输。

连接建立后必须先 Hello。服务端校验协议版本、客户端构建、凭证、Origin、Game、Realm 和 Release 兼容性。只有 HelloAck 后客户端才进入业务 ready；HelloAck 前不处理业务命令，也不发送会让 UI 误判成功登录的事件。

## 2. 管理协议契约

管理平台使用 HTTPS REST/OpenAPI，响应 envelope 为 `success/data/error/meta`。管理账号与玩家账号分离；Content Author、Reviewer、Publisher 分权。

草稿、revision、审批、构建、激活、回退和运营命令必须审计。管理写运行状态时必须走受控游戏命令或运营入口，不得直接改玩家资产、任务进度、背包或经济账本。管理 cookie 使用安全属性并防 CSRF。

## 3. 空间契约

首版只支持正交整数网格：`grid[y][x]`，左上原点，x 向右，y 向下，`0` 可走，`1` 障碍。服务端使用格坐标作为权威移动单位；客户端按声明的 origin 和 tile_size 转换为像素表现。

Godot 导出 geometry revision，至少包含地图 Content ID、schema 版本、边界、通行格、origin、tile_size、出生点、NPC 锚点、出口、触发区、区域和稳定 `placement_id`。网页只通过 `placement_id` 绑定 NPC、Encounter、任务目标、出口或 Dungeon 入口。

服务端移动校验必须检查当前位置版本、边界、通行格、路径长度、频率、对角穿墙、角色状态和触发区域。旋转地图、多层碰撞、任意导航多边形、视觉 mask 转碰撞和陌生脚本执行首版不支持，必须显式报错。

## 4. 规则执行契约

规则由受控原语组成：Condition、Action、Effect、Trigger/Event、Flow/StateMachine、Selector/Policy。Go handler 必须版本化注册；运营平台不能上传任意 Go、Lua 或 GDScript。

Release 构建时检查 Content ID、Revision、引用闭包、锚点存在、图可达性、循环边界、效果适用领域、目标数量、执行深度、资源依赖和能力支持。`unsupported` 阻止发布；`approximate` 需要显式标注并默认不进入首版核心闭环。

执行期规则只能使用上下文注入的时间、随机源、Release、权限和快照。规则计算先产生候选状态与待提交事实；事务提交成功后才更新内存和推送客户端。

## 5. 回合战斗与逐目标事件

战斗状态机：创建 → 收集授权命令 → 超时默认行动 → 服务端排序/结算 → 事件序列 → 下一回合或结束 → 持久化结算。

玩家只能为授权单位提交命令。服务端检查阶段、参与集合、目标合法性、技能消耗、冷却、状态限制、命令重复和 state_version。超时默认行动与玩家命令走同一结算路径。

技能结算按行动→目标→击次形成逐目标、逐击事件，供客户端播放和服务端审计。每条事件至少含战斗内递增的 `event_seq`、`action_id`、`event_kind`、`actor_id`、`target_id`、适用时从 1 开始的 `hit_index`、实际 `damage/heal`、`hp_before/hp_after`、暴击事实/倍率、状态变更和失败原因；不得只保留总伤害后再向目标均摊。协议必须将 `target_count` 与 `hit_count` 分开表达；消耗按 action 扣一次，伤害与声明为逐击的效果按各 `hit_index` 结算。典型事件包括：

```text
ActionDeclared(action_id)
CostApplied(action_id)
TargetSelected(action_id, target_id)
HitResolved(action_id, target_id, hit_index)
DamageApplied(action_id, target_id, hit_index, hp_before, hp_after, damage, crit)
HealApplied(action_id, target_id, hp_before, hp_after, heal)
BuffApplied(action_id, target_id)
BuffTicked(action_id, target_id)
UnitDefeated(action_id, target_id)
QuestSignalEmitted(action_id, target_id)
LootCandidateCreated(action_id, target_id)
```

多击语义只有存在明确 `hit_count` 执行器并具备逐击测试时才标记 `supported`；否则内容编译必须以 `unsupported` 拒绝，不可把多个目标伪装成多次命中。
Battle 只形成候选奖励、任务信号和结算事实；最终奖励由 Character/Economy 事务接收。客户端只播放事件，不提交伤害、掉落或胜负。

战斗结算不得就地修改共享 Unit、Buff、切片或映射。旧快照生成新候选状态；提交失败不泄漏候选状态，提交成功才替换当前状态。

## 6. PostgreSQL 事务、幂等与 outbox

PostgreSQL 是唯一业务数据库。控制面存 games、drafts/revisions、geometry revisions、releases、deployment jobs、roles、audit logs；运行面存 accounts、characters、inventory instances、wallets/ledger、quest instances、party members、dungeon instances、battle snapshots、operation results、outbox。

奖励、购买、任务交付、战斗结算和副本结算通过小型业务 Repository 和事务协调接口执行。事务回调只能使用绑定到同一 PostgreSQL transaction 的 repositories，不得在回调里借连接池另开写入、嵌套事务或执行网络/文件副作用。

```text
校验命令与权限
→ 读取当前快照和 state_version
→ 生成新状态与待提交事实
→ 单个 PostgreSQL 事务写状态、账本、operation_result、outbox
→ commit
→ 应用内存状态并推送已提交结果
```

幂等键包含 game、realm、actor、operation 和业务唯一标识，并保存 payload_hash。相同键相同载荷返回已提交结果；相同键不同载荷拒绝并审计；commit 后推送前崩溃时，重试返回已提交结果，不重复副作用。

若 commit 返回连接错误等无法确认的结果，记录为 `Unknown`：受影响聚合保持 pending/frozen，不替换内存候选状态、不报告成功或确定失败、不生成新幂等键。恢复时以原幂等键和 `payload_hash` 核验 `operation_result`；一次查询未发现记录不等于已回滚。通过相同唯一幂等键尝试领取操作记录，使 PostgreSQL 唯一约束与仍在处理的原事务串行化：若查到已提交结果则复用；只有确认前事务已回滚且本次事务独占该幂等键后才允许有界重执行；仍无法确定时继续 pending。

Outbox 与业务状态同事务写入，可至少一次投递；消费者按事件 ID 幂等处理。不承诺跨网络恰好一次，只保证业务上不重复发奖、不重复交付、不重复结算。

## 7. Realm 单写者与 `owner_epoch`

每个 Realm 首版只有一个游戏写进程。进程使用固定的 PostgreSQL session connection 获取 Realm advisory lock；锁成功后，通过 Realm owner guard 行锁定并递增 `owner_epoch`，提交 epoch 后才开放写入。不得在连接池任意连接上获取 advisory lock 后误以为锁仍归进程持有。

所有写操作采用统一锁顺序：业务事务先以 `SELECT ... FOR SHARE` 锁住 owner guard 行，验证进程 epoch；该共享行锁一直持有到业务事务结束，再按稳定排序锁定聚合并校验 `state_version`、写入状态。接管方先取得同一专用 session advisory lock，再以更新 owner guard 行的排他锁递增 epoch 并提交；epoch 更新与业务共享锁互斥，接管须等待所有旧写事务结束。禁止不带锁读取 epoch、在业务锁之后反向获取 advisory lock，或在事务外写运行状态。

旧事务要么在接管前完整提交，要么在接管后发现 epoch 过期并拒绝提交。锁连接丢失、epoch 过期或事务校验失败时，进程停止接收写操作；崩溃恢复以 PostgreSQL 已提交状态为准。

## 8. Release 兼容与回退

Release 生命周期：Draft → Validated → Reviewed → Built → Staged → Activated → Deprecated。审批针对确切 Revision；构建成功不等于激活成功。

Release manifest 至少包含 `game_id`、`release_id`、内容 `schema_version`、`instance_schema_version`、`ruleset_version`、`protocol_version`、`client_min_build`、capabilities、内容 Revision 闭包、geometry revision 闭包、公开资源 hash、服务端私有包 hash 和审计信息。服务端私有 AI、掉落、奖励和规则配置不得进入公共客户端包。

激活时游戏进程校验当前代码对 `ruleset_version`、协议和 `instance_schema_version` 的兼容读写能力、客户端最低版本和包完整性；成功后一次性切换完整快照，失败保持旧 Release 指针。

Battle、Dungeon、Quest 和 Map Instance 必须固定开始/接取时的 Release 及依赖闭包，同一 Map Instance 不得混用不同 geometry revision 或资源包。保留 JSON 不等于保留执行语义；实例格式或规则不兼容的代码/Schema 部署必须先完成可验证迁移，否则拒绝激活或排空受影响实例。旧客户端不得默认读取或写入新格式存档；必须由最低客户端版本及服务端读写兼容检查阻止。

内容指针回退只影响后续内容选择，不撤销已提交奖励、词条、货币或任务事实。代码回退和持久化格式回退是不同操作，必须分别有已验证的迁移/补偿路径、审批与审计；没有安全反向迁移时，不得以改 Release 指针冒充代码/数据回滚。旧实例引用的 Release、Revision、geometry 和资源不能提前清理。

## 9. 安全契约与未决项

安全契约：客户端不可信；WS 校验 Origin、凭证、版本和限额；管理面 RBAC、审计和 CSRF；导入只创建可审查 revision，不清空目标、不覆盖手工内容、不执行陌生脚本；未授权素材不得进入可分发包。

P0 未决项：Godot 4.7.1 与 export templates 获取方式；公开/私有包存储策略；Schema 到 Go/TypeScript/GDScript 的投影范围；战斗事件 catalog 稳定 ID；PostgreSQL 版本与迁移工具；管理认证和 CSRF 细节。
