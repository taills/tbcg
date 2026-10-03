# 0008 第一个内容编辑及发布纵切片

**状态：**未开始
**目标：**完成一个物品 Definition 从网页草稿、校验、审批到 Go/Godot 可读取 Release 的端到端内容路径。

## 范围

- 实现管理员身份、最小 RBAC、审计事件与乐观锁草稿 revision。
- React 物品编辑/预览调用 Go 管理 API；字段错误回传到表单，非法引用不能发布。
- 构建不可变 Release manifest/hash，生成服务端私有与客户端公开投影并经运行进程确认激活。
- staging/激活失败保留旧 release_id，公开接口不得读取服务端私有字段。

## 验收标准

- [ ] 授权编辑者能新建/修改/对比物品 revision；只读用户不能写；无发布权限者不能批准/激活。
- [ ] 同一 revision 冲突返回可操作错误，审批后数据不可原地改写，Release hash 可重现。
- [ ] Godot 客户端只获取公开数据；私有 drop/reward 字段通过 API/静态路径均不可访问。

## 验证

- Go API/PostgreSQL 集成测试，React 单测与 E2E 覆盖编辑、冲突、权限及发布失败。
- staging 运行中修改草稿，确认旧激活版本持续服务到新版本被确认。

## 依赖与文件

**依赖：**0003、0005、0007。
**预计文件：**`server/modules/admin/`、`server/internal/content/release/`、`contracts/admin/openapi.yaml`、`admin/src/features/content/`、`admin/src/features/releases/`。
