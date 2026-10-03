// Package mapdata 按地图空间契约校验并编译正交地图几何。
package mapdata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	geometrySchemaPath = "content/map_geometry.schema.json"
	geometrySchemaID   = "urn:tbcg:content:map-geometry:v1"
	maxGeometryCells   = 256 * 256
)

// Cell 是从地图左上角开始的整数格坐标。
type Cell struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Point 是地图世界像素空间中的坐标。
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Anchor 是地图导出的稳定空间锚点。
type Anchor struct {
	PlacementID string `json:"placement_id"`
	Kind        string `json:"kind"`
	Cell        Cell   `json:"cell"`
	Extent      Cell   `json:"extent"`
}

// Validator 持有已编译的可信地图 Schema。
type Validator struct {
	schema *jsonschema.Schema
}

// Geometry 是经校验的不可变地图快照；网格和锚点不对外暴露切片。
type Geometry struct {
	width    int
	height   int
	tileSize int
	origin   Point
	grid     []uint8
	anchors  []Anchor
}

// NewValidator 从调用方提供的本地文件系统读取并编译地图 Schema。
func NewValidator(schemaFS fs.FS) (*Validator, error) {
	if schemaFS == nil {
		return nil, errors.New("map schema filesystem is nil")
	}
	file, err := schemaFS.Open(geometrySchemaPath)
	if err != nil {
		return nil, fmt.Errorf("open trusted map schema %q: %w", geometrySchemaPath, err)
	}
	doc, readErr := jsonschema.UnmarshalJSON(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("decode trusted map schema %q: %w", geometrySchemaPath, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close trusted map schema %q: %w", geometrySchemaPath, closeErr)
	}
	object, ok := doc.(map[string]any)
	if !ok || object["$id"] != geometrySchemaID {
		return nil, fmt.Errorf("trusted map schema must declare $id %q", geometrySchemaID)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(denyLoader{})
	if err := compiler.AddResource(geometrySchemaID, doc); err != nil {
		return nil, fmt.Errorf("register trusted map schema: %w", err)
	}
	compiled, err := compiler.Compile(geometrySchemaID)
	if err != nil {
		return nil, fmt.Errorf("compile trusted map schema: %w", err)
	}
	return &Validator{schema: compiled}, nil
}

// Decode schema-校验 JSON 并返回拥有独立网格/锚点副本的几何快照。
func (v *Validator) Decode(data []byte) (*Geometry, error) {
	if v == nil || v.schema == nil {
		return nil, errors.New("map validator is not initialized")
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode map JSON: %w", err)
	}
	if err := v.schema.Validate(instance); err != nil {
		return nil, fmt.Errorf("map schema validation failed: %w", err)
	}
	var decoded geometryDocument
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("decode map geometry: %w", err)
	}
	if err := validateDimensions(decoded); err != nil {
		return nil, err
	}
	geometry := &Geometry{
		width:    decoded.Width,
		height:   decoded.Height,
		tileSize: decoded.TileSize,
		origin:   Point{X: float64(decoded.Origin.X), Y: float64(decoded.Origin.Y)},
		grid:     make([]uint8, 0, decoded.Width*decoded.Height),
		anchors:  append([]Anchor(nil), decoded.Anchors...),
	}
	for _, row := range decoded.Grid {
		geometry.grid = append(geometry.grid, row...)
	}
	if err := validateAnchors(geometry); err != nil {
		return nil, err
	}
	return geometry, nil
}

type geometryDocument struct {
	Width    int       `json:"width"`
	Height   int       `json:"height"`
	TileSize int       `json:"tile_size"`
	Origin   Cell      `json:"origin"`
	Grid     [][]uint8 `json:"grid"`
	Anchors  []Anchor  `json:"anchors"`
}

func validateDimensions(document geometryDocument) error {
	if document.Width < 1 || document.Height < 1 || document.Width > 256 || document.Height > 256 {
		return errors.New("map dimensions must be between 1 and 256 cells")
	}
	if document.Width*document.Height > maxGeometryCells {
		return errors.New("map exceeds the 65536-cell limit")
	}
	if len(document.Grid) != document.Height {
		return fmt.Errorf("grid has %d rows, want height %d", len(document.Grid), document.Height)
	}
	for y, row := range document.Grid {
		if len(row) != document.Width {
			return fmt.Errorf("grid row %d has %d cells, want width %d", y, len(row), document.Width)
		}
	}
	return nil
}

func validateAnchors(geometry *Geometry) error {
	if len(geometry.anchors) > 1024 {
		return errors.New("map has more than 1024 anchors")
	}
	ids := make(map[string]struct{}, len(geometry.anchors))
	spawn := Cell{}
	spawnFound := false
	for index, anchor := range geometry.anchors {
		if _, exists := ids[anchor.PlacementID]; exists {
			return fmt.Errorf("anchor %q has a duplicate placement_id", anchor.PlacementID)
		}
		ids[anchor.PlacementID] = struct{}{}
		if index > 0 && geometry.anchors[index-1].PlacementID > anchor.PlacementID {
			return errors.New("anchors must be sorted by placement_id")
		}
		if anchor.Cell.X < 0 || anchor.Cell.Y < 0 || anchor.Cell.X >= geometry.width || anchor.Cell.Y >= geometry.height {
			return fmt.Errorf("anchor %q cell is outside map bounds", anchor.PlacementID)
		}
		if anchor.Extent.X < 1 || anchor.Extent.Y < 1 || anchor.Cell.X+anchor.Extent.X > geometry.width || anchor.Cell.Y+anchor.Extent.Y > geometry.height {
			return fmt.Errorf("anchor %q extent is outside map bounds", anchor.PlacementID)
		}
		if isPointAnchor(anchor.Kind) {
			if anchor.Extent != (Cell{X: 1, Y: 1}) {
				return fmt.Errorf("anchor %q kind %q must have a 1x1 extent", anchor.PlacementID, anchor.Kind)
			}
			if !geometry.Walkable(anchor.Cell) {
				return fmt.Errorf("anchor %q occupies a blocked cell", anchor.PlacementID)
			}
		}
		if anchor.Kind == "spawn" && !spawnFound {
			spawn = anchor.Cell
			spawnFound = true
		}
	}
	if !spawnFound {
		return errors.New("map must contain at least one spawn anchor")
	}
	reachable := geometry.reachableFrom(spawn)
	for _, anchor := range geometry.anchors {
		if isPointAnchor(anchor.Kind) && !reachable[geometry.index(anchor.Cell)] {
			return fmt.Errorf("anchor %q is unreachable from the first spawn", anchor.PlacementID)
		}
	}
	return nil
}

func isPointAnchor(kind string) bool {
	return kind == "spawn" || kind == "npc" || kind == "portal"
}

// Walkable 在地图边界外或障碍格时返回 false。
func (g *Geometry) Walkable(cell Cell) bool {
	if g == nil || cell.X < 0 || cell.Y < 0 || cell.X >= g.width || cell.Y >= g.height {
		return false
	}
	return g.grid[g.index(cell)] == 0
}

// CanStep 验证相邻四/八向移动，并阻止斜向切过角墙。
func (g *Geometry) CanStep(from, to Cell) bool {
	if !g.Walkable(from) || !g.Walkable(to) {
		return false
	}
	dx, dy := to.X-from.X, to.Y-from.Y
	if dx == 0 && dy == 0 || dx < -1 || dx > 1 || dy < -1 || dy > 1 {
		return false
	}
	if dx != 0 && dy != 0 {
		return g.Walkable(Cell{X: from.X + dx, Y: from.Y}) && g.Walkable(Cell{X: from.X, Y: from.Y + dy})
	}
	return true
}

// WorldToCell 按 floor((pixel-origin)/tile_size) 转换坐标，并报告是否在地图内。
func (g *Geometry) WorldToCell(pixelX, pixelY float64) (Cell, bool) {
	if g == nil || math.IsNaN(pixelX) || math.IsNaN(pixelY) || math.IsInf(pixelX, 0) || math.IsInf(pixelY, 0) {
		return Cell{}, false
	}
	x := math.Floor((pixelX - g.origin.X) / float64(g.tileSize))
	y := math.Floor((pixelY - g.origin.Y) / float64(g.tileSize))
	if x < 0 || y < 0 || x >= float64(g.width) || y >= float64(g.height) {
		cell := Cell{}
		if x >= float64(math.MinInt) && x < float64(math.MaxInt) {
			cell.X = int(x)
		}
		if y >= float64(math.MinInt) && y < float64(math.MaxInt) {
			cell.Y = int(y)
		}
		return cell, false
	}
	cell := Cell{X: int(x), Y: int(y)}
	return cell, true
}

// CellCenter 返回格中心对应的世界像素坐标。
func (g *Geometry) CellCenter(cell Cell) Point {
	if g == nil {
		return Point{}
	}
	size := float64(g.tileSize)
	return Point{
		X: g.origin.X + (float64(cell.X)+0.5)*size,
		Y: g.origin.Y + (float64(cell.Y)+0.5)*size,
	}
}

// FindPath 使用有界 BFS 返回包含起点和终点的最短格路径，不可达时返回 nil。
func (g *Geometry) FindPath(from, to Cell) []Cell {
	if !g.Walkable(from) || !g.Walkable(to) {
		return nil
	}
	start, target := g.index(from), g.index(to)
	parents := make([]int, len(g.grid))
	for i := range parents {
		parents[i] = -2
	}
	parents[start] = -1
	queue := make([]int, 1, len(g.grid))
	queue[0] = start
	for head := 0; head < len(queue); head++ {
		current := queue[head]
		if current == target {
			return reconstructPath(g, parents, target)
		}
		for _, next := range g.neighbors(current) {
			if parents[next] != -2 || g.grid[next] != 0 {
				continue
			}
			fromCell, toCell := g.cell(current), g.cell(next)
			if !g.CanStep(fromCell, toCell) {
				continue
			}
			parents[next] = current
			queue = append(queue, next)
		}
	}
	return nil
}

func (g *Geometry) reachableFrom(start Cell) []bool {
	reachable := make([]bool, len(g.grid))
	startIndex := g.index(start)
	reachable[startIndex] = true
	queue := make([]int, 1, len(g.grid))
	queue[0] = startIndex
	for head := 0; head < len(queue); head++ {
		current := queue[head]
		for _, next := range g.neighbors(current) {
			if reachable[next] || g.grid[next] != 0 || !g.CanStep(g.cell(current), g.cell(next)) {
				continue
			}
			reachable[next] = true
			queue = append(queue, next)
		}
	}
	return reachable
}

func (g *Geometry) neighbors(index int) []int {
	cell := g.cell(index)
	neighbors := make([]int, 0, 8)
	for y := max(0, cell.Y-1); y <= min(g.height-1, cell.Y+1); y++ {
		for x := max(0, cell.X-1); x <= min(g.width-1, cell.X+1); x++ {
			if x != cell.X || y != cell.Y {
				neighbors = append(neighbors, y*g.width+x)
			}
		}
	}
	return neighbors
}

func (g *Geometry) index(cell Cell) int { return cell.Y*g.width + cell.X }

func (g *Geometry) cell(index int) Cell { return Cell{X: index % g.width, Y: index / g.width} }

func reconstructPath(g *Geometry, parents []int, target int) []Cell {
	path := make([]Cell, 0)
	for current := target; current >= 0; current = parents[current] {
		path = append(path, g.cell(current))
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	return path
}

// denyLoader 拒绝从本机或网络动态加载 Schema 引用。
type denyLoader struct{}

func (denyLoader) Load(location string) (any, error) {
	return nil, fmt.Errorf("external schema reference %q is not permitted", location)
}
