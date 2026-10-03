@tool
class_name MapAuthoring2D
extends Node2D

@export var grid_rows: Array[String] = []:
	set(value):
		grid_rows = value.duplicate()
		queue_redraw()
@export_range(1, 512, 1) var tile_size := 32
@export var map_id := ""
@export_range(1, 2147483647, 1) var geometry_revision := 1

func _draw() -> void:
	if tile_size <= 0:
		return
	for y in grid_rows.size():
		var row := grid_rows[y]
		for x in row.length():
			var cell := Rect2(Vector2(x * tile_size, y * tile_size), Vector2(tile_size, tile_size))
			var blocked := row.substr(x, 1) == "1"
			draw_rect(cell, Color(0.68, 0.22, 0.18, 0.2) if blocked else Color(0.2, 0.72, 0.42, 0.08), true)
			draw_rect(cell, Color(0.15, 0.15, 0.15, 0.55), false, 1.0)
