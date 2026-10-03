# ADR 0001：PostgreSQL 是唯一业务数据库

**状态：**已接受

TBCG 开发、测试和生产只使用 PostgreSQL；不提供 SQLite/MySQL Adapter、SQL 方言层或多数据库兼容测试矩阵。多库可移植性会扩大迁移、锁、事务、幂等及 DDL 的实现和验证成本，而本项目没有切换数据库的需求；Leaf 的抽象不改变该决定。存储仍以小型业务 Repository 与事务接口隔离领域逻辑，但可明确采用 PostgreSQL JSONB、行锁和事务能力。文件/对象存储只承载不可变资产与发布产物，不是业务数据库。本决定不迁移或修改参考项目 xyq 的 SQLite 数据。