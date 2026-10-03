# 0007 PostgreSQL 幂等结算与发布故障验证

**状态：**未开始
**目标：**用最小业务聚合证明奖励、账本、幂等记录和 outbox 可在 PostgreSQL 中一致提交，并验证发布失败安全性。

## 范围

- 幂等键按 game/realm/actor/operation 作用域，存 canonical payload_hash 和确定结果；相同键不同 payload 必须拒绝。
- 单事务写状态、资产账本、operation_result、outbox；仅在 commit 确认后更新运行态与回应成功。
- 明确 commit 结果未知状态：同一幂等键向 PostgreSQL 核验；核验前保持 pending，不直接再次发奖。
- 以 PostgreSQL row guard/epoch 验证单写 owner fencing；发布失败保持旧 Release，私有包不可公共访问。

## 验收标准

- [ ] 奖励写入失败、事务提交前失败和重复交付不会造成成功确认或重复资产；同键不同载荷得到冲突。
- [ ] 提交后发送前崩溃可恢复已提交结果；outbox 可重投且消费者幂等。
- [ ] 旧 owner 的写事务不能越过接管 epoch；发布构建/激活失败保持旧版本指针及私有数据隔离。

## 验证

- 真实 PostgreSQL 故障注入覆盖并发、事务中止、commit 结果不确定、锁等待、outbox 重试和激活失败。
- 重启进程并核对唯一账本记录、持久化余额和 outbox 状态；保存实际测试结果。

## 依赖与文件

**依赖：**0003、0004、0005、0006。
**预计文件：**`server/internal/persistence/`、`server/modules/economy/` 最小模块、`server/internal/content/release/`、`tests/integration/`。
