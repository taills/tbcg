extends SceneTree

const BattleReplay = preload("res://framework/state/battle_replay.gd")
const GOLDEN_PATH := "res://../tests/fixtures/battle/slime-pack-golden.json"

var _failures := 0

func _initialize() -> void:
	call_deferred("_run")

func _run() -> void:
	var golden: Dictionary = JSON.parse_string(FileAccess.get_file_as_string(GOLDEN_PATH))
	var replay = BattleReplay.new()
	var initial := []
	for unit in golden["units"]:
		var copy: Dictionary = unit.duplicate(true)
		copy["hp"] = int(copy["stats"]["max_hp"])
		initial.append(copy)
	replay.load_units(initial)
	_assert(replay.apply_events(golden["events"]), "golden events apply in order")
	_assert(not replay.desynced, "every hp_before matches the replayed state")
	_assert(replay.outcome == golden["outcome"], "outcome comes from the authoritative event")
	for unit in golden["units"]:
		_assert(int(replay.units[unit["id"]]["hp"]) == int(unit["hp"]), "final HP of %s equals server state" % unit["id"])
	var per_target := 0
	for event in golden["events"]:
		if event["kind"] == "damage":
			per_target += 1
			_assert(int(event["hp_before"]) - int(event["hp_after"]) == int(event["damage"]), "damage equals hp delta for seq %d" % int(event["event_seq"]))
	_assert(per_target > 0, "golden contains per-target damage events")

	var out_of_order = BattleReplay.new()
	out_of_order.load_units(initial)
	var shuffled: Array = golden["events"].duplicate(true)
	if shuffled.size() > 1:
		var first = shuffled[0]
		shuffled[0] = shuffled[1]
		shuffled[1] = first
	_assert(not out_of_order.apply_events(shuffled), "out-of-order events are rejected")
	_finish()

func _assert(condition: bool, message: String) -> void:
	if not condition:
		_failures += 1
		push_error("FAIL: " + message)

func _finish() -> void:
	print("battle_replay_test failures=%d" % _failures)
	quit(1 if _failures > 0 else 0)
