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
