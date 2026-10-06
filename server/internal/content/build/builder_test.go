package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"

	contentassets "github.com/taills/tbcg/server/internal/content/assets"
	"github.com/taills/tbcg/server/internal/content/schema"
)

func TestBuildProducesDeterministicSingleItemArtifacts(t *testing.T) {
	validator := buildFixtureValidator(t)
	input := validBuildInput(t)

	first, err := Build(validator, input)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	second, err := Build(validator, input)
	if err != nil {
		t.Fatalf("Build() repeat error = %v", err)
	}
	if !bytes.Equal(first.Manifest, second.Manifest) || !bytes.Equal(first.Public, second.Public) || !bytes.Equal(first.Private, second.Private) {
		t.Fatal("identical frozen input produced different artifact bytes")
	}
	var compactDefinition bytes.Buffer
	if err := json.Compact(&compactDefinition, input.Definition); err != nil {
		t.Fatalf("compact semantically equivalent Definition: %v", err)
	}
	encodedDifferently := cloneBuildInput(input)
	encodedDifferently.Definition = compactDefinition.Bytes()
	equivalent, err := Build(validator, encodedDifferently)
	if err != nil {
		t.Fatalf("Build() equivalent Definition encoding error = %v", err)
	}
	if !bytes.Equal(first.Manifest, equivalent.Manifest) || !bytes.Equal(first.Public, equivalent.Public) || !bytes.Equal(first.Private, equivalent.Private) {
		t.Fatal("semantically equivalent Definition encodings produced different artifact bytes")
	}
	var publicPackage, privatePackage contentPackage
	if err := json.Unmarshal(first.Public, &publicPackage); err != nil {
		t.Fatalf("decode public package: %v", err)
	}
	if err := json.Unmarshal(first.Private, &privatePackage); err != nil {
		t.Fatalf("decode private package: %v", err)
	}
	if publicPackage.Scope != "single_item" || privatePackage.Scope != "single_item" ||
		len(publicPackage.Items) != 1 || len(privatePackage.Items) != 1 ||
		len(publicPackage.Assets) != 1 || len(privatePackage.Assets) != 1 {
		t.Fatalf("package scope/counts public=%#v private=%#v", publicPackage, privatePackage)
	}
	if publicPackage.Items[0].ContentID != input.ContentID || privatePackage.Items[0].Revision != input.Revision ||
		publicPackage.Assets[0].AssetID != input.Asset.AssetID() || publicPackage.Assets[0].Path != input.Asset.Path() ||
		publicPackage.Assets[0].SHA256 != input.Asset.SHA256() {
		t.Fatalf("package identity or asset closure mismatch: public=%#v private=%#v", publicPackage, privatePackage)
	}
	publicAssetBytes, err := base64.StdEncoding.DecodeString(publicPackage.Assets[0].Data)
	if err != nil || !bytes.Equal(publicAssetBytes, input.Asset.Bytes()) {
		t.Fatalf("public asset bytes do not match frozen asset snapshot: err=%v", err)
	}
	privateAssetBytes, err := base64.StdEncoding.DecodeString(privatePackage.Assets[0].Data)
	if err != nil || !bytes.Equal(privateAssetBytes, input.Asset.Bytes()) {
		t.Fatalf("private asset bytes do not match frozen asset snapshot: err=%v", err)
	}
	canonicalDefinition, err := canonicalJSON(input.Definition)
	if err != nil {
		t.Fatalf("canonicalize expected Definition: %v", err)
	}
	publicDefinition, issues := validator.ProjectPublic(canonicalDefinition)
	if len(issues) != 0 || !bytes.Equal(publicPackage.Items[0].Body, publicDefinition) || !bytes.Equal(privatePackage.Items[0].Body, canonicalDefinition) {
		t.Fatalf("package bodies do not match trusted projection/private snapshot: issues=%#v", issues)
	}

	var manifest map[string]any
	if err := json.Unmarshal(first.Manifest, &manifest); err != nil {
		t.Fatalf("decode build manifest: %v", err)
	}
	if manifest["scope"] != "single_item" || manifest["game_id"] != input.GameID ||
		manifest["release_id"] != input.ReleaseID || manifest["release_version"] != float64(input.ReleaseVersion) {
		t.Fatalf("manifest identity/scope = %#v", manifest)
	}
	if manifest["schema_fingerprint"] != input.SchemaFingerprint || manifest["builder_version"] != BuilderVersion {
		t.Fatalf("manifest provenance = %#v", manifest)
	}
	capabilities, ok := manifest["capabilities"].([]any)
	if !ok || len(capabilities) != 0 {
		t.Fatalf("single-item built package claims unsupported runtime capabilities: %#v", manifest["capabilities"])
	}
	profile, ok := manifest["compatibility"].(map[string]any)
	if !ok || profile["profile_version"] != float64(input.Profile.ProfileVersion) || profile["content_schema_version"] != float64(input.Profile.ContentSchemaVersion) ||
		profile["instance_schema_version"] != float64(input.Profile.InstanceSchemaVersion) ||
		profile["ruleset_version"] != float64(input.Profile.RulesetVersion) ||
		profile["protocol_version"] != float64(input.Profile.ProtocolVersion) || profile["client_min_build"] != input.Profile.ClientMinBuild ||
		profile["asset_manifest_sha256"] != input.Profile.AssetManifestSHA256 {
		t.Fatalf("manifest compatibility profile = %#v", manifest["compatibility"])
	}
	revisions, ok := manifest["content_revisions"].([]any)
	if !ok || len(revisions) != 1 {
		t.Fatalf("manifest content revision closure = %#v", manifest["content_revisions"])
	}
	revision, ok := revisions[0].(map[string]any)
	if !ok || revision["content_id"] != input.ContentID || revision["revision"] != float64(input.Revision) || revision["body_hash"] != hex.EncodeToString(input.BodyHash) {
		t.Fatalf("manifest immutable revision = %#v", revisions[0])
	}
	manifestAssets, ok := manifest["assets"].([]any)
	if !ok || len(manifestAssets) != 1 {
		t.Fatalf("manifest asset closure = %#v", manifest["assets"])
	}
	manifestAsset, ok := manifestAssets[0].(map[string]any)
	if !ok || manifestAsset["asset_id"] != input.Asset.AssetID() || manifestAsset["path"] != input.Asset.Path() ||
		manifestAsset["sha256"] != input.Asset.SHA256() || manifestAsset["source"] != input.Asset.Source() || manifestAsset["license"] != input.Asset.License() {
		t.Fatalf("manifest asset provenance = %#v", manifestAssets[0])
	}
	if manifest["public_artifact_sha256"] != digestHex(first.Public) || manifest["private_artifact_sha256"] != digestHex(first.Private) {
		t.Fatalf("manifest artifact hashes do not match generated bytes: %#v", manifest)
	}
	if bytes.Contains(first.Manifest, []byte("created_at")) || bytes.Contains(first.Manifest, []byte("attempt")) || bytes.Contains(first.Manifest, []byte("job_id")) {
		t.Fatalf("manifest includes volatile job metadata: %s", first.Manifest)
	}
}

func TestBuildRejectsUntrustedOrMismatchedFrozenInput(t *testing.T) {
	validator := buildFixtureValidator(t)
	base := validBuildInput(t)

	tests := []struct {
		name   string
		change func(*Input)
	}{
		{name: "body hash differs from immutable Definition", change: func(input *Input) { input.BodyHash = bytes.Repeat([]byte{0xff}, sha256.Size) }},
		{name: "Game scope differs from Definition", change: func(input *Input) { input.GameID = "other-game" }},
		{name: "content ID differs from Definition", change: func(input *Input) { input.ContentID = "demo:item/other" }},
		{name: "revision differs from Definition", change: func(input *Input) { input.Revision = 2 }},
		{name: "asset ID differs from Definition", change: func(input *Input) {
			setDefinitionFields(t, input, nil, map[string]string{"icon_id": `"item/other"`})
		}},
		{name: "schema version differs from compatibility profile", change: func(input *Input) {
			setDefinitionFields(t, input, map[string]string{"schema_version": "2"}, map[string]string{"schema_version": "2"})
		}},
		{name: "trusted schema rejects invalid Definition", change: func(input *Input) {
			setDefinitionFields(t, input, nil, map[string]string{"item_type": `"unsupported"`})
		}},
		{name: "malformed Definition is rejected", change: func(input *Input) { input.Definition = []byte(`{`) }},
		{name: "release ID is a path", change: func(input *Input) { input.ReleaseID = "../escape" }},

		{name: "release version is not positive", change: func(input *Input) { input.ReleaseVersion = 0 }},
		{name: "missing verified asset snapshot", change: func(input *Input) { input.Asset = contentassets.Snapshot{} }},
		{name: "asset manifest differs from trusted Game profile", change: func(input *Input) {
			frozen := input.Asset.Frozen()
			frozen.Manifest = append(frozen.Manifest, '\n')
			snapshot, err := contentassets.NewFrozen(frozen)
			if err != nil {
				t.Fatalf("restore semantically equivalent manifest snapshot: %v", err)
			}
			input.Asset = snapshot
		}},
		{name: "profile versions are incomplete", change: func(input *Input) { input.Profile.RulesetVersion = 0 }},
		{name: "profile client build is blank", change: func(input *Input) { input.Profile.ClientMinBuild = " " }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneBuildInput(base)
			test.change(&input)
			if _, err := Build(validator, input); err == nil {
				t.Fatal("Build() accepted invalid frozen input")
			}
		})
	}
}

func TestBuildRejectsSchemaFingerprintMismatch(t *testing.T) {
	validator := buildFixtureValidator(t)
	input := validBuildInput(t)
	input.SchemaFingerprint = hex.EncodeToString(bytes.Repeat([]byte{0xff}, sha256.Size))
	if _, err := Build(validator, input); !errors.Is(err, ErrSchemaFingerprintMismatch) {
		t.Fatalf("Build() schema fingerprint error = %v, want ErrSchemaFingerprintMismatch", err)
	}
}

func buildFixtureValidator(t *testing.T) *schema.Validator {
	t.Helper()
	validator, err := schema.New(os.DirFS("../../../../contracts"))
	if err != nil {
		t.Fatalf("compile trusted content schema: %v", err)
	}
	return validator
}

func validBuildInput(t *testing.T) Input {
	t.Helper()
	definition, err := os.ReadFile("../../../../tests/fixtures/content/item.valid.json")
	if err != nil {
		t.Fatalf("read item fixture: %v", err)
	}
	definition = bytes.Replace(definition, []byte(`"icon_id": "item/potion-small"`), []byte(`"icon_id": "demo:icon/placeholder"`), 1)
	if !bytes.Contains(definition, []byte(`"icon_id": "demo:icon/placeholder"`)) {
		t.Fatal("item fixture icon identity did not update")
	}
	canonical, err := canonicalJSON(definition)
	if err != nil {
		t.Fatalf("canonicalize item fixture: %v", err)
	}
	bodyHash := sha256.Sum256(canonical)
	assetSnapshot, err := contentassets.ReadIcon(os.DirFS("../../../../games/demo/assets"), "demo:icon/placeholder")
	if err != nil {
		t.Fatalf("load trusted original placeholder asset: %v", err)
	}
	return Input{
		GameID: "demo", ContentID: "demo:item/healing-potion", Revision: 1,
		BodyHash: bodyHash[:], Definition: definition, ReleaseID: "demo-item-1", ReleaseVersion: 1,
		Profile: CompatibilityProfile{
			ProfileVersion: 1, ContentSchemaVersion: 1, InstanceSchemaVersion: 1, RulesetVersion: 1,
			ProtocolVersion: 1, ClientMinBuild: "1.0.0",
			AssetManifestSHA256: digestHex(assetSnapshot.Frozen().Manifest),
		},
		SchemaFingerprint: validatorFingerprint(t),
		Asset:             assetSnapshot,
	}
}

func validatorFingerprint(t *testing.T) string {
	t.Helper()
	validator, err := schema.New(os.DirFS("../../../../contracts"))
	if err != nil {
		t.Fatalf("compile trusted content schema: %v", err)
	}
	return validator.Fingerprint()
}

func setDefinitionFields(t *testing.T, input *Input, outerFields, publicFields map[string]string) {
	t.Helper()
	var definition map[string]json.RawMessage
	if err := json.Unmarshal(input.Definition, &definition); err != nil {
		t.Fatalf("decode test Definition: %v", err)
	}
	var public map[string]json.RawMessage
	if err := json.Unmarshal(definition["public"], &public); err != nil {
		t.Fatalf("decode test public projection: %v", err)
	}
	for field, value := range outerFields {
		definition[field] = json.RawMessage(value)
	}
	for field, value := range publicFields {
		public[field] = json.RawMessage(value)
	}
	encodedPublic, err := json.Marshal(public)
	if err != nil {
		t.Fatalf("encode test public projection: %v", err)
	}
	definition["public"] = encodedPublic
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("encode test Definition: %v", err)
	}
	canonical, err := canonicalJSON(encoded)
	if err != nil {
		t.Fatalf("canonicalize test Definition: %v", err)
	}
	hash := sha256.Sum256(canonical)
	input.Definition = encoded
	input.BodyHash = append([]byte(nil), hash[:]...)
}

func cloneBuildInput(input Input) Input {
	copyInput := input
	copyInput.BodyHash = append([]byte(nil), input.BodyHash...)
	copyInput.Definition = append([]byte(nil), input.Definition...)
	return copyInput
}

func TestCanonicalJSONRejectsMalformedAndTrailingValues(t *testing.T) {
	for _, input := range [][]byte{[]byte(`{`), []byte(`{} {}`), []byte(`{} trailing`)} {
		if _, err := canonicalJSON(input); err == nil {
			t.Errorf("canonicalJSON(%q) accepted malformed or trailing JSON", input)
		}
	}
}

func TestBuildRejectsInvalidContentIdentity(t *testing.T) {
	validator := buildFixtureValidator(t)
	base := validBuildInput(t)
	for _, test := range []struct {
		name   string
		change func(*Input)
	}{
		{name: "non canonical Game ID", change: func(input *Input) { input.GameID = "../demo" }},
		{name: "content ID with invalid character", change: func(input *Input) { input.ContentID = "demo:item/invalid id" }},
		{name: "content ID with invalid first character", change: func(input *Input) { input.ContentID = ":demo:item" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := cloneBuildInput(base)
			test.change(&input)
			if _, err := Build(validator, input); err == nil {
				t.Fatal("Build() accepted unsafe content or asset identity")
			}
		})
	}
}

func TestBuildDoesNotMutateFrozenInput(t *testing.T) {
	validator := buildFixtureValidator(t)
	input := validBuildInput(t)
	original := cloneBuildInput(input)
	if _, err := Build(validator, input); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !bytes.Equal(input.BodyHash, original.BodyHash) || !bytes.Equal(input.Definition, original.Definition) || !bytes.Equal(input.Asset.Bytes(), original.Asset.Bytes()) {
		t.Fatal("Build() mutated immutable input bytes")
	}
}
