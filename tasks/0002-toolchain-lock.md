# 0002 锁定三端开发工具链

**状态：**已完成
**目标：**确定可复现的 Go/Leaf、Godot 和管理前端版本及本地检查命令，不提前构建空模块。

## 范围

- 锁定 Go 1.26.4、`github.com/taills/leaf/v2 v2.0.3`、Godot/editor/export templates 4.7.1-stable 与 React/TypeScript/Vite/Tailwind v4 依赖。
- 写 `docs/planning/tech_doc.md`，注明 PostgreSQL 为开发、CI、生产唯一业务数据库，记录 PostgreSQL 驱动版本和数据库基线；不得引入 SQLite/MySQL 适配。
- 列出 Go/管理前端/Godot 的导入、格式化、测试、构建和 E2E 命令；确认当前机器是否具备 Godot CLI、模板与 PostgreSQL，缺失则标为阻塞，不伪造通过。
- 生成必要的最小 manifest/锁文件和忽略规则，只纳入当前已批准工具链文件。

## 验收标准

- [x] 依赖文件和 CI 使用同一明确版本；无 `replace` 指向 xyq、本地个人目录或隐式最新版。
- [x] 安装/检查命令可重复执行，锁文件入版本控制；PostgreSQL 是唯一数据库实现。
- [x] 实测记录工具是否可用；未安装的工具如实记录，不把版本声明当构建通过。

## 验证

- 运行适用的版本探测、Go 模块校验、pnpm 锁文件检查与 Godot 版本/模板探测，并记录命令输出。
- 由后端和客户端审查者交叉检查锁定版本及导入路径。

## 依赖与文件

**依赖：**0001。
**预计文件：**`server/go.mod`、`client/project.godot`、`admin/package.json` 与对应 lockfile、`docs/planning/tech_doc.md`、CI 工具链配置。
