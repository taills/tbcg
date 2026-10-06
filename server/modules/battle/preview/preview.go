// Package preview 用与 runtime 完全相同的战斗内核运行内容预览，供管理平台使用。
package preview

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/taills/tbcg/server/internal/content/catalog"
	"github.com/taills/tbcg/server/modules/battle"
	"github.com/taills/tbcg/server/modules/character"
)

// RulesVersion 标识预览所用的规则实现，结果中回显以便追溯。
const RulesVersion = "battle-engine-v1"

type member struct {
	ArchetypeID string   `json:"archetype_id"`
	Level       int      `json:"level"`
	SectID      string   `json:"sect_id"`
	Equipped    []string `json:"equipped"`
}

type request struct {
	EncounterID string   `json:"encounter_id"`
	Seed        uint64   `json:"seed"`
	Party       []member `json:"party"`
}

// Runner 实现 admin.PreviewRunner。
type Runner struct{}

// PreviewBattle 以默认命令运行一场完整战斗并返回全部权威事件。
func (Runner) PreviewBattle(content *catalog.Catalog, raw json.RawMessage) (any, error) {
	var input request
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, errors.New("预览请求格式无效")
	}
	if len(input.Party) == 0 || len(input.Party) > 5 {
		return nil, errors.New("预览队伍需要 1–5 名成员")
	}
	party := make([]battle.Combatant, 0, len(input.Party))
	for i, m := range input.Party {
		if content.Archetypes[m.ArchetypeID] == nil {
			return nil, fmt.Errorf("角色原型 %s 不存在", m.ArchetypeID)
		}
		if m.Level < 1 || m.Level > 200 {
			return nil, errors.New("等级必须在 1–200 之间")
		}
		equipped := map[string]string{}
		for _, itemID := range m.Equipped {
			if equipment := content.Equipment[itemID]; equipment != nil {
				equipped[equipment.Public.Slot] = itemID
			}
		}
		loadout := character.Loadout{ArchetypeID: m.ArchetypeID, Level: m.Level, SectID: m.SectID, Equipped: equipped}
		party = append(party, battle.Combatant{CharacterID: fmt.Sprintf("preview-%d", i+1), Name: fmt.Sprintf("预览成员%d", i+1),
			Level: m.Level, Stats: character.Stats(content, loadout), SkillIDs: character.Skills(content, loadout)})
	}
	state, err := battle.NewState(content, "preview", input.EncounterID, input.Seed, party)
	if err != nil {
		return nil, err
	}
	var events []battle.Event
	for state.Outcome == "" {
		var roundEvents []battle.Event
		state, roundEvents, err = battle.Resolve(content, state, nil)
		if err != nil {
			return nil, err
		}
		events = append(events, roundEvents...)
	}
	return map[string]any{
		"rules_version": RulesVersion, "release_scope": "draft_or_approved_preview", "outcome": state.Outcome,
		"rounds": state.Round, "events": events, "units": state.Units, "spoils": battle.ComputeSpoils(content, state),
	}, nil
}
