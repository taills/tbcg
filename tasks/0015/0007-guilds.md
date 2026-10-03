# 0015.7 公会

**状态：**未开始
**依赖：**0012、0013、0015.6（必须先确认双人副本 MVP、创作/发布体验和基础好友/社交关系成熟）
**目标：**交付最低可玩的公会创建、加入、成员权限和基础公告闭环。

## 最低可玩范围

- 网页定义并发布公会规则、人数限制、创建成本、职位权限和可见运营配置。
- Go 维护 Guild owner、成员关系、职位权限、申请/邀请、公告修改和创建成本事务；货币扣减与公会创建同 PostgreSQL 事务提交。
- Godot 展示公会信息、申请/邀请、成员列表、职位权限错误和公告。

## 不做范围

- 不实现公会战、跨服联盟、复杂捐献科技或公会拍卖。
- 不允许管理端绕过 Guild owner 直接改成员资产或职位状态。

## 验收标准

- [ ] 玩家可消耗指定资源创建公会，另一名玩家可申请或被邀请加入。
- [ ] 职位权限、重复加入、并发踢人/离会、创建事务未知结果均有确定且可恢复的结果。
- [ ] 网页定义/发布、Go 权威事务/owner、Godot 表现、真实 PostgreSQL、三端测试、覆盖率 80%+ 和评审均完成后才可标记 supported。

## 代表文件

- `contracts/content/guild*.schema.json`
- `server/modules/guild/**`
- `server/modules/social/**`
- `admin/src/features/content/guilds/**`
- `client/framework/presentation/guilds/**`

## 验证

以下命令为后续实现验收占位，当前未执行，也不表示已通过：

```bash
go test ./server/modules/guild/... ./server/modules/social/... -race -cover
pnpm --dir admin test -- guilds
/Applications/Godot.app/Contents/MacOS/Godot --headless --path client --run-tests guilds
```
