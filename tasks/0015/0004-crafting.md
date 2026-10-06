# 0015.4 制作

**状态：**开发完成（待评审）——开发与本机验证见 [运行面验证记录](../../docs/verification/0008-0015-runtime.md)；独立评审、80% 覆盖率与远端 CI 未完成，不能标记“已完成”
**依赖：**0012、0013（必须先确认双人副本 MVP 与创作/发布体验成熟）
**目标：**交付最低可玩的配方制作闭环：网页发布配方，Go 权威扣材料和产出物品，Godot 展示制作结果。

## 最低可玩范围

- 网页定义并发布配方、材料、产物、等级/门派条件、失败原因和 Release 能力。
- Go 在 Character/Inventory owner 边界内校验材料所有权、数量、条件、随机源和幂等键；扣料、产物、账本/outbox 同 PostgreSQL 事务提交。
- Godot 展示可制作列表、材料缺口、制作成功/失败和新增物品。

## 不做范围

- 不实现玩家摆摊、拍卖、跨玩家订单或复杂生产排队。
- 不允许仅存在配方 Schema 就宣称制作功能完成。

## 验收标准

- [x] 玩家可用背包材料制作一个发布产物，扣料和产出在同一事务中原子提交。
- [ ] 材料不足、条件不满足、重复请求和提交未知结果不会重复扣料或重复产出。（已实现，故障注入测试待补）
- [ ] 网页定义/发布、Go 权威事务/owner、Godot 表现、真实 PostgreSQL、三端测试、覆盖率 80%+ 和评审均完成后才可标记 supported。

## 代表文件

- `contracts/content/crafting*.schema.json`
- `server/modules/crafting/**`
- `server/modules/inventory/**`
- `admin/src/features/content/crafting/**`
- `client/framework/presentation/crafting/**`

## 验证

以下为原计划的验收命令形态；实际已执行的验证见运行面验证记录（Go 包路径为 `server/modules/<domain>`，Godot 测试以 `--script res://tests/<path>.gd` 运行）：

```bash
go test ./server/modules/crafting/... ./server/modules/inventory/... -race -cover
pnpm --dir admin test -- crafting
/Applications/Godot.app/Contents/MacOS/Godot --headless --path client --run-tests crafting
```
