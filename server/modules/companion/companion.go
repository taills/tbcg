// Package companion 实现宠物、伙伴与坐骑（任务 0015.1–0015.3）。三者共享最小的归属/版本 seam（owner 校验 +
// state_version CAS），但各自保留独立的领域规则，不合并为通用 CRUD。
package companion

import (
	"context"
	"database/sql"
	"errors"
	"unicode/utf8"

	"example.com/tbcg/server/internal/content/catalog"
	"example.com/tbcg/server/modules/character"
	"example.com/tbcg/server/modules/quest"
	rt "example.com/tbcg/server/modules/runtime"
)

const maxFormation = 2

func init() {
	rt.Register("pet.activate", true, activatePet)
	rt.Register("pet.rest", true, restPet)
	rt.Register("pet.rename", true, renamePet)
	rt.Register("partner.formation", true, formation)
	rt.Register("mount.equip", true, equipMount)
	rt.Register("mount.unequip", true, unequipMount)
	rt.RegisterDialogAction("recruit_partner", recruit)
	rt.RegisterDialogAction("grant_pet", grantPetFromNPC)
	rt.OnFact("battle.victory", petExperience)
	rt.RegisterSnapshot("companions", func(ctx context.Context, r *rt.Realm, p *rt.Player) (any, error) {
		return Snapshot(ctx, r, p.CharacterID)
	})
}

// Snapshot 返回宠物、伙伴与坐骑列表。
func Snapshot(ctx context.Context, r *rt.Realm, characterID string) (map[string]any, error) {
	pets := make([]map[string]any, 0)
	rows, err := r.DB.QueryContext(ctx, `SELECT pet_uid::text, pet_id, name, level, exp, active, state_version FROM pets WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 ORDER BY created_at`,
		r.GameID, r.RealmID, characterID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var uid, petID, name string
		var level int
		var exp, version int64
		var active bool
		if err := rows.Scan(&uid, &petID, &name, &level, &exp, &active, &version); err != nil {
			_ = rows.Close()
			return nil, err
		}
		entry := map[string]any{"pet_uid": uid, "pet_id": petID, "name": name, "level": level, "exp": exp, "active": active, "state_version": version}
		if pet := r.Content().Pets[petID]; pet != nil {
			entry["stats"] = character.PetStats(pet, level)
		}
		pets = append(pets, entry)
	}
	_ = rows.Close()
	partners := make([]map[string]any, 0)
	rows, err = r.DB.QueryContext(ctx, `SELECT partner_id, in_formation FROM partners WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 ORDER BY partner_id`, r.GameID, r.RealmID, characterID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var partnerID string
		var inFormation bool
		if err := rows.Scan(&partnerID, &inFormation); err != nil {
			_ = rows.Close()
			return nil, err
		}
		partners = append(partners, map[string]any{"partner_id": partnerID, "in_formation": inFormation})
	}
	_ = rows.Close()
	mounts := make([]map[string]any, 0)
	rows, err = r.DB.QueryContext(ctx, `SELECT mount_id, equipped FROM mounts WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 ORDER BY mount_id`, r.GameID, r.RealmID, characterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var mountID string
		var equipped bool
		if err := rows.Scan(&mountID, &equipped); err != nil {
			return nil, err
		}
		mounts = append(mounts, map[string]any{"mount_id": mountID, "equipped": equipped})
	}
	return map[string]any{"pets": pets, "partners": partners, "mounts": mounts}, rows.Err()
}

func push(tx *rt.Tx, characterID string) {
	tx.After(func() {
		snapshot, err := Snapshot(context.Background(), tx.Realm, characterID)
		if err == nil {
			tx.Realm.Push(characterID, "character.updated", map[string]any{"companions": snapshot})
		}
	})
}

// ownedPet 是共享的归属 seam：锁定并校验宠物属于该角色。
func ownedPet(tx *rt.Tx, characterID, petUID string) (int64, error) {
	if !rt.ValidUUID(petUID) {
		return 0, rt.Fail("not_owner", "宠物不属于该角色")
	}
	var owner string
	var version int64
	err := tx.QueryRowContext(tx.Ctx, `SELECT character_id, state_version FROM pets WHERE game_id=$1 AND realm_id=$2 AND pet_uid=$3::uuid FOR UPDATE`,
		tx.Realm.GameID, tx.Realm.RealmID, petUID).Scan(&owner, &version)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != characterID) {
		return 0, rt.Fail("not_owner", "宠物不属于该角色")
	}
	return version, err
}

func activatePet(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		PetUID string `json:"pet_uid"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	if c.Player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能更换出战宠物")
	}
	characterID := c.Player.CharacterID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		if _, err := ownedPet(tx, characterID, input.PetUID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(tx.Ctx, `UPDATE pets SET active=false, state_version=state_version+1 WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND active`,
			tx.Realm.GameID, tx.Realm.RealmID, characterID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(tx.Ctx, `UPDATE pets SET active=true, state_version=state_version+1 WHERE game_id=$1 AND realm_id=$2 AND pet_uid=$3::uuid`,
			tx.Realm.GameID, tx.Realm.RealmID, input.PetUID); err != nil {
			return err
		}
		push(tx, characterID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"pet_uid": input.PetUID, "active": true}, nil
}

func restPet(c *rt.Ctx) (any, *rt.ProtocolError) {
	if c.Player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能召回宠物")
	}
	characterID := c.Player.CharacterID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		if _, err := tx.ExecContext(tx.Ctx, `UPDATE pets SET active=false, state_version=state_version+1 WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND active`,
			tx.Realm.GameID, tx.Realm.RealmID, characterID); err != nil {
			return err
		}
		push(tx, characterID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"active": false}, nil
}

func renamePet(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		PetUID string `json:"pet_uid"`
		Name   string `json:"name"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	if count := utf8.RuneCountInString(input.Name); count < 1 || count > 12 {
		return nil, rt.Fail("invalid_name", "宠物名需 1–12 个字符")
	}
	characterID := c.Player.CharacterID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		version, err := ownedPet(tx, characterID, input.PetUID)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(tx.Ctx, `UPDATE pets SET name=$4, state_version=state_version+1 WHERE game_id=$1 AND realm_id=$2 AND pet_uid=$3::uuid AND state_version=$5`,
			tx.Realm.GameID, tx.Realm.RealmID, input.PetUID, input.Name, version)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows != 1 {
			return rt.ErrVersionConflict
		}
		push(tx, characterID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"pet_uid": input.PetUID, "name": input.Name}, nil
}

func grantPetFromNPC(c *rt.Ctx, _ *catalog.NPC, option catalog.DialogOption, key string) (any, *rt.ProtocolError) {
	pet := c.Content().Pets[option.Action.TargetID]
	if pet == nil {
		return nil, rt.Fail("invalid_pet", "宠物不存在")
	}
	characterID := c.Player.CharacterID
	result, _, err := c.Realm.Operation(c.Context, characterID, "pet.grant", key, pet.ContentID, func(tx *rt.Tx) (any, error) {
		uid := rt.NewUUID()
		inserted, err := tx.ExecContext(tx.Ctx, `INSERT INTO pets (game_id, realm_id, pet_uid, character_id, pet_id, name, source_key) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`,
			tx.Realm.GameID, tx.Realm.RealmID, uid, characterID, pet.ContentID, pet.Public.DisplayName, "npc:"+pet.ContentID)
		if err != nil {
			return nil, err
		}
		if rows, _ := inserted.RowsAffected(); rows == 0 {
			return nil, rt.Fail("already_claimed", "已经领取过该宠物")
		}
		push(tx, characterID)
		return map[string]any{"pet_uid": uid}, nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"result": rt.RawJSON(result), "dialog": nil}, nil
}

func petExperience(tx *rt.Tx, payload any) error {
	fact := payload.(quest.VictoryFact)
	content := tx.Realm.Content()
	encounter := content.Encounters[fact.EncounterID]
	if encounter == nil {
		return nil
	}
	var exp int64
	for _, group := range encounter.Server.Monsters {
		if monster := content.Monsters[group.MonsterID]; monster != nil {
			exp += monster.Server.ExpReward * int64(group.Count) / 2
		}
	}
	for _, characterID := range fact.Participants {
		var uid, petID string
		var level int
		var current int64
		err := tx.QueryRowContext(tx.Ctx, `SELECT pet_uid::text, pet_id, level, exp FROM pets WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND active FOR UPDATE`,
			tx.Realm.GameID, tx.Realm.RealmID, characterID).Scan(&uid, &petID, &level, &current)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		pet := content.Pets[petID]
		if pet == nil {
			continue
		}
		total := current + exp
		newLevel := catalog.LevelForExp(pet.Server.LevelExp, total)
		if _, err := tx.ExecContext(tx.Ctx, `UPDATE pets SET exp=$4, level=$5, state_version=state_version+1 WHERE game_id=$1 AND realm_id=$2 AND pet_uid=$3::uuid`,
			tx.Realm.GameID, tx.Realm.RealmID, uid, total, max(level, newLevel)); err != nil {
			return err
		}
		push(tx, characterID)
	}
	return nil
}

func recruit(c *rt.Ctx, _ *catalog.NPC, option catalog.DialogOption, key string) (any, *rt.ProtocolError) {
	partner := c.Content().Partners[option.Action.TargetID]
	if partner == nil {
		return nil, rt.Fail("invalid_partner", "伙伴不存在")
	}
	if c.Player.State.Level < partner.Public.MinLevel {
		return nil, rt.Fail("level_too_low", "等级不足，无法招募")
	}
	characterID := c.Player.CharacterID
	result, _, err := c.Realm.Operation(c.Context, characterID, "partner.recruit", key, partner.ContentID, func(tx *rt.Tx) (any, error) {
		if partner.Public.RequiredQuestID != "" {
			var status string
			err := tx.QueryRowContext(tx.Ctx, `SELECT status FROM quest_states WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND quest_id=$4`,
				tx.Realm.GameID, tx.Realm.RealmID, characterID, partner.Public.RequiredQuestID).Scan(&status)
			if err != nil || status != "completed" {
				return nil, rt.Fail("condition_failed", "需要先完成前置任务")
			}
		}
		inserted, err := tx.ExecContext(tx.Ctx, `INSERT INTO partners (game_id, realm_id, character_id, partner_id) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			tx.Realm.GameID, tx.Realm.RealmID, characterID, partner.ContentID)
		if err != nil {
			return nil, err
		}
		if rows, _ := inserted.RowsAffected(); rows == 0 {
			return nil, rt.Fail("already_recruited", "已经招募过该伙伴")
		}
		if partner.Public.RecruitCost > 0 {
			if _, err := tx.AdjustCurrency(characterID, -partner.Public.RecruitCost, "recruit:"+partner.ContentID); err != nil {
				return nil, err
			}
		}
		push(tx, characterID)
		return map[string]any{"partner_id": partner.ContentID}, nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"result": rt.RawJSON(result), "dialog": nil}, nil
}

func formation(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		PartnerID   string `json:"partner_id"`
		InFormation bool   `json:"in_formation"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	if c.Player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能调整编队")
	}
	characterID := c.Player.CharacterID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		var current bool
		err := tx.QueryRowContext(tx.Ctx, `SELECT in_formation FROM partners WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND partner_id=$4 FOR UPDATE`,
			tx.Realm.GameID, tx.Realm.RealmID, characterID, input.PartnerID).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return rt.Fail("not_owner", "尚未招募该伙伴")
		}
		if err != nil {
			return err
		}
		if input.InFormation && !current {
			var count int
			if err := tx.QueryRowContext(tx.Ctx, `SELECT count(*) FROM partners WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND in_formation`,
				tx.Realm.GameID, tx.Realm.RealmID, characterID).Scan(&count); err != nil {
				return err
			}
			if count >= maxFormation {
				return rt.Fail("formation_full", "助战编队最多 2 名伙伴")
			}
		}
		if _, err := tx.ExecContext(tx.Ctx, `UPDATE partners SET in_formation=$5 WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND partner_id=$4`,
			tx.Realm.GameID, tx.Realm.RealmID, characterID, input.PartnerID, input.InFormation); err != nil {
			return err
		}
		push(tx, characterID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"partner_id": input.PartnerID, "in_formation": input.InFormation}, nil
}

func equipMount(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		MountID string `json:"mount_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	mount := c.Content().Mounts[input.MountID]
	if mount == nil {
		return nil, rt.Fail("invalid_mount", "坐骑不存在于当前 Release")
	}
	if c.Player.State.Level < mount.Public.MinLevel {
		return nil, rt.Fail("level_too_low", "等级不足，无法骑乘")
	}
	if c.Player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能骑乘")
	}
	return setMount(c, input.MountID)
}

func unequipMount(c *rt.Ctx) (any, *rt.ProtocolError) {
	if c.Player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能下马")
	}
	return setMount(c, "")
}

func setMount(c *rt.Ctx, mountID string) (any, *rt.ProtocolError) {
	characterID := c.Player.CharacterID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		record, err := tx.LoadCharacter(characterID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(tx.Ctx, `UPDATE mounts SET equipped=false WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND equipped`,
			tx.Realm.GameID, tx.Realm.RealmID, characterID); err != nil {
			return err
		}
		if mountID != "" {
			result, err := tx.ExecContext(tx.Ctx, `UPDATE mounts SET equipped=true WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND mount_id=$4`,
				tx.Realm.GameID, tx.Realm.RealmID, characterID, mountID)
			if err != nil {
				return err
			}
			if rows, _ := result.RowsAffected(); rows != 1 {
				return rt.Fail("not_owner", "尚未拥有该坐骑")
			}
		}
		record.State.MountID = mountID
		rt.ClampVitals(c.Content(), &record.State)
		if err := tx.SaveCharacter(record); err != nil {
			return err
		}
		tx.SyncAndNotify(record)
		push(tx, characterID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	c.Realm.Broadcast(c.Player.State.MapID, characterID, "world.entity_moved", map[string]any{"character_id": characterID, "x": c.Player.State.X, "y": c.Player.State.Y, "mount_id": mountID})
	return map[string]any{"mount_id": mountID}, nil
}
