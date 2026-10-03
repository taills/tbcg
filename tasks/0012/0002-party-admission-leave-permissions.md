# 0012.2 队伍准入与离队权限

**状态：**未开始
**依赖：**0010、0011
**目标：**交付队伍创建、邀请、加入、准入校验、离队和成员命令权限，为双人副本提供安全 Party owner。

## 范围

- Go 实现 Party owner、队长权限、邀请/申请、加入/拒绝、踢出/离队、断线状态和成员 state_version。
- 校验角色在线状态、地图/距离、队伍人数、战斗中/副本中限制、重复请求和跨队伍竞态。
- Godot 展示队伍列表、邀请、成员状态和权限错误；网页仅提供必要的调试/运营可观察入口，不直接篡改成员资产。

## 不做范围

- 不实现副本阶段图、限次和奖励；这些属于 0012.3。
- 不允许 Party 直接写成员背包、任务或成长状态。

## 验收标准

- [ ] 两名玩家可完成邀请、加入和准备流程；非队长不能越权发起需要队长权限的命令。
- [ ] 战斗中、副本中、已退出、重复加入和并发离队场景有确定结果且不破坏成员状态。
- [ ] Party command 权限可被 0011 战斗和 0012.3 副本复用，非法成员不能提交他人战斗/副本命令。

## 代表文件

- `server/modules/party/**`
- `contracts/protocol/message_catalog.json`
- `server/modules/session/**`
- `client/framework/presentation/party/**`
- `tests/e2e/**/party*`

## 验证

以下命令为后续实现验收占位，当前未执行，也不表示已通过：

```bash
go test ./server/modules/party/... -race -cover
pnpm --dir admin test -- party
/Applications/Godot.app/Contents/MacOS/Godot --headless --path client --run-tests party_permissions
```
