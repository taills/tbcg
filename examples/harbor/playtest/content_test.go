package playtest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestContentPassesFrameworkValidation 用框架自带、未修改的 content-tool 校验 Schema 与全部交叉引用。
func TestContentPassesFrameworkValidation(t *testing.T) {
	command := exec.Command("go", "run", "./cmd/content-tool", "validate", "-root", stageProjectRoot(t), "-game", "harbor")
	command.Dir = filepath.Join(repoRoot(t), "server")
	command.Stderr = os.Stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("content-tool validate: %v", err)
	}
	var report struct {
		EntryCount int  `json:"entry_count"`
		Valid      bool `json:"valid"`
		Issues     any  `json:"issues"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, output)
	}
	if !report.Valid || report.EntryCount < 60 {
		t.Fatalf("content invalid (%d entries): %v", report.EntryCount, report.Issues)
	}
}

// TestItemIconsMatchManifest 确认每个物品图标都在资源白名单中，且文件哈希与清单一致。
func TestItemIconsMatchManifest(t *testing.T) {
	assets := filepath.Join(exampleDir(t), "assets")
	data, err := os.ReadFile(filepath.Join(assets, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Assets []struct {
			AssetID string `json:"asset_id"`
			Path    string `json:"path"`
			SHA256  string `json:"sha256"`
			Source  string `json:"source"`
			License string `json:"license"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, asset := range manifest.Assets {
		file, err := os.ReadFile(filepath.Join(assets, filepath.FromSlash(asset.Path)))
		if err != nil {
			t.Fatalf("%s: %v", asset.AssetID, err)
		}
		sum := sha256.Sum256(file)
		if hex.EncodeToString(sum[:]) != asset.SHA256 || asset.Source == "" || asset.License == "" {
			t.Errorf("%s: hash or provenance mismatch", asset.AssetID)
		}
		known[asset.AssetID] = true
	}
	items, _ := filepath.Glob(filepath.Join(exampleDir(t), "content", "item", "*.json"))
	for _, path := range items {
		slug := strings.TrimSuffix(filepath.Base(path), ".json")
		icon := definition(t, "item", slug)["public"].(map[string]any)["icon_id"].(string)
		if !known[icon] {
			t.Errorf("item %s references unknown icon %s", slug, icon)
		}
	}
}

var (
	gridRowsPattern = regexp.MustCompile(`grid_rows = Array\[String\]\(\[(.*)\]\)`)
	nodePattern     = regexp.MustCompile(`(?m)^position = Vector2\((\d+), (\d+)\)\nscript = ExtResource\("2_anchor"\)\nplacement_id = "([^"]+)"\nkind = "([^"]+)"\nextent = Vector2i\((\d+), (\d+)\)$`)
)

// TestMapScenesMatchContent 确认 Godot 创作场景与内容中的几何一致（网格、锚点位置、类型与范围）。
func TestMapScenesMatchContent(t *testing.T) {
	for _, slug := range []string{"pier-town", "lighthouse", "tide-flats"} {
		t.Run(slug, func(t *testing.T) {
			g := loadGeometry(t, slug)
			scene, err := os.ReadFile(filepath.Join(exampleDir(t), "client", "games", "harbor", "maps", slug+".tscn"))
			if err != nil {
				t.Fatal(err)
			}
			match := gridRowsPattern.FindSubmatch(scene)
			if match == nil {
				t.Fatal("scene has no grid_rows")
			}
			var rows []string
			for _, row := range g.Grid {
				var b strings.Builder
				for _, c := range row {
					b.WriteString(strconv.Itoa(c))
				}
				rows = append(rows, `"`+b.String()+`"`)
			}
			if string(match[1]) != strings.Join(rows, ", ") {
				t.Errorf("grid differs:\nscene   %s\ncontent %s", match[1], strings.Join(rows, ", "))
			}
			nodes := nodePattern.FindAllSubmatch(scene, -1)
			if len(nodes) != len(g.Anchors) {
				t.Fatalf("scene has %d anchors, content has %d", len(nodes), len(g.Anchors))
			}
			for _, node := range nodes {
				a, ok := g.anchor(string(node[3]))
				if !ok {
					t.Errorf("scene anchor %s missing from content", node[3])
					continue
				}
				x, _ := strconv.Atoi(string(node[1]))
				y, _ := strconv.Atoi(string(node[2]))
				ex, _ := strconv.Atoi(string(node[5]))
				ey, _ := strconv.Atoi(string(node[6]))
				// 锚点位置相对地图根节点（根节点 position 即 origin），落在左上格的格中心。
				half := g.TileSize / 2
				want := cell{a.Cell.X*g.TileSize + half, a.Cell.Y*g.TileSize + half}
				if (cell{x, y}) != want || string(node[4]) != a.Kind || (cell{ex, ey}) != a.Extent {
					t.Errorf("anchor %s differs: scene (%d,%d) %s %dx%d", a.PlacementID, x, y, node[4], ex, ey)
				}
			}
		})
	}
}

// TestGodotMapCheck 在示例 Godot 工程中无界面运行 tools/check_maps.gd：用框架导出器导出每个地图场景并与内容几何比对，
// 再用公开包检查锚点绑定。需要 Godot 4.7.1（TBCG_GODOT_BIN 或默认 macOS 安装路径），找不到时跳过。
func TestGodotMapCheck(t *testing.T) {
	godot := os.Getenv("TBCG_GODOT_BIN")
	if godot == "" {
		godot = "/Applications/Godot.app/Contents/MacOS/Godot"
	}
	if _, err := os.Stat(godot); err != nil {
		t.Skip("Godot binary not found; set TBCG_GODOT_BIN")
	}
	bundle := filepath.Join(t.TempDir(), "public-bundle.json")
	build := exec.Command("go", "run", "./cmd/content-tool", "public-bundle", "-root", stageProjectRoot(t), "-game", "harbor", "-out", bundle)
	build.Dir = filepath.Join(repoRoot(t), "server")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("content-tool public-bundle: %v\n%s", err, output)
	}
	check := exec.Command(godot, "--headless", "--path", filepath.Join(exampleDir(t), "client"),
		"--script", "res://tools/check_maps.gd", "--", "--bundle", bundle)
	output, err := check.CombinedOutput()
	if err != nil {
		t.Fatalf("check_maps.gd: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "0 failure(s)") {
		t.Fatalf("unexpected check_maps.gd output:\n%s", output)
	}
}
