# 内容契约 v1

此目录是内容字段与校验的唯一 Schema 来源。Go 和管理端加载同一份 Draft 2020-12 Schema；Godot 只加载经过批准的公开投影 Schema，不执行服务端规则。

## 身份与版本

- `game_id`：游戏标识。
- `content_id`：稳定内容标识，例 `demo:item/healing-potion`；不得使用显示名作为 ID。
- `revision`：正整数修订号，最高 `2147483647`。
- `schema_version`：当前为 `1`。
- 完整 Definition 的顶层身份与 `public` 中的 `game_id/content_id/revision/schema_version` 必须一致。标准 JSON Schema 不表达两个属性之间的动态相等约束，因此 Go 和管理端执行一层小型语义校验；不一致返回 `/public/<field>` 上的 `invalid_value`。

## 完整定义与公开投影

`item.schema.json` 描述完整定义，其中 `server` 是仅供管理及服务端使用的字段。`item-public.schema.json` 描述独立公开投影，禁止未知属性及 `server` 字段。公开投影由经过验证的 Definition 的公开字段生成，不更改输入对象。

Godot 只验证独立公开投影：完整定义的 `/public/item_type` 错误，在公开投影对应 `/item_type`。对相同公开输入，三端必须返回相同有效性判断和规范化字段路径/错误码。

## 数值与缺省

- `price_minor`、`reward_value_minor` 是规范十进制字符串，保留超过 JavaScript/Godot JSON 安全整数范围的值，禁止把收到的 number 再转为字符串。
- 当前金额范围为 0 到 18 位非负十进制整数；这不是完整 signed int64 范围。
- `description` 可缺省，但不可为 `null`；缺省时 Godot 表现 Resource 可显示为空文本，不改变传输契约。
- `stack_limit` 为 1–999 的整数；布尔值和带小数的数值均无效。

## 错误约定

错误最小结构为 `{ "path": "/field", "code": "invalid_value" }`，可附加面向开发者的 `message`，测试不比较本地化消息文本。

| code | 含义 |
|---|---|
| `missing_field` | 缺失必需属性，path 指向缺失属性 |
| `unknown_field` | 未声明的属性或混入公开投影的私有字段 |
| `invalid_value` | 类型、枚举、模式、数值范围或身份字段不一致 |
| `invalid_json` | 原始文本不是合法 JSON，path 为空字符串 |
| `schema_violation` | 可信 Schema 缺失、不受支持或初始化失败 |

path 使用 RFC 6901 JSON Pointer，`~` 编码为 `~0`，`/` 编码为 `~1`。Go/Ajv 可以返回多个已排序去重的错误；Godot 受限投影适配器可报告首个错误。单缺陷 fixtures 应具有一致的 path/code。

## 信任边界

只编译仓库提供的可信 Schema；禁止从内容输入加载任意 `$ref`、脚本或外部 URL。Go 的编译 loader 拒绝非注册资源；Ajv 同步编译只注册本地 Schema；Godot 的生产适配器接收可信 Schema/目录数据的依赖注入，不从 `res://../` 读取工作区文件。

`tests/fixtures/content/` 是三端共享的验收数据，不含真实玩家信息或真实凭据。Schema 更新须同时运行 Go、管理端和 Godot 的相关测试；支持投影加载不代表已实现物品使用、奖励或发布业务。

## 全内容类型（任务 0010–0015）

`types/` 下为其余 20 种内容类型的 Definition Schema（`urn:tbcg:content:<type>:v1`），公开投影 Schema 为同文件的
`#/$defs/public`，并通过 `types/common.schema.json` 复用身份、属性、奖励等定义。内容类型由内容 ID 的类型段决定：
`<game>:<type>/<slug>`，例如 `demo:npc/guide`；未知类型一律拒绝。

| 类型 | 公开（客户端可见） | 服务端私有 |
| --- | --- | --- |
| map | 名称、几何（`map_geometry.schema.json`）、传送、区域 | 出生锚点、单次最大步数、最小步距 |
| archetype / sect / skill | 名称、描述、外观、门派技能解锁、技能目标与消耗 | 属性/成长/升级曲线、门派加成、技能倍率/暴击/Buff |
| monster / encounter | 名称、外观、等级、遭遇说明 | 属性、技能、AI 几率、经验/货币/掉落、编成、回合上限 |
| npc / quest | 名称、外观、地图锚点、任务阶段描述 | 对话图与动作、交互距离、目标、前置、奖励 |
| equipment / consumable | 槽位、等级、门派限制、属性加成、说明 | 使用效果 |
| dungeon | 名称、人数、等级、限次、阶段名 | 入口 NPC、阶段遭遇、奖励 |
| pet / partner / mount | 名称、外观、技能、招募条件、坐骑加成 | 属性、成长、升级曲线 |
| recipe / shop | 材料/产出、商品/价格/限购 | 成功率、全服库存 |
| social / guild / trade / event | 上限、成本、税率、时间窗口、目标 | 待处理上限、官员权限、不可交易物品、活动奖励 |

跨内容引用（地图锚点、NPC 动作目标、任务目标、掉落物品等）由 Go `internal/content/catalog` 校验；构建 worker、runtime
激活与管理端校验报告使用同一套规则。`games/demo/content/` 是原创示例内容（`<type>/<slug>.json` 布局），
`server/cmd/content-tool` 可校验该目录、生成开发公开包与战斗 golden fixture。
