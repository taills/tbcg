// Package world 实现角色建立/进入、网格移动与传送（任务 0009）。位置一律以网格单位存储。
package world

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/taills/tbcg/server/internal/content/catalog"
	"github.com/taills/tbcg/server/internal/content/mapdata"
	"github.com/taills/tbcg/server/modules/character"
	rt "github.com/taills/tbcg/server/modules/runtime"
)

const (
	maxCharactersPerAccount = 4
	// 速度容差：允许客户端以略快于理论步速提交（网络抖动），超出即拒绝。
	speedTolerancePercent = 70
)

func init() {
	rt.Register("character.list", false, listCharacters)
	rt.Register("character.create", false, createCharacter)
	rt.Register("character.enter", false, enterCharacter)
	rt.Register("character.state", true, characterState)
	rt.Register("world.move", true, move)
	rt.Register("world.teleport", true, teleport)
	rt.RegisterSnapshot("map_entities", func(_ context.Context, r *rt.Realm, p *rt.Player) (any, error) {
		return Entities(r, p.State.MapID), nil
	})
}

func listCharacters(c *rt.Ctx) (any, *rt.ProtocolError) {
	rows, err := c.Realm.DB.QueryContext(c.Context, `SELECT character_id, state_version, attributes FROM characters
WHERE game_id=$1 AND realm_id=$2 AND owner_subject=$3 ORDER BY created_at`, c.Realm.GameID, c.Realm.RealmID, c.Session.AccountID)
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	defer rows.Close()
	list := make([]map[string]any, 0)
	for rows.Next() {
		var id string
		var version int64
		var raw []byte
		if err := rows.Scan(&id, &version, &raw); err != nil {
			return nil, rt.FailFromError(err)
		}
		var state rt.CharacterState
		if err := json.Unmarshal(raw, &state); err != nil {
			return nil, rt.FailFromError(err)
		}
		list = append(list, map[string]any{"character_id": id, "name": state.Name, "level": state.Level, "archetype_id": state.ArchetypeID,
			"map_id": state.MapID, "state_version": strconv.FormatInt(version, 10)})
	}
	archetypes := make([]string, 0)
	if content := c.Content(); content != nil {
		for id := range content.Archetypes {
			archetypes = append(archetypes, id)
		}
	}
	return map[string]any{"characters": list, "archetypes": archetypes}, nil
}

func validName(name string) bool {
	count := utf8.RuneCountInString(name)
	if count < 2 || count > 12 || strings.TrimSpace(name) != name {
		return false
	}
	for _, char := range name {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '_' {
			return false
		}
	}
	return true
}

func createCharacter(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		Name           string `json:"name"`
		ArchetypeID    string `json:"archetype_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	content := c.Content()
	archetype := content.Archetypes[input.ArchetypeID]
	if !validName(input.Name) || archetype == nil {
		return nil, rt.Fail("invalid_character", "角色名需 2–12 个字母/数字/下划线，且原型必须存在")
	}
	geometry := content.Geometries[archetype.Server.StartMapID]
	anchor, _ := geometry.Anchor(archetype.Server.StartAnchorID)
	cell, ok := geometry.NearestWalkable(anchor)
	if !ok {
		return nil, rt.Fail("invalid_start", "出生点不可用")
	}
	result, _, err := c.Realm.Operation(c.Context, c.Session.AccountID, "character.create", input.IdempotencyKey, input, func(tx *rt.Tx) (any, error) {
		if _, err := tx.ExecContext(c.Context, `SELECT 1 FROM player_accounts WHERE account_id=$1::uuid FOR UPDATE`, c.Session.AccountID); err != nil {
			return nil, err
		}
		var count int
		if err := tx.QueryRowContext(c.Context, `SELECT count(*) FROM characters WHERE game_id=$1 AND realm_id=$2 AND owner_subject=$3`,
			c.Realm.GameID, c.Realm.RealmID, c.Session.AccountID).Scan(&count); err != nil {
			return nil, err
		}
		if count >= maxCharactersPerAccount {
			return nil, rt.Fail("character_limit", "每个账号最多 4 个角色")
		}
		state := rt.CharacterState{Name: input.Name, ArchetypeID: archetype.ContentID, Level: 1, MapID: archetype.Server.StartMapID,
			X: cell.X, Y: cell.Y, Equipped: map[string]string{}, CreatedRelease: content.ReleaseID}
		stats := character.Stats(content, state.Loadout())
		state.HP, state.MP = stats.MaxHP, stats.MaxMP
		id := rt.NewUUID()
		encoded, _ := json.Marshal(state)
		if _, err := tx.ExecContext(c.Context, `INSERT INTO characters (game_id, realm_id, character_id, owner_subject, instance_schema_version, attributes)
VALUES ($1,$2,$3,$4,$5,$6::jsonb)`, c.Realm.GameID, c.Realm.RealmID, id, c.Session.AccountID, rt.InstanceSchemaVersion, encoded); err != nil {
			if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
				return nil, rt.Fail("name_taken", "角色名已被占用")
			}
			return nil, err
		}
		if _, err := tx.ExecContext(c.Context, `INSERT INTO wallets (game_id, realm_id, character_id, currency_code) VALUES ($1,$2,$3,$4)`,
			c.Realm.GameID, c.Realm.RealmID, id, rt.CurrencyCode); err != nil {
			return nil, err
		}
		if _, err := tx.AdjustCurrency(id, archetype.Server.StartCurrency, "start_currency"); err != nil && archetype.Server.StartCurrency > 0 {
			return nil, err
		}
		for _, stack := range archetype.Server.StartItems {
			if err := tx.AddItem(id, stack.ItemID, stack.Quantity); err != nil {
				return nil, err
			}
		}
		return map[string]any{"character_id": id, "name": input.Name}, nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return json.RawMessage(result), nil
}

func enterCharacter(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		CharacterID string `json:"character_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	if c.Session.CharacterID != "" {
		return nil, rt.Fail("already_entered", "当前连接已进入角色")
	}
	content := c.Content()
	var player *rt.Player
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		record, err := tx.LoadCharacter(input.CharacterID)
		if err != nil {
			return err
		}
		if record.Owner != c.Session.AccountID {
			return rt.Fail("forbidden", "不能进入其他账号的角色")
		}
		if relocateIfInvalid(content, &record.State) {
			if err := tx.SaveCharacter(record); err != nil {
				return err
			}
		}
		player = &rt.Player{CharacterID: record.CharacterID, AccountID: record.Owner, Version: record.Version, State: record.State, Session: c.Session}
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	c.Realm.RegisterPlayer(player)
	c.Session.CharacterID = player.CharacterID
	c.Player = player
	c.Realm.RunEnterHooks(player)
	snapshot, err := c.Realm.FullSnapshot(c.Context, player)
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	c.Realm.Broadcast(player.State.MapID, player.CharacterID, "world.entity_joined", EntityView(player))
	return snapshot, nil
}

// relocateIfInvalid 在 Release 变更导致地图或坐标失效时，把角色放回原型出生点。
func relocateIfInvalid(content *catalog.Catalog, state *rt.CharacterState) bool {
	if geometry := content.Geometries[state.MapID]; geometry != nil && geometry.Walkable(mapdata.Cell{X: state.X, Y: state.Y}) {
		return false
	}
	archetype := content.Archetypes[state.ArchetypeID]
	if archetype == nil {
		for _, candidate := range content.Archetypes {
			archetype = candidate
			break
		}
	}
	geometry := content.Geometries[archetype.Server.StartMapID]
	anchor, _ := geometry.Anchor(archetype.Server.StartAnchorID)
	cell, _ := geometry.NearestWalkable(anchor)
	state.MapID, state.X, state.Y = archetype.Server.StartMapID, cell.X, cell.Y
	return true
}

func characterState(c *rt.Ctx) (any, *rt.ProtocolError) {
	snapshot, err := c.Realm.FullSnapshot(c.Context, c.Player)
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return snapshot, nil
}

// EntityView 是其他玩家可见的角色投影（不含背包等私有状态）。
func EntityView(player *rt.Player) map[string]any {
	return map[string]any{"character_id": player.CharacterID, "name": player.State.Name, "level": player.State.Level,
		"archetype_id": player.State.ArchetypeID, "map_id": player.State.MapID, "x": player.State.X, "y": player.State.Y,
		"mount_id": player.State.MountID, "in_battle": player.BattleID != ""}
}

// Entities 返回地图上的在线角色。
func Entities(r *rt.Realm, mapID string) map[string]any {
	players := make([]map[string]any, 0)
	for _, player := range r.PlayersOnMap(mapID) {
		players = append(players, EntityView(player))
	}
	return map[string]any{"map_id": mapID, "players": players}
}

type cellJSON struct {
	X int `json:"x"`
	Y int `json:"y"`
}

func move(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		Path         []cellJSON `json:"path"`
		StateVersion string     `json:"state_version"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	player := c.Player
	if player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能移动")
	}
	if input.StateVersion != strconv.FormatInt(player.Version, 10) {
		return nil, rt.Fail("state_conflict", "角色状态版本已变化，请同步后重试")
	}
	content := c.Content()
	mapDef := content.Maps[player.State.MapID]
	geometry := content.Geometries[player.State.MapID]
	if mapDef == nil || geometry == nil {
		return nil, rt.Fail("invalid_map", "当前地图不在服务中的 Release")
	}
	if len(input.Path) == 0 || len(input.Path) > mapDef.Server.MaxPathCells {
		return nil, rt.Fail("path_too_long", "路径为空或超过单次最大步数")
	}
	now := c.Realm.Now()
	minimum := time.Duration(mapDef.Server.MinStepIntervalMS) * time.Millisecond
	if now.Before(player.LastStepAt) {
		return nil, rt.Fail("too_fast", "移动速度超过允许范围")
	}
	from := mapdata.Cell{X: player.State.X, Y: player.State.Y}
	for _, step := range input.Path {
		to := mapdata.Cell{X: step.X, Y: step.Y}
		if !geometry.CanStep(from, to) {
			return nil, rt.Fail("invalid_path", "路径越界、穿墙或不连续")
		}
		from = to
	}
	final := from
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		record, err := tx.LoadCharacter(player.CharacterID)
		if err != nil {
			return err
		}
		if record.Version != player.Version {
			return rt.ErrVersionConflict
		}
		record.State.X, record.State.Y = final.X, final.Y
		if err := tx.SaveCharacter(record); err != nil {
			return err
		}
		for _, zone := range zonesOnPath(geometry, mapDef, mapdata.Cell{X: player.State.X, Y: player.State.Y}, input.Path) {
			if err := tx.RunFacts("world.zone_entered", ZoneFact{CharacterID: player.CharacterID, MapID: mapDef.ContentID, AnchorID: zone}); err != nil {
				return err
			}
		}
		tx.SyncPlayer(record)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	// 下一次移动最早时间 = 本次步数 × 最小步距 × 容差。
	player.LastStepAt = now.Add(minimum * time.Duration(len(input.Path)) * speedTolerancePercent / 100)
	c.StateVersion = player.Version
	c.Realm.Broadcast(player.State.MapID, player.CharacterID, "world.entity_moved", map[string]any{"character_id": player.CharacterID, "path": input.Path, "x": final.X, "y": final.Y})
	return map[string]any{"x": final.X, "y": final.Y, "state_version": strconv.FormatInt(player.Version, 10)}, nil
}

// ZoneFact 是进入地图区域的权威事实。
type ZoneFact struct {
	CharacterID string
	MapID       string
	AnchorID    string
}

// zonesOnPath 只在起点位于区域外、且路径进入区域时产生事实，区域内来回移动不会重复推进。
func zonesOnPath(geometry *mapdata.Geometry, mapDef *catalog.Map, start mapdata.Cell, path []cellJSON) []string {
	var zones []string
	for _, zone := range mapDef.Public.Zones {
		anchor, ok := geometry.Anchor(zone.AnchorID)
		if !ok || anchor.Contains(start) {
			continue
		}
		for _, step := range path {
			if anchor.Contains(mapdata.Cell{X: step.X, Y: step.Y}) {
				zones = append(zones, zone.AnchorID)
				break
			}
		}
	}
	return zones
}

func teleport(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		PortalAnchorID string `json:"portal_anchor_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	player := c.Player
	if player.BattleID != "" {
		return nil, rt.Fail("in_battle", "战斗中不能传送")
	}
	content := c.Content()
	mapDef := content.Maps[player.State.MapID]
	geometry := content.Geometries[player.State.MapID]
	var portal *catalog.Portal
	for i := range mapDef.Public.Portals {
		if mapDef.Public.Portals[i].AnchorID == input.PortalAnchorID {
			portal = &mapDef.Public.Portals[i]
		}
	}
	anchor, ok := geometry.Anchor(input.PortalAnchorID)
	if portal == nil || !ok || !anchor.Contains(mapdata.Cell{X: player.State.X, Y: player.State.Y}) {
		return nil, rt.Fail("not_at_portal", "需要站在传送点上")
	}
	if portal.RequiredQuestID != "" {
		var status string
		err := c.Realm.DB.QueryRowContext(c.Context, `SELECT status FROM quest_states WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND quest_id=$4`,
			c.Realm.GameID, c.Realm.RealmID, player.CharacterID, portal.RequiredQuestID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "completed") {
			return nil, rt.Fail("portal_locked", "完成指定任务后才能使用此传送点")
		}
		if err != nil {
			return nil, rt.FailFromError(err)
		}
	}
	targetGeometry := content.Geometries[portal.TargetMapID]
	targetAnchor, _ := targetGeometry.Anchor(portal.TargetAnchorID)
	cell, ok := targetGeometry.NearestWalkable(targetAnchor)
	if !ok {
		return nil, rt.Fail("invalid_portal", "传送目标不可用")
	}
	oldMap := player.State.MapID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		record, err := tx.LoadCharacter(player.CharacterID)
		if err != nil {
			return err
		}
		record.State.MapID, record.State.X, record.State.Y = portal.TargetMapID, cell.X, cell.Y
		if err := tx.SaveCharacter(record); err != nil {
			return err
		}
		tx.SyncPlayer(record)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	c.StateVersion = player.Version
	c.Realm.Broadcast(oldMap, player.CharacterID, "world.entity_left", map[string]any{"character_id": player.CharacterID})
	c.Realm.Broadcast(player.State.MapID, player.CharacterID, "world.entity_joined", EntityView(player))
	return map[string]any{"map_id": player.State.MapID, "x": cell.X, "y": cell.Y, "entities": Entities(c.Realm, player.State.MapID)}, nil
}
