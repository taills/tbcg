package release

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrActivationConflict       = errors.New("release: active release changed during publish")
	ErrActivationOutcomeUnknown = errors.New("release: activation outcome unknown; reconciliation required")
	ErrReleaseIDInvalid         = errors.New("release: invalid release ID")
	ErrInvalidDefinition        = errors.New("release: invalid definition")
	ErrOwnerEpochMismatch       = errors.New("release: owner epoch mismatch")
	ErrReleaseConflict          = errors.New("release: release ID reused with different content")
)

var (
	gameIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	releaseIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type Definition struct {
	GameID                string
	RealmID               string
	OwnerEpoch            int64
	ReleaseID             string
	ReleaseVersion        int
	ContentSchemaVersion  int
	InstanceSchemaVersion int
	RulesetVersion        int
	ProtocolVersion       int
	ClientMinBuild        string
	Manifest              json.RawMessage
}

type Artifacts struct {
	Public  []byte
	Private []byte
}

type Published struct {
	PreviousReleaseID string
	ReleaseID         string
	PublicHash        string
	PrivateHash       string
}

type Builder func(context.Context, Definition) (Artifacts, error)
type ActivationValidator func(context.Context, Definition) error

type Publisher struct {
	db          *sql.DB
	artifactDir string
}

func NewPublisher(db *sql.DB, artifactDir string) *Publisher {
	return &Publisher{db: db, artifactDir: artifactDir}
}

// Publish 在暂存产物或切换活动指针之前构建不可变产物。
func (p *Publisher) Publish(ctx context.Context, definition Definition, build Builder, validate ActivationValidator) (Published, error) {
	if err := validateDefinition(definition); err != nil {
		return Published{}, err
	}
	if build == nil || validate == nil {
		return Published{}, errors.New("publish release: builder and activation validator are required")
	}
	previous, err := p.activeRelease(ctx, definition.GameID, definition.RealmID)
	if err != nil {
		return Published{}, err
	}
	artifacts, err := build(ctx, definition)
	if err != nil {
		return Published{}, fmt.Errorf("build release %q: %w", definition.ReleaseID, err)
	}
	if len(artifacts.Public) == 0 || len(artifacts.Private) == 0 {
		return Published{}, errors.New("build release: public and private artifacts must both be non-empty")
	}
	if err := ensurePrivateDirectory(p.artifactDir); err != nil {
		return Published{}, fmt.Errorf("prepare private release artifact directory: %w", err)
	}
	gameArtifactDir := filepath.Join(p.artifactDir, definition.GameID)
	if err := ensurePrivateDirectory(gameArtifactDir); err != nil {
		return Published{}, fmt.Errorf("prepare game release artifact directory: %w", err)
	}
	publicHash := artifactHash(artifacts.Public)
	privateHash := artifactHash(artifacts.Private)
	if err := writeImmutable(filepath.Join(gameArtifactDir, definition.ReleaseID+".public"), artifacts.Public, 0o600); err != nil {
		return Published{}, fmt.Errorf("stage public release artifact: %w", err)
	}
	if err := writeImmutable(filepath.Join(gameArtifactDir, definition.ReleaseID+".private"), artifacts.Private, 0o600); err != nil {
		return Published{}, fmt.Errorf("stage private release artifact: %w", err)
	}
	manifest, err := manifestWithHashes(definition.Manifest, publicHash, privateHash)
	if err != nil {
		return Published{}, err
	}
	if err := p.registerBuilt(ctx, definition, manifest); err != nil {
		return Published{}, err
	}
	if err := validate(ctx, definition); err != nil {
		return Published{}, fmt.Errorf("validate release activation: %w", err)
	}
	if err := p.activate(ctx, definition, previous, manifest); err != nil {
		return Published{}, err
	}
	return Published{PreviousReleaseID: previous, ReleaseID: definition.ReleaseID, PublicHash: publicHash, PrivateHash: privateHash}, nil
}

func (p *Publisher) activeRelease(ctx context.Context, gameID, realmID string) (string, error) {
	var releaseID sql.NullString
	err := p.db.QueryRowContext(ctx, `SELECT active_release_id FROM realms WHERE game_id=$1 AND realm_id=$2`, gameID, realmID).Scan(&releaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("find target realm: %w", err)
	}
	if err != nil {
		return "", fmt.Errorf("read active release: %w", err)
	}
	return releaseID.String, nil
}

func (p *Publisher) registerBuilt(ctx context.Context, definition Definition, manifest []byte) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO releases (game_id, release_id, release_version, content_schema_version, instance_schema_version, ruleset_version, protocol_version, client_min_build, status, manifest)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'built',$9::jsonb) ON CONFLICT (game_id,release_id) DO NOTHING`, releaseArguments(definition, manifest)...)
	if err != nil {
		return fmt.Errorf("register built release %q: %w", definition.ReleaseID, err)
	}
	var matches bool
	err = p.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM releases WHERE `+matchingRelease+`)`, releaseArguments(definition, manifest)...).Scan(&matches)
	if err != nil {
		return fmt.Errorf("verify immutable release registration: %w", err)
	}
	if !matches {
		return ErrReleaseConflict
	}
	return nil
}

func validateDefinition(definition Definition) error {
	if !gameIDPattern.MatchString(definition.GameID) || strings.TrimSpace(definition.RealmID) == "" || definition.OwnerEpoch < 0 {
		return ErrInvalidDefinition
	}
	if !releaseIDPattern.MatchString(definition.ReleaseID) {
		return ErrReleaseIDInvalid
	}
	if definition.ReleaseVersion <= 0 || definition.ContentSchemaVersion <= 0 || definition.InstanceSchemaVersion <= 0 || definition.RulesetVersion <= 0 || definition.ProtocolVersion <= 0 || strings.TrimSpace(definition.ClientMinBuild) == "" {
		return ErrInvalidDefinition
	}
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(definition.Manifest, &manifest); err != nil || manifest == nil {
		return errors.New("release manifest must be a JSON object")
	}
	return nil
}

func manifestWithHashes(input []byte, publicHash, privateHash string) ([]byte, error) {
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(input, &manifest); err != nil || manifest == nil {
		return nil, errors.New("release manifest must be a JSON object")
	}
	publicValue, err := json.Marshal(publicHash)
	if err != nil {
		return nil, fmt.Errorf("encode public artifact hash: %w", err)
	}
	privateValue, err := json.Marshal(privateHash)
	if err != nil {
		return nil, fmt.Errorf("encode private artifact hash: %w", err)
	}
	manifest["public_artifact_sha256"] = publicValue
	manifest["private_artifact_sha256"] = privateValue
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode release manifest: %w", err)
	}
	return encoded, nil
}

func ensurePrivateDirectory(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("artifact directory path is empty")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("artifact path is not a directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("artifact directory permissions expose private files")
	}
	return nil
}

func discardPartialArtifact(file *os.File, path string, cause error) error {
	closeErr := file.Close()
	removeErr := os.Remove(path)
	return errors.Join(cause, wrapCleanupError("close incomplete release artifact", closeErr), wrapCleanupError("remove incomplete release artifact", removeErr))
}

func removePartialArtifact(path string, cause error) error {
	removeErr := os.Remove(path)
	return errors.Join(cause, wrapCleanupError("remove incomplete release artifact", removeErr))
}

func wrapCleanupError(action string, err error) error {
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}

func artifactHash(content []byte) string {
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:])
}
