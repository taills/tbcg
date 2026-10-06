package preview

import (
	"os"
	"testing"

	"github.com/taills/tbcg/server/internal/content/catalog"
	"github.com/taills/tbcg/server/internal/content/mapdata"
	"github.com/taills/tbcg/server/internal/content/schema"
)

func TestPreviewUsesRuntimeEngineAndRejectsBadRequests(t *testing.T) {
	contracts := os.DirFS("../../../../contracts")
	registry, err := schema.NewRegistry(contracts)
	if err != nil {
		t.Fatal(err)
	}
	geometry, err := mapdata.NewValidator(contracts)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := catalog.ReadDir(os.DirFS("../../../../games/demo/content"))
	if err != nil {
		t.Fatal(err)
	}
	content, issues := catalog.Load("demo", entries, registry, geometry)
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	result, err := Runner{}.PreviewBattle(content, []byte(`{"encounter_id":"demo:encounter/slime-pack","seed":7,"party":[{"archetype_id":"demo:archetype/wanderer","level":3,"equipped":["demo:item/wooden-sword"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["rules_version"] != RulesVersion {
		t.Fatal("preview must report its rules version")
	}
	for _, bad := range []string{`{}`, `{"encounter_id":"demo:encounter/slime-pack","party":[{"archetype_id":"missing","level":1}]}`,
		`{"encounter_id":"demo:encounter/missing","party":[{"archetype_id":"demo:archetype/wanderer","level":1}]}`,
		`{"encounter_id":"demo:encounter/slime-pack","party":[{"archetype_id":"demo:archetype/wanderer","level":0}]}`, `not json`} {
		if _, err := (Runner{}).PreviewBattle(content, []byte(bad)); err == nil {
			t.Fatalf("bad preview accepted: %s", bad)
		}
	}
}
