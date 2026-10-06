// Package battle 是纯函数式的权威回合战斗内核：输入旧状态与命令，输出新状态与逐目标事件。
// 计算总在深拷贝上进行，旧快照不被修改；同一 seed 与命令序列结果完全一致。
package battle

import (
	"errors"

	"github.com/taills/tbcg/server/internal/content/catalog"
)

const (
	SidePlayers = 0
	SideEnemies = 1

	KindCharacter = "character"
	KindPet       = "pet"
	KindPartner   = "partner"
	KindMonster   = "monster"

	OutcomeVictory = "victory"
	OutcomeDefeat  = "defeat"
	OutcomeFled    = "fled"
	OutcomeTimeout = "timeout"
)

var (
	ErrInvalidCommand = errors.New("battle: invalid command")
	ErrBattleOver     = errors.New("battle: battle already finished")
)

// ActiveBuff 是带剩余回合的属性增减。
type ActiveBuff struct {
	SkillID string `json:"skill_id"`
	Stat    string `json:"stat"`
	Amount  int64  `json:"amount"`
	Turns   int    `json:"turns"`
}

// Unit 是战斗单位；所有切片在 Clone 中深拷贝。
type Unit struct {
	ID            string        `json:"id"`
	Side          int           `json:"side"`
	Kind          string        `json:"kind"`
	OwnerID       string        `json:"owner_id,omitempty"`
	DefinitionID  string        `json:"definition_id"`
	Name          string        `json:"name"`
	Level         int           `json:"level"`
	Stats         catalog.Stats `json:"stats"`
	HP            int64         `json:"hp"`
	MP            int64         `json:"mp"`
	SkillIDs      []string      `json:"skill_ids"`
	SkillChanceBP int64         `json:"skill_chance_bp,omitempty"`
	Buffs         []ActiveBuff  `json:"buffs"`
	Defending     bool          `json:"defending"`
}

// Alive 表示单位仍可行动。
func (u Unit) Alive() bool { return u.HP > 0 }

// State 是一场战斗的完整权威状态。
type State struct {
	BattleID    string                    `json:"battle_id"`
	EncounterID string                    `json:"encounter_id"`
	Seed        uint64                    `json:"seed"`
	RNG         uint64                    `json:"rng"`
	Round       int                       `json:"round"`
	MaxRounds   int                       `json:"max_rounds"`
	AllowFlee   bool                      `json:"allow_flee"`
	Units       []Unit                    `json:"units"`
	Outcome     string                    `json:"outcome"`
	EventSeq    int64                     `json:"event_seq"`
	ActionSeq   int64                     `json:"action_seq"`
	Inventory   map[string]map[string]int `json:"inventory"`  // owner -> item -> 可用数量
	ItemsUsed   map[string]map[string]int `json:"items_used"` // owner -> item -> 已消耗
}

// Clone 深拷贝状态，候选计算不会与旧快照共享可变引用。
func (s State) Clone() State {
	out := s
	out.Units = make([]Unit, len(s.Units))
	for i, unit := range s.Units {
		unit.SkillIDs = append([]string(nil), unit.SkillIDs...)
		unit.Buffs = append([]ActiveBuff(nil), unit.Buffs...)
		out.Units[i] = unit
	}
	out.Inventory = cloneCounts(s.Inventory)
	out.ItemsUsed = cloneCounts(s.ItemsUsed)
	return out
}

func cloneCounts(source map[string]map[string]int) map[string]map[string]int {
	out := make(map[string]map[string]int, len(source))
	for owner, items := range source {
		copied := make(map[string]int, len(items))
		for item, count := range items {
			copied[item] = count
		}
		out[owner] = copied
	}
	return out
}

// Unit 按 ID 返回单位下标。
func (s *State) unitIndex(id string) int {
	for i := range s.Units {
		if s.Units[i].ID == id {
			return i
		}
	}
	return -1
}

// UnitByID 返回单位副本。
func (s State) UnitByID(id string) (Unit, bool) {
	for _, unit := range s.Units {
		if unit.ID == id {
			return unit, true
		}
	}
	return Unit{}, false
}

// PlayerUnits 返回需要玩家提交命令的角色单位 ID（按 ID 排序）。
func (s State) PlayerUnits() []string {
	var ids []string
	for _, unit := range s.Units {
		if unit.Kind == KindCharacter && unit.Alive() {
			ids = append(ids, unit.ID)
		}
	}
	return ids
}

// Participants 返回参战角色 ID（owner）。
func (s State) Participants() []string {
	var ids []string
	for _, unit := range s.Units {
		if unit.Kind == KindCharacter {
			ids = append(ids, unit.OwnerID)
		}
	}
	return ids
}

// Command 是某个角色单位的回合意图。
type Command struct {
	ActorID  string `json:"actor_id"`
	Kind     string `json:"kind"`
	SkillID  string `json:"skill_id,omitempty"`
	TargetID string `json:"target_id,omitempty"`
	ItemID   string `json:"item_id,omitempty"`
}

// Event 是逐目标、逐击的权威事件；客户端只能回放。
type Event struct {
	Seq      int64  `json:"event_seq"`
	ActionID int64  `json:"action_id"`
	Round    int    `json:"round"`
	Kind     string `json:"kind"`
	ActorID  string `json:"actor_id,omitempty"`
	TargetID string `json:"target_id,omitempty"`
	SkillID  string `json:"skill_id,omitempty"`
	ItemID   string `json:"item_id,omitempty"`
	Hit      int    `json:"hit,omitempty"`
	Damage   int64  `json:"damage,omitempty"`
	Heal     int64  `json:"heal,omitempty"`
	HPBefore int64  `json:"hp_before"`
	HPAfter  int64  `json:"hp_after"`
	MPAfter  int64  `json:"mp_after"`
	Crit     bool   `json:"crit"`
	Stat     string `json:"stat,omitempty"`
	Amount   int64  `json:"amount,omitempty"`
	Turns    int    `json:"turns,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
}

// splitmix64 是固定的可重放随机源。
func (s *State) next() uint64 {
	s.RNG += 0x9E3779B97F4A7C15
	z := s.RNG
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// roll 返回 [0, n) 的整数。
func (s *State) roll(n uint64) uint64 {
	if n == 0 {
		return 0
	}
	return s.next() % n
}

// chance 返回 [0, n) 的有符号整数，n 必须为正数（概率万分比、百分比等）。
func (s *State) chance(n int64) int64 {
	if n <= 0 {
		return 0
	}
	// #nosec G115 -- 余数严格小于正数 n，转换不会溢出。
	return int64(s.next() % uint64(n))
}
