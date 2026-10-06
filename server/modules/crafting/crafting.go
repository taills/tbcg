// Package crafting 实现配方制作（任务 0015.4）：扣料与产出在同一幂等事务中原子提交。
package crafting

import (
	"crypto/rand"
	"encoding/binary"
	"sort"

	"example.com/tbcg/server/internal/content/catalog"
	"example.com/tbcg/server/internal/content/mapdata"
	rt "example.com/tbcg/server/modules/runtime"
)

func init() {
	rt.RegisterDialogAction("open_crafting", open)
	rt.Register("craft.make", true, make_)
}

type stations struct{ byCharacter map[string]string }

func stationState(r *rt.Realm) *stations {
	return r.Module("crafting", func() any { return &stations{byCharacter: map[string]string{}} }).(*stations)
}

func open(c *rt.Ctx, npc *catalog.NPC, _ catalog.DialogOption, _ string) (any, *rt.ProtocolError) {
	stationState(c.Realm).byCharacter[c.Player.CharacterID] = npc.ContentID
	content := c.Content()
	ids := make([]string, 0, len(content.Recipes))
	for id := range content.Recipes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return map[string]any{"crafting": map[string]any{"npc_id": npc.ContentID, "recipe_ids": ids}, "dialog": nil}, nil
}

func atStation(c *rt.Ctx) bool {
	npcID := stationState(c.Realm).byCharacter[c.Player.CharacterID]
	npc := c.Content().NPCs[npcID]
	if npc == nil || npc.Public.MapID != c.Player.State.MapID {
		return false
	}
	anchor, ok := c.Content().Geometries[npc.Public.MapID].Anchor(npc.Public.AnchorID)
	return ok && anchor.Distance(mapdata.Cell{X: c.Player.State.X, Y: c.Player.State.Y}) <= npc.Server.InteractRangeCells
}

func make_(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		RecipeID       string `json:"recipe_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	recipe := c.Content().Recipes[input.RecipeID]
	if recipe == nil {
		return nil, rt.Fail("invalid_recipe", "配方不存在于当前 Release")
	}
	if !atStation(c) {
		return nil, rt.Fail("no_station", "需要在制作 NPC 附近打开制作界面")
	}
	if c.Player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能制作")
	}
	if c.Player.State.Level < recipe.Public.MinLevel {
		return nil, rt.Fail("level_too_low", "等级不足")
	}
	if recipe.Public.SectID != "" && c.Player.State.SectID != recipe.Public.SectID {
		return nil, rt.Fail("sect_required", "门派不符合配方要求")
	}
	characterID := c.Player.CharacterID
	result, _, err := c.Realm.Operation(c.Context, characterID, "craft.make", input.IdempotencyKey, input.RecipeID, func(tx *rt.Tx) (any, error) {
		for _, material := range recipe.Public.Materials {
			if err := tx.RemoveItem(characterID, material.ItemID, material.Quantity); err != nil {
				return nil, err
			}
		}
		// 成功判定使用服务端随机源；结果随 operation_results 固化，同键重放不会重新掷骰。
		var roll [8]byte
		if _, err := rand.Read(roll[:]); err != nil {
			return nil, err
		}
		success := int64(binary.BigEndian.Uint64(roll[:])%10000) < recipe.Server.SuccessRateBP // #nosec G115 -- 余数小于 10000。
		if success {
			for _, output := range recipe.Public.Outputs {
				if err := tx.AddItem(characterID, output.ItemID, output.Quantity); err != nil {
					return nil, err
				}
			}
		}
		if err := tx.Emit("craft", "crafting.completed", map[string]any{"character_id": characterID, "recipe_id": recipe.ContentID, "success": success}); err != nil {
			return nil, err
		}
		pushInventory(tx, characterID)
		return map[string]any{"recipe_id": recipe.ContentID, "success": success, "outputs": recipe.Public.Outputs}, nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return rt.RawJSON(result), nil
}

func pushInventory(tx *rt.Tx, characterID string) {
	tx.After(func() {
		items, err := tx.Realm.ReadInventory(tx.Ctx, characterID)
		if err != nil {
			return
		}
		balance, _ := tx.Realm.ReadBalance(tx.Ctx, characterID)
		tx.Realm.Push(characterID, "character.updated", map[string]any{"inventory": map[string]any{"items": items, "balance": balance}})
	})
}
