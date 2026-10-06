package catalog

import (
	"fmt"
	"time"
)

type validation struct {
	catalog *Catalog
	issues  []Issue
}

func (v *validation) fail(contentID, path, message string) {
	v.issues = append(v.issues, Issue{ContentID: contentID, Path: path, Code: "broken_reference", Message: message})
}

func (v *validation) invalid(contentID, path, message string) {
	v.issues = append(v.issues, Issue{ContentID: contentID, Path: path, Code: "invalid_value", Message: message})
}

func (v *validation) item(owner, path, id string) {
	if _, ok := v.catalog.Items[id]; !ok {
		v.fail(owner, path, fmt.Sprintf("引用的物品 %s 不存在", id))
	}
}

func (v *validation) stacks(owner, path string, stacks []Stack) {
	for i, stack := range stacks {
		v.item(owner, fmt.Sprintf("%s/%d/item_id", path, i), stack.ItemID)
	}
}

func (v *validation) skill(owner, path, id string) {
	if _, ok := v.catalog.Skills[id]; !ok {
		v.fail(owner, path, fmt.Sprintf("引用的技能 %s 不存在", id))
	}
}

func (v *validation) quest(owner, path, id string) {
	if _, ok := v.catalog.Quests[id]; !ok {
		v.fail(owner, path, fmt.Sprintf("引用的任务 %s 不存在", id))
	}
}

func (v *validation) anchor(owner, path, mapID, anchorID string) {
	geometry, ok := v.catalog.Geometries[mapID]
	if !ok {
		v.fail(owner, path, fmt.Sprintf("引用的地图 %s 不存在", mapID))
		return
	}
	if _, ok := geometry.Anchor(anchorID); !ok {
		v.fail(owner, path, fmt.Sprintf("地图 %s 中不存在锚点 %s", mapID, anchorID))
	}
}

// Validate 检查所有跨内容引用；返回空切片表示可运行。
func (c *Catalog) Validate() []Issue {
	v := &validation{catalog: c}
	v.maps()
	v.characters()
	v.combat()
	v.npcs()
	v.quests()
	v.items()
	v.dungeons()
	v.companions()
	v.economy()
	v.events()
	return v.issues
}

func (v *validation) maps() {
	for id, m := range v.catalog.Maps {
		geometry := v.catalog.Geometries[id]
		spawn, ok := geometry.Anchor(m.Server.SpawnAnchorID)
		if !ok {
			v.fail(id, "/server/spawn_anchor_id", "出生锚点不存在")
		} else if _, walkable := geometry.NearestWalkable(spawn); !walkable {
			v.invalid(id, "/server/spawn_anchor_id", "出生锚点没有可走格子")
		}
		for i, portal := range m.Public.Portals {
			anchor, ok := geometry.Anchor(portal.AnchorID)
			if !ok || anchor.Kind != "portal" {
				v.fail(id, fmt.Sprintf("/public/portals/%d/anchor_id", i), "传送锚点不存在或类型不是 portal")
			}
			v.anchor(id, fmt.Sprintf("/public/portals/%d/target_anchor_id", i), portal.TargetMapID, portal.TargetAnchorID)
			if portal.RequiredQuestID != "" {
				v.quest(id, fmt.Sprintf("/public/portals/%d/required_quest_id", i), portal.RequiredQuestID)
			}
		}
		for i, zone := range m.Public.Zones {
			if _, ok := geometry.Anchor(zone.AnchorID); !ok {
				v.fail(id, fmt.Sprintf("/public/zones/%d/anchor_id", i), "区域锚点不存在")
			}
		}
	}
}

func (v *validation) characters() {
	if len(v.catalog.Archetypes) == 0 && len(v.catalog.Maps) > 0 {
		v.fail("", "", "可运行 Release 至少需要一个角色原型")
	}
	for id, a := range v.catalog.Archetypes {
		v.anchor(id, "/server/start_anchor_id", a.Server.StartMapID, a.Server.StartAnchorID)
		v.stacks(id, "/server/start_items", a.Server.StartItems)
		for i, skillID := range a.Server.DefaultSkillIDs {
			v.skill(id, fmt.Sprintf("/server/default_skill_ids/%d", i), skillID)
		}
	}
	for id, s := range v.catalog.Sects {
		for i, unlock := range s.Public.Skills {
			v.skill(id, fmt.Sprintf("/public/skills/%d/skill_id", i), unlock.SkillID)
		}
		if s.Server.JoinNPCID != "" {
			if _, ok := v.catalog.NPCs[s.Server.JoinNPCID]; !ok {
				v.fail(id, "/server/join_npc_id", "入门 NPC 不存在")
			}
		}
	}
}

func (v *validation) combat() {
	for id, m := range v.catalog.Monsters {
		for i, skillID := range m.Server.SkillIDs {
			v.skill(id, fmt.Sprintf("/server/skill_ids/%d", i), skillID)
		}
		for i, loot := range m.Server.Loot {
			v.item(id, fmt.Sprintf("/server/loot/%d/item_id", i), loot.ItemID)
		}
	}
	for id, e := range v.catalog.Encounters {
		for i, member := range e.Server.Monsters {
			if _, ok := v.catalog.Monsters[member.MonsterID]; !ok {
				v.fail(id, fmt.Sprintf("/server/monsters/%d/monster_id", i), "遭遇引用的怪物不存在")
			}
		}
	}
	for id, s := range v.catalog.Skills {
		if s.Public.Kind == "buff" && s.Server.Buff == nil {
			v.invalid(id, "/server/buff", "增益技能必须定义 buff")
		}
	}
}

func (v *validation) npcs() {
	for id, npc := range v.catalog.NPCs {
		v.anchor(id, "/public/anchor_id", npc.Public.MapID, npc.Public.AnchorID)
		if _, ok := npc.Node(npc.Server.StartNode); !ok {
			v.fail(id, "/server/start_node", "起始对话节点不存在")
		}
		seen := map[string]bool{}
		for n, node := range npc.Server.Nodes {
			if seen[node.NodeID] {
				v.invalid(id, fmt.Sprintf("/server/nodes/%d/node_id", n), "对话节点 ID 重复")
			}
			seen[node.NodeID] = true
			for o, option := range node.Options {
				v.dialogOption(id, fmt.Sprintf("/server/nodes/%d/options/%d", n, o), npc, option)
			}
		}
	}
}

func (v *validation) dialogOption(owner, path string, npc *NPC, option DialogOption) {
	action := option.Action
	if action.NextNode != "" {
		if _, ok := npc.Node(action.NextNode); !ok {
			v.fail(owner, path+"/action/next_node", "跳转节点不存在")
		}
	}
	if action.Kind == "goto" && action.NextNode == "" {
		v.invalid(owner, path+"/action/next_node", "goto 必须指定节点")
	}
	targets := map[string]func(string) bool{
		"accept_quest":    func(id string) bool { return v.catalog.Quests[id] != nil },
		"talk_objective":  func(id string) bool { return v.catalog.Quests[id] != nil },
		"deliver_item":    func(id string) bool { return v.catalog.Quests[id] != nil },
		"claim_quest":     func(id string) bool { return v.catalog.Quests[id] != nil },
		"start_encounter": func(id string) bool { return v.catalog.Encounters[id] != nil },
		"join_sect":       func(id string) bool { return v.catalog.Sects[id] != nil },
		"open_shop":       func(id string) bool { return v.catalog.Shops[id] != nil },
		"enter_dungeon":   func(id string) bool { return v.catalog.Dungeons[id] != nil },
		"recruit_partner": func(id string) bool { return v.catalog.Partners[id] != nil },
		"grant_pet":       func(id string) bool { return v.catalog.Pets[id] != nil },
	}
	if check, ok := targets[action.Kind]; ok && !check(action.TargetID) {
		v.fail(owner, path+"/action/target_id", fmt.Sprintf("动作 %s 的目标 %q 不存在", action.Kind, action.TargetID))
	}
	if option.Condition != nil {
		if option.Condition.QuestID != "" {
			v.quest(owner, path+"/condition/quest_id", option.Condition.QuestID)
		}
		if option.Condition.SectID != "" && v.catalog.Sects[option.Condition.SectID] == nil {
			v.fail(owner, path+"/condition/sect_id", "条件引用的门派不存在")
		}
	}
}

func (v *validation) quests() {
	for id, q := range v.catalog.Quests {
		if v.catalog.NPCs[q.Server.GiverNPCID] == nil {
			v.fail(id, "/server/giver_npc_id", "接取 NPC 不存在")
		}
		if v.catalog.NPCs[q.Server.TurnInNPCID] == nil {
			v.fail(id, "/server/turn_in_npc_id", "交付 NPC 不存在")
		}
		for i, prerequisite := range q.Server.Prerequisites {
			if prerequisite == id {
				v.invalid(id, fmt.Sprintf("/server/prerequisites/%d", i), "任务不能以自身为前置")
			}
			v.quest(id, fmt.Sprintf("/server/prerequisites/%d", i), prerequisite)
		}
		if len(q.Public.StageDescriptions) != len(q.Server.Stages) {
			v.invalid(id, "/public/stage_descriptions", "阶段描述数量必须与阶段数量一致")
		}
		for i, stage := range q.Server.Stages {
			v.objective(id, fmt.Sprintf("/server/stages/%d/objective", i), stage.Objective)
		}
		v.stacks(id, "/server/rewards/items", q.Server.Rewards.Items)
	}
}

func (v *validation) objective(owner, path string, objective Objective) {
	switch objective.Kind {
	case "enter_zone":
		v.anchor(owner, path+"/anchor_id", objective.MapID, objective.AnchorID)
	case "deliver_item":
		v.item(owner, path+"/item_id", objective.ItemID)
	case "defeat_encounter":
		if v.catalog.Encounters[objective.EncounterID] == nil {
			v.fail(owner, path+"/encounter_id", "目标遭遇不存在")
		}
	case "talk_npc":
		if v.catalog.NPCs[objective.NPCID] == nil {
			v.fail(owner, path+"/npc_id", "目标 NPC 不存在")
		}
	}
	if objective.ProgressScope == "party" && objective.Kind != "defeat_encounter" {
		v.invalid(owner, path+"/progress_scope", "只有遭遇胜利目标支持队伍进度")
	}
}

func (v *validation) items() {
	for itemID, e := range v.catalog.Equipment {
		v.item(e.ContentID, "/public/item_id", itemID)
		for i, sectID := range e.Public.SectIDs {
			if v.catalog.Sects[sectID] == nil {
				v.fail(e.ContentID, fmt.Sprintf("/public/sect_ids/%d", i), "装备限制的门派不存在")
			}
		}
	}
	for itemID, c := range v.catalog.Consumables {
		v.item(c.ContentID, "/public/item_id", itemID)
		for i, effect := range c.Server.Effects {
			path := fmt.Sprintf("/server/effects/%d/target_id", i)
			switch effect.Kind {
			case "grant_pet":
				if v.catalog.Pets[effect.TargetID] == nil {
					v.fail(c.ContentID, path, "效果引用的宠物不存在")
				}
			case "grant_mount":
				if v.catalog.Mounts[effect.TargetID] == nil {
					v.fail(c.ContentID, path, "效果引用的坐骑不存在")
				}
			}
		}
	}
}

func (v *validation) dungeons() {
	for id, d := range v.catalog.Dungeons {
		if d.Public.MinPlayers > d.Public.MaxPlayers {
			v.invalid(id, "/public/min_players", "最少人数不能超过最多人数")
		}
		if v.catalog.NPCs[d.Server.EntryNPCID] == nil {
			v.fail(id, "/server/entry_npc_id", "入口 NPC 不存在")
		}
		if len(d.Public.StageNames) != len(d.Server.Stages) {
			v.invalid(id, "/public/stage_names", "阶段名称数量必须与阶段数量一致")
		}
		for i, stage := range d.Server.Stages {
			if v.catalog.Encounters[stage.EncounterID] == nil {
				v.fail(id, fmt.Sprintf("/server/stages/%d/encounter_id", i), "阶段遭遇不存在")
			}
		}
		v.stacks(id, "/server/rewards/items", d.Server.Rewards.Items)
	}
}

func (v *validation) companions() {
	for id, p := range v.catalog.Pets {
		for i, skillID := range p.Public.SkillIDs {
			v.skill(id, fmt.Sprintf("/public/skill_ids/%d", i), skillID)
		}
	}
	for id, p := range v.catalog.Partners {
		for i, skillID := range p.Public.SkillIDs {
			v.skill(id, fmt.Sprintf("/public/skill_ids/%d", i), skillID)
		}
		if p.Public.RequiredQuestID != "" {
			v.quest(id, "/public/required_quest_id", p.Public.RequiredQuestID)
		}
	}
}

func (v *validation) economy() {
	for id, r := range v.catalog.Recipes {
		for i, material := range r.Public.Materials {
			v.item(id, fmt.Sprintf("/public/materials/%d/item_id", i), material.ItemID)
		}
		for i, output := range r.Public.Outputs {
			v.item(id, fmt.Sprintf("/public/outputs/%d/item_id", i), output.ItemID)
		}
		if r.Public.SectID != "" && v.catalog.Sects[r.Public.SectID] == nil {
			v.fail(id, "/public/sect_id", "配方限制的门派不存在")
		}
	}
	for id, s := range v.catalog.Shops {
		if v.catalog.NPCs[s.Public.NPCID] == nil {
			v.fail(id, "/public/npc_id", "商店 NPC 不存在")
		}
		for i, good := range s.Public.Goods {
			v.item(id, fmt.Sprintf("/public/goods/%d/item_id", i), good.ItemID)
		}
		for i, stock := range s.Server.Stock {
			if _, ok := s.Good(stock.ItemID); !ok {
				v.fail(id, fmt.Sprintf("/server/stock/%d/item_id", i), "库存条目不在商品列表中")
			}
		}
	}
	if v.catalog.Trade != nil {
		for i, itemID := range v.catalog.Trade.Server.UntradableItemIDs {
			v.item(v.catalog.Trade.ContentID, fmt.Sprintf("/server/untradable_item_ids/%d", i), itemID)
		}
	}
}

func (v *validation) events() {
	for id, e := range v.catalog.Events {
		for i, window := range e.Public.Windows {
			start, startErr := time.Parse(time.RFC3339, window.Start)
			end, endErr := time.Parse(time.RFC3339, window.End)
			if startErr != nil || endErr != nil || !end.After(start) {
				v.invalid(id, fmt.Sprintf("/public/windows/%d", i), "活动窗口结束时间必须晚于开始时间")
			}
		}
		switch e.Public.Objective.Kind {
		case "defeat_encounter":
			if v.catalog.Encounters[e.Public.Objective.TargetID] == nil {
				v.fail(id, "/public/objective/target_id", "活动目标遭遇不存在")
			}
		case "complete_dungeon":
			if v.catalog.Dungeons[e.Public.Objective.TargetID] == nil {
				v.fail(id, "/public/objective/target_id", "活动目标副本不存在")
			}
		}
		v.stacks(id, "/server/rewards/items", e.Server.Rewards.Items)
	}
}

// LevelForExp 根据累计经验与升级曲线返回等级（从 1 开始）。
func LevelForExp(levelExp []int64, exp int64) int {
	level := 1
	for _, need := range levelExp {
		if exp < need {
			break
		}
		level++
	}
	return level
}
