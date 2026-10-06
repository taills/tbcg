# 示例游戏

用 TBCG 框架搭建的完整示例。每个子目录是一款独立的 Game，自带内容、资源、可直接打开的 Godot 工程、部署配置与测试；框架本身（`server/`、`client/`、`admin/`、`contracts/`）不做任何修改。

| 目录 | 游戏 | 简介 |
| --- | --- | --- |
| [`harbor/`](harbor/README.md) | 雾港奇谭 | 海雾渔港冒险：2 个职业、3 张地图、3 条任务、门派、伙伴/宠物/坐骑、商店与制作、活动和两阶段首领副本。`docker compose up --build` 启动游戏服务与完整的游戏管理系统（管理后台、审核、构建 worker、Release 激活）；`client/` 用 Godot 打开即可游玩和编辑地图 |

示例按框架约定接入：运行时把示例目录放到 `<项目根>/games/<game_id>/`（Docker 镜像中直接复制，`run-dev.sh` 用符号链接拼出临时项目根）；Godot 工程通过符号链接复用框架的 `client/framework` 与地图创作插件，内容通过管理 API 走正式发布流程。
