package persistence

import "context"

// GameRepository provides persistence operations for games.
type GameRepository interface {
	Create(ctx context.Context, game Game) error
	ByID(ctx context.Context, id string) (Game, error)
}

// TxRepositories provides the repositories available within a transaction.
type TxRepositories interface {
	Games() GameRepository
}

// UnitOfWork runs repository operations within a transaction.
type UnitOfWork interface {
	WithinTx(ctx context.Context, fn func(TxRepositories) error) error
}
