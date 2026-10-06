package mapdata

// Width 返回地图列数。
func (g *Geometry) Width() int { return g.width }

// Height 返回地图行数。
func (g *Geometry) Height() int { return g.height }

// TileSize 返回每格像素尺寸（仅供表现层换算）。
func (g *Geometry) TileSize() int { return g.tileSize }

// Anchors 返回锚点副本。
func (g *Geometry) Anchors() []Anchor { return append([]Anchor(nil), g.anchors...) }

// Anchor 按 placement_id 查找锚点。
func (g *Geometry) Anchor(id string) (Anchor, bool) {
	for _, anchor := range g.anchors {
		if anchor.PlacementID == id {
			return anchor, true
		}
	}
	return Anchor{}, false
}

// Contains 判断格子是否在锚点覆盖范围内。
func (a Anchor) Contains(cell Cell) bool {
	return cell.X >= a.Cell.X && cell.Y >= a.Cell.Y && cell.X < a.Cell.X+a.Extent.X && cell.Y < a.Cell.Y+a.Extent.Y
}

// Distance 返回格子到锚点覆盖区域的切比雪夫距离。
func (a Anchor) Distance(cell Cell) int {
	dx := 0
	if cell.X < a.Cell.X {
		dx = a.Cell.X - cell.X
	} else if cell.X >= a.Cell.X+a.Extent.X {
		dx = cell.X - (a.Cell.X + a.Extent.X - 1)
	}
	dy := 0
	if cell.Y < a.Cell.Y {
		dy = a.Cell.Y - cell.Y
	} else if cell.Y >= a.Cell.Y+a.Extent.Y {
		dy = cell.Y - (a.Cell.Y + a.Extent.Y - 1)
	}
	if dx > dy {
		return dx
	}
	return dy
}

// NearestWalkable 返回锚点区域内第一个可走格子（行优先）。
func (g *Geometry) NearestWalkable(a Anchor) (Cell, bool) {
	for y := a.Cell.Y; y < a.Cell.Y+a.Extent.Y; y++ {
		for x := a.Cell.X; x < a.Cell.X+a.Extent.X; x++ {
			if g.Walkable(Cell{X: x, Y: y}) {
				return Cell{X: x, Y: y}, true
			}
		}
	}
	return Cell{}, false
}
