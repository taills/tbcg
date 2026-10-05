package postgres

import (
	"context"
	"errors"
	"fmt"
)

// CleanupExpiredSessions deletes at most batchSize expired sessions per call.
func (r *AdminRepository) CleanupExpiredSessions(ctx context.Context, batchSize int) (int64, error) {
	if batchSize < 1 || batchSize > 500 {
		return 0, errors.New("expired session cleanup batch must be between 1 and 500")
	}
	result, err := r.db.ExecContext(ctx, `
WITH expired AS (
	SELECT session_id
	FROM admin_sessions
	WHERE LEAST(expires_at, idle_expires_at) <= now()
	ORDER BY LEAST(expires_at, idle_expires_at), session_id
	LIMIT $1
	FOR UPDATE SKIP LOCKED
)
DELETE FROM admin_sessions s USING expired e
WHERE s.session_id = e.session_id`, batchSize)
	if err != nil {
		return 0, fmt.Errorf("delete expired admin sessions in bounded batch: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read expired admin session cleanup count: %w", err)
	}
	return deleted, nil
}
