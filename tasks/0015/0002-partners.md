# 0015.2 伙伴

**状态：**开发完成（待评审）——开发与本机验证见 [运行面验证记录](../../docs/verification/0008-0015-runtime.md)；独立评审、80% 覆盖率与远端 CI 未完成，不能标记“已完成”
**依赖：**0012、0013、0015.1 的 common owner seam（未合并前不得与 0015.1/0015.3 并行修改同一 ownership 文件）
**目标：**交付最低可玩的伙伴招募、编队、战斗助战和剧情表现闭环。

## 最低可玩范围

- 网页定义并发布伙伴原型、招募条件、成长/技能、编队限制和表现绑定。
- Go 维护 Partner owner、招募、解散/禁用、编队槽位、战斗助战投影和幂等招募事务。
- Godot 展示伙伴招募结果、队伍/助战位置、基础动作和状态错误。

## 不做范围

- 不实现复杂好感剧情树、伙伴远征、跨玩家租借或付费抽卡系统。
- 不复用宠物规则时绕过伙伴的招募条件、编队限制和剧情状态。

## 验收标准

- [x] 玩家满足条件后可招募一个伙伴并加入助战编队，战斗投影由 Go 生成。
- [ ] 重复招募、错误条件、并发编队和状态版本冲突不会破坏伙伴归属或战斗阵容。（已实现，故障注入测试待补）
- [ ] 网页定义/发布、Go 权威事务/owner、Godot 表现、真实 PostgreSQL、三端测试、覆盖率 80%+ 和评审均完成后才可标记 supported。

## 代表文件

- `contracts/content/partner*.schema.json`
- `server/modules/partner/**`
- `server/modules/petowner/**`
- `admin/src/features/content/partners/**`
- `client/framework/presentation/partners/**`

## 验证

以下为原计划的验收命令形态；实际已执行的验证见运行面验证记录（Go 包路径为 `server/modules/<domain>`，Godot 测试以 `--script res://tests/<path>.gd` 运行）：

```bash
go test ./server/modules/partner/... ./server/modules/petowner/... -race -cover
pnpm --dir admin test -- partners
/Applications/Godot.app/Contents/MacOS/Godot --headless --path client --run-tests partners
```
