# TBCG 领域词汇表

TBCG 是独立 2D 回合制多人 RPG 框架。本文只定义领域词汇，用于保持创作、发布、运行和表现讨论中的用语一致。

## 核心对象

### Definition

带稳定 Content ID 和不可变 Revision 的游戏内容定义，描述内容“应该是什么”。Definition 不代表任何玩家、队伍或世界中已经发生的运行状态。

- Avoid：把 Definition 称为存档、实例、运行对象或表现资源。

### Instance

带归属、当前状态和状态版本的运行事实，描述内容“此刻对谁发生到了哪里”。Instance 必须保留其依赖的 Definition 版本语义，不能因 Definition 后续变化而被隐式重算。

- Avoid：把 Instance 称为模板、配置或 Definition。

### Presentation

面向 Godot 的视觉、音效、动画和交互表现，负责让玩家看见、听见并操作游戏。Presentation 不决定权威奖励、任务完成、战斗胜负或持久状态。

- Avoid：把 Presentation 当成规则真源或运行事实。

### Release

不可变的发布快照，包含内容 Revision、规则能力和资源依赖的完整闭包。Release 是运行和回退讨论中的版本边界，不能只理解为一批松散文件或一个显示版本号。

- Avoid：把 Release 称为草稿、补丁包或可原地修改的配置集合。

## 内容身份与版本

### Game

一套具体游戏配置身份，用于区分不同游戏产品或样例。Game 不是单个服务器、单个内容包或单个玩家存档。

- Avoid：把 Game 与 Realm、ContentPackage 混用。

### ContentPackage

可组合使用的一组内容集合，用于组织某个主题、样例或扩展范围。ContentPackage 可以参与 Release，但它本身不等于已经激活的 Release。

- Avoid：把 ContentPackage 称为 Realm 或运行环境。

### Realm

面向运营的分服或运行分区，承载一组玩家和运行状态。Realm 使用某个已激活 Release，但 Realm 本身不是内容定义。

- Avoid：把 Realm 称为 Game、Release 或地图。

### Content ID

跨创作和运行边界引用内容的稳定字符串身份，指向某类 Definition 的长期身份。Content ID 不使用显示名、空间路径、资源名称或列表位置来表达。

- Avoid：显示名、路径、数组下标、临时编辑名称。

### Revision

某个 Definition 在一次确定内容变更后的不可变版本。Revision 一经确认就不再原地改变，后续修正产生新的 Revision。

- Avoid：把 Revision 当作可覆盖草稿或可变当前值。

## 空间、遭遇与运行玩法

### Placement

Godot 空间中的稳定锚点，用于表达 NPC、出生点、出口、触发区或其他可绑定位置。Placement 只说明“在哪里”，不直接拥有绑定到该位置的玩法规则。

- Avoid：把 Placement 当作 NPC Definition、任务规则或传送规则本身。

### Encounter

一次战斗遭遇的定义，描述敌方编成、触发条件、奖励关联和战斗入口语义。Encounter 是战斗发生前的内容定义，不等于已经进行中的战斗 Instance。

- Avoid：把 Encounter 称为怪物本体、战斗存档或地图刷点。

### Quest

任务 Definition，描述前置条件、目标、状态推进、奖励和重复策略。Quest 只定义任务规则，不记录某个玩家或队伍的当前进度。

- Avoid：把 Quest 称为任务进度、任务日志或玩家存档。

### Quest Instance

某个角色或参与范围内的任务运行进度，记录接取、目标推进、完成和领取状态。Quest Instance 依赖指定 Quest 及其相关内容版本。

- Avoid：把 Quest Instance 称为任务模板或任务 Definition。

### Party

由多个角色组成的临时协作单位，用于共同移动、进入遭遇、进入副本或共享特定玩法参与关系。Party 不天然拥有成员资产，也不能替代单个角色的状态归属。

- Avoid：把 Party 称为公会、账号、队伍副本或战斗单位集合。

### Dungeon

可进入的副本内容定义，描述地图组合、准入条件、阶段、遭遇和奖励结构。Dungeon 定义副本规则，不记录某一次进入后的运行状态。

- Avoid：把 Dungeon 称为地图、战斗或副本进度。

### Dungeon Instance

一次具体副本运行事实，记录参与者、阶段进度、已触发遭遇和结算边界。Dungeon Instance 固定依赖进入时的内容版本，不随新发布内容自动改写。

- Avoid：把 Dungeon Instance 称为 Dungeon Definition 或普通地图。

## 角色与创作职责

### NPC

非玩家角色，是可被放置、交互、对话或提供服务的游戏角色类型。统一写作 NPC，不写作 NCP。

- Avoid：NCP、玩家角色、怪物遭遇本身。

### Content Author

内容作者，负责提出和编写新的 Definition、Revision 或内容组合。Content Author 不因创建内容而自动拥有审核或发布权限。

- Avoid：把 Content Author 称为玩家、运营发布者或代码作者。

### Content Reviewer

内容审核者，负责确认内容语义、引用关系、表达质量和发布风险。Content Reviewer 的职责是审查，不等于最终激活内容。

- Avoid：把 Content Reviewer 称为内容作者或发布执行者。

### Content Publisher

内容发布者，负责把已通过审核的内容组合为 Release 并推动进入可运行状态。Content Publisher 处理发布边界，但不应改变已审核 Revision 的内容含义。

- Avoid：把 Content Publisher 称为审核者、作者或运行状态管理员。
