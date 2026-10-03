@tool
extends RefCounted

const DIRECTIONS := [
	Vector2i(-1, -1), Vector2i(0, -1), Vector2i(1, -1),
	Vector2i(-1, 0), Vector2i(1, 0),
	Vector2i(-1, 1), Vector2i(0, 1), Vector2i(1, 1)
]

static func inside(width: int, height: int, cell: Vector2i) -> bool:
	return cell.x >= 0 and cell.x < width and cell.y >= 0 and cell.y < height

static func walkable(grid: Array, cell: Vector2i) -> bool:
	return cell.y >= 0 and cell.y < grid.size() and cell.x >= 0 and cell.x < grid[cell.y].size() and int(grid[cell.y][cell.x]) == 0

static func can_step(grid: Array, source: Vector2i, target: Vector2i) -> bool:
	var delta := target - source
	if absi(delta.x) > 1 or absi(delta.y) > 1 or delta == Vector2i.ZERO:
		return false
	if not walkable(grid, source) or not walkable(grid, target):
		return false
	if delta.x != 0 and delta.y != 0:
		return walkable(grid, Vector2i(source.x + delta.x, source.y)) and walkable(grid, Vector2i(source.x, source.y + delta.y))
	return true

static func world_to_cell(pixel: Vector2, origin: Vector2, tile_size: int) -> Vector2i:
	if tile_size <= 0 or not is_finite(pixel.x) or not is_finite(pixel.y) or not is_finite(origin.x) or not is_finite(origin.y):
		return Vector2i(-1, -1)
	var x := floorf((float(pixel.x) - float(origin.x)) / tile_size)
	var y := floorf((float(pixel.y) - float(origin.y)) / tile_size)
	# Vector2i 使用32位分量，先检验范围，避免有限大数转换后回绕到合法格。
	if x < -2147483648.0 or x > 2147483647.0 or y < -2147483648.0 or y > 2147483647.0:
		return Vector2i(-1, -1)
	return Vector2i(int(x), int(y))

static func flood_fill(grid: Array, start: Vector2i) -> Dictionary:
	if not walkable(grid, start):
		return {}
	var queue: Array[Vector2i] = [start]
	var reachable := {start: true}
	var head := 0
	while head < queue.size():
		var current := queue[head]
		head += 1
		for direction in DIRECTIONS:
			var next: Vector2i = current + direction
			if reachable.has(next) or not can_step(grid, current, next):
				continue
			reachable[next] = true
			queue.append(next)
	return reachable

static func is_reachable(reachable_cells: Dictionary, target: Vector2i) -> bool:
	return reachable_cells.has(target)
