package playtest

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ---- 协议客户端：与 Godot 客户端一样只提交意图，以服务端响应与推送为准 ----

type client struct {
	t       *testing.T
	conn    *websocket.Conn
	mu      sync.Mutex
	next    int
	results map[string]chan map[string]any
	pushes  chan map[string]any
}

func dial(t *testing.T, url string) *client {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	c := &client{t: t, conn: conn, results: map[string]chan map[string]any{}, pushes: make(chan map[string]any, 1024)}
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				close(c.pushes)
				return
			}
			var message map[string]any
			if json.Unmarshal(data, &message) != nil {
				continue
			}
			id, _ := message["request_id"].(string)
			c.mu.Lock()
			ch := c.results[id]
			c.mu.Unlock()
			if ch != nil {
				ch <- message
			} else {
				c.pushes <- message
			}
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return c
}

func (c *client) call(messageType string, payload map[string]any) map[string]any {
	c.t.Helper()
	c.mu.Lock()
	c.next++
	id := fmt.Sprintf("req-%d", c.next)
	ch := make(chan map[string]any, 1)
	c.results[id] = ch
	c.mu.Unlock()
	encoded, _ := json.Marshal(map[string]any{"protocol_version": "1", "request_id": id, "type": messageType, "payload": payload})
	if err := c.conn.WriteMessage(websocket.BinaryMessage, encoded); err != nil {
		c.t.Fatalf("send %s: %v", messageType, err)
	}
	select {
	case response := <-ch:
		return response
	case <-time.After(20 * time.Second):
		c.t.Fatalf("timeout waiting for %s", messageType)
	}
	return nil
}

func (c *client) ok(messageType string, payload map[string]any) map[string]any {
	c.t.Helper()
	response := c.call(messageType, payload)
	if response["success"] != true {
		c.t.Fatalf("%s failed: %v", messageType, response["error"])
	}
	data, _ := response["data"].(map[string]any)
	return data
}

func (c *client) fail(messageType string, payload map[string]any, code string) {
	c.t.Helper()
	response := c.call(messageType, payload)
	errorObject, _ := response["error"].(map[string]any)
	if response["success"] != false || errorObject["code"] != code {
		c.t.Fatalf("%s expected failure %s, got %v", messageType, code, response)
	}
}

func (c *client) waitPush(messageType string, match func(map[string]any) bool) map[string]any {
	c.t.Helper()
	deadline := time.After(60 * time.Second)
	for {
		select {
		case message, ok := <-c.pushes:
			if !ok {
				c.t.Fatalf("connection closed waiting for %s", messageType)
			}
			if message["type"] == messageType {
				data, _ := message["data"].(map[string]any)
				if match == nil || match(data) {
					return data
				}
			}
		case <-deadline:
			c.t.Fatalf("timeout waiting for push %s", messageType)
		}
	}
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func key(t *testing.T) string { return "playtest-" + randomSuffix(t) + randomSuffix(t) }

func postJSON(t *testing.T, url string, body any) map[string]any {
	t.Helper()
	encoded, _ := json.Marshal(body)
	response, err := http.Post(url, "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(response.Body).Decode(&decoded)
	return decoded
}

// ---- 玩家：寻路只用于生成合法移动请求；背包、任务、位置都读服务端快照 ----

type player struct {
	t       *testing.T
	c       *client
	maps    map[string]geometry
	view    map[string]any
	version string
	mapID   string
	at      cell
	hp      int
	maxHP   int
	level   int
}

func (p *player) refresh() {
	p.t.Helper()
	p.view = p.c.ok("character.state", map[string]any{})
	state := p.view["state"].(map[string]any)
	p.version = p.view["state_version"].(string)
	p.mapID = state["map_id"].(string)
	p.at = cell{int(state["x"].(float64)), int(state["y"].(float64))}
	p.hp = int(state["hp"].(float64))
	p.level = int(state["level"].(float64))
	p.maxHP = int(p.view["stats"].(map[string]any)["max_hp"].(float64))
}

func (p *player) geometry() geometry { return p.maps[p.mapID] }

func (p *player) count(itemID string) int {
	items, _ := p.view["inventory"].(map[string]any)["items"].(map[string]any)
	quantity, _ := items[itemID].(float64)
	return int(quantity)
}

func (p *player) expectQuest(questID, status string, stage int) {
	p.t.Helper()
	p.refresh()
	quests, _ := p.view["quests"].([]any)
	for _, raw := range quests {
		q := raw.(map[string]any)
		if q["quest_id"] == questID {
			if q["status"] != status || int(q["stage_index"].(float64)) != stage {
				p.t.Fatalf("%s = %v/stage %v, want %s/stage %d", questID, q["status"], q["stage_index"], status, stage)
			}
			return
		}
	}
	p.t.Fatalf("%s not started, want %s/stage %d", questID, status, stage)
}

func (p *player) walkTo(target cell) {
	p.t.Helper()
	p.refresh() // 装备、招募等操作也会推进 state_version
	path := p.geometry().path(p.at, target)
	if path == nil && p.at != target {
		p.t.Fatalf("no path on %s from %v to %v", p.mapID, p.at, target)
	}
	for start := 0; start < len(path); start += 8 {
		chunk := path[start:min(start+8, len(path))]
		cells := make([]map[string]int, 0, len(chunk))
		for _, c := range chunk {
			cells = append(cells, map[string]int{"x": c.X, "y": c.Y})
		}
		data := p.c.ok("world.move", map[string]any{"path": cells, "state_version": p.version})
		p.version = data["state_version"].(string)
		p.at = chunk[len(chunk)-1]
		time.Sleep(time.Duration(len(chunk)) * 130 * time.Millisecond) // 服务端限制每步最小间隔 120ms
	}
}

func (p *player) walkToAnchor(anchorID string) {
	p.t.Helper()
	p.refresh()
	a, ok := p.geometry().anchor(anchorID)
	if !ok {
		p.t.Fatalf("anchor %s missing on %s", anchorID, p.mapID)
	}
	p.walkTo(a.Cell)
}

// walkNear 走到锚点相邻、路径最短的可走格（NPC 交互距离为 1）。
func (p *player) walkNear(anchorID string) {
	p.t.Helper()
	p.refresh()
	g := p.geometry()
	a, _ := g.anchor(anchorID)
	var best []cell
	var target cell
	for _, d := range []cell{{0, 1}, {0, -1}, {1, 0}, {-1, 0}} {
		c := cell{a.Cell.X + d.X, a.Cell.Y + d.Y}
		if !g.walkable(c) {
			continue
		}
		if c == p.at {
			return
		}
		if path := g.path(p.at, c); path != nil && (best == nil || len(path) < len(best)) {
			best, target = path, c
		}
	}
	if best == nil {
		p.t.Fatalf("cannot reach %s on %s", anchorID, p.mapID)
	}
	p.walkTo(target)
}

func (p *player) teleport(portalAnchorID, wantMap string) {
	p.t.Helper()
	p.walkToAnchor(portalAnchorID)
	p.c.ok("world.teleport", map[string]any{"portal_anchor_id": portalAnchorID})
	p.refresh()
	if p.mapID != wantMap {
		p.t.Fatalf("teleport via %s landed on %s, want %s", portalAnchorID, p.mapID, wantMap)
	}
}

func (p *player) choose(npcID, optionID string) map[string]any {
	p.t.Helper()
	dialog := p.c.ok("npc.interact", map[string]any{"npc_id": npcID})["dialog"].(map[string]any)
	return p.c.ok("npc.choose", map[string]any{"npc_id": npcID, "node_id": dialog["node_id"], "option_id": optionID, "idempotency_key": key(p.t)})
}

// heal 在战斗外喝鱼汤，直到气血达到七成（气血在战斗之间保留）。
func (p *player) heal() {
	p.t.Helper()
	p.refresh()
	for p.hp*10 < p.maxHP*7 && p.count("harbor:item/fish-soup") > 0 {
		p.c.ok("inventory.use", map[string]any{"item_id": "harbor:item/fish-soup", "idempotency_key": key(p.t)})
		p.refresh()
	}
}

// fight 每回合攻击第一个存活敌人；自身气血低于四成时在战斗中喝鱼汤。
func (p *player) fight(battleID string) string {
	p.t.Helper()
	for {
		view := p.c.ok("battle.state", map[string]any{"battle_id": battleID})
		var self map[string]any
		target := ""
		for _, raw := range view["units"].([]any) {
			unit := raw.(map[string]any)
			if unit["id"] == view["your_unit_id"] {
				self = unit
			}
			if unit["side"].(float64) == 1 && unit["hp"].(float64) > 0 && target == "" {
				target = unit["id"].(string)
			}
		}
		if self == nil {
			p.t.Fatalf("battle %s has no unit %v", battleID, view["your_unit_id"])
		}
		command := map[string]any{"kind": "attack", "target_id": target}
		inventory, _ := view["inventory"].(map[string]any)
		maxHP := self["stats"].(map[string]any)["max_hp"].(float64)
		if soups, _ := inventory["harbor:item/fish-soup"].(float64); soups > 0 && self["hp"].(float64)*10 < maxHP*4 {
			command = map[string]any{"kind": "item", "item_id": "harbor:item/fish-soup", "target_id": self["id"]}
		}
		response := p.c.call("battle.command", map[string]any{"battle_id": battleID, "round": view["round"], "command": command})
		if response["success"] != true {
			code := response["error"].(map[string]any)["code"]
			if code != "stale_round" && code != "already_submitted" {
				p.t.Fatalf("battle.command: %v", response["error"])
			}
		}
		round := p.c.waitPush("battle.round", func(data map[string]any) bool { return data["battle_id"] == battleID && data["events"] != nil })
		if outcome, _ := round["outcome"].(string); outcome != "" {
			ended := p.c.waitPush("battle.ended", func(data map[string]any) bool { return data["battle_id"] == battleID })
			time.Sleep(200 * time.Millisecond) // 结算在 Realm 循环中异步提交
			return ended["outcome"].(string)
		}
	}
}

// encounterUntil 反复通过 NPC 开战，直到 done(胜场) 为真。战斗随机：败北后喝汤重来，总场次有上限。
func (p *player) encounterUntil(npcID, optionID string, maxFights int, done func(wins int) bool) {
	p.t.Helper()
	wins := 0
	for fights := 0; ; fights++ {
		p.refresh() // 经 Realm 往返，保证上一场结算已提交后再读背包
		if done(wins) {
			return
		}
		if fights >= maxFights {
			p.t.Fatalf("%s/%s: goal not reached after %d fights (%d wins)", npcID, optionID, maxFights, wins)
		}
		p.heal()
		battleID := p.choose(npcID, optionID)["battle_id"].(string)
		if outcome := p.fight(battleID); outcome == "victory" {
			wins++
		} else {
			p.t.Logf("%s/%s fight %d outcome = %s, retrying", npcID, optionID, fights+1, outcome)
		}
	}
}

// runDungeon 带单人队伍打完沉潮窟并领奖；失败的实例不能继续，换新实例重来（每日上限 3 次）。
func (p *player) runDungeon(attempts int) {
	p.t.Helper()
	for attempt := 1; attempt <= attempts; attempt++ {
		p.heal()
		runID := p.choose("harbor:npc/tidewright", "grotto")["dungeon"].(map[string]any)["run_id"].(string)
		cleared := true
		for stage := range 2 {
			p.heal()
			battleID := p.c.ok("dungeon.start_stage", map[string]any{"run_id": runID})["battle_id"].(string)
			if outcome := p.fight(battleID); outcome != "victory" {
				p.t.Logf("drowned-grotto attempt %d stage %d outcome = %s", attempt, stage, outcome)
				cleared = false
				break
			}
		}
		if cleared {
			p.c.ok("dungeon.claim", map[string]any{"run_id": runID, "idempotency_key": key(p.t)})
			p.c.fail("dungeon.claim", map[string]any{"run_id": runID, "idempotency_key": key(p.t)}, "already_claimed")
			return
		}
		time.Sleep(300 * time.Millisecond) // 等副本失败状态释放队伍
	}
	p.t.Fatalf("drowned-grotto not cleared in %d attempts", attempts)
}

// TestPlaythrough 连接正在运行的雾港奇谭服务（docker compose 或 run-dev.sh），用新账号从建角一路打到章鱼王。
// 需要 HARBOR_HTTP_URL 与 HARBOR_WS_URL，例如 http://127.0.0.1:7080 与 ws://127.0.0.1:7443。
func TestPlaythrough(t *testing.T) {
	httpURL, wsURL := os.Getenv("HARBOR_HTTP_URL"), os.Getenv("HARBOR_WS_URL")
	if httpURL == "" || wsURL == "" {
		t.Skip("set HARBOR_HTTP_URL and HARBOR_WS_URL to a running harbor server")
	}
	httpURL = strings.TrimRight(httpURL, "/")
	response, err := http.Get(httpURL + "/api/v1/realm")
	if err != nil {
		t.Fatal(err)
	}
	var realm struct {
		Data map[string]any `json:"data"`
	}
	_ = json.NewDecoder(response.Body).Decode(&realm)
	response.Body.Close()
	if realm.Data["game_id"] != "harbor" {
		t.Fatalf("server is not running harbor: %v", realm.Data)
	}
	suffix := randomSuffix(t)
	connect := func(name string) *client {
		t.Helper()
		username := name + "_" + suffix
		credentials := map[string]string{"username": username, "password": "correct horse battery"}
		if r := postJSON(t, httpURL+"/api/v1/accounts", credentials); r["success"] != true {
			t.Fatalf("register %s: %v", username, r)
		}
		token := postJSON(t, httpURL+"/api/v1/sessions", credentials)["data"].(map[string]any)["session_token"].(string)
		c := dial(t, wsURL)
		c.ok("system.hello", map[string]any{"game_id": "harbor", "realm_id": realm.Data["realm_id"], "client_build": "0.1.0",
			"release_id": realm.Data["release_id"], "session_token": token, "capabilities": []string{"protocol.v1"}})
		return c
	}
	maps := map[string]geometry{}
	for _, slug := range []string{"pier-town", "lighthouse", "tide-flats"} {
		g := loadGeometry(t, slug)
		maps[g.MapID] = g
	}

	// 第二职业：灯语者的出生配置。
	ling := connect("ling")
	lingID := ling.ok("character.create", map[string]any{"name": "灵" + suffix, "archetype_id": "harbor:archetype/lamp-speaker", "idempotency_key": key(t)})["character_id"].(string)
	if skills := ling.ok("character.enter", map[string]any{"character_id": lingID})["skills"].([]any); len(skills) != 2 {
		t.Fatalf("lamp-speaker skills = %v", skills)
	}

	c := connect("hai")
	characterID := c.ok("character.create", map[string]any{"name": "海" + suffix, "archetype_id": "harbor:archetype/harpooner", "idempotency_key": key(t)})["character_id"].(string)
	c.ok("character.enter", map[string]any{"character_id": characterID})
	p := &player{t: t, c: c, maps: maps}
	p.refresh()
	if p.mapID != "harbor:map/pier-town" || p.count("harbor:item/fish-soup") != 5 {
		t.Fatalf("unexpected spawn %s / soups %d", p.mapID, p.count("harbor:item/fish-soup"))
	}

	// ---- 任务一：初到雾港 ----
	const q1, q2, q3 = "harbor:quest/new-in-port", "harbor:quest/fog-lights", "harbor:quest/reef-patrol"
	c.fail("world.teleport", map[string]any{"portal_anchor_id": "portal-flats"}, "not_at_portal")
	p.walkNear("harbormaster")
	p.choose("harbor:npc/harbormaster", "accept")
	p.walkToAnchor("lighthouse-steps")
	p.expectQuest(q1, "active", 1)
	p.walkToAnchor("portal-flats")
	c.fail("world.teleport", map[string]any{"portal_anchor_id": "portal-flats"}, "portal_locked")
	p.teleport("portal-lighthouse", "harbor:map/lighthouse")
	p.walkNear("lightkeeper")
	p.choose("harbor:npc/lightkeeper", "visit")
	p.expectQuest(q1, "active", 2)
	p.teleport("portal-down", "harbor:map/pier-town")
	p.walkNear("harbormaster")
	p.encounterUntil("harbor:npc/harbormaster", "crabs", 8, func(wins int) bool {
		return wins > 0 && p.count("harbor:item/crab-shell") >= 2
	})
	p.expectQuest(q1, "active", 3)
	p.choose("harbor:npc/harbormaster", "deliver")
	p.expectQuest(q1, "objectives_done", 4)
	p.choose("harbor:npc/harbormaster", "claim")
	p.expectQuest(q1, "completed", 4)
	if p.level < 2 || p.count("harbor:item/old-harpoon") != 1 {
		t.Fatalf("after new-in-port: level %d, harpoons %d", p.level, p.count("harbor:item/old-harpoon"))
	}
	c.ok("inventory.equip", map[string]any{"item_id": "harbor:item/old-harpoon"})
	p.choose("harbor:npc/harbormaster", "sailor")
	c.ok("partner.formation", map[string]any{"partner_id": "harbor:partner/old-sailor", "in_formation": true})

	// ---- 拜入观潮阁，任务二：雾中灯火 ----
	p.teleport("portal-lighthouse", "harbor:map/lighthouse")
	p.walkNear("lightkeeper")
	p.choose("harbor:npc/lightkeeper", "sect")
	p.choose("harbor:npc/lightkeeper", "accept")
	p.teleport("portal-down", "harbor:map/pier-town")
	p.teleport("portal-flats", "harbor:map/tide-flats")
	p.walkToAnchor("wreck")
	p.expectQuest(q2, "active", 1)
	p.walkNear("beachcomber")
	// 渔灯节需要 3 次胜利；多留一颗雾珠用于打造潮纹鱼叉。
	p.encounterUntil("harbor:npc/beachcomber", "jellies", 12, func(wins int) bool {
		return wins >= 3 && p.count("harbor:item/mist-pearl") >= 3
	})
	p.expectQuest(q2, "active", 2)
	c.ok("event.claim", map[string]any{"event_id": "harbor:event/lantern-festival", "idempotency_key": key(t)})
	p.teleport("portal-back", "harbor:map/pier-town")
	p.teleport("portal-lighthouse", "harbor:map/lighthouse")
	p.walkNear("lightkeeper")
	p.choose("harbor:npc/lightkeeper", "deliver")
	p.expectQuest(q2, "active", 3)
	p.walkToAnchor("lamp-room")
	p.expectQuest(q2, "objectives_done", 4)
	p.walkNear("lightkeeper")
	p.choose("harbor:npc/lightkeeper", "claim")
	p.expectQuest(q2, "completed", 4)
	c.ok("inventory.equip", map[string]any{"item_id": "harbor:item/tide-charm"})
	if p.level < 3 {
		t.Fatalf("level after fog-lights = %d, dungeon needs 3", p.level)
	}

	// ---- 码头补给：熬鱼汤、买鱼汤 ----
	p.teleport("portal-down", "harbor:map/pier-town")
	p.walkNear("harbormaster")
	p.encounterUntil("harbor:npc/harbormaster", "crabs", 8, func(int) bool { return p.count("harbor:item/crab-shell") >= 5 })
	p.walkNear("fishwife")
	p.choose("harbor:npc/fishwife", "craft")
	c.ok("craft.make", map[string]any{"recipe_id": "harbor:recipe/fish-soup", "idempotency_key": key(t)})
	p.choose("harbor:npc/fishwife", "shop")
	for range 3 {
		c.ok("shop.buy", map[string]any{"shop_id": "harbor:shop/fishwife", "item_id": "harbor:item/fish-soup", "quantity": 1, "idempotency_key": key(t)})
	}

	// ---- 退潮滩：小海豹、礁石巡逻、打造潮纹鱼叉 ----
	p.teleport("portal-flats", "harbor:map/tide-flats")
	p.walkNear("beachcomber")
	pet := p.choose("harbor:npc/beachcomber", "seal")["result"].(map[string]any)
	c.ok("pet.activate", map[string]any{"pet_uid": pet["pet_uid"]})
	p.walkNear("tidewright")
	p.choose("harbor:npc/tidewright", "patrol")
	p.encounterUntil("harbor:npc/tidewright", "eels", 10, func(wins int) bool {
		return wins > 0 && p.count("harbor:item/eel-hide") >= 2
	})
	p.expectQuest(q3, "objectives_done", 1)
	p.choose("harbor:npc/tidewright", "patrol-claim")
	p.teleport("portal-back", "harbor:map/pier-town")
	p.walkNear("fishwife")
	p.choose("harbor:npc/fishwife", "craft")
	c.ok("craft.make", map[string]any{"recipe_id": "harbor:recipe/tide-harpoon", "idempotency_key": key(t)})
	c.ok("inventory.equip", map[string]any{"item_id": "harbor:item/tide-harpoon"})

	// ---- 副本：沉潮窟（单人队伍 + 伙伴 + 宠物）----
	p.teleport("portal-flats", "harbor:map/tide-flats")
	p.walkNear("tidewright")
	c.ok("party.create", map[string]any{})
	t.Logf("进入沉潮窟：等级 %d", p.level)
	p.runDungeon(3)
	p.refresh()
	t.Logf("通关：等级 %d，气血 %d/%d，雾珠 %d", p.level, p.hp, p.maxHP, p.count("harbor:item/mist-pearl"))
}
