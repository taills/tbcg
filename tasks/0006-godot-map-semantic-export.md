# 0006 Godot 地图语义导出 spike

**状态：**未开始
**目标：**由 Godot 创建一张原创最小场景，导出与游戏引擎无关且 Go 可消费的空间数据。

## 范围

- Godot 4.7.1 场景/编辑插件记录稳定 map_id、geometry revision、正交网格、边界、出生点与 placement_id 锚点。
- 固定 `grid[y][x]`、左上原点、x 向右、y 向下、格坐标和 0/1 通行语义；tile_size/origin 明确且可验证。
- 导出障碍、spawn/portal/trigger 几何与规则绑定接口；只读网页规则投影不成为第二套写入来源。
- 明确旋转、多层、非网格几何暂不支持并在导出时报错。

## 验收标准

- [ ] 一个原创 Godot 场景可重复生成稳定、版本化 JSON；改变场景内容会按预期改变输出。
- [ ] Go 与 Godot 对边界、可达性、对角穿墙、单位换算及锚点位置共享 golden fixtures 并通过测试。
- [ ] 不可支持的几何会明确失败；视觉 mask 不会被当成碰撞/导航数据。

## 验证

- Godot headless 场景加载和 exporter 测试；Go 地图 Schema/路径单测。
- 同一场景连续导出哈希一致，故意造越界 ID、重复 placement_id 和坏 grid 时校验失败。

## 依赖与文件

**依赖：**0001、0002、0005。
**预计文件：**`client/addons/tbcg_authoring/`、`client/games/demo/scenes/`、`contracts/content/map_geometry.schema.json`、`tests/fixtures/maps/`。
