// Package quest 实现 NPC 对话、任务接取/推进/交付/领奖（任务 0010.1）与遭遇胜利推进（任务 0010.2）。
// 客户端只提交意图；距离、条件、目标与奖励全部由服务端在 PostgreSQL 事务中判定。
package quest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/taills/tbcg/server/internal/content/catalog"
	"github.com/taills/tbcg/server/internal/content/mapdata"
	rt "github.com/taills/tbcg/server/modules/runtime"
	"github.com/taills/tbcg/server/modules/world"
)

// State 是一个角色的任务实例。
type State struct {
	QuestID        string    `json:"quest_id"`
	Status         string    `json:"status"`
	StageIndex     int       `json:"stage_index"`
	StageProgress  int       `json:"stage_progress"`
	CompletedCount int       `json:"completed_count"`
	ReleaseID      string    `json:"release_id"`
	AcceptedAt     time.Time `json:"accepted_at"`
}

// VictoryFact 是 0011 战斗引擎产出的可信胜利事实。
type VictoryFact struct {
	BattleID     string
	EncounterID  string
	StartedAt    time.Time
	Participants []string
	PartyMembers []string // 同队且在场（同地图在线）的成员，含参与者
}

func init() {
	rt.Register("npc.interact", true, interact)
	rt.Register("npc.choose", true, choose)
	rt.Register("quest.abandon", true, abandon)
	rt.RegisterDialogAction("goto", gotoNode)
	rt.RegisterDialogAction("close", closeDialog)
	rt.RegisterDialogAction("accept_quest", accept)
	rt.RegisterDialogAction("deliver_item", deliver)
	rt.RegisterDialogAction("talk_objective", talk)
	rt.RegisterDialogAction("claim_quest", claim)
	rt.OnFact("world.zone_entered", onZone)
	rt.OnFact("battle.victory", onVictory)
	rt.RegisterSnapshot("quests", func(ctx context.Context, r *rt.Realm, p *rt.Player) (any, error) {
		return ReadAll(ctx, r.DB, r, p.CharacterID)
	})
	rt.ConditionChecker = checkCondition
}

type queryRower interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// ReadAll 读取角色全部任务状态。
func ReadAll(ctx context.Context, q queryRower, r *rt.Realm, characterID string) ([]State, error) {
	rows, err := q.QueryContext(ctx, `SELECT quest_id, status, stage_index, stage_progress, completed_count, release_id, accepted_at FROM quest_states
WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 ORDER BY quest_id`, r.GameID, r.RealmID, characterID)
	if err != nil {
		return nil, fmt.Errorf("read quests: %w", err)
	}
	defer rows.Close()
	out := make([]State, 0)
	for rows.Next() {
		var s State
		if err := rows.Scan(&s.QuestID, &s.Status, &s.StageIndex, &s.StageProgress, &s.CompletedCount, &s.ReleaseID, &s.AcceptedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func readOne(tx *rt.Tx, characterID, questID string, lock bool) (*State, error) {
	query := `SELECT quest_id, status, stage_index, stage_progress, completed_count, release_id, accepted_at FROM quest_states
WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND quest_id=$4`
	if lock {
		query += " FOR UPDATE"
	}
	var s State
	err := tx.QueryRowContext(tx.Ctx, query, tx.Realm.GameID, tx.Realm.RealmID, characterID, questID).Scan(&s.QuestID, &s.Status, &s.StageIndex, &s.StageProgress, &s.CompletedCount, &s.ReleaseID, &s.AcceptedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read quest state: %w", err)
	}
	return &s, nil
}

func save(tx *rt.Tx, characterID string, s *State) error {
	_, err := tx.ExecContext(tx.Ctx, `UPDATE quest_states SET status=$5, stage_index=$6, stage_progress=$7, completed_count=$8, updated_at=now()
WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND quest_id=$4`, tx.Realm.GameID, tx.Realm.RealmID, characterID, s.QuestID, s.Status, s.StageIndex, s.StageProgress, s.CompletedCount)
	return err
}

func notifyQuests(tx *rt.Tx, characterID string) {
	tx.After(func() {
		quests, err := ReadAll(context.Background(), tx.Realm.DB, tx.Realm, characterID)
		if err == nil {
			tx.Realm.Push(characterID, "character.updated", map[string]any{"quests": quests})
		}
	})
}

// ---- 对话 ----

func npcInRange(c *rt.Ctx, npcID string) (*catalog.NPC, *rt.ProtocolError) {
	content := c.Content()
	npc := content.NPCs[npcID]
	if npc == nil || npc.Public.MapID != c.Player.State.MapID {
		return nil, rt.Fail("npc_unavailable", "NPC 不在当前地图")
	}
	anchor, ok := content.Geometries[npc.Public.MapID].Anchor(npc.Public.AnchorID)
	if !ok || anchor.Distance(mapdata.Cell{X: c.Player.State.X, Y: c.Player.State.Y}) > npc.Server.InteractRangeCells {
		return nil, rt.Fail("too_far", "距离 NPC 太远")
	}
	if c.Player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能对话")
	}
	return npc, nil
}

// NodeView 返回过滤条件后的对话节点。
func NodeView(c *rt.Ctx, npc *catalog.NPC, node catalog.DialogNode) map[string]any {
	options := make([]map[string]any, 0, len(node.Options))
	for _, option := range node.Options {
		if option.Condition != nil && !checkCondition(c, option.Condition) {
			continue
		}
		options = append(options, map[string]any{"option_id": option.OptionID, "text": option.Text, "action": option.Action.Kind, "target_id": option.Action.TargetID})
	}
	c.Player.DialogNPC, c.Player.DialogNode = npc.ContentID, node.NodeID
	return map[string]any{"npc_id": npc.ContentID, "node_id": node.NodeID, "text": node.Text, "options": options}
}

func interact(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		NPCID string `json:"npc_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	npc, failure := npcInRange(c, input.NPCID)
	if failure != nil {
		return nil, failure
	}
	node, _ := npc.Node(npc.Server.StartNode)
	return map[string]any{"dialog": NodeView(c, npc, node)}, nil
}

func choose(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		NPCID          string `json:"npc_id"`
		NodeID         string `json:"node_id"`
		OptionID       string `json:"option_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	if c.Player.DialogNPC != input.NPCID || c.Player.DialogNode != input.NodeID {
		return nil, rt.Fail("dialog_stale", "对话已过期，请重新与 NPC 交谈")
	}
	npc, failure := npcInRange(c, input.NPCID)
	if failure != nil {
		return nil, failure
	}
	node, ok := npc.Node(input.NodeID)
	if !ok {
		return nil, rt.Fail("dialog_stale", "对话节点不存在")
	}
	for _, option := range node.Options {
		if option.OptionID != input.OptionID {
			continue
		}
		if option.Condition != nil && !checkCondition(c, option.Condition) {
			return nil, rt.Fail("condition_failed", "当前不满足该选项条件")
		}
		action, ok := rt.LookupDialogAction(option.Action.Kind)
		if !ok {
			return nil, rt.Fail("unsupported_action", "该对话动作尚未支持")
		}
		return action(c, npc, option, input.IdempotencyKey)
	}
	return nil, rt.Fail("invalid_option", "选项不存在")
}

func gotoNode(c *rt.Ctx, npc *catalog.NPC, option catalog.DialogOption, _ string) (any, *rt.ProtocolError) {
	node, ok := npc.Node(option.Action.NextNode)
	if !ok {
		return nil, rt.Fail("dialog_stale", "对话节点不存在")
	}
	return map[string]any{"dialog": NodeView(c, npc, node)}, nil
}

func closeDialog(c *rt.Ctx, _ *catalog.NPC, _ catalog.DialogOption, _ string) (any, *rt.ProtocolError) {
	c.Player.DialogNPC, c.Player.DialogNode = "", ""
	return map[string]any{"dialog": nil}, nil
}

// ---- 条件 ----

func checkCondition(c *rt.Ctx, condition *catalog.Condition) bool {
	state := c.Player.State
	if condition.MinLevel > 0 && state.Level < condition.MinLevel {
		return false
	}
	if condition.NoSect && state.SectID != "" {
		return false
	}
	if condition.SectID != "" && state.SectID != condition.SectID {
		return false
	}
	if condition.QuestID == "" {
		return true
	}
	quest := c.Content().Quests[condition.QuestID]
	if quest == nil {
		return false
	}
	var current *State
	_ = c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		var err error
		current, err = readOne(tx, c.Player.CharacterID, condition.QuestID, false)
		return err
	})
	switch condition.QuestStatus {
	case "available":
		return available(c, quest, current)
	case "active":
		return current != nil && current.Status == "active"
	case "objectives_done":
		return current != nil && current.Status == "objectives_done"
	case "completed":
		return current != nil && (current.Status == "completed" || current.CompletedCount > 0)
	case "not_completed":
		return current == nil || current.CompletedCount == 0
	}
	return true
}

func available(c *rt.Ctx, quest *catalog.Quest, current *State) bool {
	if c.Player.State.Level < quest.Public.MinLevel {
		return false
	}
	if current != nil && (current.Status != "completed" || quest.Server.RepeatPolicy != "repeatable") {
		return false
	}
	for _, prerequisite := range quest.Server.Prerequisites {
		var status string
		err := c.Realm.DB.QueryRowContext(c.Context, `SELECT status FROM quest_states WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND quest_id=$4`,
			c.Realm.GameID, c.Realm.RealmID, c.Player.CharacterID, prerequisite).Scan(&status)
		if err != nil || status != "completed" {
			return false
		}
	}
	return true
}

// ---- 动作 ----

func accept(c *rt.Ctx, npc *catalog.NPC, option catalog.DialogOption, key string) (any, *rt.ProtocolError) {
	quest := c.Content().Quests[option.Action.TargetID]
	if quest == nil || quest.Server.GiverNPCID != npc.ContentID {
		return nil, rt.Fail("invalid_quest", "该 NPC 不发布此任务")
	}
	characterID := c.Player.CharacterID
	result, _, err := c.Realm.Operation(c.Context, characterID, "quest.accept", key, map[string]string{"quest": quest.ContentID}, func(tx *rt.Tx) (any, error) {
		current, err := readOne(tx, characterID, quest.ContentID, true)
		if err != nil {
			return nil, err
		}
		if !available(c, quest, current) {
			return nil, rt.Fail("quest_unavailable", "当前不能接取该任务")
		}
		if current == nil {
			_, err = tx.ExecContext(tx.Ctx, `INSERT INTO quest_states (game_id, realm_id, character_id, quest_id, status, release_id) VALUES ($1,$2,$3,$4,'active',$5)`,
				tx.Realm.GameID, tx.Realm.RealmID, characterID, quest.ContentID, c.Content().ReleaseID)
		} else {
			_, err = tx.ExecContext(tx.Ctx, `UPDATE quest_states SET status='active', stage_index=0, stage_progress=0, release_id=$5, accepted_at=now(), updated_at=now()
WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND quest_id=$4`, tx.Realm.GameID, tx.Realm.RealmID, characterID, quest.ContentID, c.Content().ReleaseID)
		}
		if err != nil {
			return nil, err
		}
		notifyQuests(tx, characterID)
		return map[string]any{"quest_id": quest.ContentID, "status": "active"}, nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"result": rt.RawJSON(result), "dialog": nil}, nil
}

// advance 推进当前阶段；返回是否发生变化。
func advance(quest *catalog.Quest, s *State, amount int) bool {
	if s.Status != "active" || s.StageIndex >= len(quest.Server.Stages) {
		return false
	}
	objective := quest.Server.Stages[s.StageIndex].Objective
	s.StageProgress += amount
	if s.StageProgress >= objective.Count {
		s.StageIndex++
		s.StageProgress = 0
		if s.StageIndex >= len(quest.Server.Stages) {
			s.Status = "objectives_done"
		}
	}
	return true
}

func currentObjective(quest *catalog.Quest, s *State) (catalog.Objective, bool) {
	if s == nil || s.Status != "active" || s.StageIndex >= len(quest.Server.Stages) {
		return catalog.Objective{}, false
	}
	return quest.Server.Stages[s.StageIndex].Objective, true
}

func deliver(c *rt.Ctx, npc *catalog.NPC, option catalog.DialogOption, key string) (any, *rt.ProtocolError) {
	quest := c.Content().Quests[option.Action.TargetID]
	if quest == nil || quest.Server.TurnInNPCID != npc.ContentID {
		return nil, rt.Fail("invalid_quest", "该 NPC 不接收此任务的交付")
	}
	characterID := c.Player.CharacterID
	result, _, err := c.Realm.Operation(c.Context, characterID, "quest.deliver", key, map[string]string{"quest": quest.ContentID}, func(tx *rt.Tx) (any, error) {
		current, err := readOne(tx, characterID, quest.ContentID, true)
		if err != nil {
			return nil, err
		}
		objective, ok := currentObjective(quest, current)
		if !ok || objective.Kind != "deliver_item" {
			return nil, rt.Fail("not_deliverable", "任务当前阶段不需要交付物品")
		}
		need := objective.Count - current.StageProgress
		if err := tx.RemoveItem(characterID, objective.ItemID, need); err != nil {
			return nil, err
		}
		advance(quest, current, need)
		if err := save(tx, characterID, current); err != nil {
			return nil, err
		}
		notifyQuests(tx, characterID)
		notifyInventory(tx, characterID)
		return map[string]any{"quest_id": quest.ContentID, "status": current.Status, "delivered": need}, nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"result": rt.RawJSON(result), "dialog": nil}, nil
}

func talk(c *rt.Ctx, npc *catalog.NPC, option catalog.DialogOption, key string) (any, *rt.ProtocolError) {
	quest := c.Content().Quests[option.Action.TargetID]
	if quest == nil {
		return nil, rt.Fail("invalid_quest", "任务不存在")
	}
	characterID := c.Player.CharacterID
	result, _, err := c.Realm.Operation(c.Context, characterID, "quest.talk", key, map[string]string{"quest": quest.ContentID, "npc": npc.ContentID}, func(tx *rt.Tx) (any, error) {
		current, err := readOne(tx, characterID, quest.ContentID, true)
		if err != nil {
			return nil, err
		}
		objective, ok := currentObjective(quest, current)
		if !ok || objective.Kind != "talk_npc" || objective.NPCID != npc.ContentID {
			return nil, rt.Fail("not_talk_target", "任务当前阶段不需要与此 NPC 交谈")
		}
		advance(quest, current, 1)
		if err := save(tx, characterID, current); err != nil {
			return nil, err
		}
		notifyQuests(tx, characterID)
		return map[string]any{"quest_id": quest.ContentID, "status": current.Status}, nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"result": rt.RawJSON(result), "dialog": nil}, nil
}

func claim(c *rt.Ctx, npc *catalog.NPC, option catalog.DialogOption, key string) (any, *rt.ProtocolError) {
	content := c.Content()
	quest := content.Quests[option.Action.TargetID]
	if quest == nil || quest.Server.TurnInNPCID != npc.ContentID {
		return nil, rt.Fail("invalid_quest", "该 NPC 不发放此任务奖励")
	}
	characterID := c.Player.CharacterID
	result, _, err := c.Realm.Operation(c.Context, characterID, "quest.claim", key, map[string]string{"quest": quest.ContentID}, func(tx *rt.Tx) (any, error) {
		current, err := readOne(tx, characterID, quest.ContentID, true)
		if err != nil {
			return nil, err
		}
		if current == nil || current.Status != "objectives_done" {
			return nil, rt.Fail("not_claimable", "任务目标尚未完成或奖励已领取")
		}
		current.Status = "completed"
		current.CompletedCount++
		if err := save(tx, characterID, current); err != nil {
			return nil, err
		}
		if err := GrantRewards(tx, content, characterID, quest.Server.Rewards, "quest:"+quest.ContentID); err != nil {
			return nil, err
		}
		notifyQuests(tx, characterID)
		return map[string]any{"quest_id": quest.ContentID, "status": "completed", "rewards": quest.Server.Rewards}, nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"result": rt.RawJSON(result), "dialog": nil}, nil
}

// GrantRewards 在当前幂等操作中发放货币、经验与物品（同一事务写账本与 outbox）。
func GrantRewards(tx *rt.Tx, content *catalog.Catalog, characterID string, rewards catalog.Rewards, line string) error {
	if rewards.Currency > 0 {
		if _, err := tx.AdjustCurrency(characterID, rewards.Currency, line); err != nil {
			return err
		}
	}
	for _, stack := range rewards.Items {
		if err := tx.AddItem(characterID, stack.ItemID, stack.Quantity); err != nil {
			return err
		}
	}
	if rewards.Exp > 0 {
		record, err := tx.LoadCharacter(characterID)
		if err != nil {
			return err
		}
		rt.GrantExp(content, &record.State, rewards.Exp)
		if err := tx.SaveCharacter(record); err != nil {
			return err
		}
		tx.SyncAndNotify(record)
	}
	if err := tx.Emit("reward", "economy.rewarded", map[string]any{"character_id": characterID, "rewards": rewards, "source": line}); err != nil {
		return err
	}
	notifyInventory(tx, characterID)
	return nil
}

func notifyInventory(tx *rt.Tx, characterID string) {
	tx.After(func() {
		items, err := tx.Realm.ReadInventory(context.Background(), characterID)
		if err != nil {
			return
		}
		balance, _ := tx.Realm.ReadBalance(context.Background(), characterID)
		tx.Realm.Push(characterID, "character.updated", map[string]any{"inventory": map[string]any{"items": items, "balance": balance}})
	})
}

func abandon(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		QuestID string `json:"quest_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	characterID := c.Player.CharacterID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		result, err := tx.ExecContext(tx.Ctx, `DELETE FROM quest_states WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND quest_id=$4 AND status IN ('active','objectives_done') AND completed_count=0`,
			tx.Realm.GameID, tx.Realm.RealmID, characterID, input.QuestID)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			if _, err := tx.ExecContext(tx.Ctx, `UPDATE quest_states SET status='completed', stage_index=0, stage_progress=0 WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND quest_id=$4 AND status IN ('active','objectives_done') AND completed_count>0`,
				tx.Realm.GameID, tx.Realm.RealmID, characterID, input.QuestID); err != nil {
				return err
			}
		}
		notifyQuests(tx, characterID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"quest_id": input.QuestID, "abandoned": true}, nil
}

// ---- 事实消费 ----

func onZone(tx *rt.Tx, payload any) error {
	fact := payload.(world.ZoneFact)
	content := tx.Realm.Content()
	quests, err := ReadAll(tx.Ctx, tx.Tx, tx.Realm, fact.CharacterID)
	if err != nil {
		return err
	}
	changed := false
	for i := range quests {
		quest := content.Quests[quests[i].QuestID]
		if quest == nil {
			continue
		}
		objective, ok := currentObjective(quest, &quests[i])
		if !ok || objective.Kind != "enter_zone" || objective.MapID != fact.MapID || objective.AnchorID != fact.AnchorID {
			continue
		}
		advance(quest, &quests[i], 1)
		if err := save(tx, fact.CharacterID, &quests[i]); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		notifyQuests(tx, fact.CharacterID)
	}
	return nil
}

func onVictory(tx *rt.Tx, payload any) error {
	fact := payload.(VictoryFact)
	content := tx.Realm.Content()
	participants := map[string]bool{}
	for _, id := range fact.Participants {
		participants[id] = true
	}
	candidates := append([]string(nil), fact.PartyMembers...)
	for _, id := range fact.Participants {
		if !containsString(candidates, id) {
			candidates = append(candidates, id)
		}
	}
	for _, characterID := range candidates {
		quests, err := ReadAll(tx.Ctx, tx.Tx, tx.Realm, characterID)
		if err != nil {
			return err
		}
		changed := false
		for i := range quests {
			quest := content.Quests[quests[i].QuestID]
			if quest == nil {
				continue
			}
			objective, ok := currentObjective(quest, &quests[i])
			if !ok || objective.Kind != "defeat_encounter" || objective.EncounterID != fact.EncounterID {
				continue
			}
			if objective.ProgressScope == "personal" && !participants[characterID] {
				continue
			}
			if quests[i].AcceptedAt.After(fact.StartedAt) {
				continue // 战斗开始后才接取的任务不计入
			}
			fresh, err := tx.Fact(characterID, "quest_victory", fact.BattleID+"|"+quest.ContentID)
			if err != nil {
				return err
			}
			if !fresh {
				continue // 同一胜利事实重复投递
			}
			advance(quest, &quests[i], 1)
			if err := save(tx, characterID, &quests[i]); err != nil {
				return err
			}
			changed = true
		}
		if changed {
			notifyQuests(tx, characterID)
		}
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
