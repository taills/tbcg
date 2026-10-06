package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

func TestReadIconProducesVerifiableFrozenSnapshot(t *testing.T) {
	icon := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>`)
	files := testAssetFiles(icon, "demo:icon/potion")

	snapshot, err := ReadIcon(files, "demo:icon/potion")
	if err != nil {
		t.Fatalf("ReadIcon() error = %v", err)
	}
	if snapshot.AssetID() != "demo:icon/potion" || snapshot.Path() != "icons/potion.svg" ||
		snapshot.Source() != "Original TBCG placeholder" || snapshot.License() != "TBCG original asset; internal demo use" {
		t.Fatalf("snapshot metadata = %#v", snapshot.Frozen())
	}
	if string(snapshot.Bytes()) != string(icon) {
		t.Fatal("snapshot did not retain the exact licensed icon bytes")
	}
	frozen := snapshot.Frozen()
	restored, err := NewFrozen(frozen)
	if err != nil {
		t.Fatalf("restore frozen asset snapshot: %v", err)
	}
	if restored.AssetID() != snapshot.AssetID() || restored.SHA256() != snapshot.SHA256() || string(restored.Bytes()) != string(snapshot.Bytes()) {
		t.Fatal("restored frozen snapshot differs from original")
	}
}

func TestReadIconRejectsInvalidTrustedManifestOrAsset(t *testing.T) {
	icon := []byte("<svg></svg>")
	hash := digest(icon)
	validRow := fmt.Sprintf(`{"asset_id":"demo:icon/potion","path":"icons/potion.svg","sha256":%q,"source":"Original","license":"Internal"}`, hash)

	tests := []struct {
		name  string
		files fstest.MapFS
	}{
		{name: "missing manifest", files: fstest.MapFS{}},
		{name: "malformed JSON", files: fstest.MapFS{"manifest.json": {Data: []byte(`{`)}}},
		{name: "unknown field", files: fstest.MapFS{"manifest.json": {Data: []byte(`{"schema_version":1,"extra":true,"assets":[]}`)}}},
		{name: "wrong manifest version", files: fstest.MapFS{"manifest.json": {Data: []byte(`{"schema_version":2,"assets":[]}`)}}},
		{name: "duplicate asset identity", files: assetFilesWithManifest(icon, `{"schema_version":1,"assets":[`+validRow+`,`+validRow+`]}`)},
		{name: "unlicensed row", files: assetFilesWithManifest(icon, `{"schema_version":1,"assets":[{"asset_id":"demo:icon/potion","path":"icons/potion.svg","sha256":`+mustJSONString(t, hash)+`,"source":"","license":"Internal"}]}`)},
		{name: "path traversal", files: assetFilesWithManifest(icon, `{"schema_version":1,"assets":[{"asset_id":"demo:icon/potion","path":"../secret","sha256":`+mustJSONString(t, hash)+`,"source":"Original","license":"Internal"}]}`)},
		{name: "absolute path", files: assetFilesWithManifest(icon, strings.Replace(`{"schema_version":1,"assets":[`+validRow+`]}`, "icons/potion.svg", "/tmp/potion.svg", 1))},
		{name: "windows path", files: assetFilesWithManifest(icon, strings.Replace(`{"schema_version":1,"assets":[`+validRow+`]}`, "icons/potion.svg", `icons\\potion.svg`, 1))},
		{name: "hash mismatch", files: assetFilesWithManifest(icon, strings.Replace(`{"schema_version":1,"assets":[`+validRow+`]}`, hash, strings.Repeat("a", 64), 1))},
		{name: "missing referenced file", files: fstest.MapFS{"manifest.json": {Data: []byte(`{"schema_version":1,"assets":[` + validRow + `]}`)}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ReadIcon(test.files, "demo:icon/potion"); err == nil {
				t.Fatal("ReadIcon() accepted invalid or untrusted asset input")
			}
		})
	}
}

func TestNewFrozenRejectsTamperedAssetBytesAndManifest(t *testing.T) {
	icon := []byte("<svg></svg>")
	snapshot, err := ReadIcon(testAssetFiles(icon, "demo:icon/potion"), "demo:icon/potion")
	if err != nil {
		t.Fatalf("ReadIcon() error = %v", err)
	}

	tamperedBytes := snapshot.Frozen()
	tamperedBytes.Data[0] ^= 0xff
	if _, err := NewFrozen(tamperedBytes); err == nil {
		t.Fatal("NewFrozen() accepted bytes that differ from the licensed asset hash")
	}

	tamperedManifest := snapshot.Frozen()
	tamperedManifest.Manifest = append(append([]byte(nil), tamperedManifest.Manifest...), []byte(` {}`)...)
	if _, err := NewFrozen(tamperedManifest); err == nil {
		t.Fatal("NewFrozen() accepted trailing manifest JSON")
	}
}

func testAssetFiles(icon []byte, assetID string) fstest.MapFS {
	return assetFilesWithManifest(icon, `{"schema_version":1,"assets":[{"asset_id":`+mustJSONString(nil, assetID)+`,"path":"icons/potion.svg","sha256":`+mustJSONString(nil, digest(icon))+`,"source":"Original TBCG placeholder","license":"TBCG original asset; internal demo use"}]}`)
}

func assetFilesWithManifest(icon []byte, manifest string) fstest.MapFS {
	return fstest.MapFS{
		"manifest.json":    {Data: []byte(manifest)},
		"icons/potion.svg": {Data: append([]byte(nil), icon...)},
	}
}

func mustJSONString(t *testing.T, value string) string {
	if t != nil {
		t.Helper()
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		if t != nil {
			t.Fatalf("encode test string: %v", err)
		}
		panic(err)
	}
	return string(encoded)
}

func digest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
