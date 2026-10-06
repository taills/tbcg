package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/taills/tbcg/server/internal/persistence"
)

func TestOpenRejectsEmptyAndInvalidDSN(t *testing.T) {
	for _, dsn := range []string{"", "postgres://%zz"} {
		t.Run(fmt.Sprintf("dsn_%q", dsn), func(t *testing.T) {
			store, err := Open(context.Background(), dsn)
			if err == nil {
				if store != nil {
					_ = store.Close()
				}
				t.Fatal("Open succeeded with invalid DSN")
			}
			if store != nil {
				t.Fatal("Open returned a non-nil store on error")
			}
		})
	}
}

func TestOpenReturnsErrorForUnreachableLocalPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local test port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release local test port: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	store, err := Open(ctx, "postgres://test:test@"+address+"/test?connect_timeout=1")
	if err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("Open succeeded against an unreachable local port")
	}
	if store != nil {
		t.Fatal("Open returned a non-nil store on connection error")
	}
}

func TestOpenReturnsErrorWhenContextExpires(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	store, err := Open(ctx, "postgres://test:test@127.0.0.1:1/test?connect_timeout=1")
	if err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("Open succeeded with an expired context")
	}
	if store != nil {
		t.Fatal("Open returned a non-nil store on context error")
	}
}

func TestUnitOfWorkRejectsNilCallback(t *testing.T) {
	store, _ := newFakeStore(t, fakeTxBehavior{})
	if err := store.UnitOfWork().WithinTx(context.Background(), nil); err == nil {
		t.Fatal("WithinTx succeeded with a nil callback")
	}
}

func TestUnitOfWorkBeginFailsAfterDatabaseClose(t *testing.T) {
	store, _ := newFakeStore(t, fakeTxBehavior{})
	if err := store.Close(); err != nil {
		t.Fatalf("close store before transaction: %v", err)
	}
	if err := store.UnitOfWork().WithinTx(context.Background(), func(persistence.TxRepositories) error {
		return nil
	}); err == nil {
		t.Fatal("WithinTx succeeded after the database was closed")
	}
}

func TestUnitOfWorkPostgreSQLCommitErrorIsNotUnknown(t *testing.T) {
	commitErr := &pgconn.PgError{Severity: "ERROR", SeverityUnlocalized: "ERROR", Code: "40001", Message: "serialization failure"}
	store, _ := newFakeStore(t, fakeTxBehavior{commitErr: commitErr})

	err := store.UnitOfWork().WithinTx(context.Background(), func(persistence.TxRepositories) error {
		return nil
	})
	if errors.Is(err, persistence.ErrUnknown) {
		t.Fatalf("PostgreSQL commit error was classified as unknown: %v", err)
	}
	got, ok := errors.AsType[*pgconn.PgError](err)
	if !ok || got != commitErr {
		t.Fatalf("commit error = %v, want original PostgreSQL error %p", got, commitErr)
	}
}

func TestUnitOfWorkFatalPostgreSQLCommitErrorIsUnknownAndPreserved(t *testing.T) {
	commitErr := &pgconn.PgError{Severity: "FATAL", SeverityUnlocalized: "FATAL", Code: "57P01", Message: "admin_shutdown"}
	store, _ := newFakeStore(t, fakeTxBehavior{commitErr: commitErr})

	err := store.UnitOfWork().WithinTx(context.Background(), func(persistence.TxRepositories) error {
		return nil
	})
	if !errors.Is(err, persistence.ErrUnknown) {
		t.Fatalf("FATAL PostgreSQL commit error was not classified as unknown: %v", err)
	}
	got, ok := errors.AsType[*pgconn.PgError](err)
	if !ok || got != commitErr {
		t.Fatalf("commit error = %v, want original PostgreSQL error %p", got, commitErr)
	}
}

func TestUnitOfWorkTransportCommitErrorIsUnknownAndPreserved(t *testing.T) {
	transportErr := errors.New("connection reset")
	store, _ := newFakeStore(t, fakeTxBehavior{commitErr: transportErr})

	err := store.UnitOfWork().WithinTx(context.Background(), func(persistence.TxRepositories) error {
		return nil
	})
	if !errors.Is(err, persistence.ErrUnknown) {
		t.Errorf("commit error %v does not match ErrUnknown", err)
	}
	if !errors.Is(err, transportErr) {
		t.Errorf("commit error %v does not preserve transport error", err)
	}
}

func TestUnitOfWorkCallbackAndRollbackErrorsArePreserved(t *testing.T) {
	callbackErr := errors.New("callback failed")
	rollbackErr := errors.New("rollback failed")
	store, _ := newFakeStore(t, fakeTxBehavior{rollbackErr: rollbackErr})

	err := store.UnitOfWork().WithinTx(context.Background(), func(persistence.TxRepositories) error {
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Errorf("transaction error %v does not preserve callback error", err)
	}
	if !errors.Is(err, rollbackErr) {
		t.Errorf("transaction error %v does not preserve rollback error", err)
	}
}

func TestUnitOfWorkRollsBackWhenCallbackPanics(t *testing.T) {
	store, behavior := newFakeStore(t, fakeTxBehavior{})
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = store.UnitOfWork().WithinTx(context.Background(), func(persistence.TxRepositories) error {
			panic("callback panic")
		})
	}()
	if recovered == nil {
		t.Fatal("WithinTx did not propagate callback panic")
	}
	if behavior.rollbackCalls != 1 {
		t.Errorf("driver Rollback called %d times, want 1", behavior.rollbackCalls)
	}
}

func TestGameRepositoryCreateClassifiesErrors(t *testing.T) {
	uniqueErr := &pgconn.PgError{Code: "23505", Message: "duplicate key"}
	otherErr := errors.New("database unavailable")
	for _, test := range []struct {
		name    string
		execErr error
		wantErr error
	}{
		{name: "success"},
		{name: "unique violation", execErr: uniqueErr, wantErr: persistence.ErrConflict},
		{name: "other database error", execErr: otherErr, wantErr: otherErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newFakeStore(t, fakeTxBehavior{execErr: test.execErr})
			err := store.UnitOfWork().WithinTx(context.Background(), func(repositories persistence.TxRepositories) error {
				return repositories.Games().Create(context.Background(), persistence.Game{ID: "game-1", DisplayName: "Game"})
			})
			if test.wantErr == nil && err != nil {
				t.Fatalf("Create returned unexpected error: %v", err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("Create error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
		})
	}
}

func TestGameRepositoryByIDHandlesRowsAndErrors(t *testing.T) {
	createdAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	queryErr := errors.New("query failed")
	for _, test := range []struct {
		name      string
		queryErr  error
		queryRows [][]driver.Value
		wantGame  persistence.Game
		wantErr   error
	}{
		{
			name:      "found",
			queryRows: [][]driver.Value{{"game-1", "Game", createdAt}},
			wantGame:  persistence.Game{ID: "game-1", DisplayName: "Game", CreatedAt: createdAt},
		},
		{name: "not found", wantErr: persistence.ErrNotFound},
		{name: "query error", queryErr: queryErr, wantErr: queryErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newFakeStore(t, fakeTxBehavior{queryErr: test.queryErr, queryRows: test.queryRows})
			var got persistence.Game
			err := store.UnitOfWork().WithinTx(context.Background(), func(repositories persistence.TxRepositories) error {
				var err error
				got, err = repositories.Games().ByID(context.Background(), "game-1")
				return err
			})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("ByID error = %v, want errors.Is(_, %v)", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ByID returned unexpected error: %v", err)
			}
			if got != test.wantGame {
				t.Errorf("ByID = %+v, want %+v", got, test.wantGame)
			}
		})
	}
}

type fakeTxBehavior struct {
	commitErr     error
	rollbackErr   error
	execErr       error
	queryErr      error
	queryRows     [][]driver.Value
	rollbackCalls int
}

type fakeConnector struct {
	behavior *fakeTxBehavior
}

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return fakeConn(c), nil
}

func (fakeConnector) Driver() driver.Driver {
	return fakeDriver{}
}

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fake driver does not support Open")
}

type fakeConn struct {
	behavior *fakeTxBehavior
}

func (fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake connection does not support Prepare")
}

func (fakeConn) Close() error {
	return nil
}

func (c fakeConn) Begin() (driver.Tx, error) {
	return &fakeTx{behavior: c.behavior}, nil
}

func (c fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &fakeTx{behavior: c.behavior}, nil
}

func (c fakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	if c.behavior.execErr != nil {
		return nil, c.behavior.execErr
	}
	return fakeResult{}, nil
}

func (c fakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	if c.behavior.queryErr != nil {
		return nil, c.behavior.queryErr
	}
	return &fakeRows{
		values: c.behavior.queryRows,
	}, nil
}

type fakeRows struct {
	values [][]driver.Value
	index  int
}

func (rows *fakeRows) Columns() []string {
	return []string{"game_id", "display_name", "created_at"}
}

func (rows *fakeRows) Close() error {
	return nil
}

func (rows *fakeRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error) {
	return 0, errors.New("LastInsertId is unsupported")
}

func (fakeResult) RowsAffected() (int64, error) {
	return 1, nil
}

type fakeTx struct {
	behavior *fakeTxBehavior
}

func (tx fakeTx) Commit() error {
	return tx.behavior.commitErr
}

func (tx fakeTx) Rollback() error {
	tx.behavior.rollbackCalls++
	return tx.behavior.rollbackErr
}

func newFakeStore(t *testing.T, behavior fakeTxBehavior) (*Store, *fakeTxBehavior) {
	t.Helper()
	sharedBehavior := &behavior
	db := sql.OpenDB(fakeConnector{behavior: sharedBehavior})
	store := &Store{db: db}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close fake database: %v", err)
		}
	})
	return store, sharedBehavior
}
