# 0003 PostgreSQL Schema 与迁移入口

**状态：**进行中
**目标：**提供唯一 PostgreSQL 持久化基线、按业务划分的小型 Repository/事务接口和受控迁移入口。

## 范围

- 固定 PostgreSQL 开发/CI 镜像版本，明确连接池、环境 DSN、TLS、超时与健康检查；密钥不能写入仓库。
- 定义控制面、运行态的首批最小表/约束：game/realm、revision/release、角色、操作幂等结果、账本、outbox 与 owner guard。
- 设计业务 Repository 和事务协调 Interface；事务回调中所有读写必须使用同一事务，不暴露 `*sql.DB`/原始 SQL 给领域模块。
- 加入唯一、外键、非空、作用域和 state_version 约束；采用 PostgreSQL 专用 Tern CLI v2.4.3 执行版本化 SQL migrations，使用 PG* 环境变量传递连接配置，不在仓库保存数据库凭据。
- 明确 Tern `migrate`/`new` 的本地和 CI 命令，迁移文件使用可回滚的 up/down SQL 区段；迁移 Schema 只由独立 CLI 执行。

## 验收标准

- [ ] 新 PostgreSQL 空库能安装 Schema；连续执行迁移命令结果安全、迁移版本有记录。
- [ ] Repository/事务 Interface 的文档明确事务边界、错误分类、幂等和 commit 结果未知处理。
- [ ] 关键业务约束由 PostgreSQL 约束兜底，迁移失败/连接关闭有上下文错误且不泄漏凭据。

## 验证

- 使用真实 PostgreSQL 执行空库安装、迁移升级、重复运行与备份恢复集成测试。
- 并发测试确认唯一约束和 state_version 冲突可被识别，不假定失败事务仍可继续使用。

## 依赖与文件

**依赖：**0001、0002。
**预计文件：**`server/internal/persistence/postgres/`、`server/internal/persistence/repository.go`、`server/internal/persistence/uow.go`、`server/migrations/*.sql`、可选无凭据 `tern.conf`、`deploy/compose.yaml`、PostgreSQL 集成测试与 CI 数据库服务步骤。迁移入口使用独立固定版本的 `tern migrate`，不新增自制 Go migration runner。
