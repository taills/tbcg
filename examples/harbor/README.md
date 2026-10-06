# 雾港奇谭（`harbor`）

基于 TBCG 框架的完整原创示例游戏：一座常年被海雾笼罩的渔港，新来的年轻人帮港务长清理码头、替守灯人重燃雾灯，最后深入沉潮窟击败雾海章鱼王。

游戏本身只由**内容数据**组成——65 条 Definition、3 个 Godot 地图场景、1 个原创图标——没有游戏规则代码。服务端规则、Godot 客户端、管理后台和构建 worker 全是未经修改的框架；本目录里的 Go 代码只有发布工具和黑盒测试，它们也只通过框架的命令行工具、HTTP API 与游戏协议工作。这正是示例要展示的：换一套内容，就是另一款游戏。

`docker compose up --build` 一条命令即可启动完整的游戏与游戏管理系统。

## 覆盖的框架能力

| 系统 | 本示例中的内容 |
| --- | --- |
| 地图 / 移动 / 传送 | 雾港码头、旧灯塔、退潮滩；去退潮滩的传送阵需先完成「初到雾港」 |
| 角色原型 | 渔叉手（高血高攻）、灯语者（高法力，自带治疗） |
| 门派 / 成长 | 观潮阁：2/3/4 级依次解锁浪涌、潮汐护佑（全体增益）、迷雾灯语（对敌减益） |
| NPC 对话 | 5 名 NPC，选项按任务状态、等级、门派条件由服务端过滤 |
| 任务 | 进入区域、对话、遭遇胜利、交付物品四种目标；一次性主线 ×2，可重复的队伍任务 ×1 |
| 回合战斗 | 5 种遭遇；怪物有多段攻击（双钳夹击、缠咬）、群攻（雾刺、触手横扫）与暴击技能 |
| 副本 | 沉潮窟：1–3 人、3 级起、每日 3 次，前厅 → 章鱼王两阶段 |
| 背包 / 装备 / 物品 | 武器、护甲、饰品三个槽位；回血、回蓝、经验、坐骑四种可使用物品 |
| 宠物 / 伙伴 / 坐骑 | 小海豹（任务后由 NPC 赠送）、老水手（付费招募）、老海龟（商店缰绳） |
| 商店 / 制作 | 阿婆的小摊（限购、全服库存）；熬鱼汤、打造潮纹鱼叉（门派限定配方） |
| 活动 | 渔灯节：活动窗口内击败残骸水母 3 次领奖，带排行榜 |
| 好友 / 公会 / 交易 | 规则单例：好友上限、建会费用与等级、交易税率与不可交易物品 |

## 世界

`S` 出生/落点，`N` NPC，`P` 传送点，`z` 触发区，`r` 区域，`#` 阻挡。坐标为 (x, y)。

### 雾港码头 `harbor:map/pier-town`（12×8）

```
    0 1 2 3 4 5 6 7 8 9 0 1
 0  S . . . . . . . # . S P      P(11,0) → 旧灯塔
 1  . # # . . . . . # . . .
 2  . # # . N . # . . z z .      N(4,2) 港务长
 3  . . . . . . # . . z z .      z 灯塔石阶
 4  . . . # . . . . . # # .
 5  . . . # . . . N . # # .      N(7,5) 渔家阿婆
 6  # . . . . . . . . . S .
 7  # # . . . . . . # # . P      P(11,7) → 退潮滩（需完成「初到雾港」）
```

### 旧灯塔 `harbor:map/lighthouse`（8×6）

```
    0 1 2 3 4 5 6 7
 0  # # z z z z # #              z 灯室
 1  # . . . . . . #
 2  . . . # # . . .
 3  . . . # # . N .              N(6,3) 守灯人
 4  # . . S . . . #
 5  # # P . . . # #              P(2,5) → 雾港码头
```

### 退潮滩 `harbor:map/tide-flats`（12×8）

```
    0 1 2 3 4 5 6 7 8 9 0 1
 0  S . . . . . z z # # # #      z 沉船残骸
 1  . # . . . . z z . # # #
 2  . # . . # # . . . . # #
 3  . . . N # # . . . . . #      N(3,3) 拾贝阿螺
 4  P . . . . . . # . . N .      P(0,4) → 雾港码头；N(10,4) 老潮工
 5  # # . . . . . # . . . .
 6  # # # . . . . . . # r r      r 潮窟入口
 7  # # # # . . . . . # r r
```

## 流程

1. **初到雾港**（港务长）：登上灯塔石阶 → 去旧灯塔拜访守灯人 → 回码头击败码头蟹群 → 交付 2 枚蟹壳（蟹群掉落）。奖励旧鱼叉、2 碗鱼汤、120 经验，并解锁退潮滩传送阵和伙伴老水手。
2. **拜入观潮阁**（守灯人，2 级）。
3. **雾中灯火**（守灯人）：到退潮滩沉船残骸 → 击败残骸水母 2 次 → 交付 2 颗雾珠 → 登上灯室点亮雾灯。奖励潮声护符；之后拾贝阿螺会赠送小海豹。顺手打满 3 次水母可领取**渔灯节**奖励。
4. **礁石巡逻**（老潮工，可重复，队伍进度）：击败礁间鳗影，收集鳗鱼皮。
5. 回码头找渔家阿婆**打造潮纹鱼叉**（鳗鱼皮 ×2、蟹壳 ×3、雾珠 ×1，3 级 + 观潮阁）。
6. **沉潮窟**（老潮工，3 级）：创建队伍（单人亦可）后进入，先破前厅，再战雾海章鱼王。

气血在战斗之间保留（升级时回满），战斗外可在背包中使用鱼汤，鱼汤可在阿婆处购买或用蟹壳熬制。

## 数值

编写时用框架的战斗内核（与运行时相同，200 个随机种子、默认指令、满血、不带伙伴/宠物/道具）测得：

| 场景 | 胜率 | 胜利时平均损血 |
| --- | --- | --- |
| 1 级渔叉手 / 灯语者 vs 码头蟹群 | 100% | 16% / 37% |
| 2 级（旧鱼叉）vs 残骸水母 | 100% | 26% / 41% |
| 3 级（门派 + 中期装备）vs 礁间鳗影 | 100% | 27% / 36% |
| 4 级单人（潮纹鱼叉）vs 潮窟前厅 | 100% | 39% |
| 4 级单人 vs 章鱼王巢 | 84% | 84% |
| 4 级双人 vs 章鱼王巢 | 100% | 33% |

章鱼王对裸装单人是九死一生的首领战；带上老水手、小海豹和几碗鱼汤，或者叫上朋友，就稳妥得多。调整数值后，可在管理后台的「战斗预览」里用同一内核复测（结果会回显 `rules_version`）。按主线把支线也做完的玩家进副本时通常已到 6 级，首领会显得偏弱；想要更难，可提高副本最低等级或章鱼王属性。

## 目录

```
examples/harbor/
├── content/<type>/<slug>.json   # 65 条 Definition，内容 ID 形如 harbor:npc/harbormaster
├── assets/                      # 资源白名单 manifest.json（SHA-256 + 来源/许可）与原创图标
├── client/                      # Godot 工程：用 Godot 直接打开（见「Godot 工程」）
│   ├── project.godot、main.tscn  # 示例自己的工程配置与主场景
│   ├── games/harbor/maps/*.tscn  # 三张地图的创作场景，与 content/map 的几何一一对应
│   ├── tools/check_maps.gd       # 无界面校验：场景几何与内容一致、锚点绑定无断开
│   ├── framework → ../../../client/framework               # 符号链接：框架客户端
│   └── addons/tbcg_authoring → ../../../../client/addons/…  # 符号链接：框架地图创作插件
├── contracts → ../../contracts  # 符号链接：框架客户端按 res://../contracts 读取协议目录
├── docker-compose.yml           # 完整栈：PostgreSQL + 游戏服务 + 管理后台 + 构建 worker + 发布
├── docker/                      # 镜像（从未修改的框架源码构建）、证书与初始化脚本
├── cmd/harbor-seed/             # 发布工具：通过管理 API 导入、提交、审核、构建并激活示例内容
├── playtest/                    # 黑盒测试：内容校验 + 连接运行中的服务完整通关
├── go.mod                       # 示例自己的 Go 模块（不导入任何框架包）
├── make-public-bundle.sh        # 生成公开包，供 Godot 编辑器的「TBCG 内容」Dock 分析锚点绑定
└── run-dev.sh                   # 不用 Docker 时的开发模式启动脚本
```

## 一键启动（Docker）

前置：Docker（Compose v2），以及用于游玩的 Godot 4.7.1。在本目录执行：

```sh
docker compose up --build
```

首次启动依次完成：

1. **certs** 生成本地 CA，签发 PostgreSQL 与管理后台的服务器证书（CA 私钥签完即删）。
2. **postgres** 启用 TLS；**setup** 执行框架全部迁移，登记 `harbor` 与其构建配置，创建管理后台与构建 worker 各自的受限数据库登录，并创建管理员账号。
3. **game-server**、**admin-server**、**build-worker** 启动；游戏服务此时还没有可用 Release。
4. **seed** 用管理员账号走完整发布流程：批量导入 65 条草稿 → 提交为不可变 revision → 逐条审核批准 → 由隔离的 worker 构建完整快照 → 请求激活；游戏服务校验产物后切换到该 Release。管理 API 限流为每分钟 120 次，首次发布约需两分钟；之后再启动会直接跳过。

看到 `realm main now serves release snapshot-…` 后即可游玩：

| 入口 | 地址 | 说明 |
| --- | --- | --- |
| 游戏 | `http://127.0.0.1:7080`、`ws://127.0.0.1:7443` | 用 Godot 打开 `client/` 工程后运行（F5），或直接 `/Applications/Godot.app/Contents/MacOS/Godot --path examples/harbor/client`；默认地址即指向这里，注册登录即可（用户名 3–24 位字母、数字或下划线，密码至少 8 字节） |
| 管理后台 | `https://127.0.0.1:8443` | 账号 `admin` / `harbor-admin-local`，拥有 harbor 的 Reader、Author、Reviewer、Publisher 全部角色 |

管理后台只接受 HTTPS，证书由本栈的本地 CA 签发。导出 CA 并加入浏览器或系统信任后再访问（只信任这一份本地 CA，用完可删除）：

```sh
docker compose cp certs:/certs/ca.crt ./harbor-local-ca.crt
```

必须用 `https://127.0.0.1:8443` 访问——管理后台按精确 Origin 校验跨站请求，`localhost` 会被拒绝。

口令都是仅限本机的默认值，可用环境变量覆盖：`HARBOR_ADMIN_USERNAME`、`HARBOR_ADMIN_PASSWORD`、`HARBOR_DB_PASSWORD`、`HARBOR_ADMIN_DB_PASSWORD`、`HARBOR_WORKER_DB_PASSWORD`（数据库口令需在首次启动前设定）；游戏端口可用 `HARBOR_HTTP_PORT`、`HARBOR_WS_PORT` 调整。`docker compose down -v` 会清空数据库、证书与构建产物，下次启动重新发布。

### 在管理后台里改游戏

示例目录里的 JSON 只是**初始内容**；发布之后，权威内容在管理后台：

1. 「打开全类型内容库与发布工具」→ 在内容库中打开条目，编辑草稿（NPC 对话树、任务阶段、副本阶段有可视化编辑器），校验后提交为新 revision。
2. 「待审核 revision」中批准确切的 revision。
3. 「Release 与激活」中构建完整 Game 快照，再「激活到 main」；游戏服务在约 2 秒内校验并切换，在线玩家会收到 Release 变更通知并重新下载公开包。

同一页面还有全量交叉引用校验、revision 差异对比与战斗预览。

### 栈的结构

框架有两条安全约束：无 TLS 的 game-server 只能绑定 loopback；管理后台与构建 worker 只接受 verify-full TLS 的数据库连接。因此所有服务共享 postgres 容器的网络命名空间、全部经 `127.0.0.1` 互连，数据库使用本地 CA 证书；`gateway` 仅把游戏端口转发出来，所有端口只发布到宿主机的 `127.0.0.1`。管理后台与 worker 使用权限受限的独立登录（分别属于 `tbcg_admin_control_plane`、`tbcg_content_build_worker`），框架启动时会拒绝权限过大的登录。

这是本地演示配置，不是生产部署：游戏服务直接用数据库超级用户连接（框架的 `tbcg_game_runtime` 受限授权路径尚未经框架测试覆盖），也没有为游戏端口配置 TLS。

## 不用 Docker 运行（开发模式）

前置：Go 1.26+、PostgreSQL 18、`tern` v2.4.3（`go install github.com/jackc/tern/v2@v2.4.3`）。迁移要求预先创建三个 NOLOGIN 角色：

```sh
psql -d postgres -c "CREATE ROLE tbcg_admin_control_plane NOLOGIN" \
                 -c "CREATE ROLE tbcg_content_build_owner NOLOGIN" \
                 -c "CREATE ROLE tbcg_content_build_worker NOLOGIN"
createdb tbcg_harbor
PGDATABASE=tbcg_harbor TERN_MIGRATIONS=server/migrations tern migrate
TBCG_GAME_DATABASE_URL='postgresql://<user>@127.0.0.1:5432/tbcg_harbor?sslmode=disable' examples/harbor/run-dev.sh
```

框架按约定从 `<项目根>/games/<id>/content` 读取内容，脚本用符号链接拼出一个临时项目根，让未修改的 game-server 直接加载本目录内容，Release 标记为 `dev-local`（未经审批构建，仅供调试）。这种方式没有管理后台。

## 测试

```sh
cd examples/harbor
go test ./...                                   # 内容校验（调用框架 content-tool）、图标哈希、地图场景一致性（有 Godot 时用真实导出器）
HARBOR_HTTP_URL=http://127.0.0.1:7080 HARBOR_WS_URL=ws://127.0.0.1:7443 \
  go test -run TestPlaythrough -v ./playtest    # 连接运行中的服务，用新账号从建角一路打到章鱼王
```

通关测试只通过游戏协议操作（与 Godot 客户端相同），每次使用随机的新账号，可以对 Docker 栈或 `run-dev.sh` 反复运行；战斗随机，败北会喝汤重试，副本失败会换新实例重来。

## Godot 工程

`client/` 是一个完整的 Godot 4.7.1 工程，可以直接打开：

- **编辑器**：启动 Godot → 项目管理器「导入」→ 选择 `examples/harbor/client/project.godot`；或命令行 `/Applications/Godot.app/Contents/MacOS/Godot --editor --path examples/harbor/client`。
- **运行游戏**：编辑器中按 F5（主场景 `main.tscn`），或 `/Applications/Godot.app/Contents/MacOS/Godot --path examples/harbor/client`。需要先启动服务端（Docker 栈或 `run-dev.sh`）。

工程里没有复制框架代码：`framework/` 与 `addons/tbcg_authoring/` 是指向仓库 `client/` 的符号链接，客户端外壳、网络、状态、表现层与地图创作插件都直接复用框架；框架的脚本都自带 `.uid`，打开工程不会往框架目录写任何文件。示例自己的部分只有工程配置、主场景、三张地图场景和校验脚本。客户端运行时从服务器下载并校验公开包，所以不需要任何 harbor 专属的游戏代码。

> Windows 上 Git 默认把符号链接检出为普通文本文件。请在开启开发者模式后用 `git clone -c core.symlinks=true` 检出，否则 Godot 找不到 `framework/`。

### 编辑地图

1. 打开 `res://games/harbor/maps/` 下的场景。地图根节点（`MapAuthoring2D`）的 `grid_rows` 是可走/阻挡网格，子节点 `MapAnchor2D` 是出生点、NPC、传送点与区域锚点，拖动即可改位置。
2. 生成公开包后，在右侧「TBCG 内容」Dock 填入它的路径（默认值指向框架 demo 的 fixture），点击「分析当前地图」，即可看到哪些 NPC、传送点、区域绑定到了当前地图的锚点，以及断开的引用：

   ```sh
   examples/harbor/make-public-bundle.sh   # 输出 examples/harbor/client/harbor-public-bundle.json
   ```

3. 在场景树中只选中地图根节点，用「项目 → 工具 → 校验/导出当前地图」导出几何 JSON，替换对应地图的 `public.geometry`：在管理后台编辑该地图并走审核发布，或改 `content/map/*.json`。
4. 校验场景与内容一致（`go test ./...` 也会在找到 Godot 时自动运行它）：

   ```sh
   /Applications/Godot.app/Contents/MacOS/Godot --headless --path examples/harbor/client \
     --script res://tools/check_maps.gd -- --bundle "$PWD/examples/harbor/client/harbor-public-bundle.json"
   ```

现有三个场景经 Godot 4.7.1 导出后与内容中的几何完全一致，所有锚点都有内容绑定。

## 已知限制（框架侧，未改动）

- 管理前端的图标预览静态路由固定为 `/assets/demo/`，物品编辑器的默认占位值也是 demo 的，harbor 的图标在管理界面中不显示预览（构建与校验使用的 `games/harbor/assets` 不受影响）。
- Godot 客户端「角色」页的背包行布局很窄，物品名会竖排；demo 同样如此。
