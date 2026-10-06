package battle

import (
	"os"
	"reflect"
	"testing"

	"github.com/taills/tbcg/server/internal/content/catalog"
	"github.com/taills/tbcg/server/internal/content/mapdata"
	"github.com/taills/tbcg/server/internal/content/schema"
)

func demoCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	contracts := os.DirFS("../../../contracts")
	registry, err := schema.NewRegistry(contracts)
	if err != nil {
		t.Fatal(err)
	}
	geometry, err := mapdata.NewValidator(contracts)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := catalog.ReadDir(os.DirFS("../../../games/demo/content"))
	if err != nil {
		t.Fatal(err)
	}
	content, issues := catalog.Load("demo", entries, registry, geometry)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	return content
}

func hero() Combatant {
	return Combatant{CharacterID: "hero", Name: "hero", Level: 3, Stats: catalog.Stats{MaxHP: 200, MaxMP: 40, Attack: 30, Defense: 10, Speed: 12},
		SkillIDs: []string{"demo:skill/strike"}, Inventory: map[string]int{"demo:item/healing-potion": 1}}
}

func play(t *testing.T, content *catalog.Catalog, seed uint64) (State, []Event) {
	state, err := NewState(content, "b1", "demo:encounter/slime-pack", seed, []Combatant{hero()})
	if err != nil {
		t.Fatal(err)
	}
	var all []Event
	for state.Outcome == "" {
		before := state.Clone()
		next, events, err := Resolve(content, state, map[string]Command{"c:hero": {ActorID: "c:hero", Kind: "attack", TargetID: "m:1"}})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, state) {
			t.Fatal("Resolve mutated the previous snapshot")
		}
		state = next
		all = append(all, events...)
	}
	return state, all
}

func TestSameSeedReplaysIdentically(t *testing.T) {
	content := demoCatalog(t)
	first, firstEvents := play(t, content, 42)
	second, secondEvents := play(t, content, 42)
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(firstEvents, secondEvents) {
		t.Fatal("same seed produced different results")
	}
	if first.Outcome != OutcomeVictory {
		t.Fatalf("outcome = %s", first.Outcome)
	}
	for _, event := range firstEvents {
		if event.Kind == "damage" && event.HPBefore-event.HPAfter != event.Damage {
			t.Fatalf("damage event inconsistent: %+v", event)
		}
	}
	if len(ComputeSpoils(content, first)) != 1 {
		t.Fatal("expected spoils for one participant")
	}
}

func TestInvalidCommandsAreRejected(t *testing.T) {
	content := demoCatalog(t)
	state, err := NewState(content, "b1", "demo:encounter/slime-pack", 1, []Combatant{hero()})
	if err != nil {
		t.Fatal(err)
	}
	cases := []Command{
		{ActorID: "c:hero", Kind: "skill", SkillID: "demo:skill/whirlwind"},
		{ActorID: "c:hero", Kind: "attack", TargetID: "c:hero"},
		{ActorID: "m:1", Kind: "attack", TargetID: "c:hero"},
		{ActorID: "c:hero", Kind: "item", ItemID: "demo:item/herb", TargetID: "c:hero"},
	}
	for _, command := range cases {
		if ValidateCommand(content, state, "hero", command) == nil {
			t.Fatalf("command accepted: %+v", command)
		}
	}
	if ValidateCommand(content, state, "intruder", Command{ActorID: "c:hero", Kind: "defend"}) == nil {
		t.Fatal("foreign submitter accepted")
	}
}
