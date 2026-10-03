# 正交地图空间契约 v1

此契约用于任务 0006 的受信任 Godot 场景导出，不包含怪物/NPC/任务/传送目的地规则；这些绑定由管理端通过稳定 placement_id 维护。

- `grid[y][x]` 必须与 width/height 一致，0 可走、1 阻挡；最大 256×256。
- `origin` 是地图局部左上角在世界像素空间的整数偏移，x 右/y 下；`tile_size` 为正整数正方形格边长。服务端内部坐标为格坐标。
- 世界像素→格：`floor((pixel - origin) / tile_size)`；边界为左上包含、右下不包含。格中心→像素：`origin + (cell + 0.5) * tile_size`。
- 支持相邻四向及斜向步进；斜向时两侧正交格都必须可走，不能穿对角墙。远距离跃迁不是一步移动。
- 导出的 anchors 按 placement_id 字节序排序；ID 唯一，cell 与 extent 都在边界内。spawn/npc/portal 只能占一个可走格；trigger/region 可覆盖矩形区域（左上锚点 + extent，右下不含）。
- 至少一个 spawn；spawn/npc/portal 应能从首个 spawn 到达。触发区不要求所有覆盖格可走。
- Godot 创作根节点使用 grid_rows 字符串数组（每字符为0/1）、tile_size、map_id、geometry_revision；根节点 position 对应 origin，仅允许无旋转、无缩放、无skew的单层正交场景。
- 专用 Marker2D 子节点保存 placement_id、kind、extent；position 必须落在格中心。拖动/重排节点不改变ID，不允许旋转、缩放、嵌套位置歧义或未知空间类型；纯视觉子树须显式分组为 visual_only，不能混入碰撞/导航节点。
- `.tscn` 由 Godot 加载，禁止自行解析场景文本或加载外部陌生脚本。导出器只接受受信任创作节点；输出按键排序的确定性 JSON，不包含时间戳、node path 或本机绝对路径。
- 相同场景重复导出字节相同；Go 解码还需按 Schema 校验及语义校验重复ID、行列尺寸、可达性，不信任只通过Schema的JSON。
- `tests/fixtures/maps/training-ground.json` 是原创场景的预期语义（非素材导入）；`spatial-cases.json` 是 Go/Godot 共用的坐标、碰撞、斜向及寻路测试向量。
