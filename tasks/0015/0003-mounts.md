# 0015.3 坐骑

**状态：**开发完成（待评审）——开发与本机验证见 [运行面验证记录](../../docs/verification/0008-0015-runtime.md)；独立评审、80% 覆盖率与远端 CI 未完成，不能标记“已完成”
**依赖：**0012、0013、0015.1 的 common owner seam（未合并前不得与 0015.1/0015.2 并行修改同一 ownership 文件）
**目标：**交付最低可玩的坐骑获得、装备、移动表现和属性加成闭环。

## 最低可玩范围

- 网页定义并发布坐骑原型、获得条件、装备限制、移动表现和可选属性加成。
- Go 维护 Mount owner、获得、装备/卸下、状态版本、属性投影和幂等事务。
- Godot 展示坐骑外观、上/下骑状态、移动表现切换和错误反馈。

## 不做范围

- 不实现飞行、多层导航、竞速活动、坐骑交易或复杂培养树。
- 不让客户端通过表现速度改变服务端权威移动规则。

## 验收标准

- [x] 玩家可获得并装备一个坐骑，服务端权威状态决定可见外观和可用加成。
- [ ] 权属、准入条件、重复领取、并发装备和状态版本冲突均有明确失败或成功结果。（已实现，故障注入测试待补）
- [ ] 网页定义/发布、Go 权威事务/owner、Godot 表现、真实 PostgreSQL、三端测试、覆盖率 80%+ 和评审均完成后才可标记 supported。

## 代表文件

- `contracts/content/mount*.schema.json`
- `server/modules/mount/**`
- `server/modules/petowner/**`
- `admin/src/features/content/mounts/**`
- `client/framework/presentation/mounts/**`

## 验证

以下为原计划的验收命令形态；实际已执行的验证见运行面验证记录（Go 包路径为 `server/modules/<domain>`，Godot 测试以 `--script res://tests/<path>.gd` 运行）：

```bash
go test ./server/modules/mount/... ./server/modules/petowner/... -race -cover
pnpm --dir admin test -- mounts
/Applications/Godot.app/Contents/MacOS/Godot --headless --path client --run-tests mounts
```
