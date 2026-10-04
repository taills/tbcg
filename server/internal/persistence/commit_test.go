package persistence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCommitOutcomeClassification(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		unknown bool
	}{
		{name: "明确拒绝", err: &pgconn.PgError{SeverityUnlocalized: "ERROR", Code: "40001"}},
		{name: "本地化拒绝", err: &pgconn.PgError{Severity: "错误", SeverityUnlocalized: "ERROR", Code: "P0001"}},
		{name: "封装拒绝", err: fmt.Errorf("commit: %w", &pgconn.PgError{SeverityUnlocalized: "ERROR", Code: "23505"})},
		{name: "确认回滚", err: pgx.ErrTxCommitRollback},
		{name: "服务器断连", err: &pgconn.PgError{SeverityUnlocalized: "FATAL", Code: "57P01"}, unknown: true},
		{name: "缺少未本地化严重度", err: &pgconn.PgError{Severity: "ERROR", Code: "40001"}, unknown: true},
		{name: "传输中断", err: io.EOF, unknown: true},
		{name: "超时", err: context.DeadlineExceeded, unknown: true},
		{name: "取消", err: context.Canceled, unknown: true},
		{name: "未知错误", err: errors.New("commit failed"), unknown: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsCommitOutcomeUnknown(test.err); got != test.unknown {
				t.Fatalf("IsCommitOutcomeUnknown(%v) = %v, want %v", test.err, got, test.unknown)
			}
		})
	}
}
