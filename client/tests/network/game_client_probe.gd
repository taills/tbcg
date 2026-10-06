extends SceneTree
## 真实客户端链路探针：HTTP 登录 → 下载并校验公开包 → WSS 握手 → 建角/进图 → 寻路移动 → NPC 接取任务。
## 由 Go 集成测试启动真实 game-server 后以 headless 方式运行。

const GameConnection = preload("res://framework/network/game_connection.gd")
const GameHttp = preload("res://framework/network/game_http.gd")
const PublicBundle = preload("res://framework/content/public_bundle.gd")
const GameState = preload("res://framework/state/game_state.gd")
const GridNavigator = preload("res://framework/state/grid_navigator.gd")

var _failures := 0

func _initialize() -> void:
	call_deferred("_run")

func _check(condition: bool, message: String) -> bool:
	if condition:
		print("PASS: " + message)
	else:
		_failures += 1
		push_error("FAIL: " + message)
	return condition

func _run() -> void:
	var http_url := OS.get_environment("TBCG_PROBE_HTTP")
	var ws_url := OS.get_environment("TBCG_PROBE_WS")
	var http = GameHttp.new()
	var connection = GameConnection.new()
	root.add_child(http)
	root.add_child(connection)
	connection.load_catalog(JSON.parse_string(FileAccess.get_file_as_string("res://../contracts/protocol/message_catalog.json")))
	var credentials := {"username": "godot_probe", "password": "probe password 123"}
	await http.request_json(HTTPClient.METHOD_POST, http_url + "/api/v1/accounts", credentials)
	var login: Dictionary = await http.request_json(HTTPClient.METHOD_POST, http_url + "/api/v1/sessions", credentials)
	if not _check(login.get("success", false), "login over HTTP"):
		return _finish()
	var realm: Dictionary = (await http.request_json(HTTPClient.METHOD_GET, http_url + "/api/v1/realm"))["data"]
	var download: Dictionary = await http.get_bytes(http_url + "/api/v1/releases/%s/public" % realm["release_id"])
	var bundle = PublicBundle.new()
	_check(download.get("ok", false) and bundle.load_bytes(download["body"], str(realm["public_artifact_sha256"])), "public bundle hash and shape verified")
	var text: String = (download.get("body", PackedByteArray()) as PackedByteArray).get_string_from_utf8()
	_check(text.find("drop_weight_basis_points") == -1 and text.find("\"loot\"") == -1 and text.find("\"nodes\"") == -1, "private server fields unreachable through public bundle")
	var private_probe: Dictionary = await http.get_bytes(http_url + "/api/v1/releases/%s/private" % realm["release_id"])
	_check(not private_probe.get("ok", false), "private bundle has no HTTP route")
	if not _check(await connection.open(ws_url), "websocket connected"):
		return _finish()
	var hello: Dictionary = await connection.request("system.hello", {"game_id": realm["game_id"], "realm_id": realm["realm_id"], "client_build": "0.1.0",
		"release_id": realm["release_id"], "session_token": login["data"]["session_token"], "capabilities": ["protocol.v1"]})
	_check(hello.get("success", false), "hello acknowledged")
	var created: Dictionary = await connection.request("character.create", {"name": "探针", "archetype_id": "demo:archetype/wanderer", "idempotency_key": "godot-probe-create-1"})
	_check(created.get("success", false), "character created")
	var entered: Dictionary = await connection.request("character.enter", {"character_id": created["data"]["character_id"]})
	if not _check(entered.get("success", false), "character entered"):
		return _finish()
	var state = GameState.new()
	state.apply_snapshot(entered["data"])
	var geometry: Dictionary = bundle.geometry(state.map_id)
	var guide := GridNavigator.find_anchor(geometry, "guide")
	var path := GridNavigator.approach(geometry["grid"], state.position(), guide, 1)
	var cells := []
	for cell in path:
		cells.append({"x": cell.x, "y": cell.y})
	var moved: Dictionary = await connection.request("world.move", {"path": cells, "state_version": state.state_version})
	_check(moved.get("success", false), "client-computed path accepted by server")
	var illegal: Dictionary = await connection.request("world.move", {"path": [{"x": 1, "y": 1}], "state_version": str(moved["data"]["state_version"])})
	_check(not illegal.get("success", true) and illegal["error"]["code"] in ["invalid_path", "too_fast"], "wall step rejected by server")
	var dialog: Dictionary = await connection.request("npc.interact", {"npc_id": "demo:npc/guide"})
	_check(dialog.get("success", false), "NPC dialog opened")
	var accepted: Dictionary = await connection.request("npc.choose", {"npc_id": "demo:npc/guide", "node_id": dialog["data"]["dialog"]["node_id"], "option_id": "accept", "idempotency_key": "godot-probe-accept-1"})
	_check(accepted.get("success", false), "quest accepted through dialog")
	var snapshot: Dictionary = await connection.request("character.state", {})
	state.apply_snapshot(snapshot["data"])
	_check(state.quests.size() == 1 and state.quests[0]["status"] == "active", "quest state mirrored from server")
	_finish()

func _finish() -> void:
	print("game_client_probe failures=%d" % _failures)
	quit(1 if _failures > 0 else 0)
