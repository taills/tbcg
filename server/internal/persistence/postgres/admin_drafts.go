package postgres

import (
	"context"
	"fmt"

	"example.com/tbcg/server/internal/admin"
)

func (r *AdminRepository) GetDraft(ctx context.Context, gameID, draftID string) (admin.Draft, error) {
	return scanAdminDraft(r.db.QueryRowContext(ctx, `
SELECT draft_id::text, game_id, content_id, draft_version::text, definition, updated_at
FROM content_drafts
WHERE game_id = $1 AND draft_id = $2::uuid`, gameID, draftID))
}

func (r *AdminRepository) ListDrafts(ctx context.Context, gameID, contentID string, limit, offset int) ([]admin.Draft, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `
SELECT count(*) FROM content_drafts WHERE game_id = $1 AND content_id = $2`, gameID, contentID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count drafts for exact content id: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT draft_id::text, game_id, content_id, draft_version::text, definition, updated_at
FROM content_drafts
WHERE game_id = $1 AND content_id = $2
ORDER BY updated_at DESC, draft_id
LIMIT $3 OFFSET $4`, gameID, contentID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query drafts for exact content id: %w", err)
	}
	defer rows.Close()
	drafts := make([]admin.Draft, 0)
	for rows.Next() {
		var draft admin.Draft
		if err := rows.Scan(&draft.DraftID, &draft.GameID, &draft.ContentID, &draft.DraftVersion, &draft.Definition, &draft.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan draft recovery result: %w", err)
		}
		drafts = append(drafts, draft)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate draft recovery results: %w", err)
	}
	return drafts, total, nil
}
