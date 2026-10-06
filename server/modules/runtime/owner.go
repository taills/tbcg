package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

// AcquireOwnership 以 advisory lock 保证每个 Realm 只有一个运行进程，并递增 owner_epoch。
// 返回的连接必须在进程生命周期内保持打开；关闭即释放所有权。
func AcquireOwnership(ctx context.Context, db *sql.DB, gameID, realmID string, bootstrap bool) (*sql.Conn, int64, error) {
	if bootstrap {
		if _, err := db.ExecContext(ctx, `INSERT INTO realms (game_id, realm_id, display_name) VALUES ($1,$2,$2) ON CONFLICT DO NOTHING`, gameID, realmID); err != nil {
			return nil, 0, fmt.Errorf("bootstrap realm: %w", err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO owner_guards (game_id, realm_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, gameID, realmID); err != nil {
			return nil, 0, fmt.Errorf("bootstrap owner guard: %w", err)
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("open ownership connection: %w", err)
	}
	digest := sha256.Sum256([]byte("tbcg-realm-owner\x00" + gameID + "\x00" + realmID))
	key := int64(binary.BigEndian.Uint64(digest[:8])) // #nosec G115 -- 任意位模式都是合法 advisory key。
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
		_ = conn.Close()
		return nil, 0, fmt.Errorf("acquire realm advisory lock: %w", err)
	}
	if !locked {
		_ = conn.Close()
		return nil, 0, errors.New("another runtime process owns this realm")
	}
	var epoch int64
	if err := conn.QueryRowContext(ctx, `UPDATE owner_guards SET owner_epoch=owner_epoch+1 WHERE game_id=$1 AND realm_id=$2 RETURNING owner_epoch`, gameID, realmID).Scan(&epoch); err != nil {
		_ = conn.Close()
		return nil, 0, fmt.Errorf("advance owner epoch: %w", err)
	}
	return conn, epoch, nil
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
