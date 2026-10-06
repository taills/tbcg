package runtime

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"example.com/tbcg/server/internal/content/catalog"
	"example.com/tbcg/server/modules/character"
)

// CharacterState 是 characters.attributes 中的权威角色实例状态（网格坐标）。
type CharacterState struct {
	Name           string            `json:"name"`
	ArchetypeID    string            `json:"archetype_id"`
	Level          int               `json:"level"`
	Exp            int64             `json:"exp"`
	HP             int64             `json:"hp"`
	MP             int64             `json:"mp"`
	SectID         string            `json:"sect_id"`
	MapID          string            `json:"map_id"`
	X              int               `json:"x"`
	Y              int               `json:"y"`
	Equipped       map[string]string `json:"equipped"`
	MountID        string            `json:"mount_id"`
	CreatedRelease string            `json:"created_release"`
}

// Clone 返回深拷贝。
func (s CharacterState) Clone() CharacterState {
	out := s
	out.Equipped = map[string]string{}
	for slot, item := range s.Equipped {
		out.Equipped[slot] = item
	}
	return out
}

// Loadout 返回属性计算输入。
func (s CharacterState) Loadout() character.Loadout {
	return character.Loadout{ArchetypeID: s.ArchetypeID, Level: s.Level, SectID: s.SectID, Equipped: s.Equipped, MountID: s.MountID}
}

// Player 是在线角色的内存投影；只在 Realm 循环中修改。
type Player struct {
	CharacterID string
	AccountID   string
	Version     int64
	State       CharacterState
	Session     *Session
	DialogNPC   string
	DialogNode  string
	BattleID    string
	LastStepAt  time.Time
}

// Record 是事务中的角色行。
type Record struct {
	CharacterID string
	Owner       string
	Version     int64
	State       CharacterState
}

// LoadCharacter 在事务中加行锁读取角色。
func (tx *Tx) LoadCharacter(characterID string) (*Record, error) {
	var record Record
	var attributes []byte
	err := tx.QueryRowContext(tx.Ctx, `SELECT character_id, owner_subject, state_version, attributes FROM characters
WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 FOR UPDATE`, tx.Realm.GameID, tx.Realm.RealmID, characterID).Scan(&record.CharacterID, &record.Owner, &record.Version, &attributes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Fail("character_not_found", "角色不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("load character: %w", err)
	}
	if err := json.Unmarshal(attributes, &record.State); err != nil {
		return nil, fmt.Errorf("decode character state: %w", err)
	}
	if record.State.Equipped == nil {
		record.State.Equipped = map[string]string{}
	}
	return &record, nil
}

// SaveCharacter 以 state_version CAS 写回并递增版本。
func (tx *Tx) SaveCharacter(record *Record) error {
	encoded, err := json.Marshal(record.State)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(tx.Ctx, `UPDATE characters SET attributes=$5::jsonb, state_version=state_version+1, updated_at=now()
WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND state_version=$4`, tx.Realm.GameID, tx.Realm.RealmID, record.CharacterID, record.Version, encoded)
	if err != nil {
		return fmt.Errorf("save character: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrVersionConflict
	}
	record.Version++
	return nil
}

// SyncPlayer 在提交后把记录写回在线投影（不推送）。
func (tx *Tx) SyncPlayer(record *Record) {
	tx.syncPlayer(record, false)
}

// SyncAndNotify 在提交后写回在线投影并推送 character.updated。
func (tx *Tx) SyncAndNotify(record *Record) {
	tx.syncPlayer(record, true)
}

func (tx *Tx) syncPlayer(record *Record, notify bool) {
	snapshot := *record
	snapshot.State = record.State.Clone()
	tx.After(func() {
		if player := tx.Realm.players[snapshot.CharacterID]; player != nil {
			player.Version = snapshot.Version
			player.State = snapshot.State
			if notify {
				tx.Realm.Push(player.CharacterID, "character.updated", tx.Realm.CharacterView(player))
			}
		}
	})
}

// Notify 推送 character.updated 给在线角色。
func (r *Realm) Notify(characterID string) {
	if player := r.players[characterID]; player != nil {
		r.Push(characterID, "character.updated", r.CharacterView(player))
	}
}

// GrantExp 增加经验并按 Release 曲线升级（升级回满气血法力）。返回是否升级。
func GrantExp(content *catalog.Catalog, state *CharacterState, amount int64) bool {
	archetype := content.Archetypes[state.ArchetypeID]
	if archetype == nil || amount <= 0 {
		return false
	}
	state.Exp += amount
	newLevel := catalog.LevelForExp(archetype.Server.LevelExp, state.Exp)
	if newLevel > state.Level {
		state.Level = newLevel
		stats := character.Stats(content, state.Loadout())
		state.HP, state.MP = stats.MaxHP, stats.MaxMP
		return true
	}
	return false
}

// ClampVitals 让当前气血/法力不超过属性上限。
func ClampVitals(content *catalog.Catalog, state *CharacterState) {
	stats := character.Stats(content, state.Loadout())
	state.HP = min(max(state.HP, 0), stats.MaxHP)
	state.MP = min(max(state.MP, 0), stats.MaxMP)
}

// CharacterView 是发给角色本人的完整状态视图。
func (r *Realm) CharacterView(player *Player) map[string]any {
	stats := character.Stats(r.content, player.State.Loadout())
	return map[string]any{
		"character_id": player.CharacterID, "state_version": fmt.Sprint(player.Version), "state": player.State,
		"stats": stats, "skills": character.Skills(r.content, player.State.Loadout()),
		"next_level_exp": nextLevelExp(r.content, player.State),
	}
}

func nextLevelExp(content *catalog.Catalog, state CharacterState) int64 {
	archetype := content.Archetypes[state.ArchetypeID]
	if archetype == nil {
		return -1
	}
	return character.LevelExp(archetype.Server.LevelExp, state.Level)
}
