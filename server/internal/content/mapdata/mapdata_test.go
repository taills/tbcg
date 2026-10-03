package mapdata

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

const (
	contractsDir = "../../../../contracts"
	fixtureDir   = "../../../../tests/fixtures/maps"
)

func TestNewValidatorRejectsInvalidSchemaSources(t *testing.T) {
	validID := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"urn:tbcg:content:map-geometry:v1","type":"object"}`
	tests := []struct {
		name string
		data string
	}{
		{name: "malformed schema", data: `{"$id":`},
		{name: "wrong schema id", data: `{"$id":"urn:wrong"}`},
		{name: "remote reference", data: `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"urn:tbcg:content:map-geometry:v1","$ref":"https://example.invalid/map.json"}`},
	}
	if _, err := NewValidator(nil); err == nil {
		t.Fatal("NewValidator(nil) error = nil, want failure")
	}
	if _, err := NewValidator(fstest.MapFS{}); err == nil {
		t.Fatal("NewValidator(empty FS) error = nil, want missing schema")
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewValidator(fstest.MapFS{
				"content/map_geometry.schema.json": &fstest.MapFile{Data: []byte(tc.data)},
			})
			if err == nil {
				t.Fatal("NewValidator() error = nil, want failure")
			}
		})
	}
	if validator, err := NewValidator(fstest.MapFS{
		"content/map_geometry.schema.json": &fstest.MapFile{Data: []byte(validID)},
	}); err != nil || validator == nil {
		t.Fatalf("NewValidator(valid schema) = (%v, %v), want validator", validator, err)
	}
}

func TestDecodeTrainingGroundAndSpatialCases(t *testing.T) {
	validator := newFixtureValidator(t)
	data := readMapFixture(t, "training-ground.json")
	geometry, err := validator.Decode(data)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	var cases struct {
		Walkability []struct {
			Cell     Cell `json:"cell"`
			Walkable bool `json:"walkable"`
		} `json:"walkability"`
		Steps []struct {
			From    Cell `json:"from"`
			To      Cell `json:"to"`
			Allowed bool `json:"allowed"`
		} `json:"steps"`
		Conversions []struct {
			Pixel  Point `json:"pixel"`
			Cell   Cell  `json:"cell"`
			Inside bool  `json:"inside"`
		} `json:"conversions"`
		Routes []struct {
			From      Cell `json:"from"`
			To        Cell `json:"to"`
			Reachable bool `json:"reachable"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(readMapFixture(t, "spatial-cases.json"), &cases); err != nil {
		t.Fatalf("decode spatial cases: %v", err)
	}
	for _, tc := range cases.Walkability {
		if got := geometry.Walkable(tc.Cell); got != tc.Walkable {
			t.Errorf("Walkable(%+v) = %v, want %v", tc.Cell, got, tc.Walkable)
		}
	}
	for _, tc := range cases.Steps {
		if got := geometry.CanStep(tc.From, tc.To); got != tc.Allowed {
			t.Errorf("CanStep(%+v, %+v) = %v, want %v", tc.From, tc.To, got, tc.Allowed)
		}
	}
	for _, tc := range cases.Conversions {
		cell, inside := geometry.WorldToCell(tc.Pixel.X, tc.Pixel.Y)
		if cell != tc.Cell || inside != tc.Inside {
			t.Errorf("WorldToCell(%v, %v) = (%+v, %v), want (%+v, %v)", tc.Pixel.X, tc.Pixel.Y, cell, inside, tc.Cell, tc.Inside)
		}
	}
	for _, tc := range cases.Routes {
		path := geometry.FindPath(tc.From, tc.To)
		if (len(path) != 0) != tc.Reachable {
			t.Errorf("FindPath(%+v, %+v) = %v, reachable %v; want %v", tc.From, tc.To, path, len(path) != 0, tc.Reachable)
		}
		if tc.Reachable {
			if path[0] != tc.From || path[len(path)-1] != tc.To {
				t.Errorf("FindPath endpoints = %v, want %v to %v", path, tc.From, tc.To)
			}
			for i := 1; i < len(path); i++ {
				if !geometry.CanStep(path[i-1], path[i]) {
					t.Errorf("FindPath contains invalid step %v to %v", path[i-1], path[i])
				}
			}
		}
	}
}

func TestDecodeMaximumGridSize(t *testing.T) {
	grid := make([][]int, 256)
	for y := range grid {
		grid[y] = make([]int, 256)
	}
	document := map[string]any{
		"schema_version": 1, "map_id": "demo:map/maximum", "geometry_revision": 1,
		"width": 256, "height": 256, "tile_size": 1,
		"origin": map[string]int{"x": 0, "y": 0},
		"axis":   "x_right_y_down", "order": "row_major", "diagonal_policy": "no_corner_cutting",
		"grid": grid,
		"anchors": []any{map[string]any{
			"placement_id": "spawn", "kind": "spawn", "cell": map[string]int{"x": 0, "y": 0},
			"extent": map[string]int{"x": 1, "y": 1},
		}},
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	geometry, err := newFixtureValidator(t).Decode(data)
	if err != nil {
		t.Fatalf("Decode(65536 cells) error = %v", err)
	}
	path := geometry.FindPath(Cell{}, Cell{X: 255, Y: 255})
	if len(path) != 256 {
		t.Fatalf("FindPath(maximum map) has %d cells, want 256", len(path))
	}
}

func TestNilGeometryMethods(t *testing.T) {
	var geometry *Geometry
	if geometry.Walkable(Cell{}) || geometry.CanStep(Cell{}, Cell{}) {
		t.Fatal("nil geometry must not allow walking or steps")
	}
	if cell, inside := geometry.WorldToCell(0, 0); cell != (Cell{}) || inside {
		t.Fatalf("nil WorldToCell() = (%+v, %v), want zero cell outside", cell, inside)
	}
	if point := geometry.CellCenter(Cell{}); point != (Point{}) {
		t.Fatalf("nil CellCenter() = %+v, want zero point", point)
	}
	if path := geometry.FindPath(Cell{}, Cell{}); path != nil {
		t.Fatalf("nil FindPath() = %v, want nil", path)
	}
}

func TestCellCenterAndInputIsolation(t *testing.T) {
	validator := newFixtureValidator(t)
	data := readMapFixture(t, "training-ground.json")
	geometry, err := validator.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := geometry.CellCenter(Cell{X: 0, Y: 0}), (Point{X: 80, Y: 112}); got != want {
		t.Fatalf("CellCenter() = %+v, want %+v", got, want)
	}
	if got, want := geometry.CellCenter(Cell{X: 5, Y: 4}), (Point{X: 240, Y: 240}); got != want {
		t.Fatalf("CellCenter(last cell) = %+v, want %+v", got, want)
	}

	data[0] = ' '
	if !geometry.Walkable(Cell{X: 0, Y: 0}) || geometry.Walkable(Cell{X: 1, Y: 1}) {
		t.Fatal("mutating input bytes changed compiled geometry")
	}
	if geometry.Walkable(Cell{X: -1, Y: 0}) || geometry.Walkable(Cell{X: 6, Y: 4}) {
		t.Fatal("out-of-bounds cells must not be walkable")
	}
}

func TestDecodeRejectsSchemaAndSemanticViolations(t *testing.T) {
	validator := newFixtureValidator(t)
	valid := readMapFixture(t, "training-ground.json")
	var base map[string]any
	if err := json.Unmarshal(valid, &base); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		change func(map[string]any)
	}{
		{name: "malformed json"},
		{name: "missing schema field", change: func(doc map[string]any) { delete(doc, "map_id") }},
		{name: "zero width", change: func(doc map[string]any) { doc["width"] = 0 }},
		{name: "grid row width mismatch", change: func(doc map[string]any) { doc["grid"].([]any)[0] = []any{float64(0)} }},
		{name: "grid row count mismatch", change: func(doc map[string]any) { doc["grid"] = doc["grid"].([]any)[:4] }},
		{name: "duplicate placement id", change: func(doc map[string]any) { doc["anchors"].([]any)[1].(map[string]any)["placement_id"] = "arrival" }},
		{name: "anchor outside map", change: func(doc map[string]any) {
			doc["anchors"].([]any)[1].(map[string]any)["cell"].(map[string]any)["x"] = float64(6)
		}},
		{name: "extent outside map", change: func(doc map[string]any) {
			doc["anchors"].([]any)[3].(map[string]any)["extent"].(map[string]any)["x"] = float64(7)
		}},
		{name: "special anchor extent", change: func(doc map[string]any) {
			doc["anchors"].([]any)[1].(map[string]any)["extent"].(map[string]any)["x"] = float64(2)
		}},
		{name: "special anchor blocked", change: func(doc map[string]any) {
			doc["anchors"].([]any)[1].(map[string]any)["cell"].(map[string]any)["y"] = float64(1)
		}},
		{name: "no spawn", change: func(doc map[string]any) { doc["anchors"] = doc["anchors"].([]any)[1:] }},
		{name: "unreachable special anchor", change: func(doc map[string]any) {
			doc["anchors"].([]any)[2].(map[string]any)["cell"] = map[string]any{"x": float64(4), "y": float64(1)}
		}},
		{name: "anchors not sorted", change: func(doc map[string]any) {
			anchors := doc["anchors"].([]any)
			anchors[0], anchors[1] = anchors[1], anchors[0]
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("{")
			if tc.change != nil {
				doc := cloneJSONMap(t, base)
				tc.change(doc)
				var err error
				data, err = json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := validator.Decode(data); err == nil {
				t.Fatal("Decode() error = nil, want rejection")
			}
		})
	}
}

func TestDecodeRejectsNonFiniteAndInvalidDimensions(t *testing.T) {
	validator := newFixtureValidator(t)
	valid := string(readMapFixture(t, "training-ground.json"))
	for _, value := range []string{"NaN", "Infinity", "-Infinity"} {
		data := []byte(strings.Replace(valid, `"x": 64`, `"x": `+value, 1))
		if _, err := validator.Decode(data); err == nil {
			t.Errorf("Decode(origin.x=%s) error = nil, want rejection", value)
		}
	}
	for _, input := range [][]byte{nil, {}, []byte(`null`), []byte(`[]`)} {
		if _, err := validator.Decode(input); err == nil {
			t.Errorf("Decode(%q) error = nil, want rejection", input)
		}
	}
}

func TestWorldToCellHandlesNegativeAndNonFiniteCoordinates(t *testing.T) {
	geometry, err := newFixtureValidator(t).Decode(readMapFixture(t, "training-ground.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, coordinate := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), math.MaxFloat64, -math.MaxFloat64} {
		_, inside := geometry.WorldToCell(coordinate, 96)
		if inside {
			t.Errorf("WorldToCell(%v, 96) reports inside, want outside", coordinate)
		}
	}
	if cell, inside := geometry.WorldToCell(63.999, 96); inside || cell.X != -1 {
		t.Fatalf("negative-side WorldToCell() = (%+v, %v), want x=-1 outside", cell, inside)
	}
}

func TestFindPathBoundsWorkAndHandlesInvalidEndpoints(t *testing.T) {
	geometry, err := newFixtureValidator(t).Decode(readMapFixture(t, "training-ground.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoints := range [][2]Cell{{{X: -1}, {X: 2}}, {{X: 1, Y: 1}, {X: 2, Y: 2}}, {{X: 0}, {X: 6, Y: 0}}} {
		if path := geometry.FindPath(endpoints[0], endpoints[1]); len(path) != 0 {
			t.Errorf("FindPath(%+v, %+v) = %v, want no route", endpoints[0], endpoints[1], path)
		}
	}
	if path := geometry.FindPath(Cell{}, Cell{}); len(path) != 1 || path[0] != (Cell{}) {
		t.Fatalf("FindPath(start, start) = %v, want the single-cell route", path)
	}
}

func newFixtureValidator(t *testing.T) *Validator {
	t.Helper()
	validator, err := NewValidator(os.DirFS(contractsDir))
	if err != nil {
		t.Fatalf("NewValidator() error = %v", err)
	}
	return validator
}

func readMapFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(fixtureDir + "/" + name)
	if err != nil {
		t.Fatalf("read map fixture %q: %v", name, err)
	}
	return data
}

func cloneJSONMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
