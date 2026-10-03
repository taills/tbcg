package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"example.com/tbcg/server/internal/persistence"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultMaxOpenConnections    = 10
	defaultMaxIdleConnections    = 5
	defaultConnectionMaxLifetime = time.Hour
	defaultConnectionMaxIdleTime = 5 * time.Minute
	defaultPingTimeout           = 5 * time.Second
)

// Store provides PostgreSQL-backed persistence operations.
type Store struct {
	db *sql.DB
}

// Open connects to PostgreSQL and verifies the connection before returning.
func Open(ctx context.Context, dsn string) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("open PostgreSQL store: DSN is empty")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL connection string: %w", err)
	}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(defaultMaxOpenConnections)
	db.SetMaxIdleConns(defaultMaxIdleConnections)
	db.SetConnMaxLifetime(defaultConnectionMaxLifetime)
	db.SetConnMaxIdleTime(defaultConnectionMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, defaultPingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return &Store{db: db}, nil
}

// UnitOfWork returns the transaction boundary for this store.
func (s *Store) UnitOfWork() persistence.UnitOfWork {
	return unitOfWork{db: s.db}
}

// Close releases the store's database resources.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close PostgreSQL store: %w", err)
	}
	return nil
}

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type unitOfWork struct {
	db *sql.DB
}

func (u unitOfWork) WithinTx(ctx context.Context, fn func(persistence.TxRepositories) error) error {
	if fn == nil {
		return errors.New("run transaction: callback is nil")
	}
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin PostgreSQL transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	repositories := txRepositories{games: gameRepository{queryer: tx}}
	if err := fn(repositories); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(
				fmt.Errorf("transaction callback failed: %w", err),
				fmt.Errorf("rollback PostgreSQL transaction: %w", rollbackErr),
			)
		}
		return fmt.Errorf("transaction callback failed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		if _, ok := errors.AsType[*pgconn.PgError](err); ok {
			return fmt.Errorf("commit PostgreSQL transaction rejected: %w", err)
		}
		return fmt.Errorf("commit PostgreSQL transaction outcome unknown: %w", errors.Join(persistence.ErrUnknown, err))
	}
	return nil
}

type txRepositories struct {
	games persistence.GameRepository
}

func (r txRepositories) Games() persistence.GameRepository {
	return r.games
}

func isUniqueViolation(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == "23505"
}

type gameRepository struct {
	queryer queryer
}

func (r gameRepository) Create(ctx context.Context, game persistence.Game) error {
	_, err := r.queryer.ExecContext(ctx,
		"INSERT INTO games (game_id, display_name) VALUES ($1, $2)",
		game.ID,
		game.DisplayName,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("create game %q: %w", game.ID, errors.Join(persistence.ErrConflict, err))
		}
		return fmt.Errorf("create game %q: %w", game.ID, err)
	}
	return nil
}

func (r gameRepository) ByID(ctx context.Context, id string) (persistence.Game, error) {
	var game persistence.Game
	err := r.queryer.QueryRowContext(ctx,
		"SELECT game_id, display_name, created_at FROM games WHERE game_id = $1",
		id,
	).Scan(&game.ID, &game.DisplayName, &game.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return persistence.Game{}, fmt.Errorf("find game %q: %w", id, persistence.ErrNotFound)
	}
	if err != nil {
		return persistence.Game{}, fmt.Errorf("find game %q: %w", id, err)
	}
	return game, nil
}
