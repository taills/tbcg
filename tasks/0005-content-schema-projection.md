# 0005 共享内容 Schema 投影 spike

**状态：**进行中
**目标：**验证一类内容定义如何从单一 JSON Schema 投影到 Go、React 与 Godot，避免三端手写漂移。

## 范围

- 先选最小物品或怪物 Definition Schema，定义稳定 content_id、revision、版本、公开字段和引用形式。
- Go 用成熟 JSON Schema 实现，React 用 AJV；Godot 对共享 fixture 做受限加载/字段类型校验。
- 比较 optional/null、枚举、整数范围、字符串化大整数、未知字段策略和错误路径。
- 同时验证定义修订和公开/私有字段投影的分离。

## 验收标准

- [ ] 同一份合法/非法 fixtures 在 Go、TypeScript 和 Godot 得到约定一致的有效性结论与字段路径。
- [ ] 选择并记录可靠生成策略或受限投影；无意外类型转换、JSON 数值精度损失或私有字段泄漏。
- [ ] Schema 版本与 Definition ID/revision 规则清晰，可由后续内容类型复用。

## 验证

- Go 与前端 Schema 单测，Godot headless fixture 测试；对无效枚举、缺失引用、null 与边界整数做负例。
- 检查生成步骤可复现且仓库 diff 干净；若本机 Godot 不可用，任务未通过。

## 依赖与文件

**依赖：**0001、0002。
**预计文件：**`contracts/content/item.schema.json`、`server/internal/content/schema/`、`admin/src/shared/content/`、`client/framework/content/`、共享 fixtures。
