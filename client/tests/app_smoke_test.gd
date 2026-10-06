extends SceneTree
## 加载全部客户端脚本并实例化应用外壳，捕获 GDScript 解析/类型错误。

const SCRIPTS := ["res://framework/app/game_app.gd", "res://framework/network/game_connection.gd", "res://framework/network/game_http.gd",
	"res://framework/content/public_bundle.gd", "res://framework/state/game_state.gd", "res://framework/state/grid_navigator.gd",
	"res://framework/state/battle_replay.gd", "res://framework/presentation/map_view.gd", "res://framework/presentation/battle/battle_view.gd",
	"res://framework/presentation/hud/ui.gd", "res://framework/presentation/hud/dialog_panel.gd", "res://framework/presentation/hud/character_panel.gd",
	"res://framework/presentation/hud/quest_panel.gd", "res://framework/presentation/hud/party_panel.gd", "res://framework/presentation/hud/companion_panel.gd",
	"res://framework/presentation/hud/social_panel.gd", "res://framework/presentation/hud/trade_panel.gd", "res://framework/presentation/hud/event_panel.gd"]

var _failures := 0

func _initialize() -> void:
	call_deferred("_run")

func _run() -> void:
	for path in SCRIPTS:
		var script: Script = load(path)
		if script == null or not script.can_instantiate():
			_failures += 1
			push_error("FAIL: cannot load " + path)
	var app = load("res://framework/app/game_app.gd").new()
	root.add_child(app)
	await process_frame
	var PublicBundle = load("res://framework/content/public_bundle.gd")
	app.bundle = PublicBundle.new()
	app.bundle.load_bytes(FileAccess.get_file_as_bytes("res://../tests/fixtures/content/demo-public-bundle.json"), "")
	app._build_world()
	app.state.apply_snapshot({"character_id": "c1", "state_version": "3", "state": {"name": "测试", "level": 2, "exp": 10, "hp": 100, "mp": 20, "sect_id": "",
		"map_id": "demo:map/training-ground", "x": 0, "y": 0, "equipped": {}, "mount_id": ""}, "stats": {"max_hp": 140}, "skills": ["demo:skill/strike"],
		"inventory": {"items": {"demo:item/healing-potion": 2}, "balance": 50}, "quests": [], "map_entities": {"players": []}})
	await process_frame
	if app._map.geometry.is_empty():
		_failures += 1
		push_error("FAIL: map geometry from public bundle was not rendered")
	print("app_smoke_test failures=%d" % _failures)
	quit(1 if _failures > 0 else 0)
