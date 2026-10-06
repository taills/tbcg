package postgres

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"example.com/tbcg/server/internal/admin"
	"example.com/tbcg/server/internal/content/assets"
	contentbuild "example.com/tbcg/server/internal/content/build"
	"example.com/tbcg/server/internal/persistence"
	"github.com/jackc/pgx/v5/pgconn"
)

// AdminRepository must keep satisfying the narrow build port so the admin API registers build routes.
var _ admin.BuildStore = (*AdminRepository)(nil)

func (r *AdminRepository) ListBuildCandidates(ctx context.Context, gameID, actorID string, limit, offset int) ([]admin.BuildCandidate, int, error) {
	var encoded []byte
	var total int64
	err := r.db.QueryRowContext(ctx,
		`SELECT items, total FROM public.admin_list_build_candidates($1, $2::uuid, $3, $4)`,
		gameID, actorID, limit, offset,
	).Scan(&encoded, &total)
	if err != nil {
		return nil, 0, buildStoreError("list approved build candidates", err)
	}
	if total < 0 || total > math.MaxInt {
		return nil, 0, errors.New("list build candidates: invalid total count")
	}
	var candidates []admin.BuildCandidate
	if err := json.Unmarshal(encoded, &candidates); err != nil {
		return nil, 0, fmt.Errorf("decode approved build candidates: %w", err)
	}
	if candidates == nil {
		candidates = make([]admin.BuildCandidate, 0)
	}
	for _, candidate := range candidates {
		if candidate.GameID != gameID || candidate.ContentID == "" || candidate.Revision < 1 || !validBuildHash(candidate.BodyHash) {
			return nil, 0, errors.New("list build candidates: database returned invalid or out-of-scope identity")
		}
	}
	return candidates, int(total), nil
}

func (r *AdminRepository) ReadApprovedBuildInput(ctx context.Context, gameID, actorID, contentID string, revision int32, bodyHash []byte) (admin.BuildPreparation, error) {
	if len(bodyHash) != 32 || revision < 1 {
		return admin.BuildPreparation{}, admin.ErrInvalidBuildRequest
	}
	var encoded []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT public.admin_read_build_input($1, $2::uuid, $3, $4, $5)`,
		gameID, actorID, contentID, revision, bodyHash,
	).Scan(&encoded)
	if err != nil {
		return admin.BuildPreparation{}, buildStoreError("read exact approved build input", err)
	}
	if len(encoded) == 0 {
		return admin.BuildPreparation{}, admin.ErrNotFound
	}
	var result struct {
		Definition json.RawMessage                   `json:"definition"`
		Profile    contentbuild.CompatibilityProfile `json:"profile"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return admin.BuildPreparation{}, fmt.Errorf("decode exact approved build input: %w", err)
	}
	canonical, err := canonicalJSON(result.Definition)
	if err != nil {
		return admin.BuildPreparation{}, fmt.Errorf("canonicalize approved build input: %w", err)
	}
	actualHash := sha256.Sum256(canonical)
	if subtle.ConstantTimeCompare(actualHash[:], bodyHash) != 1 {
		return admin.BuildPreparation{}, admin.ErrConflict
	}
	var identity struct {
		GameID    string `json:"game_id"`
		ContentID string `json:"content_id"`
		Revision  int32  `json:"revision"`
	}
	if err := json.Unmarshal(canonical, &identity); err != nil {
		return admin.BuildPreparation{}, fmt.Errorf("read approved build identity: %w", err)
	}
	if identity.GameID != gameID || identity.ContentID != contentID || identity.Revision != revision || result.Profile.ProfileVersion < 1 {
		return admin.BuildPreparation{}, admin.ErrConflict
	}
	return admin.BuildPreparation{
		GameID: gameID, ContentID: contentID, Revision: revision,
		BodyHash: hex.EncodeToString(bodyHash), Definition: canonical, Profile: result.Profile,
	}, nil
}

func (r *AdminRepository) FindBuildJobByKey(ctx context.Context, gameID, actorID, idempotencyKey string, fingerprint []byte) (admin.BuildJob, bool, error) {
	if len(fingerprint) != 32 {
		return admin.BuildJob{}, false, admin.ErrInvalidBuildRequest
	}
	var encoded []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT public.admin_find_build_job($1, $2::uuid, $3, $4)`,
		gameID, actorID, idempotencyKey, fingerprint,
	).Scan(&encoded)
	if err != nil {
		return admin.BuildJob{}, false, buildStoreError("look up build idempotency key", err)
	}
	if len(encoded) == 0 {
		return admin.BuildJob{}, false, nil
	}
	job, err := decodeBuildJob(encoded, gameID)
	if err != nil {
		return admin.BuildJob{}, false, err
	}
	return job, true, nil
}

func (r *AdminRepository) EnqueueBuild(ctx context.Context, gameID, actorID string, input admin.BuildRequest, idempotencyKey string, fingerprint []byte, expectedProfileVersion int64, expectedAssetManifestSHA256 string, asset assets.FrozenSnapshot, schemaFingerprint string) (admin.BuildJob, error) {
	bodyHash, err := hex.DecodeString(input.BodyHash)
	if err != nil || len(bodyHash) != 32 || len(fingerprint) != 32 || expectedProfileVersion < 1 || !validBuildHash(expectedAssetManifestSHA256) {
		return admin.BuildJob{}, admin.ErrInvalidBuildRequest
	}
	if replay, found, err := r.FindBuildJobByKey(ctx, gameID, actorID, idempotencyKey, fingerprint); err != nil {
		return admin.BuildJob{}, err
	} else if found {
		return replay, nil
	}
	expectedManifestHash, err := hex.DecodeString(expectedAssetManifestSHA256)
	if err != nil || len(expectedManifestHash) != sha256.Size {
		return admin.BuildJob{}, admin.ErrInvalidBuildRequest
	}
	assetSnapshot, err := assets.NewFrozen(asset)
	if err != nil {
		return admin.BuildJob{}, fmt.Errorf("verify frozen build asset: %w", err)
	}
	manifestHash := sha256.Sum256(assetSnapshot.Frozen().Manifest)
	if subtle.ConstantTimeCompare(manifestHash[:], expectedManifestHash) != 1 {
		return admin.BuildJob{}, admin.ErrInvalidBuildRequest
	}
	frozenAsset, err := json.Marshal(assetSnapshot.Frozen())
	if err != nil {
		return admin.BuildJob{}, fmt.Errorf("encode frozen build asset: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return admin.BuildJob{}, fmt.Errorf("begin enqueue content build: %w", err)
	}
	defer rollbackAdminTransactionWithLog(tx, "enqueue content build")
	var encoded []byte
	err = tx.QueryRowContext(ctx, `
SELECT public.admin_enqueue_build_job($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9::jsonb, $10)`,
		gameID, actorID, input.ContentID, input.Revision, bodyHash, idempotencyKey,
		fingerprint, expectedProfileVersion, frozenAsset, schemaFingerprint,
	).Scan(&encoded)
	if err != nil {
		return admin.BuildJob{}, buildStoreError("enqueue exact approved build", err)
	}
	if len(encoded) == 0 {
		return admin.BuildJob{}, errors.New("enqueue build returned an empty result")
	}
	job, err := decodeBuildJob(encoded, gameID)
	if err != nil {
		return admin.BuildJob{}, err
	}
	if err := tx.Commit(); err != nil {
		classified := classifyAdminCommitError(err)
		if errors.Is(classified, persistence.ErrUnknown) {
			return admin.BuildJob{}, fmt.Errorf("enqueue build acknowledgement unknown: %w", errors.Join(admin.ErrBuildOutcomeUnknown, classified))
		}
		return admin.BuildJob{}, classified
	}
	return job, nil
}

func (r *AdminRepository) GetBuildJob(ctx context.Context, gameID, actorID, jobID string) (admin.BuildJob, error) {
	var encoded []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT public.admin_get_build_job($1, $2::uuid, $3::uuid)`,
		gameID, actorID, jobID,
	).Scan(&encoded)
	if err != nil {
		return admin.BuildJob{}, buildStoreError("read Publisher build job", err)
	}
	if len(encoded) == 0 {
		return admin.BuildJob{}, admin.ErrNotFound
	}
	return decodeBuildJob(encoded, gameID)
}

func decodeBuildJob(encoded []byte, expectedGameID string) (admin.BuildJob, error) {
	var job admin.BuildJob
	if err := json.Unmarshal(encoded, &job); err != nil {
		return admin.BuildJob{}, fmt.Errorf("decode build job response: %w", err)
	}
	if job.JobID == "" || job.GameID != expectedGameID || job.ContentID == "" || job.Revision < 1 ||
		!validBuildHash(job.BodyHash) || job.ReleaseID == "" || job.ReleaseVersion < 1 || !validBuildStatus(job.Status) {
		return admin.BuildJob{}, errors.New("decode build job response: invalid or out-of-scope job identity")
	}
	if job.PublicArtifactSHA256 != nil && !validBuildHash(*job.PublicArtifactSHA256) {
		return admin.BuildJob{}, errors.New("decode build job response: invalid public artifact hash")
	}
	return job, nil
}

func buildStoreError(operation string, err error) error {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if ok {
		switch pgErr.Code {
		case "42501":
			return fmt.Errorf("%s: %w", operation, errors.Join(admin.ErrForbidden, err))
		case "P0002":
			return fmt.Errorf("%s: %w", operation, errors.Join(admin.ErrNotFound, err))
		case "55000":
			return fmt.Errorf("%s: %w", operation, errors.Join(admin.ErrBuildProfileNotConfigured, err))
		case "55006":
			return fmt.Errorf("%s: %w", operation, errors.Join(admin.ErrBuildAlreadyActive, err))
		case "53400":
			return fmt.Errorf("%s: %w", operation, errors.Join(admin.ErrBuildQueueFull, err))
		case "22023", "23514":
			return fmt.Errorf("%s: %w", operation, errors.Join(admin.ErrInvalidBuildRequest, err))
		case "23505", "40001":
			return fmt.Errorf("%s: %w", operation, errors.Join(admin.ErrConflict, err))
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func validBuildHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 || value != strings.ToLower(value) {
		return false
	}
	return true
}

func validBuildStatus(value admin.BuildStatus) bool {
	switch value {
	case admin.BuildStatusQueued, admin.BuildStatusRunning, admin.BuildStatusRetryWait, admin.BuildStatusBuilt, admin.BuildStatusFailed:
		return true
	default:
		return false
	}
}
