# 0015.5 商店

**状态：**未开始
**依赖：**0012、0013（必须先确认双人副本 MVP 与创作/发布体验成熟）
**目标：**交付最低可玩的 NPC/系统商店购买闭环，包含网页商品定义、Go 权威价格/库存事务和 Godot 商店界面。

## 最低可玩范围

- 网页定义并发布商店、商品、价格、购买条件、限购/库存策略和 NPC 入口绑定。
- Go 校验商店可见性、货币余额、库存/限购、幂等键和 Release 版本；扣款、发物品、账本/outbox 同 PostgreSQL 事务提交。
- Godot 展示商店列表、价格、库存/限购、购买结果和错误原因。

## 不做范围

- 不实现拍卖行、玩家寄售、跨服市场或动态经济调价。
- 不让客户端价格、显示名或排序决定实际扣款。

## 验收标准

- [ ] 玩家可在发布商店购买一个商品，扣款和发物品原子提交且可审计。
- [ ] 余额不足、库存不足、条件不满足、重复购买和提交未知结果不会造成负余额或重复发货。
- [ ] 网页定义/发布、Go 权威事务/owner、Godot 表现、真实 PostgreSQL、三端测试、覆盖率 80%+ 和评审均完成后才可标记 supported。

## 代表文件

- `contracts/content/shop*.schema.json`
- `server/modules/shop/**`
- `server/modules/economy/**`
- `admin/src/features/content/shops/**`
- `client/framework/presentation/shops/**`

## 验证

以下命令为后续实现验收占位，当前未执行，也不表示已通过：

```bash
go test ./server/modules/shop/... ./server/modules/economy/... -race -cover
pnpm --dir admin test -- shops
/Applications/Godot.app/Contents/MacOS/Godot --headless --path client --run-tests shops
```
