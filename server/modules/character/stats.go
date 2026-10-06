// Package character 计算角色的权威属性与可用技能；所有输入来自固定 Release。
package character

import (
	"sort"

	"example.com/tbcg/server/internal/content/catalog"
)

// Loadout 是计算属性所需的已提交角色状态。
type Loadout struct {
	ArchetypeID string
	Level       int
	SectID      string
	Equipped    map[string]string // slot -> item_id
	MountID     string
}

// Stats 返回原型基础值 + 成长 + 门派 + 装备 + 坐骑加成。
func Stats(content *catalog.Catalog, loadout Loadout) catalog.Stats {
	archetype := content.Archetypes[loadout.ArchetypeID]
	if archetype == nil {
		return catalog.Stats{MaxHP: 1}
	}
	level := loadout.Level
	if level < 1 {
		level = 1
	}
	stats := archetype.Server.BaseStats.Add(archetype.Server.GrowthPerLevel.Scale(int64(level - 1)))
	if sect := content.Sects[loadout.SectID]; sect != nil {
		stats = stats.Add(sect.Server.StatBonus)
	}
	slots := make([]string, 0, len(loadout.Equipped))
	for slot := range loadout.Equipped {
		slots = append(slots, slot)
	}
	sort.Strings(slots)
	for _, slot := range slots {
		if equipment := content.Equipment[loadout.Equipped[slot]]; equipment != nil {
			stats = stats.Add(equipment.Public.Stats)
		}
	}
	if mount := content.Mounts[loadout.MountID]; mount != nil {
		stats = stats.Add(mount.Public.StatBonus)
	}
	if stats.MaxHP < 1 {
		stats.MaxHP = 1
	}
	return stats
}

// Skills 返回默认技能与已解锁门派技能（去重、稳定顺序）。
func Skills(content *catalog.Catalog, loadout Loadout) []string {
	var skills []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] && content.Skills[id] != nil {
			seen[id] = true
			skills = append(skills, id)
		}
	}
	if archetype := content.Archetypes[loadout.ArchetypeID]; archetype != nil {
		for _, id := range archetype.Server.DefaultSkillIDs {
			add(id)
		}
	}
	if sect := content.Sects[loadout.SectID]; sect != nil {
		for _, unlock := range sect.Public.Skills {
			if loadout.Level >= unlock.UnlockLevel {
				add(unlock.SkillID)
			}
		}
	}
	return skills
}

// PetStats 返回宠物在等级下的属性。
func PetStats(pet *catalog.Pet, level int) catalog.Stats {
	if level < 1 {
		level = 1
	}
	return pet.Server.BaseStats.Add(pet.Server.GrowthPerLevel.Scale(int64(level - 1)))
}

// LevelExp 返回升到下一级所需累计经验；满级返回 -1。
func LevelExp(curve []int64, level int) int64 {
	if level-1 < len(curve) {
		return curve[level-1]
	}
	return -1
}
