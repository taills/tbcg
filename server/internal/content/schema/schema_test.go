package schema

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSchemaValidationAndProjection(t *testing.T) {
	validator := newFixtureValidator(t)

	tests := []struct {
		name      string
		fixture   string
		wantValid bool
		wantPath  string
		wantCode  string
	}{
		{name: "valid definition", fixture: "item.valid.json", wantValid: true},
		{name: "large money is a string", fixture: "item.valid.json", wantValid: true},
		{name: "invalid enum", fixture: "item.invalid-enum.json", wantPath: "/public/item_type", wantCode: CodeInvalidValue},
		{name: "null optional field", fixture: "item.invalid-null-optional.json", wantPath: "/public/description", wantCode: CodeInvalidValue},
		{name: "valid public projection", fixture: "item.public.valid.json", wantValid: true},
		{name: "large money as number", fixture: "item.public.invalid-bigint-number.json", wantPath: "/price_minor", wantCode: CodeInvalidValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := readFixture(t, tt.fixture)
			if strings.HasPrefix(tt.fixture, "item.public.") {
				issues := validator.ValidatePublic(data)
				assertIssues(t, issues, tt.wantValid, tt.wantPath, tt.wantCode)
				return
			}
			issues := validator.ValidateDefinition(data)
			assertIssues(t, issues, tt.wantValid, tt.wantPath, tt.wantCode)
		})
	}
}

func TestDefinitionIdentityMatchesPublic(t *testing.T) {
	validator := newFixtureValidator(t)
	fixture := readFixture(t, "item.valid.json")

	tests := []struct {
		name     string
		field    string
		oldValue string
		newValue string
		wantPath string
	}{
		{name: "game id", field: "game_id", oldValue: `"demo"`, newValue: `"other"`, wantPath: "/public/game_id"},
		{name: "content id", field: "content_id", oldValue: `"demo:item/healing-potion"`, newValue: `"demo:item/other"`, wantPath: "/public/content_id"},
		{name: "revision", field: "revision", oldValue: "1", newValue: "2", wantPath: "/public/revision"},
		{name: "schema version", field: "schema_version", oldValue: "1", newValue: "2", wantPath: "/public/schema_version"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modified := replacePublicField(t, fixture, tt.field, tt.oldValue, tt.newValue)
			issues := validator.ValidateDefinition(modified)
			assertIssues(t, issues, false, tt.wantPath, CodeInvalidValue)
		})
	}

	modified := replacePublicField(t, fixture, "game_id", `"demo"`, `"other"`)
	modified = replacePublicField(t, modified, "content_id", `"demo:item/healing-potion"`, `"demo:item/other"`)
	modified = replacePublicField(t, modified, "revision", "1", "2")
	want := []Issue{
		{Path: "/public/content_id", Code: CodeInvalidValue},
		{Path: "/public/game_id", Code: CodeInvalidValue},
		{Path: "/public/revision", Code: CodeInvalidValue},
	}
	got := validator.ValidateDefinition(modified)
	if len(got) != len(want) {
		t.Fatalf("identity issues = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("identity issues = %#v, want stable order %#v", got, want)
		}
	}
}

func replacePublicField(t *testing.T, definition []byte, field, oldValue, newValue string) []byte {
	t.Helper()
	publicStart := strings.Index(string(definition), `"public": {`)
	if publicStart < 0 {
		t.Fatal("Definition has no public object")
	}
	public := string(definition[publicStart:])
	oldField := `"` + field + `": ` + oldValue
	newField := `"` + field + `": ` + newValue
	updated := strings.Replace(public, oldField, newField, 1)
	if updated == public {
		t.Fatalf("public field %q with value %s not found", field, oldValue)
	}
	return []byte(string(definition[:publicStart]) + updated)
}

func TestProjectPublicExcludesServerFields(t *testing.T) {
	validator := newFixtureValidator(t)
	definition := readFixture(t, "item.valid.json")
	originalDefinition := string(definition)
	projected, issues := validator.ProjectPublic(definition)
	if len(issues) != 0 {
		t.Fatalf("ProjectPublic() issues = %#v, want none", issues)
	}
	if strings.Contains(string(projected), "server") || strings.Contains(string(projected), "reward_value_minor") {
		t.Fatalf("public projection leaked server data: %s", projected)
	}
	if issues := validator.ValidatePublic(projected); len(issues) != 0 {
		t.Fatalf("projected public document is invalid: %#v", issues)
	}
	invalidDefinition := strings.Replace(string(definition), `"server": {`, `"unknown": true, "server": {`, 1)
	if _, issues := validator.ProjectPublic([]byte(invalidDefinition)); len(issues) == 0 {
		t.Fatal("ProjectPublic() accepted an invalid Definition")
	}
	projected[0] = ' '
	if string(definition) != originalDefinition {
		t.Fatal("ProjectPublic() changed the input Definition")
	}
}

func TestSchemaCompilerRejectsRemoteReferences(t *testing.T) {
	remoteRefSchema := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"urn:tbcg:content:item:v1","$ref":"https://example.invalid/schema.json"}`
	_, err := New(fstest.MapFS{
		"content/item.schema.json":        &fstest.MapFile{Data: []byte(remoteRefSchema)},
		"content/item-public.schema.json": &fstest.MapFile{Data: []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"urn:tbcg:content:item-public:v1","type":"object"}`)},
	})
	if err == nil {
		t.Fatal("New() accepted a remote $ref, want error")
	}
}

func TestNewRejectsInvalidSchemaInputs(t *testing.T) {
	validDefinition := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"urn:tbcg:content:item:v1","type":"object"}`
	validPublic := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"urn:tbcg:content:item-public:v1","type":"object"}`

	tests := []struct {
		name string
		fs   fstest.MapFS
	}{
		{name: "missing definition schema", fs: fstest.MapFS{}},
		{
			name: "malformed definition schema",
			fs: fstest.MapFS{
				"content/item.schema.json":        &fstest.MapFile{Data: []byte(`{"$id":`)},
				"content/item-public.schema.json": &fstest.MapFile{Data: []byte(validPublic)},
			},
		},
		{
			name: "wrong definition schema id",
			fs: fstest.MapFS{
				"content/item.schema.json":        &fstest.MapFile{Data: []byte(strings.Replace(validDefinition, definitionSchemaID, "urn:wrong", 1))},
				"content/item-public.schema.json": &fstest.MapFile{Data: []byte(validPublic)},
			},
		},
		{
			name: "remote public reference",
			fs: fstest.MapFS{
				"content/item.schema.json": &fstest.MapFile{Data: []byte(validDefinition)},
				"content/item-public.schema.json": &fstest.MapFile{Data: []byte(
					`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"urn:tbcg:content:item-public:v1","$ref":"https://example.invalid/public.json"}`,
				)},
			},
		},
	}

	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) error = nil, want error")
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.fs); err == nil {
				t.Fatal("New() error = nil, want error")
			}
		})
	}
}

func TestValidationAndIssueHelpers(t *testing.T) {
	if issues := validate(nil, []byte(`{}`)); len(issues) != 1 || issues[0] != (Issue{Path: "", Code: CodeSchemaViolation}) {
		t.Fatalf("validate(nil) = %#v, want schema violation", issues)
	}
	if got := jsonPointer([]string{"a~b", "c/d"}); got != "/a~0b/c~1d" {
		t.Fatalf("jsonPointer() = %q, want escaped path", got)
	}
	if got := deduplicate([]Issue{
		{Path: "/field", Code: CodeInvalidValue},
		{Path: "/field", Code: CodeInvalidValue},
		{Path: "/other", Code: CodeUnknownField},
	}); len(got) != 2 || got[0].Path != "/field" || got[1].Path != "/other" {
		t.Fatalf("deduplicate() = %#v, want unique issues", got)
	}
}

func TestFingerprintTracksExactTrustedSchemaBytes(t *testing.T) {
	definitionSchema, err := os.ReadFile("../../../../contracts/content/item.schema.json")
	if err != nil {
		t.Fatalf("read definition schema: %v", err)
	}
	publicSchema, err := os.ReadFile("../../../../contracts/content/item-public.schema.json")
	if err != nil {
		t.Fatalf("read public schema: %v", err)
	}
	base := fstest.MapFS{
		"content/item.schema.json":        &fstest.MapFile{Data: definitionSchema},
		"content/item-public.schema.json": &fstest.MapFile{Data: publicSchema},
	}
	first, err := New(base)
	if err != nil {
		t.Fatalf("compile initial schema: %v", err)
	}
	second, err := New(base)
	if err != nil {
		t.Fatalf("compile same schema: %v", err)
	}
	if first.Fingerprint() != second.Fingerprint() {
		t.Fatal("identical trusted schema bytes produced different fingerprints")
	}
	if len(first.Fingerprint()) != 64 {
		t.Fatalf("schema fingerprint length = %d, want 64 hex characters", len(first.Fingerprint()))
	}
	for _, value := range first.Fingerprint() {
		if !(value >= '0' && value <= '9' || value >= 'a' && value <= 'f') {
			t.Fatalf("schema fingerprint contains non-lowercase-hex character %q", value)
		}
	}

	changed := fstest.MapFS{
		"content/item.schema.json":        &fstest.MapFile{Data: append(append([]byte(nil), definitionSchema...), '\n')},
		"content/item-public.schema.json": &fstest.MapFile{Data: publicSchema},
	}
	third, err := New(changed)
	if err != nil {
		t.Fatalf("compile schema with changed source bytes: %v", err)
	}
	if first.Fingerprint() == third.Fingerprint() {
		t.Fatal("changed trusted schema bytes retained the same fingerprint")
	}
}

func newFixtureValidator(t *testing.T) *Validator {
	t.Helper()
	schemaFS := os.DirFS("../../../../contracts")
	validator, err := New(schemaFS)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return validator
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../../tests/fixtures/content/" + name)
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return data
}

func assertIssues(t *testing.T, issues []Issue, wantValid bool, wantPath, wantCode string) {
	t.Helper()
	if wantValid {
		if len(issues) != 0 {
			t.Fatalf("validation issues = %#v, want none", issues)
		}
		return
	}
	if len(issues) == 0 {
		t.Fatal("validation issues = none, want validation error")
	}
	for _, issue := range issues {
		if issue.Path == wantPath && issue.Code == wantCode {
			return
		}
	}
	t.Fatalf("validation issues = %#v, want path %q code %q", issues, wantPath, wantCode)
}

func TestValidationReportsStableFieldIssues(t *testing.T) {
	validator := newFixtureValidator(t)
	base := string(readFixture(t, "item.valid.json"))

	tests := []struct {
		name     string
		data     string
		wantPath string
		wantCode string
	}{
		{
			name:     "missing property",
			data:     strings.Replace(base, "    \"stack_limit\": 20,\n", "", 1),
			wantPath: "/public/stack_limit",
			wantCode: CodeMissingField,
		},
		{
			name:     "unknown property uses escaped JSON Pointer",
			data:     strings.Replace(base, `    "stack_limit": 20,`, "    \"stack_limit\": 20,\n    \"unknown/field\": true,", 1),
			wantPath: "/public/unknown~1field",
			wantCode: CodeUnknownField,
		},
		{
			name:     "malformed JSON",
			data:     `{`,
			wantPath: "",
			wantCode: CodeInvalidJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertIssues(t, validator.ValidateDefinition([]byte(tt.data)), false, tt.wantPath, tt.wantCode)
		})
	}
}

func TestDefinitionQuantityBoundary(t *testing.T) {
	validator := newFixtureValidator(t)
	data := readFixture(t, "item.valid.json")
	var definition map[string]any
	if err := json.Unmarshal(data, &definition); err != nil {
		t.Fatal(err)
	}
	public := definition["public"].(map[string]any)
	public["stack_limit"] = json.Number("999")
	data, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	if issues := validator.ValidateDefinition(data); len(issues) != 0 {
		t.Fatalf("stack_limit 999 issues = %#v, want none", issues)
	}
	public["stack_limit"] = json.Number("1000")
	data, err = json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	assertIssues(t, validator.ValidateDefinition(data), false, "/public/stack_limit", CodeInvalidValue)
}
