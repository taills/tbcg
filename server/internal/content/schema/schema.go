// Package schema 使用仓库内受信任的 JSON Schema 校验物品内容。
package schema

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

const (
	definitionSchemaPath = "content/item.schema.json"
	publicSchemaPath     = "content/item-public.schema.json"
	definitionSchemaID   = "urn:tbcg:content:item:v1"
	publicSchemaID       = "urn:tbcg:content:item-public:v1"

	CodeInvalidJSON     = "invalid_json"
	CodeInvalidValue    = "invalid_value"
	CodeMissingField    = "missing_field"
	CodeUnknownField    = "unknown_field"
	CodeSchemaViolation = "schema_violation"
)

// Issue 表示带有稳定错误码和 JSON Pointer 路径的校验错误。
type Issue struct {
	Path string `json:"path"`
	Code string `json:"code"`
}

// Validator 校验私有物品定义及公开物品投影。
type Validator struct {
	definition  *jsonschema.Schema
	public      *jsonschema.Schema
	fingerprint string
}

// New 编译调用方文件系统中提供的受信任物品 Schema。
// 编译过程仅使用已加载的 Schema 资源和本地 URN 引用。
func New(schemaFS fs.FS) (*Validator, error) {
	if schemaFS == nil {
		return nil, errors.New("schema filesystem is nil")
	}
	definitionBytes, definitionDoc, err := readSchema(schemaFS, definitionSchemaPath, definitionSchemaID)
	if err != nil {
		return nil, err
	}
	publicBytes, publicDoc, err := readSchema(schemaFS, publicSchemaPath, publicSchemaID)
	if err != nil {
		return nil, err
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(denyLoader{})
	if err := compiler.AddResource(publicSchemaID, publicDoc); err != nil {
		return nil, fmt.Errorf("register public item schema: %w", err)
	}
	if err := compiler.AddResource(definitionSchemaID, definitionDoc); err != nil {
		return nil, fmt.Errorf("register item definition schema: %w", err)
	}
	public, err := compiler.Compile(publicSchemaID)
	if err != nil {
		return nil, fmt.Errorf("compile public item schema: %w", err)
	}
	definition, err := compiler.Compile(definitionSchemaID)
	if err != nil {
		return nil, fmt.Errorf("compile item definition schema: %w", err)
	}
	return &Validator{
		definition:  definition,
		public:      public,
		fingerprint: fingerprintSchemas(definitionBytes, publicBytes),
	}, nil
}

// Fingerprint 返回参与编译的可信 Schema 原始字节的稳定 SHA-256。
func (v *Validator) Fingerprint() string {
	if v == nil {
		return ""
	}
	return v.fingerprint
}

// ValidateDefinition 校验完整的私有物品定义及其公开身份字段。
func (v *Validator) ValidateDefinition(data []byte) []Issue {
	if issues := validate(v.definition, data); len(issues) != 0 {
		return issues
	}
	return validateDefinitionIdentity(data)
}

// ValidatePublic 校验独立的公开物品投影。
func (v *Validator) ValidatePublic(data []byte) []Issue {
	return validate(v.public, data)
}

func validateDefinitionIdentity(data []byte) []Issue {
	var definition struct {
		GameID        string  `json:"game_id"`
		ContentID     string  `json:"content_id"`
		Revision      float64 `json:"revision"`
		SchemaVersion float64 `json:"schema_version"`
		Public        struct {
			GameID        string  `json:"game_id"`
			ContentID     string  `json:"content_id"`
			Revision      float64 `json:"revision"`
			SchemaVersion float64 `json:"schema_version"`
		} `json:"public"`
	}
	if err := json.Unmarshal(data, &definition); err != nil {
		return []Issue{{Path: "", Code: CodeInvalidJSON}}
	}

	fields := []struct {
		name  string
		equal bool
	}{
		{name: "game_id", equal: definition.GameID == definition.Public.GameID},
		{name: "content_id", equal: definition.ContentID == definition.Public.ContentID},
		{name: "revision", equal: definition.Revision == definition.Public.Revision},
		{name: "schema_version", equal: definition.SchemaVersion == definition.Public.SchemaVersion},
	}
	issues := make([]Issue, 0, len(fields))
	for _, field := range fields {
		if !field.equal {
			issues = append(issues, Issue{Path: "/public/" + field.name, Code: CodeInvalidValue})
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Path == issues[j].Path {
			return issues[i].Code < issues[j].Code
		}
		return issues[i].Path < issues[j].Path
	})
	return issues
}

// ProjectPublic 校验完整定义，并仅返回通过校验的公开投影副本。
func (v *Validator) ProjectPublic(data []byte) ([]byte, []Issue) {
	if issues := v.ValidateDefinition(data); len(issues) != 0 {
		return nil, issues
	}
	var definition map[string]json.RawMessage
	if err := json.Unmarshal(data, &definition); err != nil {
		return nil, []Issue{{Path: "", Code: CodeInvalidJSON}}
	}
	projection, ok := definition["public"]
	if !ok {
		return nil, []Issue{{Path: "/public", Code: CodeMissingField}}
	}
	if issues := v.ValidatePublic(projection); len(issues) != 0 {
		return nil, issues
	}
	return append([]byte(nil), projection...), nil
}

func readSchema(schemaFS fs.FS, path, expectedID string) ([]byte, any, error) {
	file, err := schemaFS.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open trusted schema %q: %w", path, err)
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, nil, fmt.Errorf("read trusted schema %q: %w", path, readErr)
	}
	if closeErr != nil {
		return nil, nil, fmt.Errorf("close trusted schema %q: %w", path, closeErr)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("decode trusted schema %q: %w", path, err)
	}
	object, ok := doc.(map[string]any)
	if !ok || object["$id"] != expectedID {
		return nil, nil, fmt.Errorf("trusted schema %q must declare $id %q", path, expectedID)
	}
	return data, doc, nil
}

func fingerprintSchemas(definition, public []byte) string {
	hash := sha256.New()
	for _, schemaBytes := range [][]byte{definition, public} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(schemaBytes)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(schemaBytes)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validate(compiled *jsonschema.Schema, data []byte) []Issue {
	if compiled == nil {
		return []Issue{{Path: "", Code: CodeSchemaViolation}}
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return []Issue{{Path: "", Code: CodeInvalidJSON}}
	}
	if err := compiled.Validate(instance); err != nil {
		validationErr, ok := errors.AsType[*jsonschema.ValidationError](err)
		if !ok {
			return []Issue{{Path: "", Code: CodeSchemaViolation}}
		}
		issues := make([]Issue, 0)
		collectIssues(validationErr, &issues)
		sort.Slice(issues, func(i, j int) bool {
			if issues[i].Path == issues[j].Path {
				return issues[i].Code < issues[j].Code
			}
			return issues[i].Path < issues[j].Path
		})
		return deduplicate(issues)
	}
	return nil
}

func collectIssues(validationErr *jsonschema.ValidationError, issues *[]Issue) {
	if len(validationErr.Causes) != 0 {
		for _, cause := range validationErr.Causes {
			collectIssues(cause, issues)
		}
		return
	}
	path := append([]string(nil), validationErr.InstanceLocation...)
	code := CodeInvalidValue
	switch errKind := validationErr.ErrorKind.(type) {
	case *kind.Required:
		code = CodeMissingField
		for _, missing := range errKind.Missing {
			missingPath := append(append([]string(nil), path...), missing)
			*issues = append(*issues, Issue{Path: jsonPointer(missingPath), Code: code})
		}
		return
	case *kind.AdditionalProperties:
		code = CodeUnknownField
		for _, unknown := range errKind.Properties {
			unknownPath := append(append([]string(nil), path...), unknown)
			*issues = append(*issues, Issue{Path: jsonPointer(unknownPath), Code: code})
		}
		return
	case *kind.FalseSchema:
		code = CodeUnknownField
	}
	*issues = append(*issues, Issue{Path: jsonPointer(path), Code: code})
}

func jsonPointer(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	for i, part := range parts {
		part = strings.ReplaceAll(part, "~", "~0")
		parts[i] = strings.ReplaceAll(part, "/", "~1")
	}
	return "/" + strings.Join(parts, "/")
}

func deduplicate(issues []Issue) []Issue {
	if len(issues) < 2 {
		return issues
	}
	unique := issues[:1]
	for _, issue := range issues[1:] {
		if issue != unique[len(unique)-1] {
			unique = append(unique, issue)
		}
	}
	return unique
}

type denyLoader struct{}

func (denyLoader) Load(location string) (any, error) {
	return nil, fmt.Errorf("external schema reference %q is not permitted", location)
}
