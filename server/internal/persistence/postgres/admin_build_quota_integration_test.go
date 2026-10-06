package postgres

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/taills/tbcg/server/internal/admin"
)

func TestEnqueueBuildRejectsWhenGameQueueIsFull(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	seed := seedApprovedBuild(t, fixture, "build_quota_", "00000000-0000-4000-8000-000000000091", "demo:item/quota")
	const fillerContent = "demo:item/quota-filler"
	filler := fmt.Appendf(nil, `{"game_id":%q,"content_id":%q,"revision":1,"schema_version":1,"public":{"game_id":%q,"content_id":%q,"revision":1,"schema_version":1}}`, seed.gameID, fillerContent, seed.gameID, fillerContent)
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO content_revisions (game_id, content_id, revision, schema_version, content_type, body) VALUES ($1, $2, 1, 1, 'item', $3::jsonb)`, seed.gameID, fillerContent, filler); err != nil {
		t.Fatalf("insert filler revision: %v", err)
	}
	zeroHash := bytes.Repeat([]byte{0}, 32)
	for index := range 20 {
		if _, err := fixture.db.ExecContext(fixture.ctx, `
INSERT INTO admin_build_jobs (game_id, requested_by, idempotency_key, request_fingerprint,
    content_id, revision, body_hash, release_id, release_version, input_snapshot)
VALUES ($1, $2, $3, $4, $5, 1, $4, $6, $7, '{}'::jsonb)`,
			seed.gameID, seed.publisherID, fmt.Sprintf("quota-filler-key-%04d", index), zeroHash, fillerContent,
			fmt.Sprintf("quota-filler-%d", index), 1000+index); err != nil {
			t.Fatalf("insert active filler job %d: %v", index, err)
		}
	}
	repository, err := openAdminRepository(fixture.ctx, fixture.appDSN, true)
	if err != nil {
		t.Fatalf("open Publisher repository: %v", err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	preparation, err := repository.ReadApprovedBuildInput(fixture.ctx, seed.gameID, seed.publisherID, seed.contentID, 1, seed.bodyHash[:])
	if err != nil {
		t.Fatalf("read approved build input: %v", err)
	}
	fingerprint := sha256.Sum256([]byte("quota request"))
	_, err = repository.EnqueueBuild(fixture.ctx, seed.gameID, seed.publisherID, seed.request, "quota-request-key-01", fingerprint[:],
		preparation.Profile.ProfileVersion, preparation.Profile.AssetManifestSHA256, seed.frozenAsset, seed.validator.Fingerprint())
	if !errors.Is(err, admin.ErrBuildQueueFull) {
		t.Fatalf("enqueue into a full Game queue = %v, want ErrBuildQueueFull", err)
	}
	var jobs int
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT count(*) FROM admin_build_jobs WHERE game_id = $1 AND content_id = $2`, seed.gameID, seed.contentID).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("rejected enqueue created %d jobs, err=%v", jobs, err)
	}
	var counterRows int
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT count(*) FROM release_version_counters WHERE game_id = $1 AND next_release_version > 1`, seed.gameID).Scan(&counterRows); err != nil || counterRows != 0 {
		t.Fatalf("rejected enqueue consumed a Release version: rows=%d err=%v", counterRows, err)
	}
}
