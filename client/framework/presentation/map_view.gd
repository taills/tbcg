extends Node2D
## 地图表现：只把服务端网格坐标换算为像素绘制；点击产生意图事件，不本地改变权威位置。

signal cell_clicked(cell: Vector2i)

const CELL_COLORS := {0: Color(0.86, 0.9, 0.8), 1: Color(0.32, 0.36, 0.3)}
const ANCHOR_COLORS := {"spawn": Color(0.4, 0.7, 1.0, 0.45), "npc": Color(1.0, 0.8, 0.3, 0.6), "portal": Color(0.7, 0.4, 1.0, 0.55),
	"trigger": Color(0.3, 0.9, 0.5, 0.35), "region": Color(0.9, 0.4, 0.4, 0.35)}

var geometry := {}
var npcs: Array = []
var players := {}
var self_id := ""
var tile := 48
var _font: Font

func _ready() -> void:
	_font = ThemeDB.fallback_font

func set_map(map_geometry: Dictionary, map_npcs: Array) -> void:
	geometry = map_geometry
	npcs = map_npcs
	queue_redraw()

func set_players(entities: Dictionary, own_id: String, own_view: Dictionary) -> void:
	players = entities.duplicate(true)
	self_id = own_id
	if not own_view.is_empty():
		players[own_id] = own_view
	queue_redraw()

func cell_rect(cell: Vector2i) -> Rect2:
	return Rect2(Vector2(cell.x * tile, cell.y * tile), Vector2(tile, tile))

func _unhandled_input(event: InputEvent) -> void:
	if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT and not geometry.is_empty():
		var local := to_local(event.position)
		var cell := Vector2i(floori(local.x / tile), floori(local.y / tile))
		if cell.x >= 0 and cell.y >= 0 and cell.x < int(geometry.get("width", 0)) and cell.y < int(geometry.get("height", 0)):
			cell_clicked.emit(cell)
			get_viewport().set_input_as_handled()

func _draw() -> void:
	if geometry.is_empty():
		return
	var grid: Array = geometry["grid"]
	for y in grid.size():
		for x in grid[y].size():
			var rect := cell_rect(Vector2i(x, y))
			draw_rect(rect, CELL_COLORS.get(int(grid[y][x]), Color.MAGENTA))
			draw_rect(rect, Color(0, 0, 0, 0.15), false, 1.0)
	for anchor in geometry.get("anchors", []):
		var rect := Rect2(Vector2(int(anchor["cell"]["x"]) * tile, int(anchor["cell"]["y"]) * tile), Vector2(int(anchor["extent"]["x"]) * tile, int(anchor["extent"]["y"]) * tile))
		draw_rect(rect, ANCHOR_COLORS.get(anchor["kind"], Color(1, 1, 1, 0.3)))
		if anchor["kind"] in ["portal", "trigger", "region"]:
			draw_string(_font, rect.position + Vector2(4, 14), str(anchor["placement_id"]), HORIZONTAL_ALIGNMENT_LEFT, -1, 11, Color.BLACK)
	for npc in npcs:
		var anchor := _anchor(str(npc.get("anchor_id", "")))
		if anchor.is_empty():
			continue
		var center := cell_rect(Vector2i(int(anchor["cell"]["x"]), int(anchor["cell"]["y"]))).get_center()
		draw_circle(center, tile * 0.35, Color(0.85, 0.55, 0.15))
		draw_string(_font, center + Vector2(-tile * 0.5, -tile * 0.45), str(npc.get("display_name", "")), HORIZONTAL_ALIGNMENT_CENTER, tile, 12, Color.BLACK)
	for id in players:
		var player: Dictionary = players[id]
		var rect := cell_rect(Vector2i(int(player.get("x", 0)), int(player.get("y", 0)))).grow(-tile * 0.18)
		var color := Color(0.2, 0.45, 0.95) if id == self_id else Color(0.25, 0.7, 0.35)
		if player.get("in_battle", false):
			color = Color(0.85, 0.2, 0.2)
		draw_rect(rect, color)
		if str(player.get("mount_id", "")) != "":
			draw_rect(rect.grow(3), Color(0.6, 0.4, 0.1), false, 2.0)
		draw_string(_font, rect.position + Vector2(-6, -4), str(player.get("name", "")), HORIZONTAL_ALIGNMENT_LEFT, -1, 12, Color.BLACK)

func _anchor(anchor_id: String) -> Dictionary:
	for anchor in geometry.get("anchors", []):
		if anchor["placement_id"] == anchor_id:
			return anchor
	return {}
