package importer

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/taills/tbcg/server/internal/content/schema"
)

func run(t *testing.T, options Options) (Report, Bundle) {
	t.Helper()
	registry, err := schema.NewRegistry(os.DirFS("../../../../contracts"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../../tests/fixtures/xyq-import/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	report, bundle, err := Run(os.DirFS("../../../../tests/fixtures/xyq-import"), manifest, registry, options)
	if err != nil {
		t.Fatal(err)
	}
	return report, bundle
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestImportIsDeterministicAndReportsSemantics(t *testing.T) {
	first, bundle := run(t, Options{})
	second, _ := run(t, Options{})
	if first.OutputSHA256 != second.OutputSHA256 {
		t.Fatal("same input produced different output hashes")
	}
	if len(first.Quarantined) != 1 {
		t.Fatalf("unlicensed source must be quarantined: %v", first.Quarantined)
	}
	if !contains(first.Blocked, "demo:item/xyq-1002") || !contains(first.Blocked, "demo:skill/xyq-3002") || !contains(first.Blocked, "demo:monster/xyq-2002") {
		t.Fatalf("script-bearing or broken records must be blocked: %v", first.Blocked)
	}
	if !contains(first.NeedsReview, "demo:item/xyq-1001") || !contains(first.NeedsReview, "demo:monster/xyq-2001") {
		t.Fatalf("approximate records need confirmation: %v", first.NeedsReview)
	}
	if len(first.Missing) == 0 {
		t.Fatal("missing dependency must be reported")
	}
	for _, definition := range bundle.Definitions {
		var identity struct {
			ContentID string `json:"content_id"`
		}
		_ = json.Unmarshal(definition, &identity)
		if contains(first.Blocked, identity.ContentID) || contains(first.NeedsReview, identity.ContentID) {
			t.Fatalf("bundle contains non-ready record %s", identity.ContentID)
		}
	}
}

func TestApproximateRecordsRequireExplicitAcceptance(t *testing.T) {
	report, bundle := run(t, Options{AcceptApproximate: map[string]bool{"demo:item/xyq-1001": true}})
	if !contains(report.Ready, "demo:item/xyq-1001") || len(bundle.Definitions) != len(report.Ready) {
		t.Fatalf("accepted approximate record must be ready: %v", report.Ready)
	}
}
