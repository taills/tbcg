# 0005 共享内容 Schema 投影 spike

**状态：**已完成（三端测试与审查已通过）
**目标：**验证一类内容定义如何从单一 JSON Schema 投影到 Go、React 与 Godot，避免三端手写漂移。

## 范围

- 先选最小物品或怪物 Definition Schema，定义稳定 content_id、revision、版本、公开字段和引用形式。
- Go 用成熟 JSON Schema 实现，React 用 AJV；Godot 对共享 fixture 做受限加载/字段类型校验。
- 比较 optional/null、枚举、整数范围、字符串化大整数、未知字段策略和错误路径。
- 同时验证定义修订和公开/私有字段投影的分离。

## 验收标准

- [x] 同一份合法/非法 fixtures 在 Go、TypeScript 和 Godot 得到约定一致的有效性结论与字段路径。
- [x] 选择并记录可靠生成策略或受限投影；无意外类型转换、JSON 数值精度损失或私有字段泄漏。
- [x] Schema 版本与 Definition ID/revision 规则清晰，可由后续内容类型复用。

## 验证

- Go 与前端 Schema 单测，Godot headless fixture 测试；对无效枚举、缺失引用、null 与边界整数做负例。
- 检查生成步骤可复现且仓库 diff 干净；若本机 Godot 不可用，任务未通过。

### 本地验证记录

- 选择受限投影方案：Go/Ajv 直接编译共享 JSON Schema；Godot 注入可信公开 Schema，返回只含公开字段的新 Resource。不生成通用 Schema 引擎、不复制第二份定义。
- Go 1.26.4：`go test -race -count=1 -cover ./internal/content/schema` 通过，语句覆盖率 88.2%；`go vet ./...` 通过。
- 管理端：16 项测试通过；V8 语句覆盖率 94.2%、分支覆盖率 81.63%，满足 80% 门禁；typecheck、build、format、frozen install 和高危依赖 audit 通过。覆盖率只排除测试文件、测试初始化和 DOM 挂载入口 `main.tsx`。
- Godot 4.7.1：公开内容 fixture 脚本与 editor headless 导入通过；未测量 GDScript 覆盖率，不声称达到 80%。
- 已覆盖必填/未知属性、枚举、null、整数范围、大整数字符串、Definition/公开投影身份不一致、私有数据隔离与 Ajv 原型链必填属性拒绝。

## 依赖与文件

**依赖：**0001、0002。
**预计文件：**`contracts/content/item.schema.json`、`server/internal/content/schema/`、`admin/src/shared/content/`、`client/framework/content/`、共享 fixtures。
