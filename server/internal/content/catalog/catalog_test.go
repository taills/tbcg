package catalog

import (
	"os"
	"testing"

	"github.com/taills/tbcg/server/internal/content/mapdata"
	"github.com/taills/tbcg/server/internal/content/schema"
)

func loadDemo(t *testing.T) *Catalog {
	t.Helper()
	contracts := os.DirFS("../../../../contracts")
	registry, err := schema.NewRegistry(contracts)
	if err != nil {
		t.Fatal(err)
	}
	geometry, err := mapdata.NewValidator(contracts)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := ReadDir(os.DirFS("../../../../games/demo/content"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, issues := Load("demo", entries, registry, geometry)
	for _, issue := range issues {
		t.Errorf("issue: %v", issue)
	}
	return catalog
}

func TestDemoContentIsValid(t *testing.T) {
	catalog := loadDemo(t)
	if len(catalog.Maps) != 2 || len(catalog.NPCs) != 2 || len(catalog.Monsters) != 2 || len(catalog.Skills) != 3 {
		t.Fatalf("unexpected demo content counts: maps=%d npcs=%d monsters=%d skills=%d", len(catalog.Maps), len(catalog.NPCs), len(catalog.Monsters), len(catalog.Skills))
	}
	if len(catalog.Quests["demo:quest/first-steps"].Server.Stages) != 3 {
		t.Fatal("first-steps must have 3 stages")
	}
}

func TestBrokenReferenceIsReported(t *testing.T) {
	catalog := loadDemo(t)
	catalog.NPCs["demo:npc/guide"].Server.StartNode = "missing"
	issues := catalog.Validate()
	if len(issues) == 0 {
		t.Fatal("expected broken reference issue")
	}
}
