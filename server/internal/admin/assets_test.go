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

func TestAssetsForGameUsesOnlyThatGameSubtree(t *testing.T) {
	firstIcon := []byte("<svg>first</svg>")
	secondIcon := []byte("<svg>second</svg>")
	firstHash := sha256.Sum256(firstIcon)
	secondHash := sha256.Sum256(secondIcon)
	root := fstest.MapFS{
		"demo/assets/manifest.json":     {Data: fmt.Appendf(nil, `{"schema_version":1,"assets":[{"asset_id":"demo:icon/potion","path":"icons/potion.svg","sha256":"%s","source":"demo source","license":"demo license"}]}`, hex.EncodeToString(firstHash[:]))},
		"demo/assets/icons/potion.svg":  {Data: firstIcon},
		"other/assets/manifest.json":    {Data: fmt.Appendf(nil, `{"schema_version":1,"assets":[{"asset_id":"other:icon/potion","path":"icons/potion.svg","sha256":"%s","source":"other source","license":"other license"}]}`, hex.EncodeToString(secondHash[:]))},
		"other/assets/icons/potion.svg": {Data: secondIcon},
	}
	for _, test := range []struct{ gameID, iconID string }{{gameID: "demo", iconID: "demo:icon/potion"}, {gameID: "other", iconID: "other:icon/potion"}} {
		gameAssets := assetsForGame(root, test.gameID)
		if gameAssets == nil || len(validateIconReference(test.iconID, gameAssets)) != 0 {
			t.Errorf("Game %q could not validate its own manifest asset", test.gameID)
		}
		wrongIcon := "demo:icon/potion"
		if test.gameID == "demo" {
			wrongIcon = "other:icon/potion"
		}
		if len(validateIconReference(wrongIcon, gameAssets)) == 0 {
			t.Errorf("Game %q accepted another Game's icon", test.gameID)
		}
	}
	if assetsForGame(root, "../demo") != nil || assetsForGame(root, ".") != nil {
		t.Fatal("assetsForGame accepted a path-traversing or dot Game id")
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
