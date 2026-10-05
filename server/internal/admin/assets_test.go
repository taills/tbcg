package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"testing/fstest"
)

func TestValidateIconReferenceRequiresManifestHashSourceAndLicense(t *testing.T) {
	icon := []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>")
	digest := sha256.Sum256(icon)
	hash := hex.EncodeToString(digest[:])
	manifest := fmt.Sprintf(`{"schema_version":1,"assets":[{"asset_id":"demo:icon/placeholder","path":"icons/placeholder.svg","sha256":"%s","source":"Original TBCG placeholder","license":"CC0-1.0"}]}`, hash)
	assets := fstest.MapFS{
		"manifest.json":         {Data: []byte(manifest)},
		"icons/placeholder.svg": {Data: icon},
	}
	if issues := validateIconReference("demo:icon/placeholder", assets); len(issues) != 0 {
		t.Fatalf("valid licensed icon issues = %#v", issues)
	}
	if issues := validateIconReference("demo:icon/missing", assets); len(issues) != 1 || issues[0].Pointer != "/public/icon_id" {
		t.Fatalf("missing icon issues = %#v, want /public/icon_id", issues)
	}
}

func TestDemoPlaceholderIconHasVerifiedManifestClosure(t *testing.T) {
	assets := os.DirFS("../../../games/demo/assets")
	if issues := validateIconReference("demo:icon/placeholder", assets); len(issues) != 0 {
		t.Fatalf("repository placeholder icon issues = %#v", issues)
	}
}

func TestValidateIconReferenceRejectsUnlicensedOrChangedAsset(t *testing.T) {
	assets := fstest.MapFS{
		"manifest.json":         {Data: []byte(`{"schema_version":1,"assets":[{"asset_id":"demo:icon/placeholder","path":"icons/placeholder.svg","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","source":"","license":""}]}`)},
		"icons/placeholder.svg": {Data: []byte("not the declared asset")},
	}
	issues := validateIconReference("demo:icon/placeholder", assets)
	if len(issues) != 1 || issues[0].Pointer != "/public/icon_id" || issues[0].Code != "invalid_reference" {
		t.Fatalf("untrusted icon issues = %#v, want invalid_reference at /public/icon_id", issues)
	}
}
