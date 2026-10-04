package persistence

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// IsCommitOutcomeUnknown 仅将明确的 PostgreSQL 拒绝或回滚视为确定失败。
// PostgreSQL 18 提供未本地化严重度；传输错误、取消与 FATAL 不能证明回滚。
func IsCommitOutcomeUnknown(err error) bool {
	if errors.Is(err, pgx.ErrTxCommitRollback) {
		return false
	}
	postgresError, ok := errors.AsType[*pgconn.PgError](err)
	return !ok || postgresError.SeverityUnlocalized != "ERROR"
}
