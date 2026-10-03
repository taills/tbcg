# 0014 xyq 可选静态内容导入器

**状态：**未开始
**目标：**提供独立、显式启用的静态导入工具，把经批准的 xyq 数据转换为 TBCG 草稿，并报告语义差异与来源授权。

## 范围

- 使用白名单选择来源文件及依赖闭包，生成 ID 映射、来源 manifest、license 状态和转换报告。
- 报告 `supported/approximate/unsupported` 并按现行 Go ruleset 做执行能力校验；不支持的内容不可发布。
- 只生成新 draft/revision，支持 dry-run、确定性重复运行与冲突报告；绝不清空输出或覆盖策划数据。
- 不执行旧 Lua/GDScript，不复制旧协议/存档，不自动公开任何未授权游戏素材。

## 验收标准

- [ ] 同输入输出哈希确定，重复导入无副作用；目标手工 revision 不被覆盖。
- [ ] 缺授权来源被隔离；unsupported 阻止发布，approximate 需要人工确认。
- [ ] 抽取 JSON 不会被当成已完整迁移的玩法行为，无法映射字段及丢失语义有精确报告。

## 验证

- 对虚构/原创 fixtures 测 dry-run、ID 冲突、缺失依赖、不同授权状态、unsupported 行为。
- 在取得素材使用授权之前，不以真实 xyq 美术/音频制作可分发包。

## 依赖与文件

**依赖：**0013。
**预计文件：**`tools/xyq-import/`、`server/internal/content/import/`、导入契约、测试 fixtures 与来源登记。
