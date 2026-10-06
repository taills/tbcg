extends Control
## 游戏客户端外壳：登录 → 下载并校验公开包 → WSS 握手 → 角色选择 → 地图/HUD/战斗。
## 客户端只提交意图；位置、任务、背包、战斗与奖励全部以服务端响应/推送为准。

const GameConnection = preload("res://framework/network/game_connection.gd")
const GameHttp = preload("res://framework/network/game_http.gd")
const PublicBundle = preload("res://framework/content/public_bundle.gd")
const GameState = preload("res://framework/state/game_state.gd")
const GridNavigator = preload("res://framework/state/grid_navigator.gd")
const MapView = preload("res://framework/presentation/map_view.gd")
const BattleView = preload("res://framework/presentation/battle/battle_view.gd")
const DialogPanel = preload("res://framework/presentation/hud/dialog_panel.gd")
const UI = preload("res://framework/presentation/hud/ui.gd")
const PANELS := ["character_panel", "quest_panel", "party_panel", "companion_panel", "social_panel", "trade_panel", "event_panel"]
const CLIENT_BUILD := "0.1.0"
const CATALOG_PATH := "res://../contracts/protocol/message_catalog.json"
const STEP_SECONDS := 0.16
const MAX_CHUNK := 8

var connection: Node
var http: Node
var bundle = PublicBundle.new()
var state = GameState.new()
var realm := {}
var token := ""
var _screen: Control
var _log: RichTextLabel
var _map: Node2D
var _battle: PanelContainer
var _dialog: PanelContainer
var _panels := {}
var _moving := false
var _names := {}
var _http_url: LineEdit
var _ws_url: LineEdit
var _user: LineEdit
var _password: LineEdit

func _ready() -> void:
	connection = GameConnection.new()
	http = GameHttp.new()
	add_child(connection)
	add_child(http)
	var catalog: Variant = JSON.parse_string(FileAccess.get_file_as_string(CATALOG_PATH))
	if not catalog is Dictionary or not connection.load_catalog(catalog):
		push_error("protocol catalog unavailable")
	connection.pushed.connect(_on_push)
	connection.disconnected.connect(func(reason): log_line("连接断开：%s" % reason))
	set_anchors_preset(Control.PRESET_FULL_RECT)
	_show_login()

# ---- 公共辅助（供面板调用） ----

func new_key() -> String:
	var bytes := Crypto.new().generate_random_bytes(12)
	return "gd-" + bytes.hex_encode()

func call_server(message_type: String, payload: Dictionary) -> Dictionary:
	var response: Dictionary = await connection.request(message_type, payload)
	if response.get("success", false) and response.get("state_version") != null:
		state.state_version = str(response["state_version"])
	return response

func send_and_log(message_type: String, payload: Dictionary) -> void:
	var response: Dictionary = await call_server(message_type, payload)
	if response.get("success", false):
		log_line("%s 成功" % message_type)
		_absorb(response.get("data", {}))
	else:
		log_line("%s 失败：%s" % [message_type, error_text(response)])
		if response.get("error", {}).get("code", "") == "state_conflict":
			await resync()

func error_text(response: Dictionary) -> String:
	var error: Dictionary = response.get("error", {}) if response.get("error") is Dictionary else {}
	return "%s（%s）" % [str(error.get("message", "未知错误")), str(error.get("code", ""))]

func log_line(text: String) -> void:
	if _log != null:
		_log.append_text(text + "\n")
	print(text)

func name_of(character_id: String) -> String:
	if character_id == state.character_id():
		return str(state.character.get("state", {}).get("name", character_id))
	if state.entities.has(character_id):
		return str(state.entities[character_id].get("name", character_id))
	return str(_names.get(character_id, character_id.left(8)))

func resync() -> void:
	var response: Dictionary = await call_server("character.state", {})
	if response.get("success", false):
		state.apply_snapshot(response["data"])
		_refresh_map()

# ---- 登录与内容 ----

func _set_screen(control: Control) -> void:
	if _screen != null:
		_screen.queue_free()
	_screen = control
	add_child(control)

func _show_login() -> void:
	var box := VBoxContainer.new()
	box.position = Vector2(40, 40)
	_http_url = UI.line_edit("http://127.0.0.1:7080")
	_http_url.text = "http://127.0.0.1:7080"
	_ws_url = UI.line_edit("ws://127.0.0.1:7443")
	_ws_url.text = "ws://127.0.0.1:7443"
	_user = UI.line_edit("用户名")
	_password = UI.line_edit("密码")
	_password.secret = true
	for control in [UI.label("TBCG 原创示例客户端"), UI.label("HTTP 地址"), _http_url, UI.label("WSS 地址"), _ws_url, _user, _password]:
		box.add_child(control)
	box.add_child(UI.row([UI.button("注册", _register), UI.button("登录", _login)]))
	_log = RichTextLabel.new()
	_log.custom_minimum_size = Vector2(600, 160)
	box.add_child(_log)
	_set_screen(box)

func _register() -> void:
	var response: Dictionary = await http.request_json(HTTPClient.METHOD_POST, _http_url.text + "/api/v1/accounts", {"username": _user.text, "password": _password.text})
	log_line("注册成功，请登录" if response.get("success", false) else "注册失败：" + error_text(response))

func _login() -> void:
	var response: Dictionary = await http.request_json(HTTPClient.METHOD_POST, _http_url.text + "/api/v1/sessions", {"username": _user.text, "password": _password.text})
	if not response.get("success", false):
		log_line("登录失败：" + error_text(response))
		return
	token = str(response["data"]["session_token"])
	if await _load_release() and await _handshake():
		await _show_characters()

func _load_release() -> bool:
	var info: Dictionary = await http.request_json(HTTPClient.METHOD_GET, _http_url.text + "/api/v1/realm")
	if not info.get("success", false) or str(info["data"].get("release_id", "")) == "":
		log_line("Realm 尚未激活 Release")
		return false
	realm = info["data"]
	var download: Dictionary = await http.get_bytes(_http_url.text + "/api/v1/releases/%s/public" % realm["release_id"])
	if not download.get("ok", false):
		log_line("公开包下载失败")
		return false
	if not bundle.load_bytes(download["body"], str(realm.get("public_artifact_sha256", ""))):
		log_line("公开包校验失败：" + bundle.last_error)
		return false
	log_line("已加载公开包 %s（%d 张地图）" % [bundle.release_id, bundle.of_type("map").size()])
	return true

func _handshake() -> bool:
	if not connection.is_open() and not await connection.open(_ws_url.text):
		log_line("WSS 连接失败：" + connection.last_error)
		return false
	var response: Dictionary = await connection.request("system.hello", {"game_id": realm["game_id"], "realm_id": realm["realm_id"],
		"client_build": CLIENT_BUILD, "release_id": realm["release_id"], "session_token": token, "capabilities": ["protocol.v1"]})
	if not response.get("success", false):
		log_line("握手失败：" + error_text(response))
		return false
	return true

# ---- 角色选择 ----

func _show_characters() -> void:
	var response: Dictionary = await call_server("character.list", {})
	if not response.get("success", false):
		log_line(error_text(response))
		return
	var box := VBoxContainer.new()
	box.position = Vector2(40, 40)
	box.add_child(UI.label("选择角色"))
	for entry in response["data"].get("characters", []):
		var id := str(entry["character_id"])
		box.add_child(UI.row([UI.label("%s Lv.%d %s" % [entry["name"], int(entry["level"]), bundle.display_name(str(entry["map_id"]))]), UI.button("进入", func(): _enter(id))]))
	var name_edit := UI.line_edit("角色名")
	var archetype := OptionButton.new()
	for id in bundle.of_type("archetype"):
		archetype.add_item(bundle.display_name(id))
		archetype.set_item_metadata(archetype.item_count - 1, id)
	box.add_child(UI.row([name_edit, archetype, UI.button("创建", func(): _create(name_edit.text, str(archetype.get_item_metadata(archetype.selected))))]))
	_log = RichTextLabel.new()
	_log.custom_minimum_size = Vector2(600, 120)
	box.add_child(_log)
	_set_screen(box)

func _create(character_name: String, archetype_id: String) -> void:
	var response: Dictionary = await call_server("character.create", {"name": character_name, "archetype_id": archetype_id, "idempotency_key": new_key()})
	if response.get("success", false):
		await _show_characters()
	else:
		log_line("创建失败：" + error_text(response))

func _enter(character_id: String) -> void:
	var response: Dictionary = await call_server("character.enter", {"character_id": character_id})
	if not response.get("success", false):
		log_line("进入失败：" + error_text(response))
		return
	_build_world()
	state.apply_snapshot(response["data"])
	_refresh_map()

# ---- 世界 ----

func _build_world() -> void:
	var root := HBoxContainer.new()
	root.set_anchors_preset(Control.PRESET_FULL_RECT)
	var left := VBoxContainer.new()
	left.custom_minimum_size = Vector2(640, 680)
	root.add_child(left)
	var viewport_container := SubViewportContainer.new()
	viewport_container.custom_minimum_size = Vector2(620, 420)
	viewport_container.stretch = true
	var viewport := SubViewport.new()
	viewport.handle_input_locally = true
	viewport_container.add_child(viewport)
	_map = MapView.new()
	_map.position = Vector2(10, 10)
	viewport.add_child(_map)
	_map.cell_clicked.connect(_on_cell_clicked)
	left.add_child(viewport_container)
	_log = RichTextLabel.new()
	_log.custom_minimum_size = Vector2(620, 200)
	_log.scroll_following = true
	left.add_child(_log)
	var tabs := TabContainer.new()
	tabs.custom_minimum_size = Vector2(600, 680)
	root.add_child(tabs)
	for panel_name in PANELS:
		var panel: VBoxContainer = load("res://framework/presentation/hud/%s.gd" % panel_name).new()
		var scroll := ScrollContainer.new()
		scroll.name = panel_name
		scroll.add_child(panel)
		panel.size_flags_horizontal = Control.SIZE_EXPAND_FILL
		tabs.add_child(scroll)
		panel.setup(self)
		scroll.name = panel.name
		_panels[panel_name] = panel
	_set_screen(root)
	_dialog = DialogPanel.new()
	_dialog.position = Vector2(80, 120)
	add_child(_dialog)
	_dialog.setup(self)
	_battle = BattleView.new()
	_battle.position = Vector2(60, 60)
	add_child(_battle)
	_battle.setup(self)
	state.changed.connect(func(section): if section in ["character", "entities"]: _refresh_map())

func _refresh_map() -> void:
	if _map == null or state.character.is_empty():
		return
	var map_id := str(state.character["state"]["map_id"])
	if _map.geometry.get("map_id", "") != map_id:
		_map.set_map(bundle.geometry(map_id), bundle.npcs_on_map(map_id))
		log_line("进入地图：%s" % bundle.display_name(map_id))
	var s: Dictionary = state.character["state"]
	_map.set_players(state.entities, state.character_id(), {"name": s["name"], "x": s["x"], "y": s["y"], "mount_id": s.get("mount_id", "")})

func _on_cell_clicked(cell: Vector2i) -> void:
	if _moving or _battle.visible:
		return
	var map_id := str(state.character["state"]["map_id"])
	var geometry: Dictionary = bundle.geometry(map_id)
	for npc in bundle.npcs_on_map(map_id):
		var anchor := GridNavigator.find_anchor(geometry, str(npc["anchor_id"]))
		if not anchor.is_empty() and GridNavigator.anchor_distance(anchor, cell) == 0:
			await _walk(GridNavigator.approach(geometry["grid"], state.position(), anchor, 1))
			await _interact(str(npc["content_id"]))
			return
	for portal in bundle.get_public(map_id).get("portals", []):
		var anchor := GridNavigator.find_anchor(geometry, str(portal["anchor_id"]))
		if not anchor.is_empty() and GridNavigator.anchor_distance(anchor, cell) == 0:
			await _walk(GridNavigator.find_path(geometry["grid"], state.position(), cell))
			var response: Dictionary = await call_server("world.teleport", {"portal_anchor_id": portal["anchor_id"]})
			if response.get("success", false):
				state.set_entities(response["data"]["entities"])
				await resync()
			else:
				log_line("传送失败：" + error_text(response))
			return
	await _walk(GridNavigator.find_path(geometry["grid"], state.position(), cell))

## 把客户端路径拆成小段依次提交，服务端校验每一步；失败时以服务端状态重新同步。
func _walk(path: Array[Vector2i]) -> void:
	if path.is_empty():
		return
	_moving = true
	var index := 0
	while index < path.size():
		var chunk := []
		for i in range(index, mini(index + MAX_CHUNK, path.size())):
			chunk.append({"x": path[i].x, "y": path[i].y})
		var response: Dictionary = await call_server("world.move", {"path": chunk, "state_version": state.state_version})
		if not response.get("success", false):
			log_line("移动被拒绝：" + error_text(response))
			await resync()
			break
		state.character["state"]["x"] = int(response["data"]["x"])
		state.character["state"]["y"] = int(response["data"]["y"])
		state.state_version = str(response["data"]["state_version"])
		_refresh_map()
		index += chunk.size()
		await get_tree().create_timer(STEP_SECONDS * chunk.size()).timeout
	_moving = false

func _interact(npc_id: String) -> void:
	var response: Dictionary = await call_server("npc.interact", {"npc_id": npc_id})
	if response.get("success", false):
		_dialog.show_dialog(response["data"]["dialog"])
	else:
		log_line("对话失败：" + error_text(response))

func _absorb(data: Dictionary) -> void:
	if data.has("entities"):
		state.set_entities(data["entities"])

func _on_push(message_type: String, data: Dictionary) -> void:
	state.apply_push(message_type, data)
	match message_type:
		"battle.started":
			_battle.start(data)
		"battle.round":
			_battle.on_round(data)
		"battle.ended":
			_battle.on_end(data)
			log_line("战斗结束：%s" % str(data.get("outcome", "")))
		"party.invited":
			if _panels.has("party_panel"):
				_panels["party_panel"].add_invite(data)
			log_line("%s 邀请你入队" % str(data.get("leader_name", "")))
		"trade.invited", "trade.updated":
			if _panels.has("trade_panel"):
				_panels["trade_panel"].on_push(message_type, data)
		"system.release_changed":
			log_line("服务器已切换到新 Release，正在重新下载公开包")
			if await _load_release():
				_refresh_map()
		"system.notice":
			log_line("通知：%s" % str(data.get("message", "")))
		"party.updated":
			for member in data.get("members", []):
				_names[str(member["character_id"])] = member["name"]
