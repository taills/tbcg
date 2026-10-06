// Package combat 把纯函数战斗内核挂接到 Realm：发起遭遇、收集授权命令、回合超时与幂等结算（任务 0011/0010.2）。
package combat

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log"
	"sort"
	"time"

	"github.com/taills/tbcg/server/internal/content/catalog"
	"github.com/taills/tbcg/server/modules/battle"
	"github.com/taills/tbcg/server/modules/character"
	"github.com/taills/tbcg/server/modules/party"
	"github.com/taills/tbcg/server/modules/quest"
	rt "github.com/taills/tbcg/server/modules/runtime"
)

// Run 是一场进行中的战斗；Content 固定为开战时的 Release。
type Run struct {
	State        battle.State
	Content      *catalog.Catalog
	Commands     map[string]battle.Command
	Deadline     time.Time
	StartedAt    time.Time
	PartyID      string
	Source       string
	Settling     bool
	OnSettle     func(tx *rt.Tx, run *Run) error // 在结算同一事务内执行（副本阶段推进等）
	AfterSettle  func(run *Run)                  // 提交成功后的内存更新
	roundTimeout time.Duration
	settleTries  int
}

type registry struct{ runs map[string]*Run }

func state(r *rt.Realm) *registry {
	return r.Module("combat", func() any { return &registry{runs: map[string]*Run{}} }).(*registry)
}

// Get 返回战斗。
func Get(r *rt.Realm, battleID string) *Run { return state(r).runs[battleID] }

func init() {
	rt.Register("battle.command", true, command)
	rt.Register("battle.state", true, snapshot)
	rt.RegisterDialogAction("start_encounter", startFromNPC)
	rt.OnTick(tick)
	rt.OnEnter(restoreOnEnter)
}

// Options 控制发起战斗的上下文。
type Options struct {
	Source      string
	Content     *catalog.Catalog
	OnSettle    func(tx *rt.Tx, run *Run) error
	AfterSettle func(run *Run)
}

// Start 为一组在线角色发起遭遇战斗。
func Start(ctx context.Context, r *rt.Realm, encounterID string, members []*rt.Player, partyID string, options Options) (*Run, *rt.ProtocolError) {
	content := options.Content
	if content == nil {
		content = r.Content()
	}
	encounter := content.Encounters[encounterID]
	if encounter == nil {
		return nil, rt.Fail("invalid_encounter", "遭遇不存在于当前 Release")
	}
	combatants := make([]battle.Combatant, 0, len(members))
	for _, player := range members {
		if player.BattleID != "" {
			return nil, rt.Fail("member_in_battle", "有成员正在战斗中")
		}
		combatant, err := combatantFor(ctx, r, content, player)
		if err != nil {
			return nil, rt.FailFromError(err)
		}
		combatants = append(combatants, combatant)
	}
	battleID := rt.NewUUID()
	var seedBytes [8]byte
	if _, err := rand.Read(seedBytes[:]); err != nil {
		return nil, rt.FailFromError(err)
	}
	initial, err := battle.NewState(content, battleID, encounterID, binary.BigEndian.Uint64(seedBytes[:]), combatants)
	if err != nil {
		return nil, rt.Fail("invalid_encounter", err.Error())
	}
	timeout := time.Duration(encounter.Server.RoundTimeoutMS) * time.Millisecond
	run := &Run{State: initial, Content: content, Commands: map[string]battle.Command{}, StartedAt: r.Now(), Deadline: r.Now().Add(timeout),
		PartyID: partyID, Source: options.Source, OnSettle: options.OnSettle, AfterSettle: options.AfterSettle, roundTimeout: timeout}
	state(r).runs[battleID] = run
	for _, player := range members {
		player.BattleID = battleID
	}
	if p := party.Get(r, partyID); p != nil {
		p.Busy = "battle:" + battleID
		party.Publish(r, p)
	}
	for _, player := range members {
		r.Push(player.CharacterID, "battle.started", view(run, player.CharacterID))
		r.Broadcast(player.State.MapID, player.CharacterID, "world.entity_moved", map[string]any{"character_id": player.CharacterID, "x": player.State.X, "y": player.State.Y, "in_battle": true})
	}
	return run, nil
}

func combatantFor(ctx context.Context, r *rt.Realm, content *catalog.Catalog, player *rt.Player) (battle.Combatant, error) {
	loadout := player.State.Loadout()
	stats := character.Stats(content, loadout)
	inventory, err := r.ReadInventory(ctx, player.CharacterID)
	if err != nil {
		return battle.Combatant{}, err
	}
	combatant := battle.Combatant{CharacterID: player.CharacterID, Name: player.State.Name, Level: player.State.Level, Stats: stats,
		HP: player.State.HP, MP: player.State.MP, SkillIDs: character.Skills(content, loadout), Inventory: inventory}
	var petUID, petID, petName string
	var petLevel int
	err = r.DB.QueryRowContext(ctx, `SELECT pet_uid::text, pet_id, name, level FROM pets WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND active`,
		r.GameID, r.RealmID, player.CharacterID).Scan(&petUID, &petID, &petName, &petLevel)
	if err == nil {
		if pet := content.Pets[petID]; pet != nil {
			combatant.Pet = &battle.Companion{ID: petUID, DefinitionID: petID, Name: petName, Level: petLevel, Stats: character.PetStats(pet, petLevel), SkillIDs: pet.Public.SkillIDs}
		}
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT partner_id FROM partners WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND in_formation ORDER BY partner_id`,
		r.GameID, r.RealmID, player.CharacterID)
	if err != nil {
		return battle.Combatant{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var partnerID string
		if err := rows.Scan(&partnerID); err != nil {
			return battle.Combatant{}, err
		}
		if partner := content.Partners[partnerID]; partner != nil {
			combatant.Partners = append(combatant.Partners, battle.Companion{ID: player.CharacterID + ":" + partnerID, DefinitionID: partnerID,
				Name: partner.Public.DisplayName, Level: player.State.Level, Stats: partner.Server.Stats, SkillIDs: partner.Public.SkillIDs})
		}
	}
	return combatant, rows.Err()
}

func view(run *Run, characterID string) map[string]any {
	return map[string]any{"battle_id": run.State.BattleID, "encounter_id": run.State.EncounterID, "round": run.State.Round,
		"max_rounds": run.State.MaxRounds, "units": run.State.Units, "your_unit_id": "c:" + characterID, "outcome": run.State.Outcome,
		"deadline_ms": max(0, time.Until(run.Deadline).Milliseconds()), "submitted": submittedUnits(run), "release_id": run.Content.ReleaseID,
		"inventory": run.State.Inventory[characterID]}
}

func submittedUnits(run *Run) []string {
	ids := make([]string, 0, len(run.Commands))
	for id := range run.Commands {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// startFromNPC 处理对话动作 start_encounter：队长带同图在线队员，或单人开战。
func startFromNPC(c *rt.Ctx, _ *catalog.NPC, option catalog.DialogOption, _ string) (any, *rt.ProtocolError) {
	members := []*rt.Player{c.Player}
	partyID := ""
	if p := party.Of(c.Realm, c.Player.CharacterID); p != nil {
		if p.Leader != c.Player.CharacterID {
			return nil, rt.Fail("not_leader", "组队时只有队长可以发起战斗")
		}
		if p.Busy != "" {
			return nil, rt.Fail("party_busy", "队伍正在战斗或副本中")
		}
		members = nil
		for _, id := range party.OnlineMembersOnMap(c.Realm, p, c.Player.State.MapID) {
			members = append(members, c.Realm.Player(id))
		}
		partyID = p.ID
	}
	run, failure := Start(c.Context, c.Realm, option.Action.TargetID, members, partyID, Options{Source: "npc"})
	if failure != nil {
		return nil, failure
	}
	c.Player.DialogNPC, c.Player.DialogNode = "", ""
	return map[string]any{"battle_id": run.State.BattleID, "dialog": nil}, nil
}

func command(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		BattleID string         `json:"battle_id"`
		Round    int            `json:"round"`
		Command  battle.Command `json:"command"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	run := Get(c.Realm, input.BattleID)
	if run == nil || c.Player.BattleID != input.BattleID {
		return nil, rt.Fail("not_in_battle", "不在该战斗中")
	}
	if run.Settling || run.State.Outcome != "" {
		return nil, rt.Fail("battle_over", "战斗已结束")
	}
	if input.Round != run.State.Round {
		return nil, rt.Fail("stale_round", "回合已推进")
	}
	unitID := "c:" + c.Player.CharacterID
	input.Command.ActorID = unitID
	if err := battle.ValidateCommand(run.Content, run.State, c.Player.CharacterID, input.Command); err != nil {
		return nil, rt.Fail("invalid_command", err.Error())
	}
	if _, exists := run.Commands[unitID]; exists {
		return nil, rt.Fail("already_submitted", "本回合已提交命令")
	}
	run.Commands[unitID] = input.Command
	if allSubmitted(run) {
		resolveRound(c.Context, c.Realm, run)
	} else {
		for _, id := range run.State.Participants() {
			c.Realm.Push(id, "battle.round", map[string]any{"battle_id": run.State.BattleID, "round": run.State.Round, "submitted": submittedUnits(run)})
		}
	}
	return map[string]any{"accepted": true, "round": input.Round}, nil
}

func snapshot(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		BattleID string `json:"battle_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	run := Get(c.Realm, input.BattleID)
	if run == nil || c.Player.BattleID != input.BattleID {
		return nil, rt.Fail("not_in_battle", "不在该战斗中")
	}
	return view(run, c.Player.CharacterID), nil
}

func allSubmitted(run *Run) bool {
	for _, id := range run.State.PlayerUnits() {
		if _, ok := run.Commands[id]; !ok {
			return false
		}
	}
	return true
}

const maxSettleTries = 5

func tick(r *rt.Realm, now time.Time) {
	for _, run := range state(r).runs {
		if !now.After(run.Deadline) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if run.Settling {
			settle(ctx, r, run)
		} else {
			resolveRound(ctx, r, run)
		}
		cancel()
	}
}

// restoreOnEnter 在断线重连或顶号后恢复进行中战斗的运行态并重发战斗视图。
func restoreOnEnter(r *rt.Realm, player *rt.Player) {
	for _, run := range state(r).runs {
		for _, id := range run.State.Participants() {
			if id == player.CharacterID {
				player.BattleID = run.State.BattleID
				r.Push(id, "battle.started", view(run, id))
				return
			}
		}
	}
	player.BattleID = ""
}

// resolveRound 在深拷贝上计算；只有计算成功后才替换权威状态。
func resolveRound(ctx context.Context, r *rt.Realm, run *Run) {
	next, events, err := battle.Resolve(run.Content, run.State, run.Commands)
	if err != nil {
		log.Printf("battle resolve failed battle=%s: %v", run.State.BattleID, err)
		return
	}
	run.State = next
	run.Commands = map[string]battle.Command{}
	run.Deadline = r.Now().Add(run.roundTimeout)
	for _, id := range run.State.Participants() {
		r.Push(id, "battle.round", map[string]any{"battle_id": run.State.BattleID, "round": run.State.Round, "events": events,
			"units": run.State.Units, "outcome": run.State.Outcome, "deadline_ms": run.roundTimeout.Milliseconds()})
	}
	if run.State.Outcome != "" {
		run.Settling = true
		settle(ctx, r, run)
	}
}

// settle 以 battle_id 为幂等键结算；失败或结果未知时保留候选并在下一次 tick 用同键重试。
func settle(ctx context.Context, r *rt.Realm, run *Run) {
	run.settleTries++
	spoils := battle.ComputeSpoils(run.Content, run.State)
	partyMembers := []string{}
	if p := party.Get(r, run.PartyID); p != nil {
		if leader := r.Player(p.Leader); leader != nil {
			partyMembers = party.OnlineMembersOnMap(r, p, leader.State.MapID)
		}
	}
	participants := run.State.Participants()
	result, _, err := r.Operation(ctx, "system:battle", "battle.settle", run.State.BattleID, map[string]any{"outcome": run.State.Outcome, "rounds": run.State.Round}, func(tx *rt.Tx) (any, error) {
		tx.Content = run.Content
		if _, err := tx.ExecContext(ctx, `INSERT INTO battle_records (game_id, realm_id, battle_id, encounter_id, release_id, seed, outcome, rounds, participants, context)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10::jsonb)`, r.GameID, r.RealmID, run.State.BattleID, run.State.EncounterID, run.Content.ReleaseID,
			int64(run.State.Seed&0x7fffffffffffffff), run.State.Outcome, run.State.Round, rt.MustJSON(participants), rt.MustJSON(map[string]any{"source": run.Source})); err != nil {
			return nil, err
		}
		for _, unit := range run.State.Units {
			if unit.Kind != battle.KindCharacter {
				continue
			}
			if err := applyAftermath(tx, run, unit, spoilsFor(spoils, unit.OwnerID)); err != nil {
				return nil, err
			}
		}
		if run.State.Outcome == battle.OutcomeVictory {
			if err := tx.RunFacts("battle.victory", quest.VictoryFact{BattleID: run.State.BattleID, EncounterID: run.State.EncounterID,
				StartedAt: run.StartedAt, Participants: participants, PartyMembers: partyMembers}); err != nil {
				return nil, err
			}
		}
		if run.OnSettle != nil {
			if err := run.OnSettle(tx, run); err != nil {
				return nil, err
			}
		}
		return map[string]any{"battle_id": run.State.BattleID, "outcome": run.State.Outcome, "spoils": spoils}, nil
	})
	if err != nil {
		log.Printf("battle settlement pending battle=%s try=%d: %v", run.State.BattleID, run.settleTries, err)
		if run.settleTries >= maxSettleTries {
			// 达到上限：放弃本场收益并释放参战者，避免整队永久卡在战斗中；已提交的结算仍以 battle_id 幂等。
			log.Printf("battle settlement abandoned battle=%s", run.State.BattleID)
			finish(ctx, r, run, rt.MustJSON(map[string]any{"battle_id": run.State.BattleID, "outcome": run.State.Outcome, "settlement": "abandoned"}))
			return
		}
		run.Deadline = r.Now().Add(2 * time.Second)
		return
	}
	finish(ctx, r, run, result)
}

func spoilsFor(all []battle.Spoils, characterID string) *battle.Spoils {
	for i := range all {
		if all[i].CharacterID == characterID {
			return &all[i]
		}
	}
	return nil
}

func applyAftermath(tx *rt.Tx, run *Run, unit battle.Unit, spoils *battle.Spoils) error {
	record, err := tx.LoadCharacter(unit.OwnerID)
	if err != nil {
		return err
	}
	record.State.HP, record.State.MP = max(unit.HP, 1), unit.MP
	if err := consumeUsedItems(tx, unit.OwnerID, run.State.ItemsUsed[unit.OwnerID]); err != nil {
		return err
	}
	if spoils != nil {
		rt.GrantExp(run.Content, &record.State, spoils.Exp)
		if spoils.Currency > 0 {
			if _, err := tx.AdjustCurrency(unit.OwnerID, spoils.Currency, "loot:"+unit.OwnerID); err != nil {
				return err
			}
		}
		for _, stack := range spoils.Items {
			if err := tx.AddItem(unit.OwnerID, stack.ItemID, stack.Quantity); err != nil {
				var failure *rt.ProtocolError
				if errors.As(err, &failure) && (failure.Code == "inventory_full" || failure.Code == "invalid_item") {
					// 装不下的战利品被舍弃并记账，不阻塞整场结算。
					if emitErr := tx.Emit("loot_dropped:"+unit.OwnerID+":"+stack.ItemID, "battle.loot_dropped", map[string]any{"character_id": unit.OwnerID, "item": stack, "reason": failure.Code}); emitErr != nil {
						return emitErr
					}
					continue
				}
				return err
			}
		}
		if err := tx.Emit("loot:"+unit.OwnerID, "battle.loot_granted", spoils); err != nil {
			return err
		}
	}
	return tx.SaveCharacter(record)
}

func finish(ctx context.Context, r *rt.Realm, run *Run, result rt.RawJSON) {
	delete(state(r).runs, run.State.BattleID)
	if p := party.Get(r, run.PartyID); p != nil && p.Busy == "battle:"+run.State.BattleID {
		p.Busy = ""
		party.Publish(r, p)
	}
	for _, id := range run.State.Participants() {
		player := r.Player(id)
		if player == nil {
			continue
		}
		if player.BattleID == run.State.BattleID {
			player.BattleID = ""
		}
		// 无论首次提交还是幂等重放，都从数据库重载权威状态。
		_ = r.Tx(ctx, func(tx *rt.Tx) error {
			record, err := tx.LoadCharacter(id)
			if err != nil {
				return err
			}
			tx.SyncAndNotify(record)
			return nil
		})
		items, _ := r.ReadInventory(ctx, id)
		balance, _ := r.ReadBalance(ctx, id)
		r.Push(id, "battle.ended", map[string]any{"battle_id": run.State.BattleID, "outcome": run.State.Outcome, "result": result})
		r.Push(id, "character.updated", map[string]any{"inventory": map[string]any{"items": items, "balance": balance}})
		r.Broadcast(player.State.MapID, id, "world.entity_moved", map[string]any{"character_id": id, "x": player.State.X, "y": player.State.Y, "in_battle": false})
	}
	if run.AfterSettle != nil {
		run.AfterSettle(run)
	}
}

// consumeUsedItems 扣除战斗中实际使用的物品；若中途数量已减少（例如被交易走），按现有数量扣除，不让结算失败。
func consumeUsedItems(tx *rt.Tx, characterID string, used map[string]int) error {
	if len(used) == 0 {
		return nil
	}
	inventory, err := tx.Inventory(characterID)
	if err != nil {
		return err
	}
	for itemID, count := range used {
		if take := min(count, inventory[itemID]); take > 0 {
			if err := tx.RemoveItem(characterID, itemID, take); err != nil {
				return err
			}
		}
	}
	return nil
}
