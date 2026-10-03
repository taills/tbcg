# 0001 项目规格与架构文档

**状态：**已完成
**目标：**把 `PLAN.md` 固化为可供各实现任务引用的项目规格、领域词汇与架构决定；不写运行时代码。

## 范围

- 编写 `docs/planning/prd.md`，定义首版玩家/内容作者场景、MVP 范围、支持等级与明确的非目标。
- 编写 `docs/planning/architecture.md` 与 `system_design.md`，说明 Godot/Go/管理平台职责、内容真源、地图网格协议、领域 owner、Release 和故障恢复策略。
- 创建根目录 `CONTEXT.md`，只记录确定的领域词汇，不能代替架构文档。
- 只对真正难反转且存在权衡的决定新增 ADR；不得重复整份 PLAN。

## 验收标准

- [ ] 定义、Instance、Presentation、Release、Game、ContentPackage、Realm、Revision、placement_id 用语一致，任务边界与非目标可测试。
- [ ] 明确 Godot 拥有几何/位置、网页拥有规则绑定；Go 是奖励/战斗/任务状态权威；唯一数据库为 PostgreSQL。
- [ ] 写明首版必须具备 3 个基础技能、多阶段双人副本，以及多击技能支持门禁；写明事务未知结果的安全恢复、`owner_epoch` 接管与运行写事务的互斥锁顺序、Map Instance 不混版、实例格式迁移/拒绝和代码/持久化回退路径。

## 验证

- 对照 `PLAN.md` 逐节检查无相互矛盾的数据库、版本、协议或内容所有权决策。
- 由独立架构审查者只读审查术语、一致性、遗漏与可执行性；修正后由 Lead 确认。

## 依赖与文件

**依赖：**无。
**预计文件：**`docs/planning/prd.md`、`docs/planning/architecture.md`、`docs/planning/system_design.md`、`CONTEXT.md`、可选 `docs/adr/0001-*.md`。
