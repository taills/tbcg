// Package trade 是跨玩家交易 owner（任务 0015.8）：报价、锁定、双确认后在单个 PostgreSQL 事务中
// 按确定锁顺序转移双方资产；取消、超时、断线与未知提交都不会复制或丢失资产。
package trade

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/taills/tbcg/server/internal/content/mapdata"
	rt "github.com/taills/tbcg/server/modules/runtime"
)

const inviteDistance = 5

// Offer 是一方放入的物品与货币。
type Offer struct {
	Items    []rt.Stack `json:"items"`
	Currency int64      `json:"currency"`
}

// Session 是一次交易会话。
type Session struct {
	ID        string
	A, B      string
	Status    string // invited | open | committing | done | cancelled
	Offers    map[string]Offer
	Locked    map[string]bool
	Confirmed map[string]bool
	Version   int64
	// OfferVersion 只在报价变化时递增；锁定与确认必须携带它，防止对方改价后的调包。
	OfferVersion int64
	CommitTries  int
	Expires      time.Time
	ReleaseID    string
}

type registry struct {
	sessions map[string]*Session
	byMember map[string]string
}

func state(r *rt.Realm) *registry {
	return r.Module("trade", func() any { return &registry{sessions: map[string]*Session{}, byMember: map[string]string{}} }).(*registry)
}

func init() {
	rt.Register("trade.invite", true, invite)
	rt.Register("trade.respond", true, respond)
	rt.Register("trade.offer", true, offer)
	rt.Register("trade.lock", true, lock)
	rt.Register("trade.confirm", true, confirm)
	rt.Register("trade.cancel", true, cancel)
	rt.OnTick(tick)
	rt.OnDisconnect(func(r *rt.Realm, p *rt.Player) {
		if s := current(r, p.CharacterID); s != nil && s.Status != "committing" {
			end(r, s, "cancelled", "partner_disconnected")
		}
	})
}

func current(r *rt.Realm, characterID string) *Session {
	reg := state(r)
	return reg.sessions[reg.byMember[characterID]]
}

func view(s *Session) map[string]any {
	return map[string]any{"trade_id": s.ID, "party_a": s.A, "party_b": s.B, "status": s.Status, "offers": s.Offers, "locked": s.Locked,
		"confirmed": s.Confirmed, "state_version": s.Version, "offer_version": s.OfferVersion, "expires_in_seconds": max(0, int(time.Until(s.Expires).Seconds()))}
}

func publish(r *rt.Realm, s *Session) {
	s.Version++
	for _, id := range []string{s.A, s.B} {
		r.Push(id, "trade.updated", view(s))
	}
}

func end(r *rt.Realm, s *Session, status, reason string) {
	reg := state(r)
	s.Status = status
	delete(reg.byMember, s.A)
	delete(reg.byMember, s.B)
	delete(reg.sessions, s.ID)
	s.Version++
	data := view(s)
	data["reason"] = reason
	for _, id := range []string{s.A, s.B} {
		r.Push(id, "trade.updated", data)
	}
}

func timeout(c *rt.Ctx) time.Duration {
	rules := c.Content().Trade
	return time.Duration(rules.Public.TimeoutSeconds) * time.Second
}

func invite(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		CharacterName string `json:"character_name"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	if c.Content().Trade == nil {
		return nil, rt.Fail("feature_disabled", "当前 Release 未开放交易")
	}
	target := c.Realm.PlayerByName(input.CharacterName)
	me := c.Player
	if target == nil || target.CharacterID == me.CharacterID {
		return nil, rt.Fail("target_offline", "目标不在线")
	}
	if current(c.Realm, me.CharacterID) != nil || current(c.Realm, target.CharacterID) != nil {
		return nil, rt.Fail("trade_busy", "一方正在交易中")
	}
	if me.BattleID != "" || target.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能交易")
	}
	anchor := mapdata.Anchor{Cell: mapdata.Cell{X: me.State.X, Y: me.State.Y}, Extent: mapdata.Cell{X: 1, Y: 1}}
	if target.State.MapID != me.State.MapID || anchor.Distance(mapdata.Cell{X: target.State.X, Y: target.State.Y}) > inviteDistance {
		return nil, rt.Fail("target_too_far", "目标不在附近")
	}
	s := &Session{ID: rt.NewUUID(), A: me.CharacterID, B: target.CharacterID, Status: "invited", Offers: map[string]Offer{},
		Locked: map[string]bool{}, Confirmed: map[string]bool{}, Expires: c.Realm.Now().Add(timeout(c)), ReleaseID: c.Content().ReleaseID}
	reg := state(c.Realm)
	reg.sessions[s.ID] = s
	reg.byMember[s.A], reg.byMember[s.B] = s.ID, s.ID
	c.Realm.Push(target.CharacterID, "trade.invited", map[string]any{"trade_id": s.ID, "from_name": me.State.Name})
	publish(c.Realm, s)
	return view(s), nil
}

func mine(c *rt.Ctx, tradeID string) (*Session, *rt.ProtocolError) {
	s := current(c.Realm, c.Player.CharacterID)
	if s == nil || s.ID != tradeID {
		return nil, rt.Fail("no_trade", "没有该交易")
	}
	if c.Content().Trade == nil {
		return nil, rt.Fail("feature_disabled", "当前 Release 未开放交易")
	}
	return s, nil
}

func respond(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		TradeID string `json:"trade_id"`
		Accept  bool   `json:"accept"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	s, failure := mine(c, input.TradeID)
	if failure != nil {
		return nil, failure
	}
	if s.B != c.Player.CharacterID || s.Status != "invited" {
		return nil, rt.Fail("invalid_state", "交易状态不允许此操作")
	}
	if !input.Accept {
		end(c.Realm, s, "cancelled", "declined")
		return map[string]any{"trade_id": s.ID, "status": "cancelled"}, nil
	}
	s.Status = "open"
	s.Expires = c.Realm.Now().Add(timeout(c))
	publish(c.Realm, s)
	return view(s), nil
}

func offer(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		TradeID  string     `json:"trade_id"`
		Items    []rt.Stack `json:"items"`
		Currency int64      `json:"currency"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	s, failure := mine(c, input.TradeID)
	if failure != nil {
		return nil, failure
	}
	me := c.Player.CharacterID
	if s.Status != "open" || s.Locked[me] {
		return nil, rt.Fail("invalid_state", "交易未开放或己方已锁定")
	}
	rules := c.Content().Trade
	if len(input.Items) > rules.Public.MaxItemSlots || input.Currency < 0 || input.Currency > rules.Server.MaxCurrency {
		return nil, rt.Fail("invalid_offer", "报价超过格子或货币上限")
	}
	merged := map[string]int{}
	for _, stack := range input.Items {
		if c.Content().Items[stack.ItemID] == nil || stack.Quantity < 1 || contains(rules.Server.UntradableItemIDs, stack.ItemID) {
			return nil, rt.Fail("untradable", "包含不可交易的物品")
		}
		merged[stack.ItemID] += stack.Quantity
	}
	inventory, err := c.Realm.ReadInventory(c.Context, me)
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	for item, quantity := range merged {
		if inventory[item] < quantity {
			return nil, rt.Fail("insufficient_items", "物品数量不足")
		}
	}
	balance, err := c.Realm.ReadBalance(c.Context, me)
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	if balance < input.Currency {
		return nil, rt.Fail("insufficient_currency", "货币不足")
	}
	items := make([]rt.Stack, 0, len(merged))
	for item, quantity := range merged {
		items = append(items, rt.Stack{ItemID: item, Quantity: quantity})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ItemID < items[j].ItemID })
	s.Offers[me] = Offer{Items: items, Currency: input.Currency}
	s.OfferVersion++
	// 任意报价变化都撤销双方锁定与确认。
	s.Locked, s.Confirmed = map[string]bool{}, map[string]bool{}
	s.Expires = c.Realm.Now().Add(timeout(c))
	publish(c.Realm, s)
	return view(s), nil
}

type versionedInput struct {
	TradeID      string `json:"trade_id"`
	OfferVersion int64  `json:"offer_version"`
}

func lock(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input versionedInput
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	s, failure := mine(c, input.TradeID)
	if failure != nil {
		return nil, failure
	}
	if input.OfferVersion != s.OfferVersion {
		return nil, rt.Fail("offer_changed", "报价已变化，请确认最新报价后再锁定")
	}
	if s.Status != "open" {
		return nil, rt.Fail("invalid_state", "交易未开放")
	}
	s.Locked[c.Player.CharacterID] = true
	publish(c.Realm, s)
	return view(s), nil
}

func confirm(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input versionedInput
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	s, failure := mine(c, input.TradeID)
	if failure != nil {
		return nil, failure
	}
	if input.OfferVersion != s.OfferVersion {
		return nil, rt.Fail("offer_changed", "报价已变化，请确认最新报价后再确认")
	}
	if s.Status != "open" || !s.Locked[s.A] || !s.Locked[s.B] {
		return nil, rt.Fail("not_locked", "双方都锁定后才能确认")
	}
	if s.Confirmed[c.Player.CharacterID] {
		return nil, rt.Fail("already_confirmed", "已经确认")
	}
	s.Confirmed[c.Player.CharacterID] = true
	if s.Confirmed[s.A] && s.Confirmed[s.B] {
		s.Status = "committing"
		publish(c.Realm, s)
		commit(c.Context, c.Realm, s)
		return map[string]any{"trade_id": s.ID, "status": s.Status}, nil
	}
	publish(c.Realm, s)
	return view(s), nil
}

func cancel(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		TradeID string `json:"trade_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	s, failure := mine(c, input.TradeID)
	if failure != nil {
		return nil, failure
	}
	if s.Status == "committing" {
		return nil, rt.Fail("committing", "交易正在提交，不能取消")
	}
	end(c.Realm, s, "cancelled", "cancelled_by_player")
	return map[string]any{"trade_id": s.ID, "status": "cancelled"}, nil
}

// commit 以 trade_id 为幂等键；按角色 ID 顺序加锁，统一事务完成双方转移、税费、账本与审计记录。
func commit(ctx context.Context, r *rt.Realm, s *Session) {
	content := r.Content()
	rules := content.Trade
	if rules == nil {
		end(r, s, "cancelled", "feature_disabled")
		return
	}
	s.CommitTries++
	payload := map[string]any{"a": s.A, "b": s.B, "offers": s.Offers}
	_, _, err := r.Operation(ctx, "system:trade", "trade.commit", s.ID, payload, func(tx *rt.Tx) (any, error) {
		ids := []string{s.A, s.B}
		sort.Strings(ids)
		for _, id := range ids {
			if _, err := tx.LoadCharacter(id); err != nil {
				return nil, err
			}
		}
		var tax int64
		// 统一锁序：先双方钱包（按 ID 顺序），再背包，与商店购买的“先钱包后背包”一致。
		for _, id := range ids {
			if _, err := tx.Balance(id); err != nil {
				return nil, err
			}
		}
		for _, pair := range [][2]string{{s.A, s.B}, {s.B, s.A}} {
			from, to := pair[0], pair[1]
			if o := s.Offers[from]; o.Currency > 0 {
				fee := o.Currency * rules.Public.TaxBP / 10000
				tax += fee
				if _, err := tx.AdjustCurrency(from, -o.Currency, "trade_out:"+from); err != nil {
					return nil, err
				}
				if _, err := tx.AdjustCurrency(to, o.Currency-fee, "trade_in:"+to); err != nil {
					return nil, err
				}
			}
		}
		for _, pair := range [][2]string{{s.A, s.B}, {s.B, s.A}} {
			from, to := pair[0], pair[1]
			for _, stack := range s.Offers[from].Items {
				if err := tx.RemoveItem(from, stack.ItemID, stack.Quantity); err != nil {
					return nil, err
				}
				if err := tx.AddItem(to, stack.ItemID, stack.Quantity); err != nil {
					return nil, err
				}
			}
		}
		if _, err := tx.ExecContext(tx.Ctx, `INSERT INTO trade_records (game_id, realm_id, trade_id, party_a, party_b, offer_a, offer_b, tax, release_id)
VALUES ($1,$2,$3::uuid,$4,$5,$6::jsonb,$7::jsonb,$8,$9)`, tx.Realm.GameID, tx.Realm.RealmID, s.ID, s.A, s.B, rt.MustJSON(s.Offers[s.A]), rt.MustJSON(s.Offers[s.B]), tax, s.ReleaseID); err != nil {
			return nil, err
		}
		if err := tx.Emit("trade", "trade.completed", map[string]any{"trade_id": s.ID, "tax": tax}); err != nil {
			return nil, err
		}
		return map[string]any{"trade_id": s.ID, "tax": tax}, nil
	})
	if err != nil {
		var failure *rt.ProtocolError
		if pe := rt.FailFromError(err); pe != nil && pe.Code != "outcome_unknown" && pe.Code != "internal_error" {
			failure = pe
		}
		if failure != nil {
			end(r, s, "cancelled", failure.Code)
			return
		}
		// 提交结果未知：保持 committing，下一次 tick 用同一 trade_id 重试核验；超过上限后以数据库记录为准收尾。
		log.Printf("trade commit pending trade=%s try=%d: %v", s.ID, s.CommitTries, err)
		if s.CommitTries >= 5 {
			var committed bool
			_ = r.DB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM trade_records WHERE game_id=$1 AND realm_id=$2 AND trade_id=$3::uuid)`, r.GameID, r.RealmID, s.ID).Scan(&committed)
			if committed {
				end(r, s, "done", "completed")
			} else {
				end(r, s, "cancelled", "commit_failed")
			}
			return
		}
		s.Expires = r.Now().Add(2 * time.Second)
		return
	}
	for _, id := range []string{s.A, s.B} {
		items, _ := r.ReadInventory(ctx, id)
		balance, _ := r.ReadBalance(ctx, id)
		r.Push(id, "character.updated", map[string]any{"inventory": map[string]any{"items": items, "balance": balance}})
	}
	end(r, s, "done", "completed")
}

func tick(r *rt.Realm, now time.Time) {
	for _, s := range state(r).sessions {
		if now.Before(s.Expires) {
			continue
		}
		if s.Status == "committing" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			commit(ctx, r, s)
			cancel()
			continue
		}
		end(r, s, "cancelled", "timeout")
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
