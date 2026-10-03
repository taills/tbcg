@tool
extends RefCounted

const ROOT_SCRIPT := "res://addons/tbcg_authoring/map_authoring_2d.gd"
const ANCHOR_SCRIPT := "res://addons/tbcg_authoring/map_anchor_2d.gd"
const SpatialGrid = preload("res://addons/tbcg_authoring/spatial_grid.gd")
const MAP_ID_PATTERN := "^[a-z0-9][a-z0-9._:/-]*$"
const PLACEMENT_ID_PATTERN := "^[a-z0-9][a-z0-9._-]*$"
const ANCHOR_KINDS := ["spawn", "npc", "portal", "trigger", "region"]
const VISUAL_NODE_TYPES := ["Node2D", "Sprite2D", "AnimatedSprite2D", "Polygon2D", "Line2D", "Label2D"]
const SPATIAL_FORBIDDEN := ["CollisionObject2D", "CollisionShape2D", "CollisionPolygon2D", "NavigationRegion2D", "NavigationLink2D", "TileMapLayer", "TileMap", "PhysicsBody2D", "Area2D"]
const MAX_ANCHORS := 1024

static func export_scene(scene_root: Node) -> Dictionary:
	var errors: Array[String] = []
	if not scene_root is Node2D or _script_path(scene_root) != ROOT_SCRIPT:
		return _failure(["根节点必须是受信任的 MapAuthoring2D"])
	var root := scene_root as Node2D
	_validate_transform(root, "根节点", errors)
	var rows: Array = root.get("grid_rows")
	var map_id := str(root.get("map_id"))
	var revision := int(root.get("geometry_revision"))
	var tile_size := int(root.get("tile_size"))
	var origin := Vector2(root.position)
	if rows.is_empty() or rows.size() > 256:
		errors.append("grid_rows 高度必须为 1..256")
	var width := 0
	if not rows.is_empty() and rows[0] is String:
		width = rows[0].length()
	if width < 1 or width > 256:
		errors.append("grid_rows 宽度必须为 1..256")
	if tile_size < 1 or tile_size > 512:
		errors.append("tile_size 必须为 1..512 的整数")
	if not _matches(map_id, MAP_ID_PATTERN) or map_id.length() > 128:
		errors.append("map_id 格式无效")
	if revision < 1 or revision > 2147483647:
		errors.append("geometry_revision 必须为正整数")
	if not _is_integer(origin.x) or not _is_integer(origin.y) or absf(origin.x) > 1000000 or absf(origin.y) > 1000000:
		errors.append("根节点 position 必须是范围内的整数 origin")
	var grid: Array = []
	for y in rows.size():
		var row: Variant = rows[y]
		if not row is String or row.length() != width:
			errors.append("grid_rows[%d] 必须与首行宽度相同" % y)
			continue
		var cells: Array[int] = []
		for x in row.length():
			var character: String = row.substr(x, 1)
			if character != "0" and character != "1":
				errors.append("grid_rows[%d][%d] 只能是 0 或 1" % [y, x])
				cells.append(1)
			else:
				cells.append(int(character))
		grid.append(cells)
	var anchors: Array[Dictionary] = []
	var seen_ids := {}
	var anchor_count := 0
	for child in root.get_children():
		if child is Marker2D and _script_path(child) == ANCHOR_SCRIPT:
			anchor_count += 1
			if anchor_count > MAX_ANCHORS:
				if anchor_count == MAX_ANCHORS + 1:
					errors.append("anchors 数量不得超过 %d" % MAX_ANCHORS)
				continue
			_validate_anchor(child as Node2D, rows, width, tile_size, origin, seen_ids, anchors, errors)
		elif child is Node2D and child.is_in_group("visual_only"):
			_validate_visual_tree(child, errors)
		else:
			errors.append("不支持的根子节点类型或未显式标记 visual_only：%s" % child.name)
	anchors.sort_custom(func(left: Dictionary, right: Dictionary) -> bool: return left["placement_id"].to_utf8_buffer().hex_encode() < right["placement_id"].to_utf8_buffer().hex_encode())
	if errors.is_empty():
		_validate_anchor_semantics(grid, anchors, errors)
	if not errors.is_empty():
		return _failure(errors)
	var data := {
		"schema_version": 1,
		"map_id": map_id,
		"geometry_revision": revision,
		"width": width,
		"height": rows.size(),
		"tile_size": tile_size,
		"origin": {"x": int(origin.x), "y": int(origin.y)},
		"axis": "x_right_y_down",
		"order": "row_major",
		"diagonal_policy": "no_corner_cutting",
		"grid": grid,
		"anchors": anchors
	}
	return {"ok": true, "data": data, "errors": []}

static func canonical_json(value: Variant) -> String:
	return JSON.stringify(_canonical_value(value), "", false, true)

static func _canonical_value(value: Variant) -> Variant:
	if value is float and is_finite(value) and value == roundf(value):
		return int(roundf(value))
	if value is Dictionary:
		var sorted_keys: Array = value.keys()
		sorted_keys.sort_custom(func(left: Variant, right: Variant) -> bool: return str(left).to_utf8_buffer().hex_encode() < str(right).to_utf8_buffer().hex_encode())
		var result := {}
		for key in sorted_keys:
			result[key] = _canonical_value(value[key])
		return result
	if value is Array:
		var result: Array = []
		for item in value:
			result.append(_canonical_value(item))
		return result
	return value

static func _validate_anchor(anchor: Node2D, rows: Array, width: int, tile_size: int, origin: Vector2, seen_ids: Dictionary, anchors: Array[Dictionary], errors: Array[String]) -> void:
	_validate_transform(anchor, "锚点 %s" % anchor.name, errors)
	if anchor.get_child_count() > 0:
		errors.append("锚点不得包含嵌套子节点：%s" % anchor.name)
	var placement_id := str(anchor.get("placement_id"))
	var kind := str(anchor.get("kind"))
	var extent: Vector2i = anchor.get("extent")
	if not _matches(placement_id, PLACEMENT_ID_PATTERN) or placement_id.length() > 128:
		errors.append("锚点 %s 的 placement_id 格式无效" % anchor.name)
	elif seen_ids.has(placement_id):
		errors.append("placement_id 重复：%s" % placement_id)
	else:
		seen_ids[placement_id] = true
	if not ANCHOR_KINDS.has(kind):
		errors.append("锚点 %s 的 kind 无效" % anchor.name)
	if extent.x < 1 or extent.x > 256 or extent.y < 1 or extent.y > 256:
		errors.append("锚点 %s 的 extent 必须为 1..256" % anchor.name)
	if not is_finite(anchor.position.x) or not is_finite(anchor.position.y):
		errors.append("锚点 %s 的 position 必须是有限数值" % anchor.name)
		return
	var cell := SpatialGrid.world_to_cell(anchor.position, Vector2.ZERO, tile_size)
	var cell_x := cell.x
	var cell_y := cell.y
	var center := Vector2((cell_x + 0.5) * tile_size, (cell_y + 0.5) * tile_size)
	if tile_size <= 0 or anchor.position != center:
		errors.append("锚点 %s 必须位于格中心" % anchor.name)
	if not SpatialGrid.inside(width, rows.size(), cell) or cell_x + extent.x > width or cell_y + extent.y > rows.size():
		errors.append("锚点 %s 超出地图边界" % anchor.name)
	anchors.append({"placement_id": placement_id, "kind": kind, "cell": {"x": cell_x, "y": cell_y}, "extent": {"x": extent.x, "y": extent.y}})

static func _validate_anchor_semantics(grid: Array, anchors: Array[Dictionary], errors: Array[String]) -> void:
	var spawns: Array[Vector2i] = []
	for anchor in anchors:
		var cell: Dictionary = anchor["cell"]
		var extent: Dictionary = anchor["extent"]
		var kind: String = anchor["kind"]
		var cell_position := Vector2i(cell["x"], cell["y"])
		if kind == "spawn":
			spawns.append(cell_position)
		if kind == "spawn" or kind == "npc" or kind == "portal":
			if not SpatialGrid.walkable(grid, cell_position):
				errors.append("锚点 %s 占用阻挡格" % anchor["placement_id"])
			if extent["x"] != 1 or extent["y"] != 1:
				errors.append("锚点 %s 必须使用 1x1 extent" % anchor["placement_id"])
	if spawns.is_empty():
		errors.append("地图至少需要一个 spawn")
		return
	var reachable_cells: Dictionary = SpatialGrid.flood_fill(grid, spawns[0])
	for anchor in anchors:
		if not ["spawn", "npc", "portal"].has(anchor["kind"]):
			continue
		var cell: Dictionary = anchor["cell"]
		var cell_position := Vector2i(cell["x"], cell["y"])
		if not SpatialGrid.is_reachable(reachable_cells, cell_position):
			errors.append("锚点 %s 无法从首个 spawn 到达" % anchor["placement_id"])

static func _validate_transform(node: Node2D, label: String, errors: Array[String]) -> void:
	if not is_zero_approx(node.rotation) or not node.scale.is_equal_approx(Vector2.ONE) or not is_zero_approx(node.skew):
		errors.append("%s 不允许旋转、缩放或 skew" % label)

static func _validate_visual_tree(node: Node, errors: Array[String]) -> void:
	for type_name in SPATIAL_FORBIDDEN:
		if node.is_class(type_name):
			errors.append("visual_only 子树包含空间节点：%s" % node.name)
			return
	if not node is Node2D or not VISUAL_NODE_TYPES.has(node.get_class()):
		errors.append("visual_only 子树包含不支持的节点类型：%s" % node.get_class())
		return
	if node.get_script() != null:
		errors.append("visual_only 子树不允许附加脚本：%s" % node.name)
		return
	for child in node.get_children():
		_validate_visual_tree(child, errors)

static func _matches(value: String, pattern: String) -> bool:
	var regex := RegEx.new()
	if regex.compile(pattern) != OK:
		return false
	var found := regex.search(value)
	return found != null and found.get_string() == value

static func _is_integer(value: float) -> bool:
	return is_finite(value) and value == roundf(value)

static func _script_path(node: Node) -> String:
	var script: Variant = node.get_script()
	return script.resource_path if script is Script else ""

static func _failure(errors: Array) -> Dictionary:
	return {"ok": false, "data": null, "errors": errors}
