package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

var snapshotParts = map[string]func(context.Context, *Realm, *Player) (any, error){}

// RegisterSnapshot 让领域模块为 character.state / character.enter 提供只读视图片段。
func RegisterSnapshot(key string, fn func(context.Context, *Realm, *Player) (any, error)) {
	registryMu.Lock()
	defer registryMu.Unlock()
	snapshotParts[key] = fn
}

// FullSnapshot 汇总角色本人可见的完整状态。
func (r *Realm) FullSnapshot(ctx context.Context, player *Player) (map[string]any, error) {
	out := r.CharacterView(player)
	keys := make([]string, 0, len(snapshotParts))
	for key := range snapshotParts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		part, err := snapshotParts[key](ctx, r, player)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", key, err)
		}
		out[key] = part
	}
	return out, nil
}

// ReadInventory 读取背包（只读，不加锁）。
func (r *Realm) ReadInventory(ctx context.Context, characterID string) (map[string]int, error) {
	return readInventory(ctx, r.DB, r, characterID)
}

// ReadBalance 读取余额（只读）。
func (r *Realm) ReadBalance(ctx context.Context, characterID string) (int64, error) {
	var balance int64
	err := r.DB.QueryRowContext(ctx, `SELECT balance FROM wallets WHERE game_id=$1 AND realm_id=$2 AND character_id=$3 AND currency_code=$4`,
		r.GameID, r.RealmID, characterID, CurrencyCode).Scan(&balance)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return balance, err
}

// RegisterPlayer 把进入游戏的角色放入在线表；同一角色的旧会话被顶下线。
func (r *Realm) RegisterPlayer(player *Player) {
	if old := r.players[player.CharacterID]; old != nil {
		player.BattleID = old.BattleID
		player.LastStepAt = old.LastStepAt
	}
	if old := r.players[player.CharacterID]; old != nil && old.Session != nil && old.Session != player.Session {
		old.Session.CharacterID = ""
		old.Session.Push("system.notice", map[string]any{"code": "replaced", "message": "角色已在其他连接登录"})
		old.Session.conn.Destroy()
	}
	r.players[player.CharacterID] = player
}

// CharacterName 读取任意角色名（离线也可）。
func (r *Realm) CharacterName(ctx context.Context, characterID string) string {
	if player := r.players[characterID]; player != nil {
		return player.State.Name
	}
	var name string
	_ = r.DB.QueryRowContext(ctx, `SELECT attributes->>'name' FROM characters WHERE game_id=$1 AND realm_id=$2 AND character_id=$3`, r.GameID, r.RealmID, characterID).Scan(&name)
	return name
}

// CharacterIDByName 按名称查找角色 ID（离线也可）。
func (r *Realm) CharacterIDByName(ctx context.Context, name string) (string, bool) {
	if player := r.PlayerByName(name); player != nil {
		return player.CharacterID, true
	}
	var id string
	err := r.DB.QueryRowContext(ctx, `SELECT character_id FROM characters WHERE game_id=$1 AND realm_id=$2 AND attributes->>'name'=$3`, r.GameID, r.RealmID, name).Scan(&id)
	return id, err == nil
}
