package admin

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound  = errors.New("admin resource not found")
	ErrConflict  = errors.New("admin resource conflict")
	ErrForbidden = errors.New("admin operation is forbidden")
)

type Role string

const (
	RoleReader    Role = "Reader"
	RoleAuthor    Role = "Author"
	RoleReviewer  Role = "Reviewer"
	RolePublisher Role = "Publisher"
)

type User struct {
	ID       string
	Username string
	Hash     string
}

type GameRole struct {
	GameID string   `json:"game_id"`
	Roles  []string `json:"roles"`
}

type Session struct {
	ID            string
	AdminID       string
	Username      string
	CSRFHash      []byte
	ExpiresAt     time.Time
	IdleExpiresAt time.Time
	Roles         []GameRole
}

type Draft struct {
	DraftID      string          `json:"draft_id"`
	GameID       string          `json:"game_id"`
	ContentID    string          `json:"content_id"`
	DraftVersion string          `json:"draft_version"`
	Definition   json.RawMessage `json:"definition"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type Revision struct {
	GameID     string          `json:"game_id"`
	ContentID  string          `json:"content_id"`
	Revision   int32           `json:"revision"`
	BodyHash   string          `json:"body_hash"`
	Definition json.RawMessage `json:"definition"`
	Submitted  time.Time       `json:"submitted_at"`
}

type Approval struct {
	GameID    string    `json:"game_id"`
	ContentID string    `json:"content_id"`
	Revision  int32     `json:"revision"`
	BodyHash  string    `json:"body_hash"`
	Decision  string    `json:"decision"`
	Reviewer  string    `json:"reviewer_id"`
	Comment   string    `json:"comment,omitempty"`
	Reviewed  time.Time `json:"reviewed_at"`
}

type AuditEvent struct {
	GameID      string
	ActorID     string
	Action      string
	Resource    string
	ResourceID  string
	Outcome     string
	DetailsJSON []byte
}

type Store interface {
	LoadSession(context.Context, []byte) (Session, error)
	CreateSession(context.Context, []byte, []byte, time.Time, time.Time) error
	UpdateCSRF(context.Context, []byte, []byte) error
	TouchSession(context.Context, []byte, time.Time) error
	Authenticate(context.Context, string) (User, error)
	RotateSession(context.Context, []byte, []byte, []byte, time.Time, time.Time, string) error
	RevokeSession(context.Context, []byte) error
	HasRole(context.Context, string, string, ...Role) (bool, error)
	WriteAudit(context.Context, AuditEvent) error
	GetDraft(context.Context, string, string) (Draft, error)
	ListDrafts(context.Context, string, string, int, int) ([]Draft, int, error)
	CreateDraft(context.Context, string, string, []byte, string) (Draft, error)
	CompareAndSwapDraft(context.Context, string, string, int64, []byte, string) (Draft, *Draft, error)
	SubmitRevision(context.Context, string, string, int64, string) (Revision, error)
	ReviewQueue(context.Context, string, int, int) ([]Revision, int, error)
	ReviewRevision(context.Context, string, string, int32, []byte, string, string, string) (Approval, error)
}
