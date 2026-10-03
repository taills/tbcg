# 0006 Godot 地图语义导出 spike

**状态：**已完成
**目标：**由 Godot 创建一张原创最小场景，导出与游戏引擎无关且 Go 可消费的空间数据。

## 范围

- Godot 4.7.1 场景/编辑插件记录稳定 map_id、geometry revision、正交网格、边界、出生点与 placement_id 锚点。
- 固定 `grid[y][x]`、左上原点、x 向右、y 向下、格坐标和 0/1 通行语义；tile_size/origin 明确且可验证。
- 导出障碍、spawn/portal/trigger 几何与规则绑定接口；只读网页规则投影不成为第二套写入来源。
- 明确旋转、多层、非网格几何暂不支持并在导出时报错。

## 验收标准

- [x] 一个原创 Godot 场景可重复生成稳定、版本化 JSON；改变场景内容会按预期改变输出。
- [x] Go 与 Godot 对边界、可达性、对角穿墙、单位换算及锚点位置共享 golden fixtures 并通过测试。
- [x] 不可支持的几何会明确失败；视觉 mask 不会被当成碰撞/导航数据。

## 验证

- Godot headless 场景加载和 exporter 测试；Go 地图 Schema/路径单测。
- 同一场景连续导出哈希一致，故意造越界 ID、重复 placement_id 和坏 grid 时校验失败。

## 实测与审查记录

- Go 1.26.4：mapdata race 测试通过，语句覆盖率 94.4%；map-validate CLI 92.3%；全局 `go build ./...` 与 `go vet ./...` 通过。
- Godot 4.7.1：editor/plugin 初始化、地图场景 headless 与文件写入测试通过，退出码为0且无 SCRIPT ERROR；覆盖实际挂入 SceneTree 的地图、非法变换/非整数坐标、NaN、锚点上限/重复/越界/不可达，以及 visual_only 不能隐藏碰撞。
- 共享空间向量直接调用生产 `spatial_grid.gd` 与 Go Geometry，不以测试里的重复算法冒充验证。
- `python3 tools/verify_map_export.py`：两次真实场景导出字节相同、JSON语义与共享golden一致、Go CLI接受导出文件。合法场景内容/修订改变会改变输出，原场景保持不变。
- Python工具11项单测通过；结合真实跨端执行，地图工具覆盖率91%、WSS工具90%。Black/isort/Ruff通过。CI增加地图headless与跨端检查，尚未在远端运行。
- Go/代码/Python审查无剩余HIGH；文件写入错误虚报、测试重复算法两项MEDIUM已修复并定点复核关闭。没有宣称已完成交互式点击菜单验收、Godot导出包或完整游戏地图功能。

### 使用入口

打开 `client/project.godot`，打开 `client/games/demo/maps/training-ground.tscn`，选中地图根节点，通过“项目 → 工具 → 校验/导出当前地图”导出。网格/锚点字段约定见 `contracts/content/map_geometry.md`。独立跨端检查从仓库根运行 `python3 tools/verify_map_export.py --godot <Godot4.7.1路径>`。

## 依赖与文件

**依赖：**0001、0002、0005。
**预计文件：**`client/addons/tbcg_authoring/`、`client/games/demo/scenes/`、`contracts/content/map_geometry.schema.json`、`tests/fixtures/maps/`。
