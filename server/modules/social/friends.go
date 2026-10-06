// Package social 实现好友申请/同意/删除与在线展示（任务 0015.6）。好友关系以有序对单行存储，
// 删除一行即双向解除，不会出现单边幽灵关系。
package social

import (
	"context"

	rt "github.com/taills/tbcg/server/modules/runtime"
)

func init() {
	rt.Register("friend.list", true, list)
	rt.Register("friend.request", true, request)
	rt.Register("friend.respond", true, respond)
	rt.Register("friend.remove", true, remove)
	rt.RegisterSnapshot("friends", func(ctx context.Context, r *rt.Realm, p *rt.Player) (any, error) { return View(ctx, r, p.CharacterID) })
}

func pair(a, b string) (string, string) {
	if a < b {
		return a, b
	}
	return b, a
}

// View 返回好友列表与待处理申请。
func View(ctx context.Context, r *rt.Realm, characterID string) (map[string]any, error) {
	friends := make([]map[string]any, 0)
	rows, err := r.DB.QueryContext(ctx, `SELECT CASE WHEN low_id=$3 THEN high_id ELSE low_id END FROM friendships
WHERE game_id=$1 AND realm_id=$2 AND (low_id=$3 OR high_id=$3) ORDER BY 1`, r.GameID, r.RealmID, characterID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	for _, id := range ids {
		friends = append(friends, map[string]any{"character_id": id, "name": r.CharacterName(ctx, id), "online": r.Player(id) != nil})
	}
	incoming, err := requests(ctx, r, `SELECT from_id FROM friend_requests WHERE game_id=$1 AND realm_id=$2 AND to_id=$3 ORDER BY created_at`, characterID)
	if err != nil {
		return nil, err
	}
	outgoing, err := requests(ctx, r, `SELECT to_id FROM friend_requests WHERE game_id=$1 AND realm_id=$2 AND from_id=$3 ORDER BY created_at`, characterID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"friends": friends, "incoming": incoming, "outgoing": outgoing}, nil
}

func requests(ctx context.Context, r *rt.Realm, query, characterID string) ([]map[string]any, error) {
	rows, err := r.DB.QueryContext(ctx, query, r.GameID, r.RealmID, characterID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{"character_id": id, "name": r.CharacterName(ctx, id)})
	}
	return out, nil
}

func notify(tx *rt.Tx, ids ...string) {
	tx.After(func() {
		for _, id := range ids {
			if view, err := View(context.Background(), tx.Realm, id); err == nil {
				tx.Realm.Push(id, "friend.updated", view)
			}
		}
	})
}

func list(c *rt.Ctx) (any, *rt.ProtocolError) {
	view, err := View(c.Context, c.Realm, c.Player.CharacterID)
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return view, nil
}

func countFriends(tx *rt.Tx, id string) (int, error) {
	var count int
	err := tx.QueryRowContext(tx.Ctx, `SELECT count(*) FROM friendships WHERE game_id=$1 AND realm_id=$2 AND (low_id=$3 OR high_id=$3)`, tx.Realm.GameID, tx.Realm.RealmID, id).Scan(&count)
	return count, err
}

func befriend(tx *rt.Tx, a, b string, maxFriends int) error {
	for _, id := range []string{a, b} {
		count, err := countFriends(tx, id)
		if err != nil {
			return err
		}
		if count >= maxFriends {
			return rt.Fail("friend_limit", "好友数量已达上限")
		}
	}
	low, high := pair(a, b)
	if _, err := tx.ExecContext(tx.Ctx, `INSERT INTO friendships (game_id, realm_id, low_id, high_id) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, tx.Realm.GameID, tx.Realm.RealmID, low, high); err != nil {
		return err
	}
	_, err := tx.ExecContext(tx.Ctx, `DELETE FROM friend_requests WHERE game_id=$1 AND realm_id=$2 AND ((from_id=$3 AND to_id=$4) OR (from_id=$4 AND to_id=$3))`, tx.Realm.GameID, tx.Realm.RealmID, a, b)
	return err
}

func request(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		CharacterName string `json:"character_name"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	rules := c.Content().Social
	if rules == nil {
		return nil, rt.Fail("feature_disabled", "当前 Release 未开放好友功能")
	}
	me := c.Player.CharacterID
	target, ok := c.Realm.CharacterIDByName(c.Context, input.CharacterName)
	if !ok || target == me {
		return nil, rt.Fail("invalid_target", "目标角色不存在")
	}
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		low, high := pair(me, target)
		var exists bool
		if err := tx.QueryRowContext(tx.Ctx, `SELECT EXISTS (SELECT 1 FROM friendships WHERE game_id=$1 AND realm_id=$2 AND low_id=$3 AND high_id=$4)`, tx.Realm.GameID, tx.Realm.RealmID, low, high).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return rt.Fail("already_friends", "已经是好友")
		}
		var reverse bool
		if err := tx.QueryRowContext(tx.Ctx, `SELECT EXISTS (SELECT 1 FROM friend_requests WHERE game_id=$1 AND realm_id=$2 AND from_id=$3 AND to_id=$4)`, tx.Realm.GameID, tx.Realm.RealmID, target, me).Scan(&reverse); err != nil {
			return err
		}
		if reverse {
			if err := befriend(tx, me, target, rules.Public.MaxFriends); err != nil {
				return err
			}
			notify(tx, me, target)
			return nil
		}
		var pending int
		if err := tx.QueryRowContext(tx.Ctx, `SELECT count(*) FROM friend_requests WHERE game_id=$1 AND realm_id=$2 AND from_id=$3`, tx.Realm.GameID, tx.Realm.RealmID, me).Scan(&pending); err != nil {
			return err
		}
		if pending >= rules.Server.MaxPendingRequests {
			return rt.Fail("too_many_requests", "待处理的好友申请过多")
		}
		result, err := tx.ExecContext(tx.Ctx, `INSERT INTO friend_requests (game_id, realm_id, from_id, to_id) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, tx.Realm.GameID, tx.Realm.RealmID, me, target)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return rt.Fail("duplicate_request", "已经发送过申请")
		}
		notify(tx, me, target)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"target_id": target}, nil
}

func respond(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		CharacterID string `json:"character_id"`
		Accept      bool   `json:"accept"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	rules := c.Content().Social
	if rules == nil {
		return nil, rt.Fail("feature_disabled", "当前 Release 未开放好友功能")
	}
	me := c.Player.CharacterID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		result, err := tx.ExecContext(tx.Ctx, `DELETE FROM friend_requests WHERE game_id=$1 AND realm_id=$2 AND from_id=$3 AND to_id=$4`, tx.Realm.GameID, tx.Realm.RealmID, input.CharacterID, me)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return rt.Fail("no_request", "没有该好友申请")
		}
		if input.Accept {
			if err := befriend(tx, me, input.CharacterID, rules.Public.MaxFriends); err != nil {
				return err
			}
		}
		notify(tx, me, input.CharacterID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"character_id": input.CharacterID, "accepted": input.Accept}, nil
}

func remove(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		CharacterID string `json:"character_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	me := c.Player.CharacterID
	low, high := pair(me, input.CharacterID)
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		result, err := tx.ExecContext(tx.Ctx, `DELETE FROM friendships WHERE game_id=$1 AND realm_id=$2 AND low_id=$3 AND high_id=$4`, tx.Realm.GameID, tx.Realm.RealmID, low, high)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return rt.Fail("not_friends", "不是好友")
		}
		notify(tx, me, input.CharacterID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"removed": input.CharacterID}, nil
}
