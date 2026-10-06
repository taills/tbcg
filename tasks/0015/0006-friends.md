# 0015.6 好友

**状态：**开发完成（待评审）——开发与本机验证见 [运行面验证记录](../../docs/verification/0008-0015-runtime.md)；独立评审、80% 覆盖率与远端 CI 未完成，不能标记“已完成”
**依赖：**0012、0013（必须先确认双人副本 MVP 与创作/发布体验成熟）
**目标：**交付最低可玩的好友申请、同意、删除和在线展示闭环，为后续公会/交易提供基础社交关系。

## 最低可玩范围

- 网页定义并发布好友规则、申请限制、黑名单/隐私策略和必要运营查询；规则随 Release 或受控运营配置生效。
- Go 维护 Social/Friend owner 或成对关系事务，校验申请、同意、删除、黑名单/重复关系、状态版本和审计日志。
- Godot 展示好友列表、申请、同意/拒绝、在线状态和错误反馈。

## 不做范围

- 不实现聊天审核平台、推荐算法、跨服好友或社交图挖掘。
- 不把好友关系当成可由任意管理端直接改写的普通配置。

## 验收标准

- [x] 两名玩家可完成申请和同意流程，并在客户端看到一致好友关系。
- [ ] 重复申请、互删并发、黑名单限制和状态版本冲突不会产生单边幽灵关系。（已实现，故障注入测试待补）
- [ ] 网页定义/发布、Go 权威事务/owner、Godot 表现、真实 PostgreSQL、三端测试、覆盖率 80%+ 和评审均完成后才可标记 supported。

## 代表文件

- `server/modules/social/friends/**`
- `server/modules/session/**`
- `contracts/protocol/message_catalog.json`
- `admin/src/features/social/**`
- `client/framework/presentation/friends/**`

## 验证

以下为原计划的验收命令形态；实际已执行的验证见运行面验证记录（Go 包路径为 `server/modules/<domain>`，Godot 测试以 `--script res://tests/<path>.gd` 运行）：

```bash
go test ./server/modules/social/... -race -cover
pnpm --dir admin test -- friends social
/Applications/Godot.app/Contents/MacOS/Godot --headless --path client --run-tests friends
```
