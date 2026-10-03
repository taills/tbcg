package persistence

import "errors"

var (
	// ErrNotFound indicates that a requested entity does not exist.
	ErrNotFound = errors.New("persistence: not found")
	// ErrConflict indicates that an operation conflicts with existing data.
	ErrConflict = errors.New("persistence: conflict")
	// ErrUnknown indicates that a transaction's commit outcome could not be confirmed.
	ErrUnknown = errors.New("persistence: outcome unknown")
)
