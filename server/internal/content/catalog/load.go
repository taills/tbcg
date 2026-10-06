package catalog

import (
	"encoding/json"
	"fmt"
	"sort"

	"example.com/tbcg/server/internal/content/mapdata"
	"example.com/tbcg/server/internal/content/schema"
)

// Entry 是 Release 中一个已批准的不可变 Definition。
type Entry struct {
	ContentID   string          `json:"content_id"`
	ContentType string          `json:"content_type"`
	Revision    int32           `json:"revision"`
	Definition  json.RawMessage `json:"definition"`
}

// PublicEntry 是客户端可读取的白名单投影。
type PublicEntry struct {
	ContentID   string          `json:"content_id"`
	ContentType string          `json:"content_type"`
	Revision    int32           `json:"revision"`
	Public      json.RawMessage `json:"public"`
}

// Issue 指向某个内容的稳定校验错误。
type Issue struct {
	ContentID string `json:"content_id"`
	Path      string `json:"path"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

func (i Issue) Error() string {
	return fmt.Sprintf("%s%s: %s (%s)", i.ContentID, i.Path, i.Message, i.Code)
}

// Catalog 是某个 Release 的只读运行时内容；创建后不得修改。
type Catalog struct {
	GameID         string
	ReleaseID      string
	ReleaseVersion int

	Items       map[string]*Item
	Maps        map[string]*Map
	Geometries  map[string]*mapdata.Geometry
	Archetypes  map[string]*Archetype
	Sects       map[string]*Sect
	Skills      map[string]*Skill
	Monsters    map[string]*Monster
	Encounters  map[string]*Encounter
	NPCs        map[string]*NPC
	Quests      map[string]*Quest
	Equipment   map[string]*Equipment  // 按 item_id 索引
	Consumables map[string]*Consumable // 按 item_id 索引
	Dungeons    map[string]*Dungeon
	Pets        map[string]*Pet
	Partners    map[string]*Partner
	Mounts      map[string]*Mount
	Recipes     map[string]*Recipe
	Shops       map[string]*Shop
	Events      map[string]*Event
	Social      *Social
	Guild       *Guild
	Trade       *Trade

	entries []Entry
}

func newCatalog() *Catalog {
	return &Catalog{
		Items: map[string]*Item{}, Maps: map[string]*Map{}, Geometries: map[string]*mapdata.Geometry{},
		Archetypes: map[string]*Archetype{}, Sects: map[string]*Sect{}, Skills: map[string]*Skill{},
		Monsters: map[string]*Monster{}, Encounters: map[string]*Encounter{}, NPCs: map[string]*NPC{},
		Quests: map[string]*Quest{}, Equipment: map[string]*Equipment{}, Consumables: map[string]*Consumable{},
		Dungeons: map[string]*Dungeon{}, Pets: map[string]*Pet{}, Partners: map[string]*Partner{},
		Mounts: map[string]*Mount{}, Recipes: map[string]*Recipe{}, Shops: map[string]*Shop{}, Events: map[string]*Event{},
	}
}

// Entries 返回按 content_id 排序的条目副本。
func (c *Catalog) Entries() []Entry { return append([]Entry(nil), c.entries...) }

// Load 先按类型 Schema 校验每个条目，再解码并做跨内容引用检查。
func Load(gameID string, entries []Entry, registry *schema.Registry, geometry *mapdata.Validator) (*Catalog, []Issue) {
	catalog := newCatalog()
	catalog.GameID = gameID
	sorted := append([]Entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ContentID < sorted[j].ContentID })
	var issues []Issue
	seen := map[string]bool{}
	for _, entry := range sorted {
		if seen[entry.ContentID] {
			issues = append(issues, Issue{ContentID: entry.ContentID, Code: "duplicate_content", Message: "同一 Release 中内容 ID 重复"})
			continue
		}
		seen[entry.ContentID] = true
		issues = append(issues, catalog.add(gameID, entry, registry, geometry)...)
	}
	catalog.entries = sorted
	if len(issues) == 0 {
		issues = catalog.Validate()
	}
	return catalog, issues
}

func (c *Catalog) add(gameID string, entry Entry, registry *schema.Registry, geometry *mapdata.Validator) []Issue {
	issue := func(path, code, message string) []Issue {
		return []Issue{{ContentID: entry.ContentID, Path: path, Code: code, Message: message}}
	}
	contentType, err := schema.ContentTypeOf(entry.ContentID)
	if err != nil || contentType != entry.ContentType {
		return issue("", "invalid_content_type", "内容 ID 与内容类型不一致")
	}
	validator, err := registry.For(contentType)
	if err != nil {
		return issue("", "invalid_content_type", "不支持的内容类型")
	}
	if schemaIssues := validator.ValidateDefinition(entry.Definition); len(schemaIssues) != 0 {
		out := make([]Issue, 0, len(schemaIssues))
		for _, schemaIssue := range schemaIssues {
			out = append(out, Issue{ContentID: entry.ContentID, Path: schemaIssue.Path, Code: schemaIssue.Code, Message: "内容不符合 Schema"})
		}
		return out
	}
	var id identity
	if err := json.Unmarshal(entry.Definition, &id); err != nil || id.GameID != gameID || id.ContentID != entry.ContentID || id.Revision != entry.Revision {
		return issue("", "identity_mismatch", "内容身份与 Release 条目不一致")
	}
	target, err := c.slot(contentType, entry.ContentID)
	if err != nil {
		return issue("", "duplicate_content", err.Error())
	}
	if err := json.Unmarshal(entry.Definition, target); err != nil {
		return issue("", "invalid_json", "内容无法解码")
	}
	if contentType == "map" {
		m := c.Maps[entry.ContentID]
		raw, err := json.Marshal(m.Public.Geometry)
		if err != nil {
			return issue("/public/geometry", "invalid_value", "地图几何无效")
		}
		decoded, err := geometry.Decode(raw)
		if err != nil {
			return issue("/public/geometry", "invalid_value", "地图几何无效："+err.Error())
		}
		if m.Public.Geometry["map_id"] != entry.ContentID {
			return issue("/public/geometry/map_id", "invalid_value", "地图几何 map_id 必须与内容 ID 一致")
		}
		c.Geometries[entry.ContentID] = decoded
	}
	return nil
}

// slot 为类型分配解码目标；单例规则类型只允许一个。
func (c *Catalog) slot(contentType, contentID string) (any, error) {
	switch contentType {
	case "item":
		v := &Item{}
		c.Items[contentID] = v
		return v, nil
	case "map":
		v := &Map{}
		c.Maps[contentID] = v
		return v, nil
	case "archetype":
		v := &Archetype{}
		c.Archetypes[contentID] = v
		return v, nil
	case "sect":
		v := &Sect{}
		c.Sects[contentID] = v
		return v, nil
	case "skill":
		v := &Skill{}
		c.Skills[contentID] = v
		return v, nil
	case "monster":
		v := &Monster{}
		c.Monsters[contentID] = v
		return v, nil
	case "encounter":
		v := &Encounter{}
		c.Encounters[contentID] = v
		return v, nil
	case "npc":
		v := &NPC{}
		c.NPCs[contentID] = v
		return v, nil
	case "quest":
		v := &Quest{}
		c.Quests[contentID] = v
		return v, nil
	case "equipment":
		return &equipmentSlot{catalog: c, id: contentID}, nil
	case "consumable":
		return &consumableSlot{catalog: c, id: contentID}, nil
	case "dungeon":
		v := &Dungeon{}
		c.Dungeons[contentID] = v
		return v, nil
	case "pet":
		v := &Pet{}
		c.Pets[contentID] = v
		return v, nil
	case "partner":
		v := &Partner{}
		c.Partners[contentID] = v
		return v, nil
	case "mount":
		v := &Mount{}
		c.Mounts[contentID] = v
		return v, nil
	case "recipe":
		v := &Recipe{}
		c.Recipes[contentID] = v
		return v, nil
	case "shop":
		v := &Shop{}
		c.Shops[contentID] = v
		return v, nil
	case "event":
		v := &Event{}
		c.Events[contentID] = v
		return v, nil
	case "social":
		if c.Social != nil {
			return nil, fmt.Errorf("同一 Release 只能有一个好友规则")
		}
		c.Social = &Social{}
		return c.Social, nil
	case "guild":
		if c.Guild != nil {
			return nil, fmt.Errorf("同一 Release 只能有一个公会规则")
		}
		c.Guild = &Guild{}
		return c.Guild, nil
	case "trade":
		if c.Trade != nil {
			return nil, fmt.Errorf("同一 Release 只能有一个交易规则")
		}
		c.Trade = &Trade{}
		return c.Trade, nil
	}
	return nil, fmt.Errorf("未知内容类型 %q", contentType)
}

type equipmentSlot struct {
	catalog *Catalog
	id      string
}

func (s *equipmentSlot) UnmarshalJSON(data []byte) error {
	value := &Equipment{}
	if err := json.Unmarshal(data, value); err != nil {
		return err
	}
	if _, exists := s.catalog.Equipment[value.Public.ItemID]; exists {
		return fmt.Errorf("物品 %s 已绑定装备模板", value.Public.ItemID)
	}
	s.catalog.Equipment[value.Public.ItemID] = value
	return nil
}

type consumableSlot struct {
	catalog *Catalog
	id      string
}

func (s *consumableSlot) UnmarshalJSON(data []byte) error {
	value := &Consumable{}
	if err := json.Unmarshal(data, value); err != nil {
		return err
	}
	if _, exists := s.catalog.Consumables[value.Public.ItemID]; exists {
		return fmt.Errorf("物品 %s 已绑定使用效果", value.Public.ItemID)
	}
	s.catalog.Consumables[value.Public.ItemID] = value
	return nil
}

// PublicProjection 返回全部条目的白名单投影，按 content_id 排序。
func PublicProjection(registry *schema.Registry, entries []Entry) ([]PublicEntry, error) {
	out := make([]PublicEntry, 0, len(entries))
	for _, entry := range entries {
		validator, err := registry.For(entry.ContentType)
		if err != nil {
			return nil, err
		}
		public, issues := validator.ProjectPublic(entry.Definition)
		if len(issues) != 0 {
			return nil, fmt.Errorf("project %s public fields: %s %s", entry.ContentID, issues[0].Path, issues[0].Code)
		}
		out = append(out, PublicEntry{ContentID: entry.ContentID, ContentType: entry.ContentType, Revision: entry.Revision, Public: public})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContentID < out[j].ContentID })
	return out, nil
}
