package schema

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	commonSchemaPath   = "content/types/common.schema.json"
	commonSchemaID     = "urn:tbcg:content:common:v1"
	geometrySchemaPath = "content/map_geometry.schema.json"
	geometrySchemaID   = "urn:tbcg:content:map-geometry:v1"

	// ContentTypeItem 是 0008 第一纵切片的物品类型，沿用独立公开 Schema。
	ContentTypeItem = "item"
)

// ContentTypes 列出受支持的全部内容类型；顺序即构建与展示顺序。
var ContentTypes = []string{
	"item", "map", "archetype", "sect", "skill", "monster", "encounter", "npc", "quest",
	"equipment", "consumable", "dungeon", "pet", "partner", "mount", "recipe", "shop",
	"social", "guild", "trade", "event",
}

// ErrUnknownContentType 表示内容 ID 指向未登记的类型。
var ErrUnknownContentType = errors.New("unknown content type")

// Registry 持有全部受信任内容类型的已编译 Schema。
type Registry struct {
	validators  map[string]*Validator
	fingerprint string
}

// NewRegistry 编译仓库内所有内容类型 Schema；不加载任何外部引用。
func NewRegistry(schemaFS fs.FS) (*Registry, error) {
	if schemaFS == nil {
		return nil, errors.New("schema filesystem is nil")
	}
	item, err := New(schemaFS)
	if err != nil {
		return nil, err
	}
	commonBytes, commonDoc, err := readSchema(schemaFS, commonSchemaPath, commonSchemaID)
	if err != nil {
		return nil, err
	}
	geometryBytes, geometryDoc, err := readSchema(schemaFS, geometrySchemaPath, geometrySchemaID)
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(denyLoader{})
	if err := compiler.AddResource(commonSchemaID, commonDoc); err != nil {
		return nil, fmt.Errorf("register common content schema: %w", err)
	}
	if err := compiler.AddResource(geometrySchemaID, geometryDoc); err != nil {
		return nil, fmt.Errorf("register map geometry schema: %w", err)
	}
	raw := map[string][]byte{commonSchemaID: commonBytes, geometrySchemaID: geometryBytes}
	validators := map[string]*Validator{ContentTypeItem: item}
	for _, contentType := range ContentTypes[1:] {
		id := "urn:tbcg:content:" + contentType + ":v1"
		data, doc, err := readSchema(schemaFS, "content/types/"+contentType+".schema.json", id)
		if err != nil {
			return nil, err
		}
		if err := compiler.AddResource(id, doc); err != nil {
			return nil, fmt.Errorf("register %s schema: %w", contentType, err)
		}
		raw[id] = data
	}
	for _, contentType := range ContentTypes[1:] {
		id := "urn:tbcg:content:" + contentType + ":v1"
		definition, err := compiler.Compile(id)
		if err != nil {
			return nil, fmt.Errorf("compile %s schema: %w", contentType, err)
		}
		public, err := compiler.Compile(id + "#/$defs/public")
		if err != nil {
			return nil, fmt.Errorf("compile %s public schema: %w", contentType, err)
		}
		validators[contentType] = &Validator{definition: definition, public: public, fingerprint: fingerprintSchemas(raw[id], raw[commonSchemaID])}
	}
	return &Registry{validators: validators, fingerprint: fingerprintAll(item.Fingerprint(), raw)}, nil
}

// For 返回指定类型的校验器。
func (r *Registry) For(contentType string) (*Validator, error) {
	if r == nil {
		return nil, ErrUnknownContentType
	}
	validator, ok := r.validators[contentType]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownContentType, contentType)
	}
	return validator, nil
}

// Item 返回 0008 物品校验器，保持旧调用方兼容。
func (r *Registry) Item() *Validator { return r.validators[ContentTypeItem] }

// Fingerprint 覆盖全部受信任 Schema 原始字节。
func (r *Registry) Fingerprint() string {
	if r == nil {
		return ""
	}
	return r.fingerprint
}

// ContentTypeOf 从 `<game>:<type>/<slug>` 形式的内容 ID 推导类型。
func ContentTypeOf(contentID string) (string, error) {
	colon := strings.IndexByte(contentID, ':')
	if colon < 1 {
		return "", fmt.Errorf("%w: content id %q lacks type segment", ErrUnknownContentType, contentID)
	}
	rest := contentID[colon+1:]
	slash := strings.IndexByte(rest, '/')
	if slash < 1 || slash == len(rest)-1 {
		return "", fmt.Errorf("%w: content id %q lacks type segment", ErrUnknownContentType, contentID)
	}
	contentType := rest[:slash]
	for _, known := range ContentTypes {
		if known == contentType {
			return contentType, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownContentType, contentType)
}

func fingerprintAll(itemFingerprint string, raw map[string][]byte) string {
	ids := make([]string, 0, len(raw))
	for id := range raw {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	hash := sha256.New()
	_, _ = hash.Write([]byte(itemFingerprint))
	for _, id := range ids {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(raw[id])))
		_, _ = hash.Write([]byte(id))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(raw[id])
	}
	return hex.EncodeToString(hash.Sum(nil))
}
