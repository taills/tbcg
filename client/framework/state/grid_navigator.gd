extends RefCounted
## 客户端寻路只用于生成意图路径；可走性与步进规则与服务端/地图契约完全一致（禁止对角穿角）。

const SpatialGrid = preload("res://addons/tbcg_authoring/spatial_grid.gd")

static func find_path(grid: Array, from_cell: Vector2i, to_cell: Vector2i, max_nodes: int = 4096) -> Array[Vector2i]:
	var empty: Array[Vector2i] = []
	if from_cell == to_cell or not SpatialGrid.walkable(grid, from_cell) or not SpatialGrid.walkable(grid, to_cell):
		return empty
	var parents := {from_cell: from_cell}
	var queue: Array[Vector2i] = [from_cell]
	var head := 0
	while head < queue.size() and head < max_nodes:
		var current := queue[head]
		head += 1
		if current == to_cell:
			break
		for direction in SpatialGrid.DIRECTIONS:
			var next: Vector2i = current + direction
			if parents.has(next) or not SpatialGrid.can_step(grid, current, next):
				continue
			parents[next] = current
			queue.append(next)
	if not parents.has(to_cell):
		return empty
	var path: Array[Vector2i] = []
	var cursor := to_cell
	while cursor != from_cell:
		path.push_front(cursor)
		cursor = parents[cursor]
	return path

static func anchor_distance(anchor: Dictionary, cell: Vector2i) -> int:
	var origin := Vector2i(int(anchor["cell"]["x"]), int(anchor["cell"]["y"]))
	var extent := Vector2i(int(anchor["extent"]["x"]), int(anchor["extent"]["y"]))
	var dx := 0
	if cell.x < origin.x:
		dx = origin.x - cell.x
	elif cell.x >= origin.x + extent.x:
		dx = cell.x - (origin.x + extent.x - 1)
	var dy := 0
	if cell.y < origin.y:
		dy = origin.y - cell.y
	elif cell.y >= origin.y + extent.y:
		dy = cell.y - (origin.y + extent.y - 1)
	return maxi(dx, dy)

static func find_anchor(geometry: Dictionary, anchor_id: String) -> Dictionary:
	for anchor in geometry.get("anchors", []):
		if anchor.get("placement_id", "") == anchor_id:
			return anchor
	return {}

## 返回与锚点距离不超过 range 的最近可达格（用于走到 NPC 旁）。
static func approach(grid: Array, from_cell: Vector2i, anchor: Dictionary, reach: int) -> Array[Vector2i]:
	if anchor_distance(anchor, from_cell) <= reach:
		var none: Array[Vector2i] = []
		return none
	var best: Array[Vector2i] = []
	for y in grid.size():
		for x in grid[y].size():
			var cell := Vector2i(x, y)
			if anchor_distance(anchor, cell) > reach or not SpatialGrid.walkable(grid, cell):
				continue
			var path := find_path(grid, from_cell, cell)
			if not path.is_empty() and (best.is_empty() or path.size() < best.size()):
				best = path
	return best
