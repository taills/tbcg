package persistence

import "time"

// Game is a persisted game definition.
type Game struct {
	ID          string
	DisplayName string
	CreatedAt   time.Time
}
