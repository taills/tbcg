// Package inventory 实现物品使用、装备穿脱与门派入门（任务 0012.1），所有变化在 PostgreSQL 事务中提交。
package inventory

import (
	"context"

	"example.com/tbcg/server/internal/content/catalog"
	rt "example.com/tbcg/server/modules/runtime"
)

func init() {
	rt.Register("inventory.use", true, use)
	rt.Register("inventory.equip", true, equip)
	rt.Register("inventory.unequip", true, unequip)
	rt.RegisterDialogAction("join_sect", joinSect)
	rt.RegisterSnapshot("inventory", func(ctx context.Context, r *rt.Realm, p *rt.Player) (any, error) {
		items, err := r.ReadInventory(ctx, p.CharacterID)
		if err != nil {
			return nil, err
		}
		balance, err := r.ReadBalance(ctx, p.CharacterID)
		return map[string]any{"items": items, "balance": balance}, err
	})
}

func pushInventory(tx *rt.Tx, characterID string) {
	tx.After(func() {
		items, err := tx.Realm.ReadInventory(context.Background(), characterID)
		if err != nil {
			return
		}
		balance, _ := tx.Realm.ReadBalance(context.Background(), characterID)
		tx.Realm.Push(characterID, "character.updated", map[string]any{"inventory": map[string]any{"items": items, "balance": balance}})
	})
}

// PushInventory 在提交后推送背包（供其他模块复用）。
func PushInventory(tx *rt.Tx, characterID string) { pushInventory(tx, characterID) }

func use(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		ItemID         string `json:"item_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	if c.Player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中请使用战斗指令使用物品")
	}
	content := c.Content()
	consumable := content.Consumables[input.ItemID]
	if consumable == nil {
		return nil, rt.Fail("not_usable", "该物品不可使用")
	}
	characterID := c.Player.CharacterID
	result, _, err := c.Realm.Operation(c.Context, characterID, "inventory.use", input.IdempotencyKey, input.ItemID, func(tx *rt.Tx) (any, error) {
		record, err := tx.LoadCharacter(characterID)
		if err != nil {
			return nil, err
		}
		granted := map[string]any{}
		for _, effect := range consumable.Server.Effects {
			if err := applyEffect(tx, content, record, effect, granted); err != nil {
				return nil, err
			}
		}
		if err := tx.RemoveItem(characterID, input.ItemID, 1); err != nil {
			return nil, err
		}
		rt.ClampVitals(content, &record.State)
		if err := tx.SaveCharacter(record); err != nil {
			return nil, err
		}
		tx.SyncAndNotify(record)
		pushInventory(tx, characterID)
		return map[string]any{"item_id": input.ItemID, "effects": granted}, nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return rt.RawJSON(result), nil
}

func applyEffect(tx *rt.Tx, content *catalog.Catalog, record *rt.Record, effect catalog.Effect, granted map[string]any) error {
	state := &record.State
	switch effect.Kind {
	case "heal_hp":
		state.HP += effect.Amount
		granted["heal_hp"] = effect.Amount
	case "heal_mp":
		state.MP += effect.Amount
		granted["heal_mp"] = effect.Amount
	case "grant_exp":
		rt.GrantExp(content, state, effect.Amount)
		granted["exp"] = effect.Amount
	case "grant_pet":
		pet := content.Pets[effect.TargetID]
		if pet == nil {
			return rt.Fail("invalid_effect", "宠物不存在于当前 Release")
		}
		petUID := rt.NewUUID()
		// source_key 绑定幂等键，事务重试不会重复产生宠物。
		if _, err := tx.ExecContext(tx.Ctx, `INSERT INTO pets (game_id, realm_id, pet_uid, character_id, pet_id, name, source_key) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			tx.Realm.GameID, tx.Realm.RealmID, petUID, record.CharacterID, pet.ContentID, pet.Public.DisplayName, "item:"+tx.Key); err != nil {
			return err
		}
		granted["pet_uid"] = petUID
	case "grant_mount":
		mount := content.Mounts[effect.TargetID]
		if mount == nil {
			return rt.Fail("invalid_effect", "坐骑不存在于当前 Release")
		}
		result, err := tx.ExecContext(tx.Ctx, `INSERT INTO mounts (game_id, realm_id, character_id, mount_id) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			tx.Realm.GameID, tx.Realm.RealmID, record.CharacterID, mount.ContentID)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return rt.Fail("already_owned", "已经拥有该坐骑")
		}
		granted["mount_id"] = mount.ContentID
	default:
		return rt.Fail("invalid_effect", "未知物品效果")
	}
	return nil
}

func equip(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		ItemID string `json:"item_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	if c.Player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能更换装备")
	}
	content := c.Content()
	equipment := content.Equipment[input.ItemID]
	if equipment == nil {
		return nil, rt.Fail("not_equipment", "该物品不是装备")
	}
	state := c.Player.State
	if state.Level < equipment.Public.MinLevel {
		return nil, rt.Fail("level_too_low", "等级不足")
	}
	if len(equipment.Public.SectIDs) > 0 && !containsString(equipment.Public.SectIDs, state.SectID) {
		return nil, rt.Fail("sect_required", "门派不符合装备要求")
	}
	characterID := c.Player.CharacterID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		record, err := tx.LoadCharacter(characterID)
		if err != nil {
			return err
		}
		if err := tx.RemoveItem(characterID, input.ItemID, 1); err != nil {
			return err
		}
		if previous := record.State.Equipped[equipment.Public.Slot]; previous != "" {
			if err := tx.AddItem(characterID, previous, 1); err != nil {
				return err
			}
		}
		record.State.Equipped[equipment.Public.Slot] = input.ItemID
		rt.ClampVitals(content, &record.State)
		if err := tx.SaveCharacter(record); err != nil {
			return err
		}
		tx.SyncAndNotify(record)
		pushInventory(tx, characterID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"slot": equipment.Public.Slot, "item_id": input.ItemID}, nil
}

func unequip(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		Slot string `json:"slot"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	if c.Player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能更换装备")
	}
	characterID := c.Player.CharacterID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		record, err := tx.LoadCharacter(characterID)
		if err != nil {
			return err
		}
		itemID := record.State.Equipped[input.Slot]
		if itemID == "" {
			return rt.Fail("slot_empty", "该槽位没有装备")
		}
		delete(record.State.Equipped, input.Slot)
		if err := tx.AddItem(characterID, itemID, 1); err != nil {
			return err
		}
		rt.ClampVitals(c.Content(), &record.State)
		if err := tx.SaveCharacter(record); err != nil {
			return err
		}
		tx.SyncAndNotify(record)
		pushInventory(tx, characterID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"slot": input.Slot}, nil
}

func joinSect(c *rt.Ctx, npc *catalog.NPC, option catalog.DialogOption, _ string) (any, *rt.ProtocolError) {
	content := c.Content()
	sect := content.Sects[option.Action.TargetID]
	if sect == nil || (sect.Server.JoinNPCID != "" && sect.Server.JoinNPCID != npc.ContentID) {
		return nil, rt.Fail("invalid_sect", "该 NPC 不负责此门派入门")
	}
	characterID := c.Player.CharacterID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		record, err := tx.LoadCharacter(characterID)
		if err != nil {
			return err
		}
		if record.State.SectID != "" {
			return rt.Fail("already_in_sect", "已经加入门派")
		}
		if record.State.Level < sect.Public.MinLevel {
			return rt.Fail("level_too_low", "等级不足，不能入门")
		}
		record.State.SectID = sect.ContentID
		if err := tx.SaveCharacter(record); err != nil {
			return err
		}
		tx.SyncAndNotify(record)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"sect_id": sect.ContentID, "dialog": nil}, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
