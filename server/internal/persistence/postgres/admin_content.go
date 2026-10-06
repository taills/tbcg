package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/taills/tbcg/server/internal/admin"
	"github.com/taills/tbcg/server/internal/content/assets"
	"github.com/taills/tbcg/server/internal/content/schema"
	"github.com/taills/tbcg/server/internal/persistence"
)

var (
	_ admin.ContentStore = (*AdminRepository)(nil)
	_ admin.ReleaseStore = (*AdminRepository)(nil)
)

func (r *AdminRepository) ListContent(ctx context.Context, gameID string, limit int) ([]admin.ContentSummary, error) {
	rows, err := r.db.QueryContext(ctx, `
WITH latest AS (
    SELECT content_id, max(revision) AS latest FROM content_revisions WHERE game_id = $1 GROUP BY content_id
), approved AS (
    SELECT content_id, max(revision) AS approved FROM revision_approvals
    WHERE game_id = $1 AND decision = 'approved' GROUP BY content_id
), ids AS (
    SELECT content_id FROM content_drafts WHERE game_id = $1
    UNION SELECT content_id FROM latest
)
SELECT ids.content_id, COALESCE(d.draft_id::text, ''), COALESCE(d.draft_version::text, ''),
       COALESCE(l.latest, 0), COALESCE(a.approved, 0),
       COALESCE(d.definition, c.body)->'public'->>'display_name', COALESCE(d.updated_at, c.created_at)
FROM ids
LEFT JOIN content_drafts d ON d.game_id = $1 AND d.content_id = ids.content_id
LEFT JOIN latest l ON l.content_id = ids.content_id
LEFT JOIN approved a ON a.content_id = ids.content_id
LEFT JOIN content_revisions c ON c.game_id = $1 AND c.content_id = ids.content_id AND c.revision = l.latest
ORDER BY ids.content_id
LIMIT $2`, gameID, limit)
	if err != nil {
		return nil, fmt.Errorf("list content: %w", err)
	}
	defer rows.Close()
	out := make([]admin.ContentSummary, 0)
	for rows.Next() {
		var summary admin.ContentSummary
		var displayName sql.NullString
		if err := rows.Scan(&summary.ContentID, &summary.DraftID, &summary.DraftVersion, &summary.LatestRevision,
			&summary.ApprovedRevision, &displayName, &summary.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan content summary: %w", err)
		}
		summary.DisplayName = displayName.String
		summary.ContentType, _ = schema.ContentTypeOf(summary.ContentID)
		out = append(out, summary)
	}
	return out, rows.Err()
}

func (r *AdminRepository) ContentDefinitions(ctx context.Context, gameID, source string) ([]admin.ContentDefinition, error) {
	query := `
SELECT DISTINCT ON (c.content_id) c.content_id, 'approved', c.revision, c.body
FROM content_revisions c
JOIN revision_approvals a ON a.game_id = c.game_id AND a.content_id = c.content_id AND a.revision = c.revision
WHERE c.game_id = $1 AND a.decision = 'approved'
ORDER BY c.content_id, c.revision DESC`
	if source == "draft" {
		query = `
WITH approved AS (
    SELECT DISTINCT ON (c.content_id) c.content_id, c.revision, c.body
    FROM content_revisions c
    JOIN revision_approvals a ON a.game_id = c.game_id AND a.content_id = c.content_id AND a.revision = c.revision
    WHERE c.game_id = $1 AND a.decision = 'approved'
    ORDER BY c.content_id, c.revision DESC
)
SELECT d.content_id, 'draft', 0, d.definition FROM content_drafts d WHERE d.game_id = $1
UNION ALL
SELECT a.content_id, 'approved', a.revision, a.body FROM approved a
WHERE NOT EXISTS (SELECT 1 FROM content_drafts d WHERE d.game_id = $1 AND d.content_id = a.content_id)
ORDER BY 1`
	} else if source != "approved" {
		return nil, errors.New("invalid content source")
	}
	rows, err := r.db.QueryContext(ctx, query, gameID)
	if err != nil {
		return nil, fmt.Errorf("read content definitions: %w", err)
	}
	defer rows.Close()
	out := make([]admin.ContentDefinition, 0)
	for rows.Next() {
		var definition admin.ContentDefinition
		var body []byte
		if err := rows.Scan(&definition.ContentID, &definition.Source, &definition.Revision, &body); err != nil {
			return nil, fmt.Errorf("scan content definition: %w", err)
		}
		canonical, err := canonicalJSON(body)
		if err != nil {
			return nil, fmt.Errorf("canonicalize content definition: %w", err)
		}
		definition.Definition = canonical
		out = append(out, definition)
	}
	return out, rows.Err()
}

func (r *AdminRepository) RevisionDefinition(ctx context.Context, gameID, contentID string, revision int32) (json.RawMessage, error) {
	var body []byte
	err := r.db.QueryRowContext(ctx, `SELECT body FROM content_revisions WHERE game_id = $1 AND content_id = $2 AND revision = $3`,
		gameID, contentID, revision).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, admin.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read content revision: %w", err)
	}
	return canonicalJSON(body)
}

func (r *AdminRepository) PreviewSnapshot(ctx context.Context, gameID, actorID string) (admin.SnapshotPreview, error) {
	var encoded []byte
	if err := r.db.QueryRowContext(ctx, `SELECT public.admin_preview_snapshot($1, $2::uuid)`, gameID, actorID).Scan(&encoded); err != nil {
		return admin.SnapshotPreview{}, buildStoreError("preview Game snapshot", err)
	}
	var preview admin.SnapshotPreview
	if err := json.Unmarshal(encoded, &preview); err != nil {
		return admin.SnapshotPreview{}, fmt.Errorf("decode snapshot preview: %w", err)
	}
	if len(preview.Entries) == 0 {
		return admin.SnapshotPreview{}, fmt.Errorf("preview Game snapshot: %w", admin.ErrNotFound)
	}
	return preview, nil
}

type frozenAssetJSON struct {
	AssetID  string `json:"asset_id"`
	Manifest string `json:"manifest_bytes"`
	Data     string `json:"asset_bytes"`
}

func (r *AdminRepository) EnqueueSnapshot(ctx context.Context, gameID, actorID, key string, fingerprint []byte, profileVersion int64, frozen []assets.FrozenSnapshot, schemaFingerprint string) (json.RawMessage, error) {
	encodedAssets := make([]frozenAssetJSON, 0, len(frozen))
	for _, asset := range frozen {
		encodedAssets = append(encodedAssets, frozenAssetJSON{AssetID: asset.AssetID, Manifest: base64.StdEncoding.EncodeToString(asset.Manifest), Data: base64.StdEncoding.EncodeToString(asset.Data)})
	}
	assetJSON, err := json.Marshal(encodedAssets)
	if err != nil {
		return nil, fmt.Errorf("encode frozen snapshot assets: %w", err)
	}
	var encoded []byte
	err = r.db.QueryRowContext(ctx, `SELECT public.admin_enqueue_snapshot_build($1, $2::uuid, $3, $4, $5, $6::jsonb, $7)`,
		gameID, actorID, key, fingerprint, profileVersion, assetJSON, schemaFingerprint).Scan(&encoded)
	if err != nil {
		if persistence.IsCommitOutcomeUnknown(err) {
			return nil, fmt.Errorf("enqueue snapshot: %w", errors.Join(admin.ErrBuildOutcomeUnknown, err))
		}
		return nil, buildStoreError("enqueue Game snapshot", err)
	}
	return encoded, nil
}

func (r *AdminRepository) GetSnapshotJob(ctx context.Context, gameID, actorID, jobID string) (json.RawMessage, error) {
	var encoded []byte
	if err := r.db.QueryRowContext(ctx, `SELECT public.admin_get_snapshot_job($1, $2::uuid, $3::uuid)`, gameID, actorID, jobID).Scan(&encoded); err != nil {
		return nil, buildStoreError("read snapshot job", err)
	}
	return encoded, nil
}

func (r *AdminRepository) ListReleases(ctx context.Context, gameID, actorID string) (json.RawMessage, error) {
	var encoded []byte
	if err := r.db.QueryRowContext(ctx, `SELECT public.admin_list_releases($1, $2::uuid)`, gameID, actorID).Scan(&encoded); err != nil {
		return nil, buildStoreError("list releases", err)
	}
	return encoded, nil
}

func (r *AdminRepository) RequestActivation(ctx context.Context, gameID, realmID, actorID, releaseID, key string) (json.RawMessage, error) {
	var encoded []byte
	err := r.db.QueryRowContext(ctx, `SELECT public.admin_request_activation($1, $2, $3::uuid, $4, $5)`,
		gameID, realmID, actorID, releaseID, key).Scan(&encoded)
	if err != nil {
		if persistence.IsCommitOutcomeUnknown(err) {
			return nil, fmt.Errorf("request activation: %w", errors.Join(admin.ErrBuildOutcomeUnknown, err))
		}
		return nil, buildStoreError("request Release activation", err)
	}
	return encoded, nil
}
