# xyq 可选静态内容导入器（任务 0014）

导入器是**可选**工具，运行时与内容管线都不依赖它。它只把经过白名单与授权登记的静态数据转换为 TBCG 草稿定义，
并给出逐字段的 `supported / approximate / unsupported` 报告。

## 安全与授权边界

- 不读取 `/Users/admin/Games/xyq`，除非操作者显式把某个**已获授权**的导出目录作为 `--source` 传入；仓库内测试只使用
  `tests/fixtures/xyq-import/` 下的原创虚构数据。
- `manifest.json` 必须逐文件登记 `license`；非 `authorized` 的来源整体隔离，不产生任何草稿。
- 不执行旧 Lua/GDScript，不复制旧协议或存档；含 `script/formula/lua` 的字段一律 `unsupported`，该记录被阻止。
- 不导入美术/音频：源图标字段被替换为 manifest 指定的已授权占位图标，并标记 `approximate`。
- `approximate` 记录必须经 `--accept-approximate` 逐个人工确认后才进入草稿批次。

## 用法

```sh
cd server
# 只看报告（默认 dry-run）
go run ./cmd/xyq-import --source ../tests/fixtures/xyq-import --contracts ../contracts
# 生成草稿批次（输出文件已存在且内容不同则拒绝覆盖）
go run ./cmd/xyq-import --source ../tests/fixtures/xyq-import --contracts ../contracts \
  --dry-run=false --accept-approximate demo:item/xyq-1001 --out /tmp/xyq-drafts.json
```

生成的批次（`{"dry_run": false, "definitions": [...]}`）通过管理平台「创作工具 → 批量导入」或
`POST /api/v1/games/{gameId}/content-imports` 提交：服务端只创建**新**草稿，已有草稿报告 `conflict`，从不覆盖；
导入的草稿仍需正常的校验、提交、审批与构建流程才能发布。

## 报告字段

| 字段 | 含义 |
| --- | --- |
| `records[].fields[]` | 每个源字段的映射状态与说明（含丢失语义） |
| `quarantined_sources` | 授权缺失或类型不支持而被隔离的来源 |
| `id_conflicts` | 映射后内容 ID 冲突 |
| `missing_dependencies` | 引用了白名单外内容的依赖缺口 |
| `ready / needs_confirmation / blocked` | 可导入 / 需人工确认 / 被阻止 |
| `output_sha256` | 草稿批次的确定性哈希（同输入同哈希） |
