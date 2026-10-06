// Package party 是队伍 owner（任务 0012.2）：队长权限、邀请/加入、离队/踢出、准备与断线状态。
// 队伍只存在于 Realm 单写者内存中，成员资产永远不由队伍直接写入。
package party

import (
	"context"
	"sort"
	"time"

	"github.com/taills/tbcg/server/internal/content/mapdata"
	rt "github.com/taills/tbcg/server/modules/runtime"
)

const (
	MaxMembers     = 5
	inviteTTL      = 60 * time.Second
	inviteDistance = 10
)

// Party 是一支队伍；Version 是成员 state_version。
type Party struct {
	ID      string
	Leader  string
	Members []string
	Ready   map[string]bool
	Invites map[string]time.Time
	Version int64
	Busy    string // 非空表示处于战斗或副本中（battle:<id> / dungeon:<id>）
}

type registry struct {
	parties  map[string]*Party
	byMember map[string]string
}

var leaveHooks []func(*rt.Realm, *Party, string)

// OnLeave 注册成员离开队伍时的回调（例如副本资格移除）。
func OnLeave(fn func(*rt.Realm, *Party, string)) { leaveHooks = append(leaveHooks, fn) }

func state(r *rt.Realm) *registry {
	return r.Module("party", func() any { return &registry{parties: map[string]*Party{}, byMember: map[string]string{}} }).(*registry)
}

// Of 返回角色所在队伍。
func Of(r *rt.Realm, characterID string) *Party {
	reg := state(r)
	return reg.parties[reg.byMember[characterID]]
}

// Get 按 ID 返回队伍。
func Get(r *rt.Realm, partyID string) *Party { return state(r).parties[partyID] }

func init() {
	rt.Register("party.create", true, create)
	rt.Register("party.invite", true, invite)
	rt.Register("party.respond", true, respond)
	rt.Register("party.leave", true, leave)
	rt.Register("party.kick", true, kick)
	rt.Register("party.ready", true, ready)
	rt.Register("party.transfer", true, transfer)
	rt.OnDisconnect(onDisconnect)
	rt.RegisterSnapshot("party", func(_ context.Context, r *rt.Realm, p *rt.Player) (any, error) {
		if party := Of(r, p.CharacterID); party != nil {
			return View(r, party), nil
		}
		return nil, nil
	})
}

// View 是队伍对成员可见的视图。
func View(r *rt.Realm, party *Party) map[string]any {
	members := make([]map[string]any, 0, len(party.Members))
	for _, id := range party.Members {
		player := r.Player(id)
		member := map[string]any{"character_id": id, "name": r.CharacterName(context.Background(), id), "online": player != nil, "ready": party.Ready[id]}
		if player != nil {
			member["level"], member["map_id"] = player.State.Level, player.State.MapID
		}
		members = append(members, member)
	}
	return map[string]any{"party_id": party.ID, "leader_id": party.Leader, "members": members, "state_version": party.Version, "busy": party.Busy}
}

// Publish 递增版本并向全部成员推送最新视图。
func Publish(r *rt.Realm, party *Party) {
	party.Version++
	view := View(r, party)
	for _, id := range party.Members {
		r.Push(id, "party.updated", view)
	}
}

func create(c *rt.Ctx) (any, *rt.ProtocolError) {
	reg := state(c.Realm)
	if reg.byMember[c.Player.CharacterID] != "" {
		return nil, rt.Fail("already_in_party", "已经在队伍中")
	}
	party := &Party{ID: rt.NewUUID(), Leader: c.Player.CharacterID, Members: []string{c.Player.CharacterID}, Ready: map[string]bool{}, Invites: map[string]time.Time{}}
	reg.parties[party.ID] = party
	reg.byMember[c.Player.CharacterID] = party.ID
	Publish(c.Realm, party)
	return View(c.Realm, party), nil
}

func invite(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		CharacterName string `json:"character_name"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	party := Of(c.Realm, c.Player.CharacterID)
	if party == nil || party.Leader != c.Player.CharacterID {
		return nil, rt.Fail("not_leader", "只有队长可以邀请")
	}
	if party.Busy != "" {
		return nil, rt.Fail("party_busy", "战斗或副本中不能邀请")
	}
	target := c.Realm.PlayerByName(input.CharacterName)
	if target == nil {
		return nil, rt.Fail("target_offline", "目标角色不在线")
	}
	if target.CharacterID == c.Player.CharacterID || Of(c.Realm, target.CharacterID) != nil {
		return nil, rt.Fail("target_in_party", "目标已在队伍中")
	}
	if target.State.MapID != c.Player.State.MapID || chebyshev(c.Player, target) > inviteDistance {
		return nil, rt.Fail("target_too_far", "目标不在附近")
	}
	if len(party.Members) >= MaxMembers {
		return nil, rt.Fail("party_full", "队伍已满")
	}
	party.Invites[target.CharacterID] = c.Realm.Now().Add(inviteTTL)
	c.Realm.Push(target.CharacterID, "party.invited", map[string]any{"party_id": party.ID, "leader_name": c.Player.State.Name, "expires_in_seconds": int(inviteTTL.Seconds())})
	return map[string]any{"party_id": party.ID, "invited": target.CharacterID}, nil
}

func chebyshev(a, b *rt.Player) int {
	anchor := mapdata.Anchor{Cell: mapdata.Cell{X: a.State.X, Y: a.State.Y}, Extent: mapdata.Cell{X: 1, Y: 1}}
	return anchor.Distance(mapdata.Cell{X: b.State.X, Y: b.State.Y})
}

func respond(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		PartyID string `json:"party_id"`
		Accept  bool   `json:"accept"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	reg := state(c.Realm)
	party := reg.parties[input.PartyID]
	me := c.Player.CharacterID
	if party == nil {
		return nil, rt.Fail("invite_expired", "邀请已失效")
	}
	expires, ok := party.Invites[me]
	delete(party.Invites, me)
	if !ok || c.Realm.Now().After(expires) {
		return nil, rt.Fail("invite_expired", "邀请已失效")
	}
	if !input.Accept {
		return map[string]any{"party_id": party.ID, "joined": false}, nil
	}
	if reg.byMember[me] != "" {
		return nil, rt.Fail("already_in_party", "已经在队伍中")
	}
	if party.Busy != "" {
		return nil, rt.Fail("party_busy", "队伍正在战斗或副本中")
	}
	if len(party.Members) >= MaxMembers {
		return nil, rt.Fail("party_full", "队伍已满")
	}
	party.Members = append(party.Members, me)
	reg.byMember[me] = party.ID
	Publish(c.Realm, party)
	return View(c.Realm, party), nil
}

// Remove 把成员移出队伍（离队、踢出、断线清理共用）。
func Remove(r *rt.Realm, party *Party, characterID string) {
	reg := state(r)
	kept := party.Members[:0]
	for _, id := range party.Members {
		if id != characterID {
			kept = append(kept, id)
		}
	}
	party.Members = kept
	delete(party.Ready, characterID)
	delete(reg.byMember, characterID)
	for _, hook := range leaveHooks {
		hook(r, party, characterID)
	}
	r.Push(characterID, "party.updated", map[string]any{"party_id": party.ID, "members": []any{}, "left": true})
	if len(party.Members) == 0 {
		delete(reg.parties, party.ID)
		return
	}
	if party.Leader == characterID {
		party.Leader = nextLeader(r, party)
	}
	Publish(r, party)
}

func nextLeader(r *rt.Realm, party *Party) string {
	sorted := append([]string(nil), party.Members...)
	sort.SliceStable(sorted, func(i, j int) bool { return r.Player(sorted[i]) != nil && r.Player(sorted[j]) == nil })
	return sorted[0]
}

func leave(c *rt.Ctx) (any, *rt.ProtocolError) {
	party := Of(c.Realm, c.Player.CharacterID)
	if party == nil {
		return nil, rt.Fail("not_in_party", "不在队伍中")
	}
	if c.Player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能离队")
	}
	Remove(c.Realm, party, c.Player.CharacterID)
	return map[string]any{"left": true}, nil
}

func kick(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		CharacterID string `json:"character_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	party := Of(c.Realm, c.Player.CharacterID)
	if party == nil || party.Leader != c.Player.CharacterID {
		return nil, rt.Fail("not_leader", "只有队长可以踢出成员")
	}
	if input.CharacterID == c.Player.CharacterID || Of(c.Realm, input.CharacterID) != party {
		return nil, rt.Fail("not_member", "目标不是本队成员")
	}
	if party.Busy != "" {
		return nil, rt.Fail("party_busy", "战斗或副本中不能踢人")
	}
	Remove(c.Realm, party, input.CharacterID)
	return View(c.Realm, party), nil
}

func ready(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		Ready bool `json:"ready"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	party := Of(c.Realm, c.Player.CharacterID)
	if party == nil {
		return nil, rt.Fail("not_in_party", "不在队伍中")
	}
	party.Ready[c.Player.CharacterID] = input.Ready
	Publish(c.Realm, party)
	return View(c.Realm, party), nil
}

func transfer(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		CharacterID string `json:"character_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	party := Of(c.Realm, c.Player.CharacterID)
	if party == nil || party.Leader != c.Player.CharacterID {
		return nil, rt.Fail("not_leader", "只有队长可以移交")
	}
	if Of(c.Realm, input.CharacterID) != party || c.Realm.Player(input.CharacterID) == nil {
		return nil, rt.Fail("not_member", "目标不是在线的本队成员")
	}
	party.Leader = input.CharacterID
	Publish(c.Realm, party)
	return View(c.Realm, party), nil
}

func onDisconnect(r *rt.Realm, player *rt.Player) {
	party := Of(r, player.CharacterID)
	if party == nil {
		return
	}
	party.Ready[player.CharacterID] = false
	if party.Busy == "" {
		online := 0
		for _, id := range party.Members {
			if id != player.CharacterID && r.Player(id) != nil {
				online++
			}
		}
		if online == 0 {
			// 全员离线且不在活动中：解散。
			for _, id := range append([]string(nil), party.Members...) {
				Remove(r, party, id)
			}
			return
		}
	}
	if party.Leader == player.CharacterID {
		for _, id := range party.Members {
			if id != player.CharacterID && r.Player(id) != nil {
				party.Leader = id
				break
			}
		}
	}
	r.Do(func() { Publish(r, party) })
}

// OnlineMembersOnMap 返回同地图在线成员（含自己）。
func OnlineMembersOnMap(r *rt.Realm, party *Party, mapID string) []string {
	var ids []string
	for _, id := range party.Members {
		if player := r.Player(id); player != nil && player.State.MapID == mapID {
			ids = append(ids, id)
		}
	}
	return ids
}
