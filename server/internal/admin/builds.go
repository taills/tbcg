package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	contentassets "github.com/taills/tbcg/server/internal/content/assets"
	contentbuild "github.com/taills/tbcg/server/internal/content/build"
)

var (
	buildGameIDPattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	ErrInvalidBuildRequest       = errors.New("admin build request is invalid")
	ErrBuildProfileNotConfigured = errors.New("game build profile is not configured")
	ErrBuildOutcomeUnknown       = errors.New("build enqueue outcome unknown; retry with the same idempotency key")
	ErrBuildAlreadyActive        = errors.New("exact revision already has an active build job")
	ErrBuildQueueFull            = errors.New("game build queue is full")
)

type BuildRequest struct {
	ContentID string `json:"content_id"`
	Revision  int32  `json:"revision"`
	BodyHash  string `json:"body_hash"`
}

type BuildCandidate struct {
	GameID      string    `json:"game_id"`
	ContentID   string    `json:"content_id"`
	Revision    int32     `json:"revision"`
	BodyHash    string    `json:"body_hash"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type BuildStatus string

const (
	BuildStatusQueued    BuildStatus = "queued"
	BuildStatusRunning   BuildStatus = "running"
	BuildStatusRetryWait BuildStatus = "retry_wait"
	BuildStatusBuilt     BuildStatus = "built"
	BuildStatusFailed    BuildStatus = "failed"
)

type BuildJob struct {
	JobID                string      `json:"job_id"`
	GameID               string      `json:"game_id"`
	ContentID            string      `json:"content_id"`
	Revision             int32       `json:"revision"`
	BodyHash             string      `json:"body_hash"`
	ReleaseID            string      `json:"release_id"`
	ReleaseVersion       int32       `json:"release_version"`
	Status               BuildStatus `json:"status"`
	CreatedAt            time.Time   `json:"created_at"`
	UpdatedAt            time.Time   `json:"updated_at"`
	PublicArtifactSHA256 *string     `json:"public_artifact_sha256"`
	ErrorCode            *string     `json:"error_code"`
}

type BuildPreparation struct {
	GameID     string
	ContentID  string
	Revision   int32
	BodyHash   string
	Definition []byte
	Profile    contentbuild.CompatibilityProfile
}

// BuildStore keeps the HTTP control-plane boundary separate from durable job operations.
type BuildStore interface {
	ListBuildCandidates(context.Context, string, string, int, int) ([]BuildCandidate, int, error)
	ReadApprovedBuildInput(context.Context, string, string, string, int32, []byte) (BuildPreparation, error)
	FindBuildJobByKey(context.Context, string, string, string, []byte) (BuildJob, bool, error)
	EnqueueBuild(context.Context, string, string, BuildRequest, string, []byte, int64, string, contentassets.FrozenSnapshot, string) (BuildJob, error)
	GetBuildJob(context.Context, string, string, string) (BuildJob, error)
}

func validateBuildRequest(gameID string, input BuildRequest) error {
	if !buildGameIDPattern.MatchString(gameID) || !validContentID(input.ContentID) || input.Revision < 1 {
		return errors.New("invalid build content identity")
	}
	decoded, err := hex.DecodeString(input.BodyHash)
	if err != nil || len(decoded) != sha256.Size || input.BodyHash != strings.ToLower(input.BodyHash) {
		return errors.New("invalid approved revision body hash")
	}
	return nil
}

func validateIdempotencyKey(key string) error {
	if len(key) < 16 || len(key) > 128 {
		return errors.New("idempotency key length must be between 16 and 128 bytes")
	}
	for _, char := range key {
		if !(char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("._~-", char)) {
			return errors.New("idempotency key contains an unsupported character")
		}
	}
	return nil
}

func buildRequestFingerprint(gameID string, input BuildRequest) ([]byte, error) {
	if err := validateBuildRequest(gameID, input); err != nil {
		return nil, fmt.Errorf("validate build request fingerprint input: %w", err)
	}
	canonical, err := json.Marshal(struct {
		GameID    string `json:"game_id"`
		ContentID string `json:"content_id"`
		Revision  int32  `json:"revision"`
		BodyHash  string `json:"body_hash"`
	}{GameID: gameID, ContentID: input.ContentID, Revision: input.Revision, BodyHash: input.BodyHash})
	if err != nil {
		return nil, fmt.Errorf("encode canonical build request: %w", err)
	}
	hash := sha256.Sum256(canonical)
	return append([]byte(nil), hash[:]...), nil
}
