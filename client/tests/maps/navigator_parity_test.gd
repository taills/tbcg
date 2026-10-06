extends SceneTree

const GridNavigator = preload("res://framework/state/grid_navigator.gd")
const SpatialGrid = preload("res://addons/tbcg_authoring/spatial_grid.gd")
const CASES_PATH := "res://../tests/fixtures/maps/spatial-cases.json"
const MAP_PATH := "res://../tests/fixtures/maps/training-ground.json"

var _failures := 0

func _initialize() -> void:
	call_deferred("_run")

func _cell(value: Dictionary) -> Vector2i:
	return Vector2i(int(value["x"]), int(value["y"]))

func _run() -> void:
	var cases: Dictionary = JSON.parse_string(FileAccess.get_file_as_string(CASES_PATH))
	var geometry: Dictionary = JSON.parse_string(FileAccess.get_file_as_string(MAP_PATH))
	var grid: Array = geometry["grid"]
	for item in cases["walkability"]:
		_assert(SpatialGrid.walkable(grid, _cell(item["cell"])) == item["walkable"], "walkable %s" % str(item["cell"]))
	for item in cases["steps"]:
		_assert(SpatialGrid.can_step(grid, _cell(item["from"]), _cell(item["to"])) == item["allowed"], "step %s->%s" % [str(item["from"]), str(item["to"])])
	for item in cases["routes"]:
		var path := GridNavigator.find_path(grid, _cell(item["from"]), _cell(item["to"]))
		_assert((not path.is_empty()) == item["reachable"], "route %s->%s" % [str(item["from"]), str(item["to"])])
		var current := _cell(item["from"])
		for step in path:
			_assert(SpatialGrid.can_step(grid, current, step), "every client path step is server-valid")
			current = step
	var guide := GridNavigator.find_anchor(geometry, "guide")
	var approach := GridNavigator.approach(grid, Vector2i(0, 0), guide, 1)
	_assert(not approach.is_empty() and GridNavigator.anchor_distance(guide, approach[-1]) <= 1, "approach ends within NPC range")
	_finish()

func _assert(condition: bool, message: String) -> void:
	if not condition:
		_failures += 1
		push_error("FAIL: " + message)

func _finish() -> void:
	print("navigator_parity_test failures=%d" % _failures)
	quit(1 if _failures > 0 else 0)
