# TBCG 游戏框架设计与实施计划

## Context

在 `/Users/admin/Games/TBCG` 建设独立的、内容驱动的 **2D 回合制多人 RPG 框架**。参考正在迁移的 `/Users/admin/Games/xyq`，提取领域需求、算法语义和工具经验，不承接其历史客户端协议或玩家存档。三端分别为 Godot 游戏客户端与场景创作工具、Leaf 驱动的 Go 权威后端、Go 管理后端与 React + Tailwind 管理前端。

**用户已确认：**回合制 RPG 优先；独立框架、xyq 按需导入；Godot 编辑场景与表现，网页编辑结构化规则；**TBCG 仅使用 PostgreSQL，不考虑 SQLite/MySQL 等多数据库适配，以控制方案复杂度。**调研起点的 `TIPS.md:1–10` 将 TBCG 定义为 Turn-Based-Combat-Game-Framework，强调多人 RPG、门派、养成与团队策略；当时目录没有业务代码或项目级 CLAUDE.md/AGENTS.md。方案现已获批并按任务推进：`TIPS.md` 已删除（原文保留在初始 Git 检查点），`PLAN.md` 和 `tasks/` 已提交，当前执行任务 0001 的规格文档；尚未实现游戏业务代码，也不修改 xyq。

**目标是完整创作闭环，而不只是 CRUD 后台：**新增内容 → 校验引用与规则 → 预览/模拟 → 审批发布 → 游戏运行 → 观测与安全回退。已有机制支持配置化增量；新机制、新交互或新表现需要经过测试的 Go/GDScript 扩展，不承诺“所有玩法永远零代码”。

## 1. 调研结论与复用策略

### 1.1 已核验的技术基线

- Leaf 固定 `github.com/taills/leaf/v2 v2.0.3`，标签对应 `e7d50fb60b9e1e451ce0c5aba4dba331e9cc7379`，Apache-2.0。公开仓库及 `xyq/server/go.mod:3–6` 均声明 Go `1.26.4`；本机为 Go 1.27.0。工程与 CI 基线固定 Go 1.26.4，并额外验证本机工具链，不沿用旧 `/leaf` 导入路径。
- Godot 固定 **4.7.1-stable**：`xyq/client/project.godot:15` 声明 4.7，`.github/workflows/ci.yml:14` 固定 4.7.1；官方 4.7.1 稳定发行版已核验。README/CLAUDE 中的 4.6 描述不能作为版本依据。本机 Godot CLI 尚未在 PATH 中找到，需定位编辑器及匹配 export templates。
- 管理前端采用 React + TypeScript + Vite + Tailwind v4，使用官方 `@tailwindcss/vite` 集成；具体依赖在 P0 锁定。JSON Schema 校验使用成熟 Go 库与 AJV，已查询包注册表，不手写通用校验器。
- 已进行 GitHub repo/code 检索，并通过 Godot 4.7、Tailwind 官方资料核验接口。Leaf skeleton、Godot RPG 编辑插件仅供模式参考，不能替代 Go 权威执行与内容发布链路。

### 1.2 xyq 的实际能力与迁移缺口

以下为静态源码证据，本轮没有运行项目或测试；参考项目仍在迁移，实施复用前应再次确认版本。

| 参考代码 | 已有能力 | TBCG 使用方式 |
|---|---|---|
| `xyq/server/battlesys/battle.go:73`，`state.go:64` | 回合结算、速度排序、Buff 生命周期、战斗演出事件 | 移植语义与测试，重建版本化规则接口；现实现会就地修改 Unit，不称为纯 reducer |
| `xyq/server/game/internal/battle_handler.go:99`，`battle_timeout_test.go:52` | 同图在线队伍入战、收集指令、超时默认行动 | 作为多人状态机与断线/超时测试样例，不复制 Leaf handler 与全局状态 |
| `xyq/client/scripts/MapData.gd:22`，`server/mapdata/reader.go:57`，`server/world/world.go:546` | 同格式通行栅格、A*、禁止对角穿墙 | 提取共享地图契约与测试，首版采用明确整数网格 |
| `xyq/server/game/internal/move_validate.go:46` | 越界、路径长度、通行段校验 | 移植安全校验思想；原偏差和格尺寸不写死进框架 |
| `xyq/server/game/internal/story_handler.go:122`，`daily_handler.go:98` | 主线事件推进、日常、专属任务怪 | 重建指定目标、参与归属与奖励事务；原 kill 只按数量，不自动视为精确目标系统 |
| `xyq/client/scripts/ResPaths.gd:33`，`SpriteLoader.gd:90`，`TileStreamer.gd:106` | 素材寻址、帧缓存、旧地图瓦片流式加载 | 隔离为可选 Legacy Adapter，不复制中文名称路径与固定尺寸 |
| `xyq/tools/map_tiles/export.py:112` | 旧 `.map` 的视觉 tile/mask 导出 | 可选旧素材转换；mask 是视觉遮罩，不是通行/碰撞数据，也不是 Godot 语义导出 |

**不能照搬或当成已完成的部分：**
- 没有发现 `client/addons` 及通用 EditorPlugin/Resource 创作工具；Godot 地图语义导出、网页规则编辑与统一 Release 发布必须新建。`server/obs/http.go:29–48` 的 HTTP 入口仅为 health/metrics/pprof，NPC 注释中的“后台编辑 JSON”不是已实现的网页后台。
- xyq 已有 SQLite/WAL/busy_timeout（`server/db/db.go:22–59`）、追加式数据库迁移、备份，并在 `server/db/character.go:387–412` 中将角色与物品同事务保存。可借鉴事务语义与故障场景，但仍是同步数据库读写，不能称为已有异步内存角色模型；TBCG 选择 PostgreSQL 并新建实例模型，不复制旧存档结构。
- xyq 有测试/CI/部署脚手架，但 `tools/ci/covercheck.sh:2–3` 只统计有测试的包，不能据此承诺全项目覆盖率或当前测试全绿；本轮均未运行。新门禁覆盖手写业务代码整体，排除项须明确，不能用“无测试包不计入”掩盖未测试逻辑。
- `client/autoload/Net.gd:610` 是 `{消息类型名:body}`；`:617–622` 在 HelloAck 前即 emit connected，且包含账号密码重登与大量领域状态。只复用网络经验，不整体复制。
- `server/game/internal/dialog_handler.go:29/60` 的打开对话推进不等于 `OptionChosen → AcceptQuest`。新框架必须显式校验选项、角色状态和交互归属。
- `server/game/internal/dungeon_handler.go:99/148` 是单人自动阶段战斗，不等于原 Lua 多人副本状态机。首版不整迁原剧情副本。
- 抽取 JSON 不等于执行一致：`server/data/assets/skill/横扫千军/data.json:3/29` 的单目标三击与 `server/battlesys/battle_skill.go:40/86` 的多目标各一次不同。导入必须标记语义支持矩阵，不能静默退化。
- `server/game/internal/story_handler.go:218/223/237` 的发奖/推送/存档顺序不能复用为新事务方案。
- `tools/extract_server_data/main.go:76/82` 会清空输出目录；不得直接指向 TBCG 内容库。新导入器只创建可审查 revision，不覆盖手工内容。
- 原游戏素材商用/分发权未找到明确证明。Leaf 的 Apache-2.0 不外推到游戏内容，首包使用原创或明确授权素材。

## 2. 产品范围与领域模型

### 2.1 四种必须分离的对象

| 术语 | 含义 | 示例 |
|---|---|---|
| Definition | 策划定义，以稳定 ID + 不可变 revision 寻址 | 怪物模板、任务图、门派、物品模板 |
| Instance | 运行时事实与状态，有归属和 state_version | 角色存档、装备词条、任务进度、战斗/副本 |
| Presentation | 客户端视觉、音效与交互表现 | 动画、场景、UI、技能演出 |
| Release | 完整引用闭包、能力清单与版本的不可变快照 | 内容修订、地图语义、资产 manifest |

`Game` 是一套具体游戏；`ContentPackage` 是组合使用的内容集；`Realm` 是运营分服。地图定义不等于地图实例，角色原型不等于玩家存档；“NCP”统一按 NPC（非玩家角色）理解。

采用 `game_id + 稳定 content_id`，示例 `demo:map/harbor`、`demo:monster/slime`。引用禁止使用显示名、Godot 路径或数组下标。实例保存实际依赖版本；装备保存已经生成的词条等事实，不能修改模板后悄悄重算玩家资产。删除使用弃用与迁移，不复用旧 ID。

### 2.2 内容覆盖与机制归属

| 内容族 | 可配置的定义 | Go 执行机制/依赖 |
|---|---|---|
| 地图/传送 | 进入条件、区域、锚点绑定、遇敌表、出口目标 | 世界实例、移动校验、Godot 空间语义 |
| 角色/成长 | 原型、属性、等级曲线、外观、解锁 | 角色实例、经验、属性计算 |
| 门派 | 入门条件、技能集、成长修正、装备限制 | 条件、技能、成长 |
| 怪物/遭遇 | 属性、技能、AI 策略、队伍编成、掉落表 | 遭遇、刷怪、战斗与奖励 |
| 物品/装备 | 类型、堆叠、品质、词条、使用效果 | 背包、实例、装备、经济账本 |
| 技能/Buff | 选择目标、消耗、冷却、效果、触发 | 战斗执行器、表现绑定 |
| NPC/对话 | 服务、分支、选项动作、条件 | 交互距离、任务、商店、传送 |
| 任务 | 前置、指定目标、状态图、奖励、重复策略 | 事件、参与归属、任务实例、幂等结算 |
| 副本 | 地图组合、准入、阶段图、遭遇、限次 | 队伍、副本实例、阶段与奖励 |
| 商店/制作 | 商品、价格、库存、配方 | 经济事务、材料与产出 |
| 宠物/伙伴/坐骑 | 种族、成长、技能、招募、外观 | 归属、养成、战斗单位 |
| 活动/玩法 | 日历、入口、阶段、排行榜规则 | 可信时钟、任务/副本/战斗组合 |
| 社交/交易 | 队伍、好友、公会与交易限制 | 专用领域模块，不以配置替代事务一致性 |

首个可玩版本至少包含一个角色原型和一个门派、3 个基础技能、原创地图/NPC/怪物/物品/任务、回合制组队战斗，以及一个多阶段双人副本闭环；Buff 可作为额外能力，多击技能只有实际执行器和逐击事件测试通过后才能标记 supported。复杂宠物、公会、交易/拍卖、跨服和即时战斗后续逐个纵向交付；Schema 可描述不等于玩法已实现。

## 3. 推荐架构：三端 + 内容构建链

### 3.1 架构形态与模块职责

采用单仓、**模块化单体游戏进程 + 独立管理进程 + 隔离内容构建任务**。首版每个 Realm 只有一个游戏写进程；不预先引入大量微服务、Redis、MQ 或 Kubernetes。**PostgreSQL 是唯一业务数据库**，开发、测试和生产均使用 PostgreSQL，不以 SQLite 替代集成测试。本地文件/对象存储承载不可变产物，不属于可替换业务数据库。

- **Godot 客户端 Module**：输入、场景/UI、动画、网络状态、资源加载、握手与版本检查；不计算权威奖励、价格、胜负或任务完成。
- **Godot 创作 Module**：自定义 Resource、空间锚点、EditorPlugin、网格导出、只读玩法投影和预览。
- **Go 运行 Module**：会话、角色、世界、队伍、战斗、任务、背包/经济、副本，执行命令与状态转换。
- **Go 内容 Module**：内容类型注册、Schema、引用图、语义编译、公开/私有投影与 Release 加载。
- **Go 管理 Module**：身份、RBAC、草稿修订/冲突、审批、构建、部署、导入、审计与受控运营命令。
- **React 管理 Module**：专用内容编辑、ID 引用选择、任务/对话/副本图、差异比较、校验报告、发布与运营页面。

Leaf Adapter 放在网络和执行调度 seam；领域 Interface 不暴露 `gate.Agent` 或 ChanRPC 参数约定。基础模块对调用者隐藏队列、超时与存储 Implementation。按领域组织深模块，不用万能 ECS 或万能流程图取代经济、任务与战斗模型。

### 3.2 数据驱动规则与代码扩展

可组合的共同原语：
- **Condition**：等级、门派、道具、任务状态、队伍条件与 `all/any/not`。
- **Action**：交互、接任务、移动、释放技能等意图，必须检验权限与状态。
- **Effect**：属性变化、Buff、任务推进和奖励请求等受控变化。
- **Trigger/Event**：服务器确认的选项选择、区域进入、回合结束、指定遭遇胜利等事实。
- **Flow/StateMachine**：对话分支、任务与副本阶段，各自保留领域约束。
- **Selector/Policy**：目标筛选、AI、掉落、成长策略。

注册表绑定受审查的、带版本 Go handlers。首版使用类型化规则树、预置公式和整数/定点，不接受运营平台上传任意 Go/Lua/GDScript。编译期检查类型、引用、可达性、循环边界、效果适用领域和能力支持；执行期限制步数、深度、目标数与事件连锁。条件/公式不读网写库，时间、随机、快照由上下文注入。

任务目标明确 `target_ref/encounter_ref/count/progress_scope/repeat_policy`；只对符合接取时间、参与人、指定目标的可信事件推进。个人专属怪、公共怪、队伍共享目标分别定义，不把 FlowEvent 动画序列直接当通用任务总线。

扩展承诺分三层：新增已有机制下的内容只改定义；新流程组合现有原语；新机制/交互/表现添加 Go/GDScript Module，并同步 Schema、能力版本、编辑器与测试。禁止仅注册一个 Schema 就声称模块完整可用。

### 3.3 权威回合战斗

状态机：创建 → 收集授权命令 → 超时默认行动 → 服务器排序/结算 → 事件序列 → 下一回合/结束 → 持久化结算。统一技能消耗、行动顺序、Buff 生命周期、随机源、断线托管和参与归属。

领域计算使用独立候选状态返回新状态与待提交事实，不直接改共享对象或发奖；Unit/Buff/切片/map 不共享可变引用，提交成功后才替换当前状态。战斗事件按行动→目标→击次编号，包含 event_seq、action_id、actor/target、hit_index、实际伤害/治疗、HP 前后值、暴击事实/倍率和状态结果；消耗按行动执行一次，伤害与逐击效果按击次记录，禁止把群体伤害总值均摊回目标。未实现的多击规则必须标记 unsupported，不可静默仿真。测试旧快照不变、失败不泄漏候选状态、固定 seed/假时钟结果一致；客户端仅播放事件与提交选择，不能提交“伤害值/掉落结果”。xyq 的稳定速度排序、超时与状态清理测试可作为语义回归起点。

## 4. Godot 与网页的创作分工

### 4.1 单向字段所有权

- 网页结构化规则的真源是管理 PostgreSQL 的 revision。
- Godot 场景/素材真源是受版本控制的可信项目文件。
- **Godot 拥有**地图几何、稳定 `placement_id` 锚点及位置；**网页拥有**锚点到 NPC/怪物/出口目标/触发规则的绑定。Godot 中显示的玩法引用仅作只读预览，不能双方同时编辑同字段。
- 语义导出生成绑定 `map_id` 的 geometry revision；发布者组合它与网页规则，并校验全部锚点及引用。node path、资源 UID 和中文名称不充当跨端主键。

`.gd` 用于插件、标记节点和表现逻辑；`.tscn` 保存场景，`.tres` 保存 Resource。Go 不直接加载这些文件或执行 GDScript，而读取 Godot 导出的无引擎数据。

### 4.2 首版空间契约

首版只支持 **正交整数网格**：`grid[y][x]`、左上原点、x 向右/y 向下、0 可走/1 障碍。服务端使用格坐标；客户端按声明的 origin、tile_size 转换像素；NPC/传送锚点使用统一格位置。禁止隐式乘 20、固定瓦片尺寸或客户端兜底猜单位。

导出：schema/map/geometry 版本、地图边界、通行格、出生/放置/出口/区域锚点及稳定 ID。旋转地图、多层/非网格碰撞、任意导航多边形暂不支持，显式报错，不默默近似。传送位置、可达性、对角穿墙、寻路边界在 Go/Godot 使用共同 fixtures 验证。

薄插件调用 Godot 自己加载可信场景，不手工解析 `.tscn` 为主要生产方案；不加载陌生项目中的脚本。地图 mask/渲染前景不能转换为碰撞。

### 4.3 内容契约与编辑 Interface

- `contracts/content/*.schema.json`：字段、枚举、引用形式与版本的权威契约。
- `contracts/protocol/message_catalog.json`：稳定命令/事件 ID、payload 和兼容规则。
- `contracts/admin/openapi.yaml`：管理接口及统一错误 envelope。

Go 使用成熟 JSON Schema 库，React 使用 AJV；Go 编译器权威检查外键和玩法语义。GDScript Resource/客户端 DTO 是生成或受限加载投影，不维护第二份字段真源。P0 验证 optional/null、枚举、字符串大整数及联合类型；若全量生成不可靠，选定受限投影，不自研通用生成/解析引擎。

网页共享编辑基础提供搜索、引用选择、批量导入、错误字段定位、修订冲突；再为怪物、物品、技能、门派、任务、NPC、副本建立专用编辑 Module。图编辑采用成熟库，地图空间编辑仍在 Godot。早期任务可用结构化步骤表单，后续增强可视化图，不以 JSON textarea 充当最终产品。

## 5. 内容发布、运行版本和安全回退

Lifecycle：`Draft → Validated → Reviewed → Built → Staged → Activated → Deprecated`。审批针对确切 revision，审批后不能原地修改；构建成功和游戏进程确认激活成功是两个状态。

1. 捕获完整内容依赖闭包、geometry revision、资产源 commit、Schema/规则能力版本。
2. 校验 ID、类型、引用、资源、数量、图可达性、怪物/NPC位置、目标归属、任务/副本/技能执行支持程度。
3. 在隔离任务中生成服务端私有包与客户端公开包；运行模拟和契约测试。
4. 生成 manifest、hash/签名、审计结果并完整上传；仅在产物齐全后标记可部署，失败可安全重试。
5. staging 试玩、审批和维护排空后，游戏进程校验兼容与完整性，一次性切换完整快照；失败保持旧指针。多进程扩展时逐服确认状态，不声称跨进程瞬时原子切换。

manifest 包含 `game_id/release_id/schema_version/ruleset_version/protocol_version/client_min_build/asset_hashes/capabilities`。服务端 AI、掉落、奖励配置等私有产物不能由公共静态路由提供。

**版本原则：**战斗/副本固定开始时的 Release；任务固定定义及目标/奖励等依赖闭包，不只保存任务自身 ID。当前 Go ruleset 必须仍支持这些版本，保留 JSON 不能替代保留执行语义。不兼容代码部署必须迁移、保留执行版本或排空后切换。

兼容声明式数据/批准素材可更新；新增 Go handler 或修改 `.gd` 属于代码部署，不是通用内容热更新。首版优先维护切换而非不停服热更。地图实例不混版，旧快照按引用留存；删除 NPC、改变物品语义、改变实例格式的发布必须先迁移或拒绝。

回退只影响后续激活/内容选择，不撤销已提交奖励、词条和货币，也不保证旧客户端可读新版存档。需要回退代码或数据时必须有验证过的迁移/补偿路径，破坏性操作单独批准。

## 6. 协议、状态所有权与存储

### 6.1 WS 与管理协议

游戏首版使用 **WSS + binary frame 中的 UTF-8 JSON**。Godot 使用 `WebSocketPeer`，不以 Godot 高级 RPC/ENet 直接对接 Go。

Leaf `network.Processor` Adapter 定义稳定消息 ID 与 envelope（协议版本、请求 ID、命令类型、payload）；响应包含结果码/状态版本，推送包含服务器序列。Leaf 默认 JSON Processor 只支持单类型名外层键，不能不做适配就换 envelope；WS 不加 TCP length prefix。

只有协议/内容/客户端能力校验通过、收到 HelloAck 后才业务 ready。短期会话凭证、心跳、重连快照、未知类型拒绝、消息长度/频率上限、有界队列与慢客户端断开从首个可联通切片交付。ID 是字符串，大整数明确十进制字符串编码；货币在服务端/数据库用整数，不依靠 JSON 浮点精度。

管理采用 HTTPS REST/OpenAPI、`success/data/error/meta`、分页、字段路径错误、乐观锁和请求审计。玩家鉴权与管理员鉴权分离；管理 cookie 使用安全属性并防 CSRF，WS 显式校验 Origin/凭证。账号密码不保存进全局客户端状态用于反复重登。

### 6.2 单写者与聚合 owner

| 聚合 | 独占状态 | 禁止的跨 owner 写入 |
|---|---|---|
| Character | 资产、成长、任务实例 | 战斗/管理进程不能直接发奖或改余额 |
| Map | 位置、可见性、放置实例 | 不能直接写玩家任务和背包 |
| Party | 成员、角色/队长权限 | 不能越权提交其他人的战斗命令 |
| Battle | 回合、命令、战斗单位 | 最终奖励由角色结算事务接收 |
| Dungeon | 阶段、限次、参与集合 | 不绕过经济/角色结算入口 |

首版每 Realm 一个游戏写进程。进程以固定 PostgreSQL session connection 获取 advisory lock，再锁定 Realm owner guard 行并递增 owner_epoch，epoch 提交后才开放写入。运行写事务先对同一 guard 行执行 SELECT ... FOR SHARE 并持锁至事务结束，验证 epoch 后按稳定顺序锁聚合/检查 state_version；接管方的 epoch 更新与其互斥。禁止无锁快照校验及反向锁顺序。锁连接丢失后停止接收写操作；故障测试覆盖锁丢失和旧事务接管竞态。管理进程使用控制面权限，只写控制面；运营操作经过游戏命令 Interface，不直接改玩家状态表。

跨 goroutine 传递不可变命令/快照；每个聚合提交期间处于 pending/串行提交边界。数据库采用版本条件更新；成功提交后必须应用已提交结果，崩溃则从数据库重建，不能用“回调过期”丢弃已提交事务。未来再扩展多写进程的分片/租约机制，不把仅凭租约时间判断当可靠锁。

### 6.3 事务、幂等与恢复

奖励/购买/合成/交付：校验 → 新状态/事实 → PostgreSQL 单事务写状态、账本、操作结果、outbox → commit → 应用内存/推送。幂等键包含 game/realm/actor/operation，保存 payload_hash；相同键不同载荷必须拒绝。若 commit 返回结果未知，聚合保持 pending，不确认成功/失败、不生成新键或重发奖励；以同键/载荷核验结果。单次查询缺记录不代表回滚，须用同一唯一键的原子领取与仍在执行事务串行化，只有证明旧事务已中止后才能有界重执行。

Outbox 可至少一次重投，消费者按事件 ID 幂等，不宣称跨网络“恰好一次”；业务验收要求只产生一笔有效奖励。跨玩家交易首版延期，后续集中事务协调，不提前建设泛化分布式事务。

持久化分层：
- 控制面：games、content_drafts/revisions、geometry_revisions、releases/entries、deployment_jobs、roles、audit_logs。
- 运行面：accounts、characters、inventory_instances、wallets/ledger、quest_instances、party_members、dungeon_instances、battle_snapshots、operation_results、outbox。
- 内容定义用版本化 JSONB 与必要索引；资产/归属/金额/幂等用明确表及作用域约束，不用万能 EAV 或大 JSON blob 代替事务。
- 地图移动有 checkpoint；战斗接受命令、回合快照、最终结算有明确恢复点；崩溃不能重新随机生成已承诺结果或重复发奖。不默认建设完整事件溯源系统。
- 大资产及发布产物存文件/对象存储，数据库只存 manifest 与索引；对象存储不与数据库假装处于同一个原子事务。部署任务可恢复、可重试并清理孤立产物。

### 6.4 PostgreSQL 单库实施约束

- Go 使用 `pgx/v5/stdlib` 的 `database/sql` 驱动；连接池、超时、TLS、健康检查和事务生命周期集中在 PostgreSQL 存储模块。DSN 从环境变量/秘密管理读取，错误日志不得泄露凭据。
- 只保留按业务定义的小型 Repository 和必要的事务协调 Interface；不创建通用数据库工厂、Dialect/Capabilities 层、SQLite/MySQL Adapter 或万能 BaseRepository。领域不直接操作原始 SQL，但存储实现可以明确使用 PostgreSQL 原生能力。
- 草稿/revision 使用 JSONB，关键资产/归属/幂等使用明确表与约束；事务、行锁、advisory lock、owner_epoch、唯一约束和 outbox 按 PostgreSQL 语义实现，不为其他数据库降级。
- 只维护一套 PostgreSQL 迁移脚本；使用 PostgreSQL 专用 Tern CLI `github.com/jackc/tern/v2 v2.4.3` 执行 SQL migrations，独立受控入口运行，不作为游戏服务运行时依赖，不让游戏与管理进程竞争修改 Schema。P0 固定数据库版本与对应开发/CI 环境。
- 仓库单测可使用 fake/mock，但集成与故障验收必须使用真实 PostgreSQL；SQLite 通过不能替代 PostgreSQL 通过，也不建设三数据库兼容性矩阵。
- 当前 TBCG 没有既存 SQLite 数据库，因此这是新项目的存储选择，不是实际数据迁移；不修改或迁移参考项目 xyq 的 SQLite。

## 7. 目录与关键文件（批准实施后新增）

```text
TBCG/
  docs/planning/{prd,architecture,system_design,tech_doc,task_list}.md
  docs/adr/                         # 必要的真实取舍
  CONTEXT.md                        # 领域词汇，不混入实现设计
  contracts/content/{item,map_geometry,map_rules,quest,...}.schema.json
  contracts/protocol/message_catalog.json
  contracts/admin/openapi.yaml
  server/go.mod
  server/cmd/{game-server,admin-server,contentctl}/main.go
  server/internal/{transport/leaf,runtime,content,rules,auth,persistence,admin}/
  server/modules/{character,world,party,battle,quest,inventory,economy,dungeon}/
  client/project.godot
  client/addons/tbcg_authoring/{plugin.cfg,plugin.gd,exporter.gd}/
  client/addons/tbcg_authoring/markers/{map,npc_anchor,spawn,portal,trigger}.gd
  client/framework/{network,content,state,presentation}/
  client/games/demo/{scenes,resources}/
  admin/package.json
  admin/src/{app,shared,features/content,features/releases,features/operations}/
  games/demo/                        # 原创样例、来源/授权记录
  tools/xyq-import/                  # 独立可选的导入 Adapter
  tests/{contracts,integration,e2e,fixtures}/
  deploy/{compose.yaml,Dockerfile.game,Dockerfile.admin}/
```

核心不出现专属 xyq 名称/规则。领域文件模式为 definition/state/command/handler/test；仅为当前切片创建文件，不预先生成全部空壳。生成契约产物必须可复现，固定版本运行后无意外 diff。TBCG 已初始化 Git；按任务建立本地提交检查点以便回档，不自动推送远端。

## 8. 实施顺序与可验证切片

### 8.1 P0：规划文档与高风险可行性（批准后第一阶段）

每个任务独立验收，通常控制在 3–5 个代表文件；下面是依赖关系，不是一次性大批量编码指令。

| 任务 | 文件/动作 | 验收 | 依赖 |
|---|---|---|---|
| 0.1 需求/词汇 | `docs/planning/prd.md`、`CONTEXT.md` | 范围、四类对象、内容支持矩阵与原创样例明确 | 无 |
| 0.2 架构/owner | `architecture.md`、`system_design.md`、必要 ADR | 字段真源、坐标、单写者、版本/事务失败策略无未决项 | 0.1 |
| 0.3 工具链/技术计划 | `tech_doc.md`、`task_list.md`、三端版本文件 | 固定 Leaf、Go、Godot/模板、前端依赖与验证入口 | 0.2 |
| 0.4 WS 契约 spike | message_catalog、`processor_test.go`、`contract_probe.gd` | 真 Leaf binary JSON 双向；HelloAck 前禁止 ready；坏类型/版本/超限拒绝 | 0.2/0.3 |
| 0.5 Schema 投影 spike | item Schema、Go/AJV 测试、Godot fixture 加载 | optional/null/枚举/大整数 golden 一致，明确生成或受限投影方式 | 0.2/0.3 |
| 0.6 地图 spike | geometry Schema、`exporter.gd`、原创小场景、grid fixture | Go/Godot 坐标、边界、障碍/锚点一致；不支持几何报错 | 0.2/0.3 |
| 0.7 故障验证 spike | `transaction_probe_test.go`、`release_probe_test.go`、最小存储 Adapter | 真实 PG 下幂等、双写防护、commit 前后故障、outbox 重投、激活失败和私有包隔离通过 | 0.4–0.6 |

0.4–0.6 在契约固定后可并行；0.7 使用最小测试聚合，不提前建设完整经济系统。测试先 RED 后 GREEN。P0 退出必须有实际命令、环境和结果，缺编辑器/模板/数据库即阻塞，不能用静态阅读冒充通过。

### 8.2 后续纵向里程碑

| 阶段 | 三端交付切片 | 退出标准 | 依赖 |
|---|---|---|---|
| P1 内容发布闭环 | 管理登录/权限；一个物品草稿、校验、审计构建、公开/私有包、服务器确认激活、客户端读取 | 改值生成新 Release；旧版不变；修订冲突可见；未授权发布拒绝；激活失败保留旧版 | P0 |
| P2 场景/角色 | 玩家会话、角色创建、Godot地图导出、受控移动 | 原创地图可进入，越界/穿墙拒绝，重连恢复，版本不匹配阻止 | P1 |
| P3 NPC/任务/背包 | NPC选项、明确接取、指定目标与交付、事务奖励 | 网页新增NPC/任务后可发布运行；重复交付只一笔奖励；存档失败不提示成功 | P2 |
| P4 战斗/怪物/技能 | 回合战斗、遭遇、Buff、AI、掉落与怪物编辑 | 可新增已有机制的怪物/技能；超时/越权处理正确；重启不重复战利品 | P3 |
| P5 门派/成长/组队/副本 | 门派、成长、双人队伍、多阶段副本与编辑 | 双客户端跑通 NPC→组队→副本→战斗→奖励；实例版本固定、断线可恢复 | P4 |
| P6 创作体验增强 | 图编辑、引用追踪、批量导入、模拟器、发布差异与治理UI | 无需改规则源码增加同机制内容，错误可定位，不破坏存量实例；基础安全不是到此才补 | P5 |
| P7 xyq 可选导入 | 白名单选择、依赖闭包、ID映射、来源与语义支持报告 | dry-run、同输入同输出、不清空目标、不执行Lua、不覆盖手工revision；unsupported阻止发布 | P6 |
| P8 领域拓展 | 宠物、制作、好友、公会、交易、活动按模块各自纵切片 | 每项具备创作→发布→运行→故障测试，非只有表与Schema | P6/P7 |

每阶段遵循 TDD、覆盖率、构建和评审门禁；每个里程碑都是可运行切片。首个可玩包为原创 2 张地图、至少 1 个角色原型和 1 个门派、2 个 NPC、2 种怪物、3 个基础技能、3–5 种物品、1 条三阶段任务、1 个多阶段双人副本；数量是样例验收规模，不是框架限制。

## 9. 如何新增内容与新玩法

- **新增地图**：Godot 建场景/通行格/锚点 → 导出 geometry revision → 网页绑定 NPC、出口、遇敌规则 → 校验/发布，无需给服务端加地图专用 handler。
- **新增怪物/物品/门派**：网页使用已注册机制填写定义与引用 → 预览数值/资源 → 发布；运行时由统一执行 Module 加载。
- **新增 NPC/任务/副本**：网页配置服务、选项动作、指定目标与阶段；Godot只放置锚点；服务器校验归属、距离与状态后执行。
- **新增机制**：例如当前原语不能表达的连击、捕捉或小游戏，增加版本化 Go handler 与需要的 Godot表现 Module、Schema及编辑支持，并先写行为测试。
- **导入 xyq**：静态读取经许可的来源 → 依赖闭包/稳定ID映射 → supported/approximate/unsupported报告 → 创建草稿 → 人工重编或确认语义 → 正常发布。approximate 不默认放行，unsupported 不允许静默回退；不迁移旧协议/玩家存档。

## 10. 验证与质量闸门

1. **跨端契约**：Go/TypeScript/Godot golden fixtures 覆盖 binary WS、大整数、null/optional、未知类型、作用域、ID引用与版本不兼容。
2. **单测/覆盖率**：Go/TypeScript 手写业务代码总体与核心模块 ≥80%，固定 seed/假时钟；规则、任务、战斗、经济、权限做表驱动/fuzz。Godot 要有headless与真实客户端测试；覆盖率统计方案在P0核验，未统计不宣称达到80%。
3. **持久化集成**：真实 PostgreSQL 测事务/唯一约束、重复不同载荷、并发发奖、奖励写入失败、提交前失败、提交后发送前崩溃、重复交付、双写与outbox重投；验证失败不发成功消息、已提交结果可恢复且不重复奖励；Go 执行 `go test -race ./...`。
4. **Godot**：headless 导入/脚本/场景加载、地图导出golden、匹配模板的桌面导出；真实双客户端连接并验证地图/NPC/战斗演出，不用脚本解析通过代替可玩性。
5. **网页E2E**：Playwright/MCP 验证登录、编辑、引用错误、修订冲突、审批、激活、只读/编辑/发布角色隔离和私有包不可达。
6. **全链路**：网页新增内容 → Godot新增/调整锚点并导出 → staging → 双人副本/战斗/交付 → 一笔有效奖励 → 重启/重连不重复，状态可恢复。
7. **版本/故障**：发布中断保留旧包；旧实例保留旧闭包；不兼容内容/代码部署被阻止；回退不抹玩家资产。引用中的旧包不能被清理。
8. **构建/负载**：Go build/vet/race、前端 typecheck/test/build、Godot headless/export；记录硬件、在线连接、回合耗时、队列和数据库延迟，不在未经压测时承诺容量。
9. **评审**：实施时使用 tdd-guide；自动检查通过后用 code-reviewer、go-reviewer/TypeScript reviewer与安全评审，修复 CRITICAL/HIGH 后才能完成。

未来命令入口以各项目脚本为准：`server` 中 `go test ./...`、`go test -race ./...`、`go test -coverprofile=coverage.out ./...`、`go vet ./...`；`admin` 中 pnpm typecheck/test/build 与 Playwright；Godot 用 `--headless --path client --import`、约定 `--script` 测试入口和 `--export-release`。这些入口尚待实现，本轮没有执行上述测试，也没有声称三端可运行。

## 11. 风险与执行边界

| 风险 | 等级 | 缓解 |
|---|---|---|
| 无限DSL/所有内容首版全实现 | 高 | 配置、组合、代码扩展分层，支持矩阵，先闭环 |
| 双规则真源/空间单位不一致 | 高 | 锚点与规则单向所有权、grid契约、golden |
| 奖励重放/客户端作弊/双写 | 高 | Go权威、作用域与授权、数据库事务幂等、单写者 |
| 内容发布破坏存量实例/代码不兼容 | 高 | 完整闭包、ruleset版本、阻断/迁移/排空、留存 |
| 陌生脚本/导入清空/素材权利 | 高 | 可信构建隔离、静态导入、dry-run、原创/明确许可 |
| 管理权限/发布供应链 | 高 | 编辑审核发布分权、审计、哈希/签名、私有包隔离、CSRF/Origin |
| 过早集群化/模板整仓复制 | 中 | 单写进程模块化、适配复用、测量后扩展 |

整体复杂度 **高**，包含创作工具、游戏运行与控制面三个产品。批准后先交付 P0 文档与真实集成验证，再按 P1→P5 达到最小可玩闭环，P6以后推广创作体验和可选迁移。任何破坏性数据变更、素材公开分发、正式部署、提交或推送另行授权；不修改正在迁移的 xyq，不一次性生成大而空的工程。
