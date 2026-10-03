# 0012.1 门派、成长、装备与物品使用

**状态：**未开始
**依赖：**0010、0011
**目标：**补齐首个可玩版本要求的角色门派、等级成长、装备穿脱和物品使用能力，为组队副本准入与战斗属性提供权威运行基础。

## 范围

- 网页定义并发布至少一个门派、入门条件、门派技能集、等级解锁、等级曲线、属性成长、装备限制、装备模板和可使用物品效果。
- Go 维护 Character owner 下的门派入门、经验、等级、技能解锁、装备槽、物品使用与属性重算；背包扣减、装备实例状态、账本和 outbox 使用 PostgreSQL 事务。
- Godot 展示门派/等级/装备/物品使用结果，不在客户端重算权威属性或消耗。

## 不做范围

- 不实现宠物、伙伴、坐骑、制作或商店；这些属于 0015。
- 不实现双人副本阶段图或队伍准入命令；这些属于 0012.2 和 0012.3。

## 验收标准

- [ ] 角色只有满足入门条件才能加入门派，升级后属性按固定 Release 的成长定义计算并持久化。
- [ ] 门派技能集按等级解锁并影响战斗可用技能；未解锁、错误门派或错误 Release 的技能不能使用。
- [ ] 装备穿脱与物品使用校验门派、等级、装备限制、槽位、所有权和数量，失败不改变实例状态。
- [ ] 战斗读取的属性快照与已提交角色/装备状态一致，事务重试不重复扣物品或发效果。

## 代表文件

- `contracts/content/sect*.schema.json`
- `contracts/content/equipment*.schema.json`
- `server/modules/character/**`
- `server/modules/inventory/**`
- `admin/src/features/content/**`、`client/framework/presentation/character/**`

## 验证

以下命令为后续实现验收占位，当前未执行，也不表示已通过：

```bash
go test ./server/modules/character/... ./server/modules/inventory/... -cover
pnpm --dir admin test -- content character equipment
/Applications/Godot.app/Contents/MacOS/Godot --headless --path client --run-tests character_equipment_items
```
