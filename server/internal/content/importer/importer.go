// Package importer 把经白名单与授权登记的 xyq 风格静态数据转换为 TBCG 草稿定义，并精确报告语义差异。
// 它不执行任何脚本、不复制协议/存档、不导入美术；输出只用于创建新草稿，绝不覆盖已有数据。
package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/taills/tbcg/server/internal/content/schema"
)

// 字段映射状态。
const (
	StatusSupported   = "supported"
	StatusApproximate = "approximate"
	StatusUnsupported = "unsupported"
)

// Manifest 是来源白名单与授权登记。
type Manifest struct {
	SchemaVersion     int      `json:"schema_version"`
	Description       string   `json:"description"`
	TargetGameID      string   `json:"target_game_id"`
	PlaceholderIconID string   `json:"placeholder_icon_id"`
	Sources           []Source `json:"sources"`
}

// Source 是一个白名单来源文件。
type Source struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	License string `json:"license"`
	Origin  string `json:"origin"`
}

// FieldNote 描述一个字段的映射结果。
type FieldNote struct {
	Field  string `json:"field"`
	Status string `json:"status"`
	Note   string `json:"note"`
}

// Record 是一条源记录的转换结果。
type Record struct {
	SourcePath string          `json:"source_path"`
	SourceID   string          `json:"source_id"`
	ContentID  string          `json:"content_id"`
	Status     string          `json:"status"`
	Fields     []FieldNote     `json:"fields"`
	Definition json.RawMessage `json:"definition,omitempty"`
	Issues     []string        `json:"issues"`
}

// Report 是确定性的导入报告；OutputSHA256 只依赖输入内容。
type Report struct {
	TargetGameID string   `json:"target_game_id"`
	Records      []Record `json:"records"`
	Quarantined  []string `json:"quarantined_sources"`
	Conflicts    []string `json:"id_conflicts"`
	Missing      []string `json:"missing_dependencies"`
	Ready        []string `json:"ready"`
	NeedsReview  []string `json:"needs_confirmation"`
	Blocked      []string `json:"blocked"`
	OutputSHA256 string   `json:"output_sha256"`
}

// Bundle 是可提交给管理 API `/content-imports` 的草稿批次（只创建新草稿）。
type Bundle struct {
	DryRun      bool              `json:"dry_run"`
	Definitions []json.RawMessage `json:"definitions"`
}

// Options 控制人工确认的 approximate 记录。
type Options struct {
	AcceptApproximate map[string]bool
}

type converter func(game string, manifest Manifest, raw map[string]any, record *Record) map[string]any

var converters = map[string]converter{"item": convertItem, "monster": convertMonster, "skill": convertSkill}

// Run 读取白名单来源并生成报告与草稿批次；validator 为全部内容类型的可信 Schema。
func Run(source fs.FS, manifest Manifest, registry *schema.Registry, options Options) (Report, Bundle, error) {
	if manifest.SchemaVersion != 1 || !validGameID(manifest.TargetGameID) {
		return Report{}, Bundle{}, errors.New("import manifest version or target game is invalid")
	}
	report := Report{TargetGameID: manifest.TargetGameID, Records: []Record{}, Quarantined: []string{}, Conflicts: []string{},
		Missing: []string{}, Ready: []string{}, NeedsReview: []string{}, Blocked: []string{}}
	sources := append([]Source(nil), manifest.Sources...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	known := map[string]string{} // content id -> source id
	var records []Record
	for _, src := range sources {
		clean := path.Clean(src.Path)
		if clean != src.Path || strings.HasPrefix(clean, "..") || path.IsAbs(clean) {
			return Report{}, Bundle{}, fmt.Errorf("source path %q escapes the import root", src.Path)
		}
		if src.License != "authorized" {
			report.Quarantined = append(report.Quarantined, fmt.Sprintf("%s（授权状态 %s）", src.Path, src.License))
			continue
		}
		convert, ok := converters[src.Kind]
		if !ok {
			report.Quarantined = append(report.Quarantined, fmt.Sprintf("%s（不支持的来源类型 %s）", src.Path, src.Kind))
			continue
		}
		data, err := fs.ReadFile(source, clean)
		if err != nil {
			return Report{}, Bundle{}, fmt.Errorf("read whitelisted source %s: %w", clean, err)
		}
		var rows []map[string]any
		if err := json.Unmarshal(data, &rows); err != nil {
			return Report{}, Bundle{}, fmt.Errorf("decode %s: %w", clean, err)
		}
		for _, raw := range rows {
			record := Record{SourcePath: clean, SourceID: idString(raw["id"]), Fields: []FieldNote{}, Issues: []string{}}
			definition := convert(manifest.TargetGameID, manifest, raw, &record)
			record.ContentID, _ = definition["content_id"].(string)
			if previous, exists := known[record.ContentID]; exists {
				report.Conflicts = append(report.Conflicts, fmt.Sprintf("%s 同时来自 %s 与 %s", record.ContentID, previous, record.SourcePath+"#"+record.SourceID))
				continue
			}
			known[record.ContentID] = record.SourcePath + "#" + record.SourceID
			encoded, err := json.Marshal(definition)
			if err != nil {
				return Report{}, Bundle{}, err
			}
			record.Definition = encoded
			records = append(records, record)
		}
	}
	// 依赖闭包：引用未在白名单内的内容即为缺失依赖。
	for i := range records {
		for _, dependency := range dependencies(records[i].Definition) {
			if _, ok := known[dependency]; !ok {
				records[i].Issues = append(records[i].Issues, "缺失依赖 "+dependency)
				report.Missing = append(report.Missing, records[i].ContentID+" → "+dependency)
			}
		}
	}
	bundle := Bundle{DryRun: false, Definitions: []json.RawMessage{}}
	for i := range records {
		record := &records[i]
		record.Status = summarize(record.Fields)
		validator, err := registry.For(contentTypeOf(record.ContentID))
		if err == nil {
			for _, issue := range validator.ValidateDefinition(record.Definition) {
				record.Issues = append(record.Issues, "Schema "+issue.Path+" "+issue.Code)
			}
		}
		switch {
		case record.Status == StatusUnsupported || len(record.Issues) > 0:
			record.Status = StatusUnsupported
			report.Blocked = append(report.Blocked, record.ContentID)
		case record.Status == StatusApproximate && !options.AcceptApproximate[record.ContentID]:
			report.NeedsReview = append(report.NeedsReview, record.ContentID)
		default:
			report.Ready = append(report.Ready, record.ContentID)
			bundle.Definitions = append(bundle.Definitions, record.Definition)
		}
	}
	report.Records = records
	sort.Strings(report.Missing)
	encoded, err := json.Marshal(bundle.Definitions)
	if err != nil {
		return Report{}, Bundle{}, err
	}
	digest := sha256.Sum256(encoded)
	report.OutputSHA256 = hex.EncodeToString(digest[:])
	return report, bundle, nil
}

func summarize(fields []FieldNote) string {
	status := StatusSupported
	for _, field := range fields {
		if field.Status == StatusUnsupported {
			return StatusUnsupported
		}
		if field.Status == StatusApproximate {
			status = StatusApproximate
		}
	}
	return status
}

func validGameID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("._-", char)) {
			return false
		}
	}
	return true
}

func contentTypeOf(contentID string) string {
	contentType, _ := schema.ContentTypeOf(contentID)
	return contentType
}

func idString(value any) string {
	switch typed := value.(type) {
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case string:
		return typed
	}
	return "unknown"
}

func contentID(game, kind string, sourceID any) string {
	return fmt.Sprintf("%s:%s/xyq-%s", game, kind, strings.ToLower(idString(sourceID)))
}

func identity(game, id string) map[string]any {
	return map[string]any{"game_id": game, "content_id": id, "revision": 1, "schema_version": 1}
}

func withIdentity(game, id string, public, server map[string]any) map[string]any {
	definition := identity(game, id)
	for key, value := range identity(game, id) {
		public[key] = value
	}
	definition["public"] = public
	definition["server"] = server
	return definition
}

func note(record *Record, field, status, text string) {
	record.Fields = append(record.Fields, FieldNote{Field: field, Status: status, Note: text})
}

// unmapped 报告源记录中未被规则消费的字段：脚本类字段为 unsupported，其余为 approximate（语义丢失）。
func unmapped(raw map[string]any, consumed []string, record *Record) {
	used := map[string]bool{}
	for _, field := range consumed {
		used[field] = true
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		if !used[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.Contains(key, "script") || strings.Contains(key, "formula") || strings.Contains(key, "lua") {
			note(record, key, StatusUnsupported, "行为脚本不会被执行或迁移，需要以 TBCG Go 规则重新实现")
		} else {
			note(record, key, StatusApproximate, "无对应字段，语义丢失")
		}
	}
}

func number(raw map[string]any, field string) float64 {
	value, _ := raw[field].(float64)
	return value
}

func text(raw map[string]any, field string) string {
	value, _ := raw[field].(string)
	return value
}

func convertItem(game string, manifest Manifest, raw map[string]any, record *Record) map[string]any {
	id := contentID(game, "item", raw["id"])
	kinds := map[string]string{"drug": "consumable", "weapon": "equipment", "armor": "equipment", "material": "material", "quest": "quest"}
	itemType, ok := kinds[text(raw, "kind")]
	if !ok {
		itemType = "material"
		note(record, "kind", StatusApproximate, "未知物品种类，按材料处理")
	} else {
		note(record, "kind", StatusSupported, "映射为 "+itemType)
	}
	note(record, "name", StatusSupported, "display_name")
	note(record, "desc", StatusSupported, "description")
	stack := int(number(raw, "stack"))
	if stack < 1 || stack > 999 {
		stack = min(max(stack, 1), 999)
		note(record, "stack", StatusApproximate, "堆叠上限截断到 1–999")
	} else {
		note(record, "stack", StatusSupported, "stack_limit")
	}
	note(record, "price", StatusSupported, "price_minor")
	if _, has := raw["icon"]; has {
		note(record, "icon", StatusApproximate, "源图标不导入，替换为已授权占位图标")
	}
	unmapped(raw, []string{"id", "kind", "name", "desc", "stack", "price", "icon"}, record)
	return withIdentity(game, id, map[string]any{"item_type": itemType, "display_name": text(raw, "name"), "description": text(raw, "desc"),
		"icon_id": manifest.PlaceholderIconID, "stack_limit": stack, "price_minor": strconv.FormatInt(int64(math.Max(0, number(raw, "price"))), 10)},
		map[string]any{"reward_value_minor": "0", "drop_weight_basis_points": 0})
}

func convertMonster(game string, _ Manifest, raw map[string]any, record *Record) map[string]any {
	id := contentID(game, "monster", raw["id"])
	for _, field := range []string{"name", "level", "hp", "mp", "atk", "def", "spd", "exp", "money"} {
		note(record, field, StatusSupported, "数值直接映射")
	}
	skills := []any{}
	if list, ok := raw["skills"].([]any); ok {
		for _, skill := range list {
			skills = append(skills, contentID(game, "skill", skill))
		}
		note(record, "skills", StatusSupported, "映射为技能引用")
	}
	loot := []any{}
	if drops, ok := raw["drops"].([]any); ok {
		for _, drop := range drops {
			entry, _ := drop.(map[string]any)
			loot = append(loot, map[string]any{"item_id": contentID(game, "item", entry["item"]), "chance_bp": int(math.Round(number(entry, "rate") * 10000)), "quantity": 1})
		}
		note(record, "drops", StatusApproximate, "掉落数量统一为 1，概率换算为万分比")
	}
	unmapped(raw, []string{"id", "name", "level", "hp", "mp", "atk", "def", "spd", "exp", "money", "skills", "drops"}, record)
	chance := 0
	if len(skills) > 0 {
		chance = 3000
	}
	return withIdentity(game, id, map[string]any{"display_name": text(raw, "name"), "appearance_id": game + ":appearance/placeholder", "level": max(1, int(number(raw, "level")))},
		map[string]any{"stats": map[string]any{"max_hp": max(1, int(number(raw, "hp"))), "max_mp": int(number(raw, "mp")), "attack": int(number(raw, "atk")),
			"defense": int(number(raw, "def")), "speed": int(number(raw, "spd"))}, "skill_ids": skills, "skill_chance_bp": chance,
			"exp_reward": int(number(raw, "exp")), "currency_reward": int(number(raw, "money")), "loot": loot})
}

func convertSkill(game string, _ Manifest, raw map[string]any, record *Record) map[string]any {
	id := contentID(game, "skill", raw["id"])
	target := "enemy_single"
	if text(raw, "target") == "all" {
		target = "enemy_all"
	}
	note(record, "target", StatusSupported, target)
	note(record, "power", StatusApproximate, "倍率换算为百分比，伤害公式以 TBCG 内核为准")
	note(record, "crit", StatusApproximate, "暴击率换算为万分比")
	for _, field := range []string{"name", "desc", "mp", "hits"} {
		note(record, field, StatusSupported, "直接映射")
	}
	unmapped(raw, []string{"id", "name", "desc", "target", "mp", "power", "hits", "crit"}, record)
	return withIdentity(game, id, map[string]any{"display_name": text(raw, "name"), "description": text(raw, "desc"), "kind": "damage", "target": target, "mp_cost": int(number(raw, "mp"))},
		map[string]any{"power_percent": int(math.Round(number(raw, "power") * 100)), "hits": min(5, max(1, int(number(raw, "hits")))), "crit_rate_bp": int(math.Round(number(raw, "crit") * 10000))})
}

// dependencies 返回定义中引用的其他内容 ID（技能与掉落物品）。
func dependencies(definition json.RawMessage) []string {
	var value struct {
		Server struct {
			SkillIDs []string `json:"skill_ids"`
			Loot     []struct {
				ItemID string `json:"item_id"`
			} `json:"loot"`
		} `json:"server"`
	}
	_ = json.Unmarshal(definition, &value)
	out := append([]string(nil), value.Server.SkillIDs...)
	for _, loot := range value.Server.Loot {
		out = append(out, loot.ItemID)
	}
	return out
}
