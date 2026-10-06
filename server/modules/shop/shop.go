// Package shop 实现 NPC 商店购买（任务 0015.5）：价格、限购与库存只以服务端 Release 为准，扣款发货同事务。
package shop

import (
	"context"
	"database/sql"
	"errors"

	"example.com/tbcg/server/internal/content/catalog"
	"example.com/tbcg/server/internal/content/mapdata"
	rt "example.com/tbcg/server/modules/runtime"
)

func init() {
	rt.RegisterDialogAction("open_shop", open)
	rt.Register("shop.buy", true, buy)
}

func nearShop(c *rt.Ctx, shop *catalog.Shop) bool {
	npc := c.Content().NPCs[shop.Public.NPCID]
	if npc == nil || npc.Public.MapID != c.Player.State.MapID {
		return false
	}
	anchor, ok := c.Content().Geometries[npc.Public.MapID].Anchor(npc.Public.AnchorID)
	return ok && anchor.Distance(mapdata.Cell{X: c.Player.State.X, Y: c.Player.State.Y}) <= npc.Server.InteractRangeCells
}

// View 返回商店对该角色的视图（含已购数量与剩余库存）。
func View(ctx context.Context, r *rt.Realm, shop *catalog.Shop, characterID string) (map[string]any, error) {
	goods := make([]map[string]any, 0, len(shop.Public.Goods))
	for _, good := range shop.Public.Goods {
		entry := map[string]any{"item_id": good.ItemID, "price": good.Price, "purchase_limit": good.PurchaseLimit}
		var bought int
		err := r.DB.QueryRowContext(ctx, `SELECT quantity FROM shop_purchases WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND shop_id=$4 AND item_id=$5`,
			r.GameID, r.RealmID, characterID, shop.ContentID, good.ItemID).Scan(&bought)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		entry["purchased"] = bought
		for _, stock := range shop.Server.Stock {
			if stock.ItemID == good.ItemID {
				remaining := stock.Quantity
				err := r.DB.QueryRowContext(ctx, `SELECT remaining FROM shop_stock WHERE game_id=$1 AND realm_id=$2 AND shop_id=$3 AND item_id=$4`,
					r.GameID, r.RealmID, shop.ContentID, good.ItemID).Scan(&remaining)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return nil, err
				}
				entry["stock_remaining"] = remaining
			}
		}
		goods = append(goods, entry)
	}
	return map[string]any{"shop_id": shop.ContentID, "display_name": shop.Public.DisplayName, "currency_code": shop.Public.CurrencyCode, "goods": goods}, nil
}

func open(c *rt.Ctx, _ *catalog.NPC, option catalog.DialogOption, _ string) (any, *rt.ProtocolError) {
	shop := c.Content().Shops[option.Action.TargetID]
	if shop == nil {
		return nil, rt.Fail("invalid_shop", "商店不存在")
	}
	view, err := View(c.Context, c.Realm, shop, c.Player.CharacterID)
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"shop": view, "dialog": nil}, nil
}

func buy(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		ShopID         string `json:"shop_id"`
		ItemID         string `json:"item_id"`
		Quantity       int    `json:"quantity"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	shop := c.Content().Shops[input.ShopID]
	if shop == nil || !nearShop(c, shop) {
		return nil, rt.Fail("shop_unavailable", "商店不可用或距离太远")
	}
	good, ok := shop.Good(input.ItemID)
	if !ok || input.Quantity < 1 || input.Quantity > 99 {
		return nil, rt.Fail("invalid_good", "商品或数量无效")
	}
	characterID := c.Player.CharacterID
	result, _, err := c.Realm.Operation(c.Context, characterID, "shop.buy", input.IdempotencyKey, input, func(tx *rt.Tx) (any, error) {
		// 先锁定钱包行，串行化同一角色的并发购买（首次购买时限购行尚不存在，无法依赖其行锁）。
		if _, err := tx.Balance(characterID); err != nil {
			return nil, err
		}
		if good.PurchaseLimit > 0 {
			var bought int
			err := tx.QueryRowContext(tx.Ctx, `SELECT quantity FROM shop_purchases WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND shop_id=$4 AND item_id=$5 FOR UPDATE`,
				tx.Realm.GameID, tx.Realm.RealmID, characterID, shop.ContentID, good.ItemID).Scan(&bought)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if bought+input.Quantity > good.PurchaseLimit {
				return nil, rt.Fail("purchase_limit", "超过限购数量")
			}
		}
		for _, stock := range shop.Server.Stock {
			if stock.ItemID != good.ItemID {
				continue
			}
			if _, err := tx.ExecContext(tx.Ctx, `INSERT INTO shop_stock (game_id, realm_id, shop_id, item_id, remaining) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
				tx.Realm.GameID, tx.Realm.RealmID, shop.ContentID, good.ItemID, stock.Quantity); err != nil {
				return nil, err
			}
			updated, err := tx.ExecContext(tx.Ctx, `UPDATE shop_stock SET remaining=remaining-$5 WHERE game_id=$1 AND realm_id=$2 AND shop_id=$3 AND item_id=$4 AND remaining >= $5`,
				tx.Realm.GameID, tx.Realm.RealmID, shop.ContentID, good.ItemID, input.Quantity)
			if err != nil {
				return nil, err
			}
			if rows, _ := updated.RowsAffected(); rows != 1 {
				return nil, rt.Fail("out_of_stock", "库存不足")
			}
		}
		total := good.Price * int64(input.Quantity)
		balance, err := tx.AdjustCurrency(characterID, -total, "shop:"+shop.ContentID+":"+good.ItemID)
		if err != nil {
			return nil, err
		}
		if err := tx.AddItem(characterID, good.ItemID, input.Quantity); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(tx.Ctx, `INSERT INTO shop_purchases (game_id, realm_id, character_id, shop_id, item_id, quantity) VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (game_id, realm_id, character_id, shop_id, item_id) DO UPDATE SET quantity = shop_purchases.quantity + EXCLUDED.quantity`,
			tx.Realm.GameID, tx.Realm.RealmID, characterID, shop.ContentID, good.ItemID, input.Quantity); err != nil {
			return nil, err
		}
		if err := tx.Emit("purchase", "shop.purchased", map[string]any{"character_id": characterID, "shop_id": shop.ContentID, "item_id": good.ItemID, "quantity": input.Quantity, "total": total}); err != nil {
			return nil, err
		}
		tx.After(func() {
			items, _ := tx.Realm.ReadInventory(context.Background(), characterID)
			tx.Realm.Push(characterID, "character.updated", map[string]any{"inventory": map[string]any{"items": items, "balance": balance}})
		})
		return map[string]any{"item_id": good.ItemID, "quantity": input.Quantity, "total": total, "balance": balance}, nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return rt.RawJSON(result), nil
}
