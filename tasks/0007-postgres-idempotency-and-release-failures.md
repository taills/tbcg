# 0007 PostgreSQL 幂等结算与发布故障验证

**状态：**已完成
**目标：**用最小业务聚合证明奖励、账本、幂等记录和 outbox 可在 PostgreSQL 中一致提交，并验证发布失败安全性。

## 范围

- 幂等键按 game/realm/actor/operation 作用域，存 canonical payload_hash 和确定结果；相同键不同 payload 必须拒绝。
- 单事务写状态、资产账本、operation_result、outbox；仅在 commit 确认后更新运行态与回应成功。
- 明确 commit 结果未知状态：同一幂等键向 PostgreSQL 核验；核验前保持 pending，不直接再次发奖。
- 以 PostgreSQL row guard/epoch 验证单写 owner fencing；发布失败保持旧 Release，私有包不可公共访问。

## 验收标准

- [x] 奖励写入失败、事务提交前失败和重复交付不会造成成功确认或重复资产；同键不同载荷得到冲突。
- [x] 提交后发送前崩溃可恢复已提交结果；outbox 可重投且消费者幂等。
- [x] 旧 owner 的写事务不能越过接管 epoch；发布构建/激活失败保持旧版本指针及私有数据隔离。

## 验证

- 真实 PostgreSQL 故障注入覆盖并发、事务中止、commit 结果不确定、锁等待、outbox 重试和激活失败。
- 重启进程并核对唯一账本记录、持久化余额和 outbox 状态；保存实际测试结果。

### 实测与审查记录

- 真实 PostgreSQL 18.3 下完成奖励/账本失败、提交拒绝、COMMIT 成功后确认丢失、接管等待、同键并发结算、outbox 重投与消费者事务回滚；新进程可恢复唯一结果。
- 激活事务内验证 owner epoch；同 Release 重试与跨 realm 复用核对完整定义/产物，不覆盖既有文件。并发发布通过 builder 会合屏障确保双方读取同一旧指针，race 三轮通过。
- `Settle` 要求当前 owner；旧 epoch 只读恢复使用 `Recover`，新 owner 可用当前 epoch 与同键重放，真实 PG 验证资产不重复。
- 全量 Go race、build、vet、staticcheck、goimports 和 gosec 通过。经济模块语句覆盖 83.1%、Release 84.9%、全部 Go 87.8%。三路独立审查无剩余 HIGH/CRITICAL；MEDIUM 接口约定和确定性并发测试已处理。
- 详细证据、测试注入边界与清理记录见 [`../docs/verification/0007.md`](../docs/verification/0007.md)。本任务不代表 0008 管理平台、公共 HTTP 路由或真实游戏运行进程激活确认已完成。


## 依赖与文件

**依赖：**0003、0004、0005、0006。
**预计文件：**`server/internal/persistence/`、`server/modules/economy/` 最小模块、`server/internal/content/release/`、`tests/integration/`。
