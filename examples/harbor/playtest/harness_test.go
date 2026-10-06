// Package playtest 是「雾港奇谭」自己的黑盒测试：只通过框架的命令行工具与公开协议使用框架，不导入任何框架包。
package playtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// exampleDir 返回 examples/harbor 的绝对路径。
func exampleDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	return filepath.Dir(filepath.Dir(file))
}

// repoRoot 返回框架仓库根目录（examples/harbor 的上两级）。
func repoRoot(t *testing.T) string {
	return filepath.Dir(filepath.Dir(exampleDir(t)))
}

// stageProjectRoot 按框架约定拼出临时项目根：contracts/ 与 games/harbor/ 都是符号链接，不复制、不修改框架。
func stageProjectRoot(t *testing.T) string {
	t.Helper()
	stage := t.TempDir()
	if err := os.Symlink(filepath.Join(repoRoot(t), "contracts"), filepath.Join(stage, "contracts")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stage, "games"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exampleDir(t), filepath.Join(stage, "games", "harbor")); err != nil {
		t.Fatal(err)
	}
	return stage
}

type cell struct{ X, Y int }

type anchor struct {
	PlacementID string `json:"placement_id"`
	Kind        string `json:"kind"`
	Cell        cell   `json:"cell"`
	Extent      cell   `json:"extent"`
}

type geometry struct {
	MapID    string   `json:"map_id"`
	Width    int      `json:"width"`
	Height   int      `json:"height"`
	TileSize int      `json:"tile_size"`
	Origin   cell     `json:"origin"`
	Grid     [][]int  `json:"grid"`
	Anchors  []anchor `json:"anchors"`
}

func (g geometry) anchor(id string) (anchor, bool) {
	for _, a := range g.Anchors {
		if a.PlacementID == id {
			return a, true
		}
	}
	return anchor{}, false
}

func (g geometry) walkable(c cell) bool {
	return c.X >= 0 && c.Y >= 0 && c.X < g.Width && c.Y < g.Height && g.Grid[c.Y][c.X] == 0
}

// path 用四向 BFS 生成合法路径（不含起点）；四向步进永远不会切墙角。
func (g geometry) path(from, to cell) []cell {
	parents := map[cell]cell{from: from}
	queue := []cell{from}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == to {
			var out []cell
			for c := to; c != from; c = parents[c] {
				out = append([]cell{c}, out...)
			}
			return out
		}
		for _, d := range []cell{{0, 1}, {0, -1}, {1, 0}, {-1, 0}} {
			next := cell{current.X + d.X, current.Y + d.Y}
			if _, seen := parents[next]; !seen && g.walkable(next) {
				parents[next] = current
				queue = append(queue, next)
			}
		}
	}
	return nil
}

// definition 读取一条内容定义的原始 JSON。
func definition(t *testing.T, contentType, slug string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(exampleDir(t), "content", contentType, slug+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func loadGeometry(t *testing.T, slug string) geometry {
	t.Helper()
	raw, _ := json.Marshal(definition(t, "map", slug)["public"].(map[string]any)["geometry"])
	var g geometry
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}
