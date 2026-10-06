package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/tbcg/server/internal/content/mapdata"
	"example.com/tbcg/server/internal/content/schema"
	rt "example.com/tbcg/server/modules/runtime"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/taills/leaf/v2/network"

	_ "example.com/tbcg/server/modules/combat"
	_ "example.com/tbcg/server/modules/companion"
	_ "example.com/tbcg/server/modules/crafting"
	_ "example.com/tbcg/server/modules/dungeon"
	_ "example.com/tbcg/server/modules/event"
	_ "example.com/tbcg/server/modules/guild"
	_ "example.com/tbcg/server/modules/inventory"
	_ "example.com/tbcg/server/modules/party"
	_ "example.com/tbcg/server/modules/quest"
	_ "example.com/tbcg/server/modules/shop"
	_ "example.com/tbcg/server/modules/social"
	_ "example.com/tbcg/server/modules/trade"
	_ "example.com/tbcg/server/modules/world"
)

// newGameTestDatabase 创建隔离数据库并执行全部迁移，返回维护者连接。
func newGameTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	maintenanceURL := os.Getenv("TBCG_TEST_DATABASE_URL")
	if maintenanceURL == "" {
		if os.Getenv("TBCG_REQUIRE_TEST_DATABASE") == "1" {
			t.Fatal("CI requires TBCG_TEST_DATABASE_URL")
		}
		t.Skip("set TBCG_TEST_DATABASE_URL to a disposable PostgreSQL maintenance database URL")
	}
	ternPath, err := exec.LookPath("tern")
	if err != nil {
		t.Fatalf("find tern: %v", err)
	}
	maintenanceConfig := parseIsolatedTestDatabase(t, maintenanceURL)
	maintenanceDB := stdlib.OpenDB(*maintenanceConfig)
	t.Cleanup(func() { _ = maintenanceDB.Close() })
	prepareAdminRoleFixture(t, ctx, maintenanceDB, maintenanceConfig)
	databaseName, err := randomTestDatabaseName()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maintenanceDB.ExecContext(ctx, "CREATE DATABASE \""+databaseName+"\""); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = maintenanceDB.ExecContext(cleanup, "DROP DATABASE IF EXISTS \""+databaseName+"\" WITH (FORCE)")
	})
	env, err := ternEnvironment(maintenanceConfig, databaseName, filepath.Join(findServerRoot(t), "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	runTernMigration(t, ctx, ternPath, findServerRoot(t), env, "migrate")
	config := maintenanceConfig.Copy()
	config.Database = databaseName
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type gameClient struct {
	t       *testing.T
	conn    *websocket.Conn
	mu      sync.Mutex
	next    int
	results map[string]chan map[string]any
	pushes  chan map[string]any
}

func dialGame(t *testing.T, address string) *gameClient {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+address, nil)
	if err != nil {
		t.Fatalf("dial game server: %v", err)
	}
	client := &gameClient{t: t, conn: conn, results: map[string]chan map[string]any{}, pushes: make(chan map[string]any, 1024)}
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				close(client.pushes)
				return
			}
			var message map[string]any
			if json.Unmarshal(data, &message) != nil {
				continue
			}
			id, _ := message["request_id"].(string)
			client.mu.Lock()
			ch := client.results[id]
			client.mu.Unlock()
			if ch != nil {
				ch <- message
			} else {
				client.pushes <- message
			}
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return client
}

func (c *gameClient) call(messageType string, payload map[string]any) map[string]any {
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

func (c *gameClient) ok(messageType string, payload map[string]any) map[string]any {
	c.t.Helper()
	response := c.call(messageType, payload)
	if response["success"] != true {
		c.t.Fatalf("%s failed: %v", messageType, response["error"])
	}
	data, _ := response["data"].(map[string]any)
	return data
}

func (c *gameClient) fail(messageType string, payload map[string]any, code string) {
	c.t.Helper()
	response := c.call(messageType, payload)
	errorObject, _ := response["error"].(map[string]any)
	if response["success"] != false || errorObject["code"] != code {
		c.t.Fatalf("%s expected failure %s, got %v", messageType, code, response)
	}
}

func (c *gameClient) waitPush(messageType string, match func(map[string]any) bool) map[string]any {
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

var keySeq int

func key() string {
	keySeq++
	return fmt.Sprintf("test-key-%08d", keySeq)
}

func httpJSON(t *testing.T, server *httptest.Server, method, path string, body any) map[string]any {
	t.Helper()
	encoded, _ := json.Marshal(body)
	request, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(response.Body).Decode(&decoded)
	return decoded
}

func freeAddress(t *testing.T) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func TestGameRuntimeEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	db := newGameTestDatabase(t, ctx)
	if _, err := db.ExecContext(ctx, `INSERT INTO games (game_id, display_name) VALUES ('demo','Demo')`); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(findServerRoot(t))
	contracts := os.DirFS(filepath.Join(root, "contracts"))
	registry, err := schema.NewRegistry(contracts)
	if err != nil {
		t.Fatal(err)
	}
	geometry, err := mapdata.NewValidator(contracts)
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := rt.LoadCatalog(filepath.Join(root, "contracts/protocol/message_catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	loader := &rt.ReleaseLoader{Registry: registry, Geometry: geometry, DevDir: os.DirFS(filepath.Join(root, "games/demo/content"))}
	ownerConn, epoch, err := rt.AcquireOwnership(ctx, db, "demo", "main", true)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerConn.Close()
	if _, _, err := rt.AcquireOwnership(ctx, db, "demo", "main", false); err == nil {
		t.Fatal("second owner acquired the same realm")
	}
	realm := rt.NewRealm("demo", "main", db, protocol, loader)
	realm.Epoch = epoch
	content, info, bundle, err := loader.LoadDev("demo")
	if err != nil {
		t.Fatal(err)
	}
	realm.UseDevContent(content, info)
	go realm.Run(ctx)
	auth, err := rt.NewAuth(db, "demo")
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer((&rt.HTTPAPI{Realm: realm, Auth: auth, Loader: loader, DevBundle: bundle}).Handler())
	defer api.Close()
	address := freeAddress(t)
	wsServer := &network.WSServer{Addr: address, MaxConnNum: 16, PendingWriteNum: 256, MaxMsgLen: rt.MaxMessageBytes, HTTPTimeout: 5 * time.Second,
		NewAgent: func(conn *network.WSConn) network.Agent { return rt.NewSession(conn, realm, auth, "0.1.0") }}
	wsServer.Start()
	defer wsServer.Close()
	time.Sleep(200 * time.Millisecond)

	realmInfo := httpJSON(t, api, http.MethodGet, "/api/v1/realm", nil)["data"].(map[string]any)
	releaseID := realmInfo["release_id"].(string)
	publicBundle, err := api.Client().Get(api.URL + "/api/v1/releases/" + releaseID + "/public")
	if err != nil {
		t.Fatal(err)
	}
	var bundleBuffer bytes.Buffer
	_, _ = bundleBuffer.ReadFrom(publicBundle.Body)
	publicBundle.Body.Close()
	if strings.Contains(bundleBuffer.String(), "drop_weight_basis_points") || strings.Contains(bundleBuffer.String(), "\"loot\"") {
		t.Fatal("public bundle leaked private server fields")
	}

	login := func(name string) string {
		if response := httpJSON(t, api, http.MethodPost, "/api/v1/accounts", map[string]string{"username": name, "password": "correct horse battery"}); response["success"] != true {
			t.Fatalf("register %s: %v", name, response)
		}
		response := httpJSON(t, api, http.MethodPost, "/api/v1/sessions", map[string]string{"username": name, "password": "correct horse battery"})
		return response["data"].(map[string]any)["session_token"].(string)
	}
	hello := func(client *gameClient, token string) {
		client.ok("system.hello", map[string]any{"game_id": "demo", "realm_id": "main", "client_build": "0.1.0", "release_id": releaseID,
			"session_token": token, "capabilities": []string{"protocol.v1"}})
	}
	alice, bob := dialGame(t, address), dialGame(t, address)
	hello(alice, login("alice"))
	hello(bob, login("bob"))
	aliceID := alice.ok("character.create", map[string]any{"name": "阿丽", "archetype_id": "demo:archetype/wanderer", "idempotency_key": key()})["character_id"].(string)
	bob.fail("character.create", map[string]any{"name": "阿丽", "archetype_id": "demo:archetype/wanderer", "idempotency_key": key()}, "name_taken")
	bobID := bob.ok("character.create", map[string]any{"name": "阿宝", "archetype_id": "demo:archetype/wanderer", "idempotency_key": key()})["character_id"].(string)
	bob.fail("character.enter", map[string]any{"character_id": aliceID}, "forbidden")

	// Bob 作为第二名副本成员：入场前直接设定等级与地图（测试准备数据）。
	if _, err := db.ExecContext(ctx, `UPDATE characters SET attributes = attributes || '{"level":2,"exp":120,"map_id":"demo:map/forest-edge","x":0,"y":0}'::jsonb WHERE character_id=$1`, bobID); err != nil {
		t.Fatal(err)
	}
	state := alice.ok("character.enter", map[string]any{"character_id": aliceID})
	version := state["state_version"].(string)
	bob.ok("character.enter", map[string]any{"character_id": bobID})

	move := func(path [][2]int) {
		t.Helper()
		cells := make([]map[string]int, 0, len(path))
		for _, cell := range path {
			cells = append(cells, map[string]int{"x": cell[0], "y": cell[1]})
		}
		data := alice.ok("world.move", map[string]any{"path": cells, "state_version": version})
		version = data["state_version"].(string)
		time.Sleep(time.Duration(len(path)) * 130 * time.Millisecond)
	}
	alice.fail("world.move", map[string]any{"path": []map[string]int{{"x": 1, "y": 1}}, "state_version": version}, "invalid_path")
	alice.fail("world.move", map[string]any{"path": []map[string]int{{"x": 2, "y": 0}}, "state_version": version}, "invalid_path")
	alice.fail("world.teleport", map[string]any{"portal_anchor_id": "portal"}, "not_at_portal")
	move([][2]int{{0, 1}, {0, 2}, {0, 3}, {1, 3}, {2, 3}})

	dialog := alice.ok("npc.interact", map[string]any{"npc_id": "demo:npc/guide"})["dialog"].(map[string]any)
	choose := func(npc, option string) map[string]any {
		t.Helper()
		current := alice.ok("npc.interact", map[string]any{"npc_id": npc})["dialog"].(map[string]any)
		return alice.ok("npc.choose", map[string]any{"npc_id": npc, "node_id": current["node_id"], "option_id": option, "idempotency_key": key()})
	}
	_ = dialog
	choose("demo:npc/guide", "accept")
	move([][2]int{{1, 4}})
	move([][2]int{{2, 3}})
	if _, err := db.ExecContext(ctx, `INSERT INTO inventory_items (game_id, realm_id, character_id, item_id, quantity) VALUES ('demo','main',$1,'demo:item/herb',2)`, aliceID); err != nil {
		t.Fatal(err)
	}
	choose("demo:npc/guide", "deliver")
	battleStart := choose("demo:npc/guide", "train")
	battleID := battleStart["battle_id"].(string)
	fight := func(client *gameClient, battleID string) string {
		t.Helper()
		for {
			view := client.ok("battle.state", map[string]any{"battle_id": battleID})
			units := view["units"].([]any)
			target := ""
			for _, raw := range units {
				unit := raw.(map[string]any)
				if unit["side"].(float64) == 1 && unit["hp"].(float64) > 0 {
					target = unit["id"].(string)
					break
				}
			}
			response := client.call("battle.command", map[string]any{"battle_id": battleID, "round": view["round"], "command": map[string]any{"kind": "attack", "target_id": target}})
			if response["success"] != true {
				code := response["error"].(map[string]any)["code"]
				if code == "not_in_battle" || code == "battle_over" {
					break
				}
				if code != "stale_round" && code != "already_submitted" {
					t.Fatalf("battle.command: %v", response["error"])
				}
			}
			round := client.waitPush("battle.round", func(data map[string]any) bool { return data["battle_id"] == battleID && data["events"] != nil })
			if outcome, _ := round["outcome"].(string); outcome != "" {
				ended := client.waitPush("battle.ended", func(data map[string]any) bool { return data["battle_id"] == battleID })
				return ended["outcome"].(string)
			}
		}
		return ""
	}
	if outcome := fight(alice, battleID); outcome != "victory" {
		t.Fatalf("training battle outcome = %s", outcome)
	}
	claimed := choose("demo:npc/guide", "claim")
	if claimed["result"] == nil {
		t.Fatalf("claim returned %v", claimed)
	}
	alice.fail("npc.choose", map[string]any{"npc_id": "demo:npc/guide", "node_id": "root", "option_id": "claim", "idempotency_key": key()}, "condition_failed")
	state = alice.ok("character.state", map[string]any{})
	version = state["state_version"].(string)
	if int(state["state"].(map[string]any)["level"].(float64)) < 2 {
		t.Fatalf("expected level up after quest, got %v", state["state"])
	}
	alice.ok("inventory.equip", map[string]any{"item_id": "demo:item/wooden-sword"})
	choose("demo:npc/guide", "sect")
	state = alice.ok("character.state", map[string]any{})
	version = state["state_version"].(string)
	move([][2]int{{3, 4}, {4, 4}, {5, 4}})
	alice.ok("world.teleport", map[string]any{"portal_anchor_id": "portal"})
	state = alice.ok("character.state", map[string]any{})
	version = state["state_version"].(string)
	move([][2]int{{1, 0}, {2, 0}, {3, 0}, {3, 1}})

	// 队伍与双人副本。
	partyView := alice.ok("party.create", map[string]any{})
	partyID := partyView["party_id"].(string)
	bob.fail("party.invite", map[string]any{"character_name": "阿丽"}, "not_leader")
	alice.ok("party.invite", map[string]any{"character_name": "阿宝"})
	bob.ok("party.respond", map[string]any{"party_id": partyID, "accept": true})
	bob.ok("party.ready", map[string]any{"ready": true})
	entered := choose("demo:npc/merchant", "dungeon")["dungeon"].(map[string]any)
	runID := entered["run_id"].(string)
	bob.fail("dungeon.start_stage", map[string]any{"run_id": runID}, "not_allowed")
	for stage := 0; stage < 2; stage++ {
		started := alice.ok("dungeon.start_stage", map[string]any{"run_id": runID})
		stageBattle := started["battle_id"].(string)
		var wg sync.WaitGroup
		outcomes := make([]string, 2)
		for i, client := range []*gameClient{alice, bob} {
			wg.Add(1)
			go func(i int, client *gameClient) {
				defer wg.Done()
				outcomes[i] = fight(client, stageBattle)
			}(i, client)
		}
		wg.Wait()
		if outcomes[0] != "victory" {
			t.Fatalf("dungeon stage %d outcome %v", stage, outcomes)
		}
		time.Sleep(300 * time.Millisecond)
	}
	claimKey := key()
	first := alice.ok("dungeon.claim", map[string]any{"run_id": runID, "idempotency_key": claimKey})
	replay := alice.ok("dungeon.claim", map[string]any{"run_id": runID, "idempotency_key": claimKey})
	if fmt.Sprint(first) != fmt.Sprint(replay) {
		t.Fatalf("idempotent replay differs: %v vs %v", first, replay)
	}
	alice.fail("dungeon.claim", map[string]any{"run_id": runID, "idempotency_key": key()}, "already_claimed")
	bob.ok("dungeon.claim", map[string]any{"run_id": runID, "idempotency_key": key()})
	var rewardRows int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM dungeon_rewards WHERE run_id=$1::uuid`, runID).Scan(&rewardRows)
	if rewardRows != 2 {
		t.Fatalf("dungeon rewards = %d", rewardRows)
	}

	// 商店、制作、好友、交易、活动。
	shop := choose("demo:npc/merchant", "shop")["shop"].(map[string]any)
	if shop["shop_id"] != "demo:shop/merchant" {
		t.Fatalf("shop view %v", shop)
	}
	alice.ok("shop.buy", map[string]any{"shop_id": "demo:shop/merchant", "item_id": "demo:item/healing-potion", "quantity": 1, "idempotency_key": key()})
	if _, err := db.ExecContext(ctx, `INSERT INTO inventory_items (game_id, realm_id, character_id, item_id, quantity) VALUES ('demo','main',$1,'demo:item/wolf-fang',3)
ON CONFLICT (game_id, realm_id, character_id, item_id) DO UPDATE SET quantity = 3`, aliceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO inventory_items (game_id, realm_id, character_id, item_id, quantity) VALUES ('demo','main',$1,'demo:item/herb',2)
ON CONFLICT (game_id, realm_id, character_id, item_id) DO UPDATE SET quantity = inventory_items.quantity + 2`, aliceID); err != nil {
		t.Fatal(err)
	}
	choose("demo:npc/merchant", "craft")
	crafted := alice.ok("craft.make", map[string]any{"recipe_id": "demo:recipe/iron-sword", "idempotency_key": key()})
	if crafted["success"] != true {
		t.Fatalf("craft result %v", crafted)
	}
	alice.ok("friend.request", map[string]any{"character_name": "阿宝"})
	bob.ok("friend.respond", map[string]any{"character_id": aliceID, "accept": true})
	friends := bob.ok("friend.list", map[string]any{})["friends"].([]any)
	if len(friends) != 1 {
		t.Fatalf("friends = %v", friends)
	}
	tradeView := alice.ok("trade.invite", map[string]any{"character_name": "阿宝"})
	tradeID := tradeView["trade_id"].(string)
	bob.ok("trade.respond", map[string]any{"trade_id": tradeID, "accept": true})
	offered := alice.ok("trade.offer", map[string]any{"trade_id": tradeID, "items": []map[string]any{{"item_id": "demo:item/healing-potion", "quantity": 1}}, "currency": 10})
	offerVersion := offered["offer_version"]
	bob.fail("trade.lock", map[string]any{"trade_id": tradeID, "offer_version": 0}, "offer_changed")
	alice.fail("trade.confirm", map[string]any{"trade_id": tradeID, "offer_version": offerVersion}, "not_locked")
	alice.ok("trade.lock", map[string]any{"trade_id": tradeID, "offer_version": offerVersion})
	bob.ok("trade.lock", map[string]any{"trade_id": tradeID, "offer_version": offerVersion})
	alice.ok("trade.confirm", map[string]any{"trade_id": tradeID, "offer_version": offerVersion})
	bob.ok("trade.confirm", map[string]any{"trade_id": tradeID, "offer_version": offerVersion})
	var traded int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM trade_records WHERE trade_id=$1::uuid`, tradeID).Scan(&traded)
	if traded != 1 {
		t.Fatalf("trade records = %d", traded)
	}
	events := alice.ok("event.list", map[string]any{})["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("events = %v", events)
	}
	alice.fail("event.claim", map[string]any{"event_id": "demo:event/slime-festival", "idempotency_key": key()}, "objective_incomplete")
	alice.ok("event.leaderboard", map[string]any{"event_id": "demo:event/slime-festival"})

	// 非参与者不能领取副本奖励。
	carol := dialGame(t, address)
	hello(carol, login("carol"))
	carolID := carol.ok("character.create", map[string]any{"name": "阿卡", "archetype_id": "demo:archetype/wanderer", "idempotency_key": key()})["character_id"].(string)
	carol.ok("character.enter", map[string]any{"character_id": carolID})
	carol.fail("dungeon.claim", map[string]any{"run_id": runID, "idempotency_key": key()}, "not_participant")

	// 宠物、坐骑、伙伴与公会。
	if _, err := db.ExecContext(ctx, `INSERT INTO inventory_items (game_id, realm_id, character_id, item_id, quantity) VALUES ('demo','main',$1,'demo:item/spirit-egg',1),('demo','main',$1,'demo:item/pony-whistle',2)`, aliceID); err != nil {
		t.Fatal(err)
	}
	used := alice.ok("inventory.use", map[string]any{"item_id": "demo:item/spirit-egg", "idempotency_key": key()})
	petUID := used["effects"].(map[string]any)["pet_uid"].(string)
	alice.ok("pet.activate", map[string]any{"pet_uid": petUID})
	bob.fail("pet.activate", map[string]any{"pet_uid": petUID}, "not_owner")
	alice.ok("inventory.use", map[string]any{"item_id": "demo:item/pony-whistle", "idempotency_key": key()})
	alice.fail("inventory.use", map[string]any{"item_id": "demo:item/pony-whistle", "idempotency_key": key()}, "already_owned")
	alice.ok("mount.equip", map[string]any{"mount_id": "demo:mount/pony"})
	recruited := choose("demo:npc/merchant", "partner")
	if recruited["result"] == nil {
		t.Fatalf("recruit result %v", recruited)
	}
	alice.ok("partner.formation", map[string]any{"partner_id": "demo:partner/swordsman", "in_formation": true})
	companions := alice.ok("character.state", map[string]any{})["companions"].(map[string]any)
	if len(companions["pets"].([]any)) != 1 || len(companions["mounts"].([]any)) != 1 || len(companions["partners"].([]any)) != 1 {
		t.Fatalf("companions %v", companions)
	}
	bob.ok("guild.create", map[string]any{"name": "试炼会", "idempotency_key": key()})
	bob.ok("guild.invite", map[string]any{"character_name": "阿卡"})
	guildInfo := carol.ok("guild.info", map[string]any{})
	invites := guildInfo["invites"].([]any)
	if len(invites) != 1 {
		t.Fatalf("guild invites %v", guildInfo)
	}
	carol.ok("guild.respond", map[string]any{"guild_id": invites[0].(map[string]any)["guild_id"], "accept": true})
	carol.fail("guild.kick", map[string]any{"character_id": bobID}, "forbidden")
	members := bob.ok("guild.info", map[string]any{})["guild"].(map[string]any)["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("guild members %v", members)
	}

	// 断线重连恢复服务器记录位置与版本。
	recorded := alice.ok("character.state", map[string]any{})
	_ = alice.conn.Close()
	time.Sleep(300 * time.Millisecond)
	again := dialGame(t, address)
	response := httpJSON(t, api, http.MethodPost, "/api/v1/sessions", map[string]string{"username": "alice", "password": "correct horse battery"})
	hello(again, response["data"].(map[string]any)["session_token"].(string))
	restored := again.ok("character.enter", map[string]any{"character_id": aliceID})
	if fmt.Sprint(restored["state"].(map[string]any)["x"]) != fmt.Sprint(recorded["state"].(map[string]any)["x"]) || restored["state_version"] != recorded["state_version"] {
		t.Fatalf("reconnect state mismatch: %v vs %v", restored["state"], recorded["state"])
	}
	var ledgerRows, outboxRows int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM wallet_ledger`).Scan(&ledgerRows)
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM outbox`).Scan(&outboxRows)
	if ledgerRows == 0 || outboxRows == 0 {
		t.Fatalf("expected ledger and outbox rows, got %d/%d", ledgerRows, outboxRows)
	}
}
