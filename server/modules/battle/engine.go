package battle

import (
	"fmt"
	"sort"

	"github.com/taills/tbcg/server/internal/content/catalog"
)

// Combatant 描述一名参战角色及其随从的已提交属性快照。
type Combatant struct {
	CharacterID string
	Name        string
	Level       int
	Stats       catalog.Stats
	HP          int64
	MP          int64
	SkillIDs    []string
	Inventory   map[string]int
	Pet         *Companion
	Partners    []Companion
}

// Companion 是宠物或伙伴的战斗投影。
type Companion struct {
	ID           string
	DefinitionID string
	Name         string
	Level        int
	Stats        catalog.Stats
	SkillIDs     []string
}

// NewState 从遭遇定义与参战者创建初始状态（深拷贝输入）。
func NewState(content *catalog.Catalog, battleID, encounterID string, seed uint64, party []Combatant) (State, error) {
	encounter := content.Encounters[encounterID]
	if encounter == nil {
		return State{}, fmt.Errorf("battle: encounter %s not in Release", encounterID)
	}
	if len(party) == 0 || len(party) > 5 {
		return State{}, fmt.Errorf("battle: party size %d out of range", len(party))
	}
	state := State{BattleID: battleID, EncounterID: encounterID, Seed: seed, RNG: seed, Round: 1,
		MaxRounds: encounter.Server.MaxRounds, AllowFlee: encounter.Server.AllowFlee,
		Inventory: map[string]map[string]int{}, ItemsUsed: map[string]map[string]int{}}
	for _, member := range party {
		hp := member.HP
		if hp <= 0 || hp > member.Stats.MaxHP {
			hp = member.Stats.MaxHP
		}
		mp := member.MP
		if mp < 0 || mp > member.Stats.MaxMP {
			mp = member.Stats.MaxMP
		}
		state.Units = append(state.Units, Unit{ID: "c:" + member.CharacterID, Side: SidePlayers, Kind: KindCharacter, OwnerID: member.CharacterID,
			DefinitionID: member.CharacterID, Name: member.Name, Level: member.Level, Stats: member.Stats, HP: hp, MP: mp,
			SkillIDs: append([]string(nil), member.SkillIDs...)})
		items := map[string]int{}
		for item, count := range member.Inventory {
			if consumable := content.Consumables[item]; consumable != nil && consumable.Public.UsableInBattle && count > 0 {
				items[item] = count
			}
		}
		state.Inventory[member.CharacterID] = items
		if member.Pet != nil {
			state.Units = append(state.Units, companionUnit(KindPet, member.CharacterID, *member.Pet))
		}
		for _, partner := range member.Partners {
			state.Units = append(state.Units, companionUnit(KindPartner, member.CharacterID, partner))
		}
	}
	index := 0
	for _, group := range encounter.Server.Monsters {
		monster := content.Monsters[group.MonsterID]
		if monster == nil {
			return State{}, fmt.Errorf("battle: monster %s not in Release", group.MonsterID)
		}
		for i := 0; i < group.Count; i++ {
			index++
			state.Units = append(state.Units, Unit{ID: fmt.Sprintf("m:%d", index), Side: SideEnemies, Kind: KindMonster,
				DefinitionID: monster.ContentID, Name: monster.Public.DisplayName, Level: monster.Public.Level,
				Stats: monster.Server.Stats, HP: monster.Server.Stats.MaxHP, MP: monster.Server.Stats.MaxMP,
				SkillIDs: append([]string(nil), monster.Server.SkillIDs...), SkillChanceBP: monster.Server.SkillChanceBP})
		}
	}
	return state, nil
}

func companionUnit(kind, owner string, companion Companion) Unit {
	return Unit{ID: kind[:2] + ":" + companion.ID, Side: SidePlayers, Kind: kind, OwnerID: owner, DefinitionID: companion.DefinitionID,
		Name: companion.Name, Level: companion.Level, Stats: companion.Stats, HP: companion.Stats.MaxHP, MP: companion.Stats.MaxMP,
		SkillIDs: append([]string(nil), companion.SkillIDs...)}
}

// ValidateCommand 校验提交者对单位的授权与命令合法性；非法命令不得进入结算。
func ValidateCommand(content *catalog.Catalog, state State, submitter string, command Command) error {
	if state.Outcome != "" {
		return ErrBattleOver
	}
	actor, ok := state.UnitByID(command.ActorID)
	if !ok || actor.Kind != KindCharacter || actor.OwnerID != submitter || !actor.Alive() {
		return fmt.Errorf("%w: actor is not controlled by submitter", ErrInvalidCommand)
	}
	switch command.Kind {
	case "attack":
		return requireTarget(state, command.TargetID, SideEnemies)
	case "defend":
		return nil
	case "flee":
		if !state.AllowFlee {
			return fmt.Errorf("%w: encounter does not allow fleeing", ErrInvalidCommand)
		}
		return nil
	case "skill":
		skill := content.Skills[command.SkillID]
		if skill == nil || !contains(actor.SkillIDs, command.SkillID) {
			return fmt.Errorf("%w: skill not available", ErrInvalidCommand)
		}
		if actor.MP < skill.Public.MPCost {
			return fmt.Errorf("%w: not enough MP", ErrInvalidCommand)
		}
		switch skill.Public.Target {
		case "enemy_single":
			return requireTarget(state, command.TargetID, SideEnemies)
		case "ally_single":
			return requireTarget(state, command.TargetID, SidePlayers)
		}
		return nil
	case "item":
		consumable := content.Consumables[command.ItemID]
		if consumable == nil || !consumable.Public.UsableInBattle || state.Inventory[submitter][command.ItemID]-state.ItemsUsed[submitter][command.ItemID] < 1 {
			return fmt.Errorf("%w: item unavailable", ErrInvalidCommand)
		}
		return requireTarget(state, command.TargetID, SidePlayers)
	}
	return fmt.Errorf("%w: unknown command kind", ErrInvalidCommand)
}

func requireTarget(state State, id string, side int) error {
	target, ok := state.UnitByID(id)
	if !ok || target.Side != side || !target.Alive() {
		return fmt.Errorf("%w: invalid target", ErrInvalidCommand)
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// Resolve 在旧状态的深拷贝上结算一个回合；缺失的玩家命令使用服务器默认行为（攻击）。
func Resolve(content *catalog.Catalog, previous State, commands map[string]Command) (State, []Event, error) {
	if previous.Outcome != "" {
		return previous, nil, ErrBattleOver
	}
	state := previous.Clone()
	var events []Event
	emit := func(event Event) {
		state.EventSeq++
		event.Seq = state.EventSeq
		event.ActionID = state.ActionSeq
		event.Round = state.Round
		events = append(events, event)
	}
	for i := range state.Units {
		state.Units[i].Defending = false
	}
	for _, actorID := range turnOrder(state) {
		index := state.unitIndex(actorID)
		if index < 0 || !state.Units[index].Alive() || state.Outcome != "" {
			continue
		}
		state.ActionSeq++
		command, explicit := commands[actorID]
		if state.Units[index].Kind == KindCharacter {
			if !explicit || ValidateCommand(content, state, state.Units[index].OwnerID, command) != nil {
				command = Command{ActorID: actorID, Kind: "attack", TargetID: firstAlive(state, SideEnemies)}
			}
		} else {
			command = aiCommand(content, &state, index)
		}
		execute(content, &state, index, command, emit)
		checkOutcome(&state, emit)
	}
	if state.Outcome == "" {
		for i := range state.Units {
			kept := state.Units[i].Buffs[:0]
			for _, buff := range state.Units[i].Buffs {
				buff.Turns--
				if buff.Turns > 0 {
					kept = append(kept, buff)
				}
			}
			state.Units[i].Buffs = kept
		}
		state.Round++
		if state.Round > state.MaxRounds {
			state.Outcome = OutcomeTimeout
			emit(Event{Kind: "battle_end", Outcome: state.Outcome})
		}
	}
	return state, events, nil
}

func turnOrder(state State) []string {
	type entry struct {
		id    string
		speed int64
	}
	order := make([]entry, 0, len(state.Units))
	for _, unit := range state.Units {
		if unit.Alive() {
			order = append(order, entry{id: unit.ID, speed: effective(unit, "speed")})
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].speed != order[j].speed {
			return order[i].speed > order[j].speed
		}
		return order[i].id < order[j].id
	})
	ids := make([]string, len(order))
	for i, item := range order {
		ids[i] = item.id
	}
	return ids
}

func effective(unit Unit, stat string) int64 {
	var value int64
	switch stat {
	case "attack":
		value = unit.Stats.Attack
	case "defense":
		value = unit.Stats.Defense
	case "speed":
		value = unit.Stats.Speed
	}
	for _, buff := range unit.Buffs {
		if buff.Stat == stat {
			value += buff.Amount
		}
	}
	if value < 0 {
		return 0
	}
	return value
}

func firstAlive(state State, side int) string {
	for _, unit := range state.Units {
		if unit.Side == side && unit.Alive() {
			return unit.ID
		}
	}
	return ""
}

func randomAlive(state *State, side int) string {
	var alive []string
	for _, unit := range state.Units {
		if unit.Side == side && unit.Alive() {
			alive = append(alive, unit.ID)
		}
	}
	if len(alive) == 0 {
		return ""
	}
	return alive[state.roll(uint64(len(alive)))]
}

func aiCommand(content *catalog.Catalog, state *State, index int) Command {
	unit := state.Units[index]
	enemySide := SideEnemies
	if unit.Side == SideEnemies {
		enemySide = SidePlayers
	}
	usable := make([]string, 0, len(unit.SkillIDs))
	for _, id := range unit.SkillIDs {
		if skill := content.Skills[id]; skill != nil && skill.Public.Kind == "damage" && unit.MP >= skill.Public.MPCost {
			usable = append(usable, id)
		}
	}
	if unit.Kind == KindMonster {
		if len(usable) > 0 && state.chance(10000) < unit.SkillChanceBP {
			return Command{ActorID: unit.ID, Kind: "skill", SkillID: usable[state.roll(uint64(len(usable)))], TargetID: randomAlive(state, enemySide)}
		}
		return Command{ActorID: unit.ID, Kind: "attack", TargetID: randomAlive(state, enemySide)}
	}
	target := weakest(*state, enemySide)
	if len(usable) > 0 && content.Skills[usable[0]].Public.MPCost > 0 && unit.MP >= content.Skills[usable[0]].Public.MPCost*2 {
		return Command{ActorID: unit.ID, Kind: "skill", SkillID: usable[0], TargetID: target}
	}
	return Command{ActorID: unit.ID, Kind: "attack", TargetID: target}
}

func weakest(state State, side int) string {
	best := ""
	var bestHP int64
	for _, unit := range state.Units {
		if unit.Side == side && unit.Alive() && (best == "" || unit.HP < bestHP) {
			best, bestHP = unit.ID, unit.HP
		}
	}
	return best
}

func execute(content *catalog.Catalog, state *State, index int, command Command, emit func(Event)) {
	actor := &state.Units[index]
	switch command.Kind {
	case "defend":
		actor.Defending = true
		emit(Event{Kind: "defend", ActorID: actor.ID, HPBefore: actor.HP, HPAfter: actor.HP, MPAfter: actor.MP})
	case "flee":
		if state.roll(100) < 50 {
			state.Outcome = OutcomeFled
			emit(Event{Kind: "flee", ActorID: actor.ID, HPBefore: actor.HP, HPAfter: actor.HP, MPAfter: actor.MP, Outcome: OutcomeFled})
			emit(Event{Kind: "battle_end", Outcome: OutcomeFled})
			return
		}
		emit(Event{Kind: "flee_failed", ActorID: actor.ID, HPBefore: actor.HP, HPAfter: actor.HP, MPAfter: actor.MP})
	case "item":
		useItem(content, state, index, command, emit)
	case "skill":
		skill := content.Skills[command.SkillID]
		if skill == nil || actor.MP < skill.Public.MPCost {
			attack(state, index, command.TargetID, "", 100, 1, 0, emit)
			return
		}
		actor.MP -= skill.Public.MPCost
		castSkill(state, index, skill, command.TargetID, emit)
	default:
		attack(state, index, command.TargetID, "", 100, 1, 0, emit)
	}
}

func useItem(content *catalog.Catalog, state *State, index int, command Command, emit func(Event)) {
	actor := &state.Units[index]
	owner := actor.OwnerID
	if state.ItemsUsed[owner] == nil {
		state.ItemsUsed[owner] = map[string]int{}
	}
	state.ItemsUsed[owner][command.ItemID]++
	targetIndex := state.unitIndex(command.TargetID)
	if targetIndex < 0 || !state.Units[targetIndex].Alive() {
		targetIndex = index
	}
	target := &state.Units[targetIndex]
	for _, effect := range content.Consumables[command.ItemID].Server.Effects {
		before := target.HP
		switch effect.Kind {
		case "heal_hp":
			target.HP = min(target.Stats.MaxHP, target.HP+effect.Amount)
		case "heal_mp":
			target.MP = min(target.Stats.MaxMP, target.MP+effect.Amount)
		default:
			continue
		}
		emit(Event{Kind: "item", ActorID: actor.ID, TargetID: target.ID, ItemID: command.ItemID, Heal: target.HP - before,
			HPBefore: before, HPAfter: target.HP, MPAfter: target.MP})
	}
}

func castSkill(state *State, index int, skill *catalog.Skill, targetID string, emit func(Event)) {
	actor := state.Units[index]
	allySide, enemySide := actor.Side, SideEnemies
	if actor.Side == SideEnemies {
		enemySide = SidePlayers
	}
	var targets []string
	switch skill.Public.Target {
	case "enemy_single":
		targets = []string{validOr(state, targetID, enemySide)}
	case "enemy_all":
		targets = aliveOn(*state, enemySide)
	case "ally_single":
		targets = []string{validOr(state, targetID, allySide)}
	case "ally_all":
		targets = aliveOn(*state, allySide)
	case "self":
		targets = []string{actor.ID}
	}
	for _, target := range targets {
		if target == "" {
			continue
		}
		switch skill.Public.Kind {
		case "damage":
			attack(state, index, target, skill.ContentID, skill.Server.PowerPercent, skill.Server.Hits, skill.Server.CritRateBP, emit)
		case "heal":
			heal(state, index, target, skill, emit)
		case "buff":
			if skill.Server.Buff != nil {
				targetIndex := state.unitIndex(target)
				unit := &state.Units[targetIndex]
				unit.Buffs = append(unit.Buffs, ActiveBuff{SkillID: skill.ContentID, Stat: skill.Server.Buff.Stat, Amount: skill.Server.Buff.Amount, Turns: skill.Server.Buff.Turns})
				emit(Event{Kind: "buff", ActorID: actor.ID, TargetID: target, SkillID: skill.ContentID, Stat: skill.Server.Buff.Stat,
					Amount: skill.Server.Buff.Amount, Turns: skill.Server.Buff.Turns, HPBefore: unit.HP, HPAfter: unit.HP, MPAfter: unit.MP})
			}
		}
	}
}

func validOr(state *State, id string, side int) string {
	index := state.unitIndex(id)
	if index >= 0 && state.Units[index].Side == side && state.Units[index].Alive() {
		return id
	}
	return randomAlive(state, side)
}

func aliveOn(state State, side int) []string {
	var ids []string
	for _, unit := range state.Units {
		if unit.Side == side && unit.Alive() {
			ids = append(ids, unit.ID)
		}
	}
	return ids
}

func attack(state *State, index int, targetID, skillID string, powerPercent int64, hits int, critBP int64, emit func(Event)) {
	actor := state.Units[index]
	enemySide := SideEnemies
	if actor.Side == SideEnemies {
		enemySide = SidePlayers
	}
	targetID = validOr(state, targetID, enemySide)
	targetIndex := state.unitIndex(targetID)
	if targetIndex < 0 {
		return
	}
	if hits < 1 {
		hits = 1
	}
	for hit := 1; hit <= hits; hit++ {
		target := &state.Units[targetIndex]
		if !target.Alive() {
			break
		}
		base := effective(actor, "attack")*powerPercent/100 - effective(*target, "defense")/2
		if base < 1 {
			base = 1
		}
		variance := state.chance(21) - 10 // -10%..+10%
		damage := base + base*variance/100
		crit := critBP > 0 && state.chance(10000) < critBP
		if crit {
			damage = damage * 3 / 2
		}
		if target.Defending {
			damage /= 2
		}
		if damage < 1 {
			damage = 1
		}
		before := target.HP
		target.HP = max(0, target.HP-damage)
		emit(Event{Kind: "damage", ActorID: actor.ID, TargetID: target.ID, SkillID: skillID, Hit: hit, Damage: before - target.HP,
			HPBefore: before, HPAfter: target.HP, MPAfter: state.Units[index].MP, Crit: crit})
		if !target.Alive() {
			emit(Event{Kind: "death", TargetID: target.ID, HPBefore: 0, HPAfter: 0})
		}
	}
}

func heal(state *State, index int, targetID string, skill *catalog.Skill, emit func(Event)) {
	actor := state.Units[index]
	targetIndex := state.unitIndex(targetID)
	if targetIndex < 0 {
		return
	}
	target := &state.Units[targetIndex]
	amount := effective(actor, "attack") * skill.Server.PowerPercent / 100
	amount += amount * (state.chance(11) - 5) / 100
	if amount < 1 {
		amount = 1
	}
	before := target.HP
	target.HP = min(target.Stats.MaxHP, target.HP+amount)
	emit(Event{Kind: "heal", ActorID: actor.ID, TargetID: target.ID, SkillID: skill.ContentID, Heal: target.HP - before,
		HPBefore: before, HPAfter: target.HP, MPAfter: state.Units[index].MP})
}

func checkOutcome(state *State, emit func(Event)) {
	if state.Outcome != "" {
		return
	}
	players, enemies := false, false
	for _, unit := range state.Units {
		if !unit.Alive() {
			continue
		}
		if unit.Side == SidePlayers && unit.Kind == KindCharacter {
			players = true
		}
		if unit.Side == SideEnemies {
			enemies = true
		}
	}
	switch {
	case !enemies:
		state.Outcome = OutcomeVictory
	case !players:
		state.Outcome = OutcomeDefeat
	default:
		return
	}
	emit(Event{Kind: "battle_end", Outcome: state.Outcome})
}

// Spoils 是胜利后每名参战角色的确定性收益。
type Spoils struct {
	CharacterID string          `json:"character_id"`
	Exp         int64           `json:"exp"`
	Currency    int64           `json:"currency"`
	Items       []catalog.Stack `json:"items"`
}

// ComputeSpoils 以战斗 seed 派生每名参战者独立的掉落；非胜利无收益。
func ComputeSpoils(content *catalog.Catalog, state State) []Spoils {
	if state.Outcome != OutcomeVictory {
		return nil
	}
	participants := state.Participants()
	sort.Strings(participants)
	out := make([]Spoils, 0, len(participants))
	for index, characterID := range participants {
		roller := State{RNG: state.Seed ^ uint64(index+1)*0x9E3779B97F4A7C15}
		spoils := Spoils{CharacterID: characterID}
		items := map[string]int{}
		for _, unit := range state.Units {
			if unit.Kind != KindMonster {
				continue
			}
			monster := content.Monsters[unit.DefinitionID]
			if monster == nil {
				continue
			}
			spoils.Exp += monster.Server.ExpReward
			spoils.Currency += monster.Server.CurrencyReward
			for _, loot := range monster.Server.Loot {
				if roller.chance(10000) < loot.ChanceBP {
					items[loot.ItemID] += loot.Quantity
				}
			}
		}
		keys := make([]string, 0, len(items))
		for item := range items {
			keys = append(keys, item)
		}
		sort.Strings(keys)
		for _, item := range keys {
			spoils.Items = append(spoils.Items, catalog.Stack{ItemID: item, Quantity: items[item]})
		}
		out = append(out, spoils)
	}
	return out
}
