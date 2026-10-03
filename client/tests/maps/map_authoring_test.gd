extends SceneTree

const Exporter = preload("res://addons/tbcg_authoring/map_geometry_exporter.gd")
const AnchorScript = preload("res://addons/tbcg_authoring/map_anchor_2d.gd")
const SpatialGrid = preload("res://addons/tbcg_authoring/spatial_grid.gd")
const SCENE := "res://games/demo/maps/training-ground.tscn"
const GOLDEN := "res://../tests/fixtures/maps/training-ground.json"
const SPATIAL := "res://../tests/fixtures/maps/spatial-cases.json"
var _failures := 0

func _initialize() -> void:
	call_deferred("_run")

func _run() -> void:
	var packed: PackedScene = load(SCENE)
	_assert(packed != null, "training scene loads as PackedScene")
	if packed == null:
		quit(1)
		return
	var root := packed.instantiate()
	var result: Dictionary = Exporter.export_scene(root)
	_assert(result.get("ok", false), "training scene export: %s" % [result.get("errors", [])])
	if result.get("ok", false):
		_assert(Exporter.canonical_json(result.data) == Exporter.canonical_json(_read_json(GOLDEN)), "training scene equals golden semantics: %s != %s" % [Exporter.canonical_json(result.data), Exporter.canonical_json(_read_json(GOLDEN))])
		var bytes: String = Exporter.canonical_json(result.data)
		_assert(bytes == Exporter.canonical_json(Exporter.export_scene(root).data), "repeat export is byte-identical")
		var anchors: Array = result.data.anchors.duplicate(true)
		root.move_child(root.get_node("arrival"), root.get_child_count() - 1)
		_assert(Exporter.export_scene(root).data.anchors == anchors, "node order does not affect ID-sorted anchors")
		_assert(result.data.anchors == anchors, "export does not mutate returned data")
		_test_spatial_vectors(root)
		_test_invalid_maps(root)
		_test_strict_numeric_validation(root)
		_test_anchor_limit(root)
		_test_scene_changes(root)
	root.free()
	_test_attached_scene(packed)
	quit(1 if _failures > 0 else 0)

func _test_scene_changes(root: Node2D) -> void:
	var original: Dictionary = Exporter.export_scene(root)
	var changed := root.duplicate() as Node2D
	changed.geometry_revision += 1
	changed.grid_rows[0] = "000001"
	var exported: Dictionary = Exporter.export_scene(changed)
	_assert(exported.get("ok", false), "合法场景修改仍能导出")
	if exported.get("ok", false):
		_assert(exported.data.geometry_revision == 2, "修订号保留作者修改")
		_assert(exported.data.grid[0][5] == 1, "场景障碍修改进入导出数据")
		_assert(Exporter.canonical_json(exported.data) != Exporter.canonical_json(original.data), "场景变化改变导出字节")
	_assert(root.geometry_revision == 1 and root.grid_rows[0] == "000000", "导出副本不修改原场景")
	changed.free()

func _test_spatial_vectors(root: Node2D) -> void:
	_assert(SpatialGrid.world_to_cell(Vector2(1e20, 0), Vector2.ZERO, 1) == Vector2i(-1, -1), "巨大有限坐标不得溢出为合法格")
	var vectors := _read_json(SPATIAL)
	var data: Dictionary = Exporter.export_scene(root).data
	var grid: Array = data.grid
	var width: int = data.width
	var height: int = data.height
	var tile_size: int = data.tile_size
	var origin := Vector2(data.origin.x, data.origin.y)
	var expected_inside := [true, true, true, false, false]
	for index in vectors.walkability.size():
		var item: Dictionary = vectors.walkability[index]
		var cell := Vector2i(item.cell.x, item.cell.y)
		_assert(SpatialGrid.inside(width, height, cell) == expected_inside[index], "shared inside vector %s" % [cell])
		_assert(SpatialGrid.walkable(grid, cell) == item.walkable, "shared walkability vector %s" % [cell])
	for item in vectors.steps:
		var source := Vector2i(item.from.x, item.from.y)
		var target := Vector2i(item.to.x, item.to.y)
		_assert(SpatialGrid.can_step(grid, source, target) == item.allowed, "shared step vector %s -> %s" % [source, target])
	for item in vectors.conversions:
		var pixel := Vector2(item.pixel.x, item.pixel.y)
		var cell := SpatialGrid.world_to_cell(pixel, origin, tile_size)
		_assert(cell == Vector2i(item.cell.x, item.cell.y) and SpatialGrid.inside(width, height, cell) == item.inside, "shared conversion vector")
	for item in vectors.routes:
		var source := Vector2i(item.from.x, item.from.y)
		var target := Vector2i(item.to.x, item.to.y)
		var reachable_cells: Dictionary = SpatialGrid.flood_fill(grid, source)
		_assert(SpatialGrid.is_reachable(reachable_cells, target) == item.reachable, "shared route vector %s -> %s" % [source, target])

func _test_invalid_maps(root: Node2D) -> void:
	var cases := [
		["root rotation", func(copy: Node2D): copy.rotation = 0.1],
		["root scale", func(copy: Node2D): copy.scale = Vector2(2, 1)],
		["root skew", func(copy: Node2D): copy.skew = 0.1],
		["negative non-grid origin", func(copy: Node2D): copy.position.x = -0.5],
		["invalid grid character", func(copy: Node2D): copy.grid_rows[0] = "00x000"],
		["unequal row width", func(copy: Node2D): copy.grid_rows[0] = "0"],
		["duplicate ID", func(copy: Node2D): copy.get_node("guide").placement_id = "arrival"],
		["out of bounds anchor", func(copy: Node2D): copy.get_node("portal").position = Vector2(208, 144)],
		["anchor rotation", func(copy: Node2D): copy.get_node("guide").rotation = 0.2],
		["nested anchor child", func(copy: Node2D): copy.get_node("guide").add_child(Node2D.new())],
		["blocked placement cell", func(copy: Node2D): copy.get_node("guide").position = Vector2(48, 48)],
		["unreachable portal", func(copy: Node2D): copy.grid_rows[3] = "111111"]
	]
	for item in cases:
		var copy := root.duplicate() as Node2D
		item[1].call(copy)
		var result: Dictionary = Exporter.export_scene(copy)
		_assert(not result.get("ok", true) and not result.get("errors", []).is_empty(), "%s is rejected" % item[0])
		copy.free()
	var visual_root := root.duplicate() as Node2D
	var visual := Node2D.new()
	visual.add_to_group("visual_only")
	visual_root.add_child(visual)
	_assert(Exporter.export_scene(visual_root).get("ok", false), "visual_only subtree is accepted")
	visual_root.free()
	var unsafe_root := root.duplicate() as Node2D
	var unsafe := Node2D.new()
	unsafe.add_to_group("visual_only")
	unsafe_root.add_child(unsafe)
	unsafe.add_child(CollisionShape2D.new())
	_assert(not Exporter.export_scene(unsafe_root).get("ok", true), "visual_only cannot hide collision geometry")
	unsafe_root.free()

func _test_attached_scene(packed: PackedScene) -> void:
	var mounted := packed.instantiate()
	get_root().add_child(mounted)
	var result: Dictionary = Exporter.export_scene(mounted)
	_assert(result.get("ok", false), "mounted edited scene remains exportable: %s" % [result.get("errors", [])])
	if result.get("ok", false):
		_assert(Exporter.canonical_json(result.data) == Exporter.canonical_json(_read_json(GOLDEN)), "mounted scene keeps golden semantics")
	mounted.free()

func _test_strict_numeric_validation(root: Node2D) -> void:
	var fractional_origin := root.duplicate() as Node2D
	fractional_origin.position = Vector2(999999.5, 96)
	_assert(not Exporter.export_scene(fractional_origin).get("ok", true), "large fractional origin is rejected exactly")
	_assert(Exporter.canonical_json({"origin": {"x": 999999.5}}).contains("999999.5"), "canonical JSON preserves fractional numbers exactly")
	fractional_origin.free()

	var fractional_anchor := root.duplicate() as Node2D
	fractional_anchor.tile_size = 512
	for y in fractional_anchor.grid_rows.size():
		fractional_anchor.grid_rows[y] = "0".repeat(256)
	fractional_anchor.get_node("arrival").position = Vector2(256, 256)
	fractional_anchor.get_node("guide").position = Vector2(130816.25, 1280)
	fractional_anchor.get_node("portal").position = Vector2(2816, 2304)
	fractional_anchor.get_node("training-area").position = Vector2(256, 1792)
	var fractional_anchor_result: Dictionary = Exporter.export_scene(fractional_anchor)
	var has_center_error := false
	for error in fractional_anchor_result.get("errors", []):
		if str(error).contains("必须位于格中心"):
			has_center_error = true
	_assert(has_center_error, "large fractional anchor position is rejected exactly")
	fractional_anchor.free()

	var non_finite_anchor := root.duplicate() as Node2D
	non_finite_anchor.get_node("guide").position = Vector2(NAN, 80)
	var non_finite_result: Dictionary = Exporter.export_scene(non_finite_anchor)
	_assert(not non_finite_result.get("ok", true), "non-finite anchor position is rejected without exporting")
	non_finite_anchor.free()

func _test_anchor_limit(root: Node2D) -> void:
	var oversized := root.duplicate() as Node2D
	for index in 1021:
		var anchor = AnchorScript.new()
		anchor.name = "extra-%04d" % index
		anchor.placement_id = "extra-%04d" % index
		anchor.kind = "trigger"
		anchor.extent = Vector2i.ONE
		anchor.position = Vector2(16, 16)
		oversized.add_child(anchor)
	var result: Dictionary = Exporter.export_scene(oversized)
	_assert(not result.get("ok", true), "more than 1024 anchors are rejected")
	oversized.free()

func _read_json(path: String) -> Dictionary:
	var parsed: Variant = JSON.parse_string(FileAccess.get_file_as_string(ProjectSettings.globalize_path(path)))
	return parsed as Dictionary if parsed is Dictionary else {}

func _assert(condition: bool, message: String) -> void:
	if not condition:
		_failures += 1
		push_error(message)
