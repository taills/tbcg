// Package dungeon 是双人多阶段副本实例 owner（任务 0012.3）：准入、阶段推进、限次与每位参与者的幂等奖励。
// 副本实例固定创建时的 Release；进行中发布新 Release 不改变其 Definition、遭遇与奖励。
package dungeon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"example.com/tbcg/server/internal/content/catalog"
	"example.com/tbcg/server/modules/battle"
	"example.com/tbcg/server/modules/combat"
	"example.com/tbcg/server/modules/party"
	"example.com/tbcg/server/modules/quest"
	rt "example.com/tbcg/server/modules/runtime"
)

// RunRow 是 dungeon_runs 的权威行。
type RunRow struct {
	RunID      string          `json:"run_id"`
	DungeonID  string          `json:"dungeon_id"`
	ReleaseID  string          `json:"release_id"`
	Definition catalog.Dungeon `json:"-"`
	Members    []string        `json:"members"`
	StageIndex int             `json:"stage_index"`
	Status     string          `json:"status"`
	Version    int64           `json:"state_version"`
	Claimed    []string        `json:"claimed"`
	PartyID    string          `json:"party_id"`
}

// CompletionFact 是副本完成的可信事实（活动模块消费）。
type CompletionFact struct {
	RunID     string
	DungeonID string
	Members   []string
}

func init() {
	rt.Register("dungeon.start_stage", true, startStage)
	rt.Register("dungeon.claim", true, claim)
	rt.Register("dungeon.leave", true, leave)
	rt.RegisterDialogAction("enter_dungeon", enter)
	party.OnLeave(onPartyLeave)
	rt.RegisterSnapshot("dungeon", func(ctx context.Context, r *rt.Realm, p *rt.Player) (any, error) {
		run, err := activeFor(ctx, r.DB, r, p.CharacterID)
		if err != nil || run == nil {
			return nil, err
		}
		return viewOf(run), nil
	})
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanRun(row *sql.Row) (*RunRow, error) {
	var run RunRow
	var definition, members []byte
	err := row.Scan(&run.RunID, &run.DungeonID, &run.ReleaseID, &definition, &members, &run.StageIndex, &run.Status, &run.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(definition, &run.Definition); err != nil {
		return nil, err
	}
	var stored struct {
		Members []string `json:"members"`
		PartyID string   `json:"party_id"`
	}
	if err := json.Unmarshal(members, &stored.Members); err != nil {
		return nil, err
	}
	run.Members = stored.Members
	return &run, nil
}

const runColumns = `run_id::text, dungeon_id, release_id, definition, members, stage_index, status, state_version`

func load(ctx context.Context, q rowQueryer, r *rt.Realm, runID string, lock bool) (*RunRow, error) {
	query := `SELECT ` + runColumns + ` FROM dungeon_runs WHERE game_id=$1 AND realm_id=$2 AND run_id=$3::uuid`
	if lock {
		query += " FOR UPDATE"
	}
	return scanRun(q.QueryRowContext(ctx, query, r.GameID, r.RealmID, runID))
}

func activeFor(ctx context.Context, q rowQueryer, r *rt.Realm, characterID string) (*RunRow, error) {
	run, err := scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM dungeon_runs d
WHERE game_id=$1 AND realm_id=$2 AND members ? $3 AND (status='active' OR (status='completed' AND NOT EXISTS (
  SELECT 1 FROM dungeon_rewards w WHERE w.game_id=d.game_id AND w.realm_id=d.realm_id AND w.run_id=d.run_id AND w.character_id=$3)))
ORDER BY created_at DESC LIMIT 1`, r.GameID, r.RealmID, characterID))
	if err != nil || run == nil {
		return run, err
	}
	run.Claimed, err = claimedMembers(ctx, r, run.RunID)
	return run, err
}

func claimedMembers(ctx context.Context, r *rt.Realm, runID string) ([]string, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT character_id FROM dungeon_rewards WHERE game_id=$1 AND realm_id=$2 AND run_id=$3::uuid`, r.GameID, r.RealmID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func viewOf(run *RunRow) map[string]any {
	return map[string]any{"run_id": run.RunID, "dungeon_id": run.DungeonID, "release_id": run.ReleaseID, "members": run.Members,
		"stage_index": run.StageIndex, "stage_count": len(run.Definition.Server.Stages), "stage_names": run.Definition.Public.StageNames,
		"status": run.Status, "state_version": run.Version, "claimed": run.Claimed}
}

func publish(r *rt.Realm, run *RunRow) {
	view := viewOf(run)
	for _, id := range run.Members {
		r.Push(id, "dungeon.updated", view)
	}
}

func saveRun(tx *rt.Tx, run *RunRow) error {
	result, err := tx.ExecContext(tx.Ctx, `UPDATE dungeon_runs SET members=$4::jsonb, stage_index=$5, status=$6, state_version=state_version+1, updated_at=now()
WHERE game_id=$1 AND realm_id=$2 AND run_id=$3::uuid AND state_version=$7`, tx.Realm.GameID, tx.Realm.RealmID, run.RunID, rt.MustJSON(run.Members), run.StageIndex, run.Status, run.Version)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return rt.ErrVersionConflict
	}
	run.Version++
	return nil
}

func enter(c *rt.Ctx, npc *catalog.NPC, option catalog.DialogOption, _ string) (any, *rt.ProtocolError) {
	content := c.Content()
	dungeon := content.Dungeons[option.Action.TargetID]
	if dungeon == nil || dungeon.Server.EntryNPCID != npc.ContentID {
		return nil, rt.Fail("invalid_dungeon", "该 NPC 不是此副本入口")
	}
	p := party.Of(c.Realm, c.Player.CharacterID)
	if p == nil || p.Leader != c.Player.CharacterID {
		return nil, rt.Fail("not_leader", "需要由队长带队进入副本")
	}
	if p.Busy != "" {
		return nil, rt.Fail("party_busy", "队伍正在战斗或副本中")
	}
	members := party.OnlineMembersOnMap(c.Realm, p, c.Player.State.MapID)
	if len(members) != len(p.Members) || len(members) < dungeon.Public.MinPlayers || len(members) > dungeon.Public.MaxPlayers {
		return nil, rt.Fail("party_size", "需要全部队员在线且在附近，人数符合副本要求")
	}
	for _, id := range members {
		member := c.Realm.Player(id)
		if member.State.Level < dungeon.Public.MinLevel {
			return nil, rt.Fail("level_too_low", member.State.Name+" 等级不足")
		}
		if !p.Ready[id] && id != p.Leader {
			return nil, rt.Fail("not_ready", member.State.Name+" 尚未准备")
		}
		if member.BattleID != "" {
			return nil, rt.Fail("member_in_battle", "有成员正在战斗")
		}
	}
	definition, _ := json.Marshal(dungeon)
	runID := rt.NewUUID()
	today := c.Realm.Now().UTC().Format("2006-01-02")
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		for _, id := range members {
			var used int
			if err := tx.QueryRowContext(tx.Ctx, `SELECT count(*) FROM dungeon_entries WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND dungeon_id=$4 AND entry_day=$5::date`,
				tx.Realm.GameID, tx.Realm.RealmID, id, dungeon.ContentID, today).Scan(&used); err != nil {
				return err
			}
			if used >= dungeon.Public.DailyLimit {
				return rt.Fail("daily_limit", c.Realm.Player(id).State.Name+" 今日进入次数已用完")
			}
		}
		if _, err := tx.ExecContext(tx.Ctx, `INSERT INTO dungeon_runs (game_id, realm_id, run_id, dungeon_id, release_id, definition, members) VALUES ($1,$2,$3::uuid,$4,$5,$6::jsonb,$7::jsonb)`,
			tx.Realm.GameID, tx.Realm.RealmID, runID, dungeon.ContentID, content.ReleaseID, definition, rt.MustJSON(members)); err != nil {
			return err
		}
		for _, id := range members {
			if _, err := tx.ExecContext(tx.Ctx, `INSERT INTO dungeon_entries (game_id, realm_id, character_id, dungeon_id, run_id, entry_day) VALUES ($1,$2,$3,$4,$5::uuid,$6::date)`,
				tx.Realm.GameID, tx.Realm.RealmID, id, dungeon.ContentID, runID, today); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	p.Busy = "dungeon:" + runID
	party.Publish(c.Realm, p)
	run, err := load(c.Context, c.Realm.DB, c.Realm, runID, false)
	if err != nil || run == nil {
		return nil, rt.FailFromError(err)
	}
	publish(c.Realm, run)
	c.Player.DialogNPC, c.Player.DialogNode = "", ""
	return map[string]any{"dungeon": viewOf(run), "dialog": nil}, nil
}

func startStage(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		RunID string `json:"run_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	if !rt.ValidUUID(input.RunID) {
		return nil, rt.Fail("invalid_run", "副本实例 ID 无效")
	}
	run, err := load(c.Context, c.Realm.DB, c.Realm, input.RunID, false)
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	p := party.Of(c.Realm, c.Player.CharacterID)
	if run == nil || run.Status != "active" || p == nil || p.Leader != c.Player.CharacterID || p.Busy != "dungeon:"+run.RunID {
		return nil, rt.Fail("not_allowed", "只有副本中的队长可以开始阶段")
	}
	if len(run.Members) < run.Definition.Public.MinPlayers {
		return nil, rt.Fail("party_size", "副本成员不足最少人数，不能继续")
	}
	var members []*rt.Player
	for _, id := range run.Members {
		player := c.Realm.Player(id)
		if player == nil || party.Of(c.Realm, id) != p {
			return nil, rt.Fail("member_missing", "有副本成员离线或已离队，请等待重连或让其离开副本")
		}
		members = append(members, player)
	}
	content, err := c.Realm.CatalogFor(c.Context, run.ReleaseID)
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	stage := run.Definition.Server.Stages[run.StageIndex]
	stageIndex, version := run.StageIndex, run.Version
	battleRun, failure := combat.Start(c.Context, c.Realm, stage.EncounterID, members, p.ID, combat.Options{
		Source: "dungeon:" + run.RunID, Content: content,
		OnSettle: func(tx *rt.Tx, b *combat.Run) error {
			current, err := load(tx.Ctx, tx.Tx, tx.Realm, run.RunID, true)
			if err != nil || current == nil {
				return err
			}
			if current.Version != version || current.StageIndex != stageIndex || current.Status != "active" {
				return nil // 已被其他路径推进（重放），不重复推进
			}
			switch b.State.Outcome {
			case battle.OutcomeVictory:
				current.StageIndex++
				if current.StageIndex >= len(current.Definition.Server.Stages) {
					current.Status = "completed"
					if err := tx.RunFacts("dungeon.completed", CompletionFact{RunID: current.RunID, DungeonID: current.DungeonID, Members: current.Members}); err != nil {
						return err
					}
				}
			default:
				current.Status = "failed"
			}
			return saveRun(tx, current)
		},
		AfterSettle: func(*combat.Run) {
			c.Realm.Do(func() { refresh(c.Realm, run.RunID, p) })
		},
	})
	if failure != nil {
		return nil, failure
	}
	return map[string]any{"battle_id": battleRun.State.BattleID, "stage_index": stageIndex}, nil
}

func refresh(r *rt.Realm, runID string, p *party.Party) {
	run, err := load(context.Background(), r.DB, r, runID, false)
	if err != nil || run == nil {
		return
	}
	run.Claimed, _ = claimedMembers(context.Background(), r, runID)
	if run.Status != "active" && p != nil && p.Busy == "dungeon:"+runID {
		p.Busy = ""
		party.Publish(r, p)
	} else if run.Status == "active" && p != nil && p.Busy == "" {
		p.Busy = "dungeon:" + runID
	}
	publish(r, run)
}

func claim(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		RunID          string `json:"run_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	if !rt.ValidUUID(input.RunID) {
		return nil, rt.Fail("invalid_run", "副本实例 ID 无效")
	}
	characterID := c.Player.CharacterID
	result, _, err := c.Realm.Operation(c.Context, characterID, "dungeon.claim", input.IdempotencyKey, input.RunID, func(tx *rt.Tx) (any, error) {
		run, err := load(tx.Ctx, tx.Tx, tx.Realm, input.RunID, true)
		if err != nil {
			return nil, err
		}
		if run == nil || run.Status != "completed" {
			return nil, rt.Fail("not_completed", "副本尚未完成")
		}
		if !contains(run.Members, characterID) {
			return nil, rt.Fail("not_participant", "只有完成副本的参与者可以领奖")
		}
		inserted, err := tx.ExecContext(tx.Ctx, `INSERT INTO dungeon_rewards (game_id, realm_id, run_id, character_id) VALUES ($1,$2,$3::uuid,$4) ON CONFLICT DO NOTHING`,
			tx.Realm.GameID, tx.Realm.RealmID, run.RunID, characterID)
		if err != nil {
			return nil, err
		}
		if rows, _ := inserted.RowsAffected(); rows == 0 {
			return nil, rt.Fail("already_claimed", "已经领取过该副本奖励")
		}
		content, err := tx.Realm.CatalogFor(tx.Ctx, run.ReleaseID)
		if err != nil {
			return nil, err
		}
		if err := quest.GrantRewards(tx, content, characterID, run.Definition.Server.Rewards, "dungeon:"+run.RunID); err != nil {
			return nil, err
		}
		return map[string]any{"run_id": run.RunID, "rewards": run.Definition.Server.Rewards}, nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	if p := party.Of(c.Realm, characterID); p != nil {
		refresh(c.Realm, input.RunID, p)
	}
	return rt.RawJSON(result), nil
}

func removeMember(ctx context.Context, r *rt.Realm, runID, characterID string) error {
	return r.Tx(ctx, func(tx *rt.Tx) error {
		run, err := load(tx.Ctx, tx.Tx, tx.Realm, runID, true)
		if err != nil || run == nil || run.Status != "active" || !contains(run.Members, characterID) {
			return err
		}
		kept := run.Members[:0]
		for _, id := range run.Members {
			if id != characterID {
				kept = append(kept, id)
			}
		}
		run.Members = kept
		if len(run.Members) == 0 {
			run.Status = "abandoned"
		}
		return saveRun(tx, run)
	})
}

func leave(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		RunID string `json:"run_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	if c.Player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能离开副本")
	}
	if !rt.ValidUUID(input.RunID) {
		return nil, rt.Fail("invalid_run", "副本实例 ID 无效")
	}
	if err := removeMember(c.Context, c.Realm, input.RunID, c.Player.CharacterID); err != nil {
		return nil, rt.FailFromError(err)
	}
	if p := party.Of(c.Realm, c.Player.CharacterID); p != nil {
		refresh(c.Realm, input.RunID, p)
		if p.Busy == "dungeon:"+input.RunID {
			// 离开副本即失去资格；仍在队伍中但不再参与。
			party.Remove(c.Realm, p, c.Player.CharacterID)
		}
	}
	return map[string]any{"left": true}, nil
}

func onPartyLeave(r *rt.Realm, p *party.Party, characterID string) {
	if len(p.Busy) <= len("dungeon:") || p.Busy[:len("dungeon:")] != "dungeon:" {
		return
	}
	runID := p.Busy[len("dungeon:"):]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = removeMember(ctx, r, runID, characterID)
	if len(p.Members) == 0 {
		p.Busy = ""
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
