// Package event 实现活动日历、参与进度、领奖与单 Realm 简单排行榜（任务 0015.9）。
// 开放窗口只依据服务器可信时钟；客户端时间不参与任何判定。
package event

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"example.com/tbcg/server/internal/content/catalog"
	"example.com/tbcg/server/modules/dungeon"
	"example.com/tbcg/server/modules/quest"
	rt "example.com/tbcg/server/modules/runtime"
)

func init() {
	rt.Register("event.list", true, list)
	rt.Register("event.claim", true, claim)
	rt.Register("event.leaderboard", true, leaderboard)
	rt.OnFact("battle.victory", func(tx *rt.Tx, payload any) error {
		fact := payload.(quest.VictoryFact)
		return progress(tx, "defeat_encounter", fact.EncounterID, fact.Participants, "battle:"+fact.BattleID)
	})
	rt.OnFact("dungeon.completed", func(tx *rt.Tx, payload any) error {
		fact := payload.(dungeon.CompletionFact)
		return progress(tx, "complete_dungeon", fact.DungeonID, fact.Members, "dungeon:"+fact.RunID)
	})
}

// OpenWindow 返回 now 所在的开放窗口。
func OpenWindow(e *catalog.Event, now time.Time) (time.Time, time.Time, bool) {
	for _, window := range e.Public.Windows {
		start, err1 := time.Parse(time.RFC3339, window.Start)
		end, err2 := time.Parse(time.RFC3339, window.End)
		if err1 == nil && err2 == nil && !now.Before(start) && now.Before(end) {
			return start, end, true
		}
	}
	return time.Time{}, time.Time{}, false
}

func progress(tx *rt.Tx, kind, targetID string, characters []string, factKey string) error {
	now := tx.Realm.Now()
	for _, e := range tx.Realm.Content().Events {
		if e.Public.Objective.Kind != kind || e.Public.Objective.TargetID != targetID {
			continue
		}
		start, _, open := OpenWindow(e, now)
		if !open {
			continue
		}
		for _, characterID := range characters {
			fresh, err := tx.Fact(characterID, "event:"+e.ContentID, factKey)
			if err != nil {
				return err
			}
			if !fresh {
				continue
			}
			if _, err := tx.ExecContext(tx.Ctx, `INSERT INTO event_progress (game_id, realm_id, event_id, window_start, character_id, progress, score) VALUES ($1,$2,$3,$4,$5,1,1)
ON CONFLICT (game_id, realm_id, event_id, window_start, character_id) DO UPDATE SET progress=event_progress.progress+1, score=event_progress.score+1, updated_at=now()`,
				tx.Realm.GameID, tx.Realm.RealmID, e.ContentID, start, characterID); err != nil {
				return err
			}
		}
	}
	return nil
}

func list(c *rt.Ctx) (any, *rt.ProtocolError) {
	now := c.Realm.Now()
	ids := make([]string, 0, len(c.Content().Events))
	for id := range c.Content().Events {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	events := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		e := c.Content().Events[id]
		start, end, open := OpenWindow(e, now)
		entry := map[string]any{"event_id": id, "open": open, "objective": e.Public.Objective, "max_claims_per_window": e.Public.MaxClaimsPerWindow, "server_time": now.UTC().Format(time.RFC3339)}
		if open {
			var value, claims int
			err := c.Realm.DB.QueryRowContext(c.Context, `SELECT progress, claims FROM event_progress WHERE game_id=$1 AND realm_id=$2 AND event_id=$3 AND window_start=$4 AND character_id=$5`,
				c.Realm.GameID, c.Realm.RealmID, id, start, c.Player.CharacterID).Scan(&value, &claims)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, rt.FailFromError(err)
			}
			entry["window_start"], entry["window_end"], entry["progress"], entry["claims"] = start.Format(time.RFC3339), end.Format(time.RFC3339), value, claims
		}
		events = append(events, entry)
	}
	return map[string]any{"events": events}, nil
}

func claim(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		EventID        string `json:"event_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	e := c.Content().Events[input.EventID]
	if e == nil {
		return nil, rt.Fail("invalid_event", "活动不存在")
	}
	start, _, open := OpenWindow(e, c.Realm.Now())
	if !open {
		return nil, rt.Fail("event_closed", "活动不在开放时间")
	}
	characterID := c.Player.CharacterID
	result, _, err := c.Realm.Operation(c.Context, characterID, "event.claim", input.IdempotencyKey, map[string]any{"event": e.ContentID, "window": start}, func(tx *rt.Tx) (any, error) {
		var value, claims int
		err := tx.QueryRowContext(tx.Ctx, `SELECT progress, claims FROM event_progress WHERE game_id=$1 AND realm_id=$2 AND event_id=$3 AND window_start=$4 AND character_id=$5 FOR UPDATE`,
			tx.Realm.GameID, tx.Realm.RealmID, e.ContentID, start, characterID).Scan(&value, &claims)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, rt.Fail("objective_incomplete", "活动目标尚未完成")
		}
		if err != nil {
			return nil, err
		}
		if claims >= e.Public.MaxClaimsPerWindow {
			return nil, rt.Fail("already_claimed", "本期奖励已领取")
		}
		if value < e.Public.Objective.Count*(claims+1) {
			return nil, rt.Fail("objective_incomplete", "活动目标尚未完成")
		}
		if _, err := tx.ExecContext(tx.Ctx, `UPDATE event_progress SET claims=claims+1, updated_at=now() WHERE game_id=$1 AND realm_id=$2 AND event_id=$3 AND window_start=$4 AND character_id=$5`,
			tx.Realm.GameID, tx.Realm.RealmID, e.ContentID, start, characterID); err != nil {
			return nil, err
		}
		if err := quest.GrantRewards(tx, c.Content(), characterID, e.Server.Rewards, "event:"+e.ContentID); err != nil {
			return nil, err
		}
		return map[string]any{"event_id": e.ContentID, "rewards": e.Server.Rewards}, nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return rt.RawJSON(result), nil
}

func leaderboard(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		EventID string `json:"event_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	e := c.Content().Events[input.EventID]
	if e == nil || !e.Public.Leaderboard {
		return nil, rt.Fail("no_leaderboard", "该活动没有排行榜")
	}
	start, _, open := OpenWindow(e, c.Realm.Now())
	if !open {
		return map[string]any{"event_id": e.ContentID, "entries": []any{}}, nil
	}
	rows, err := c.Realm.DB.QueryContext(context.Background(), `SELECT character_id, score FROM event_progress WHERE game_id=$1 AND realm_id=$2 AND event_id=$3 AND window_start=$4
ORDER BY score DESC, updated_at ASC, character_id LIMIT 20`, c.Realm.GameID, c.Realm.RealmID, e.ContentID, start)
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	type row struct {
		id    string
		score int64
	}
	var ranked []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.id, &item.score); err != nil {
			_ = rows.Close()
			return nil, rt.FailFromError(err)
		}
		ranked = append(ranked, item)
	}
	_ = rows.Close()
	entries := make([]map[string]any, 0, len(ranked))
	for i, item := range ranked {
		entries = append(entries, map[string]any{"rank": i + 1, "character_id": item.id, "name": c.Realm.CharacterName(c.Context, item.id), "score": item.score})
	}
	return map[string]any{"event_id": e.ContentID, "window_start": start.Format(time.RFC3339), "entries": entries}, nil
}
