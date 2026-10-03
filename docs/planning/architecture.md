# TBCG 推荐架构

本文只定义首版架构边界和执行契约；产品范围以 `docs/planning/prd.md` 为准，领域词汇以 `CONTEXT.md` 为准。

## 1. 架构形态

TBCG 首版采用单仓内的模块化单体：Godot 游戏客户端与创作工具、Go 游戏运行进程、Go 管理进程、隔离内容构建任务、React 管理前端。每个 Realm 首版只有一个游戏写进程；PostgreSQL 是唯一业务数据库。

不引入多数据库适配、微服务、跨服、Redis/MQ/Kubernetes 或客户端权威执行。文件/对象存储只保存不可变发布产物和资源，不承担业务数据库职责。

## 2. 模块职责

| 模块 | 职责 | 不允许 |
|---|---|---|
| Godot 游戏客户端 | 输入、UI、场景、动画、音效、网络状态、事件播放 | 决定奖励、战斗胜负、价格、任务完成或经济变更 |
| Godot 创作工具 | 地图几何、通行格、位置、稳定 `placement_id`、几何 revision 导出 | 编辑 NPC/任务/遭遇/副本等规则绑定 |
| Go 游戏运行模块 | 会话、角色、移动、队伍、战斗、任务、背包、经济、副本、运行恢复 | 读取 Godot 场景或执行 GDScript |
| Go 内容/Release 模块 | Schema、引用闭包、规则能力、编译校验、公开/私有包、Release 加载 | 修改玩家运行状态 |
| Go 管理模块 | 身份、RBAC、草稿、revision、审批、构建、激活、审计、受控运营命令 | 绕过游戏命令直接改玩家资产 |
| React 管理前端 | 内容编辑、引用选择、差异、校验报告、发布与运营页面 | 拥有地图几何或运行权威状态 |

## 3. 依赖方向

```text
Godot 客户端 --WSS/JSON--> Leaf Adapter --> Go 游戏运行模块 --> PostgreSQL
Godot 创作工具 --几何 revision--> Go 内容/Release 模块 --> 不可变产物
React 管理前端 --HTTPS/OpenAPI--> Go 管理模块 --> Go 内容/Release 模块 --> PostgreSQL
Go 游戏运行模块 --读取已激活 Release--> Go 内容/Release 模块
```

Leaf 只作为网络和调度适配层；领域接口不得暴露 `gate.Agent`、ChanRPC 或连接对象。Go 运行模块只读取已激活 Release 的运行投影，不读取管理草稿。

## 4. 字段单向所有权

- Godot 拥有：地图几何、边界、通行格、位置、稳定 `placement_id`。
- 网页/内容模块拥有：NPC、出口、任务目标、Encounter、副本入口等锚点绑定和结构化 Definition。
- Go 运行模块拥有：Character、Quest Instance、Party、Battle、Dungeon Instance、Inventory、Wallet 等运行 Instance。
- Godot Presentation 只负责表现，不作为规则、奖励或持久状态真源。

跨端引用只使用 Content ID、Revision、Release ID、`placement_id` 或运行 Instance ID。显示名、Godot node path、资源 UID、数组下标和临时草稿名都不能作为稳定主键。

## 5. 版本与扩展边界

Definition 通过稳定 Content ID 和不可变 Revision 寻址；Release 是不可变内容、规则能力和资源依赖闭包。运行 Instance 必须绑定实际依赖版本，不能因 Definition 后续变化而隐式重算。

新增已有机制下的内容只改 Definition；组合已有机制必须通过校验；新增机制、新交互或新表现必须增加版本化 Go handler、必要 GDScript 表现、Schema、编辑支持和测试。`unsupported` 阻止发布，`approximate` 不得静默降级。

## 6. Realm 写入边界

首版每个 Realm 单写者。游戏写进程通过 PostgreSQL advisory lock、递增 `owner_epoch` 和事务内校验防双写。Character、Map、Party、Battle、Dungeon 各自拥有独占状态；跨 owner 变化只能通过命令、事实和事务协调完成。

成功提交后的状态以 PostgreSQL 为准；崩溃后从已提交状态恢复，不用客户端预测或过期回调覆盖已提交事实。
