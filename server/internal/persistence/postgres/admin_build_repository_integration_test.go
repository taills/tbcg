package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/taills/tbcg/server/internal/admin"
	contentassets "github.com/taills/tbcg/server/internal/content/assets"
)

func TestAdminBuildCandidateFunctionsRequirePublisherAndReturnExactApprovedRevision(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	gameID := randomTestIdentifier(t, "build_candidate_")
	publisherID := "00000000-0000-4000-8000-000000000011"
	authorID := "00000000-0000-4000-8000-000000000012"
	contentID := "demo:item/build-candidate"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{query: `INSERT INTO games (game_id, display_name) VALUES ($1, 'Build candidate test')`, args: []any{gameID}},
		{query: `INSERT INTO admin_users (admin_id, username, password_hash) VALUES ($1, $2, 'test-hash')`, args: []any{publisherID, gameID + "-publisher"}},
		{query: `INSERT INTO admin_users (admin_id, username, password_hash) VALUES ($1, $2, 'test-hash')`, args: []any{authorID, gameID + "-author"}},
		{query: `INSERT INTO admin_role_bindings (game_id, admin_id, role) VALUES ($1, $2, 'Publisher')`, args: []any{gameID, publisherID}},
		{query: `INSERT INTO admin_role_bindings (game_id, admin_id, role) VALUES ($1, $2, 'Author')`, args: []any{gameID, authorID}},
		{query: `INSERT INTO game_build_profiles (game_id, content_schema_version, instance_schema_version, ruleset_version, protocol_version, client_min_build, asset_manifest_sha256) VALUES ($1, 1, 1, 1, 1, '1.0.0', decode(repeat('7a', 32), 'hex'))`, args: []any{gameID}},
	} {
		if _, err := fixture.db.ExecContext(fixture.ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed controlled build repository fixture: %v", err)
		}
	}
	approvedHash := sha256.Sum256([]byte("approved immutable revision"))
	rejectedHash := sha256.Sum256([]byte("rejected immutable revision"))
	insertRevision := func(revision int32, bodyHash [32]byte, decision string) {
		t.Helper()
		body := fmt.Appendf(nil, `{"game_id":%q,"content_id":%q,"revision":%d,"schema_version":1,"public":{"game_id":%q,"content_id":%q,"revision":%d,"schema_version":1}}`, gameID, contentID, revision, gameID, contentID, revision)
		if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO content_revisions (game_id, content_id, revision, schema_version, content_type, body) VALUES ($1, $2, $3, 1, 'item', $4::jsonb)`, gameID, contentID, revision, body); err != nil {
			t.Fatalf("insert candidate immutable revision: %v", err)
		}
		if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO revision_approvals (game_id, content_id, revision, body_hash, decision, reviewer_id) VALUES ($1, $2, $3, $4, $5, $6)`, gameID, contentID, revision, bodyHash[:], decision, publisherID); err != nil {
			t.Fatalf("insert candidate approval: %v", err)
		}
	}
	insertRevision(1, approvedHash, "approved")
	insertRevision(2, rejectedHash, "rejected")

	repository, err := openAdminRepository(fixture.ctx, fixture.appDSN, true)
	if err != nil {
		t.Fatalf("open restricted build caller repository: %v", err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close restricted build caller repository: %v", err)
		}
	})
	var encodedCandidates []byte
	var total int64
	if err := repository.db.QueryRowContext(fixture.ctx, `SELECT items, total FROM public.admin_list_build_candidates($1, $2::uuid, 10, 0)`, gameID, publisherID).Scan(&encodedCandidates, &total); err != nil {
		t.Fatalf("list exact approved build candidates: %v", err)
	}
	var candidates []admin.BuildCandidate
	if err := json.Unmarshal(encodedCandidates, &candidates); err != nil {
		t.Fatalf("decode build candidate list: %v", err)
	}
	if total != 1 || len(candidates) != 1 || candidates[0].GameID != gameID || candidates[0].ContentID != contentID || candidates[0].Revision != 1 || candidates[0].BodyHash != hex.EncodeToString(approvedHash[:]) {
		t.Fatalf("build candidates total/items = %d/%#v", total, candidates)
	}
	var encodedInput []byte
	if err := repository.db.QueryRowContext(fixture.ctx, `SELECT public.admin_read_build_input($1, $2::uuid, $3, 1, $4)`, gameID, publisherID, contentID, approvedHash[:]).Scan(&encodedInput); err != nil {
		t.Fatalf("read approved build input for trusted Publisher server code: %v", err)
	}
	var preparation struct {
		Definition json.RawMessage `json:"definition"`
		Profile    struct {
			Version int64 `json:"profile_version"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(encodedInput, &preparation); err != nil {
		t.Fatalf("decode private build preparation: %v", err)
	}
	if len(preparation.Definition) == 0 || preparation.Profile.Version != 1 {
		t.Fatalf("approved build preparation omitted exact body/profile version: %#v", preparation)
	}
	_, err = repository.db.ExecContext(fixture.ctx, `SELECT public.admin_read_build_input($1, $2::uuid, $3, 1, $4)`, gameID, authorID, contentID, approvedHash[:])
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("non-Publisher build preparation error = %v, want permission denied", err)
	}
}

func TestEnqueueBuildFreezesApprovedInputAndReusesIdempotencyResult(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	seed := seedApprovedBuild(t, fixture, "build_enqueue_", "00000000-0000-4000-8000-000000000021", "demo:item/frozen-potion")
	gameID, publisherID, contentID := seed.gameID, seed.publisherID, seed.contentID
	frozenAsset, canonical, bodyHash, validator, request := seed.frozenAsset, seed.canonical, seed.bodyHash, seed.validator, seed.request
	fingerprint := sha256.Sum256([]byte("canonical request fingerprint for integration"))
	repository, err := openAdminRepository(fixture.ctx, fixture.appDSN, true)
	if err != nil {
		t.Fatalf("open Publisher repository: %v", err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close Publisher repository: %v", err)
		}
	})
	preparation, err := repository.ReadApprovedBuildInput(fixture.ctx, gameID, publisherID, contentID, 1, bodyHash[:])
	if err != nil {
		t.Fatalf("read exact approved build preparation: %v", err)
	}
	if preparation.Profile.ProfileVersion != 1 || !bytes.Equal(preparation.Definition, canonical) {
		t.Fatalf("build preparation profile/Definition mismatch: version=%d", preparation.Profile.ProfileVersion)
	}
	profileTx, err := fixture.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatalf("begin profile update race: %v", err)
	}
	t.Cleanup(func() { _ = profileTx.Rollback() })
	if _, updateErr := profileTx.ExecContext(fixture.ctx, `UPDATE game_build_profiles SET client_min_build = '1.0.1' WHERE game_id = $1`, gameID); updateErr != nil {
		t.Fatalf("lock and update Game profile: %v", updateErr)
	}
	enqueueCtx, cancelEnqueue := context.WithTimeout(fixture.ctx, 10*time.Second)
	defer cancelEnqueue()
	staleResult := make(chan error, 1)
	go func() {
		_, enqueueErr := repository.EnqueueBuild(enqueueCtx, gameID, publisherID, request, "enqueue-key-0000001", fingerprint[:], preparation.Profile.ProfileVersion, preparation.Profile.AssetManifestSHA256, frozenAsset, validator.Fingerprint())
		staleResult <- enqueueErr
	}()
	waitForBuildProfileLock(t, fixture, enqueueCtx)
	if commitErr := profileTx.Commit(); commitErr != nil {
		t.Fatalf("commit profile update race: %v", commitErr)
	}
	if enqueueErr := <-staleResult; !errors.Is(enqueueErr, admin.ErrConflict) {
		t.Fatalf("enqueue with stale profile version error = %v, want conflict", enqueueErr)
	}
	if _, err := repository.EnqueueBuild(fixture.ctx, gameID, publisherID, request, "enqueue-key-0000001", fingerprint[:], preparation.Profile.ProfileVersion, preparation.Profile.AssetManifestSHA256, frozenAsset, validator.Fingerprint()); !errors.Is(err, admin.ErrConflict) {
		t.Fatalf("enqueue with stale profile version error = %v, want conflict", err)
	}
	preparation, err = repository.ReadApprovedBuildInput(fixture.ctx, gameID, publisherID, contentID, 1, bodyHash[:])
	if err != nil {
		t.Fatalf("read refreshed profile for approved revision: %v", err)
	}
	changedManifest := frozenAsset
	changedManifest.Manifest = append(append([]byte(nil), frozenAsset.Manifest...), '\n')
	if _, err := contentassets.NewFrozen(changedManifest); err != nil {
		t.Fatalf("restore valid manifest encoding for pin mismatch test: %v", err)
	}
	if _, err := repository.EnqueueBuild(fixture.ctx, gameID, publisherID, request, "enqueue-key-0000001", fingerprint[:], preparation.Profile.ProfileVersion, preparation.Profile.AssetManifestSHA256, changedManifest, validator.Fingerprint()); !errors.Is(err, admin.ErrInvalidBuildRequest) {
		t.Fatalf("enqueue asset manifest outside trusted Game profile = %v, want invalid request", err)
	}
	type enqueueResult struct {
		job admin.BuildJob
		err error
	}
	start := make(chan struct{})
	results := make(chan enqueueResult, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Go(func() {
			<-start
			job, err := repository.EnqueueBuild(fixture.ctx, gameID, publisherID, request, "enqueue-key-0000001", fingerprint[:], preparation.Profile.ProfileVersion, preparation.Profile.AssetManifestSHA256, frozenAsset, validator.Fingerprint())
			results <- enqueueResult{job: job, err: err}
		})
	}
	close(start)
	wait.Wait()
	close(results)
	parallelJobs := make([]admin.BuildJob, 0, 2)
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent same-key enqueue: %v", result.err)
		}
		parallelJobs = append(parallelJobs, result.job)
	}
	if len(parallelJobs) != 2 || parallelJobs[0].JobID != parallelJobs[1].JobID || parallelJobs[0].ReleaseVersion != parallelJobs[1].ReleaseVersion {
		t.Fatalf("concurrent same-key results = %#v, want one stable job and Release version", parallelJobs)
	}
	first := parallelJobs[0]
	replayed, found, err := repository.FindBuildJobByKey(fixture.ctx, gameID, publisherID, "enqueue-key-0000001", fingerprint[:])
	if err != nil || !found || replayed.JobID != first.JobID || replayed.ReleaseID != first.ReleaseID || replayed.ReleaseVersion != first.ReleaseVersion {
		t.Fatalf("idempotency lookup = %#v/%t/%v, want same fixed job", replayed, found, err)
	}
	second, err := repository.EnqueueBuild(fixture.ctx, gameID, publisherID, request, "enqueue-key-0000001", fingerprint[:], preparation.Profile.ProfileVersion, preparation.Profile.AssetManifestSHA256, frozenAsset, validator.Fingerprint())
	if err != nil || second.JobID != first.JobID {
		t.Fatalf("same-key enqueue replay = %#v/%v, want same job", second, err)
	}
	otherKeyFingerprint := sha256.Sum256([]byte("another key for the same exact revision"))
	if _, err := repository.EnqueueBuild(fixture.ctx, gameID, publisherID, request, "enqueue-key-0000002", otherKeyFingerprint[:], preparation.Profile.ProfileVersion, preparation.Profile.AssetManifestSHA256, frozenAsset, validator.Fingerprint()); !errors.Is(err, admin.ErrBuildAlreadyActive) {
		t.Fatalf("second key for an actively building revision = %v, want ErrBuildAlreadyActive", err)
	}
	if first.Status != admin.BuildStatusQueued || first.ReleaseVersion != 1 || first.PublicArtifactSHA256 != nil || first.ErrorCode != nil {
		t.Fatalf("new build job initial state = %#v", first)
	}
	var jobs, audits int
	var nextVersion int64
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT count(*) FROM admin_build_jobs WHERE game_id = $1`, gameID).Scan(&jobs); err != nil {
		t.Fatalf("count enqueue jobs: %v", err)
	}
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT next_release_version FROM release_version_counters WHERE game_id = $1`, gameID).Scan(&nextVersion); err != nil {
		t.Fatalf("read release counter after idempotent replay: %v", err)
	}
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT count(*) FROM audit_logs WHERE game_id = $1 AND action = 'release.build.request'`, gameID).Scan(&audits); err != nil {
		t.Fatalf("count enqueue audit rows: %v", err)
	}
	if jobs != 1 || nextVersion != 2 || audits != 1 {
		t.Fatalf("enqueue side effects jobs/version/audits = %d/%d/%d, want 1/2/1", jobs, nextVersion, audits)
	}
	otherRequest := request
	otherRequest.BodyHash = hex.EncodeToString(bytes.Repeat([]byte{0xff}, 32))
	otherFingerprint := sha256.Sum256([]byte("different request fingerprint"))
	if _, err := repository.EnqueueBuild(fixture.ctx, gameID, publisherID, otherRequest, "enqueue-key-0000001", otherFingerprint[:], preparation.Profile.ProfileVersion, preparation.Profile.AssetManifestSHA256, frozenAsset, validator.Fingerprint()); !errors.Is(err, admin.ErrConflict) {
		t.Fatalf("different payload with same key error = %v, want conflict", err)
	}
}

func waitForBuildProfileLock(t *testing.T, fixture *adminACLFixture, ctx context.Context) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		if err := fixture.db.QueryRowContext(ctx, `
SELECT count(*) FROM pg_stat_activity
WHERE datname = current_database()
  AND wait_event_type = 'Lock'
  AND query LIKE '%admin_enqueue_build_job%'`).Scan(&waiting); err != nil {
			t.Fatalf("inspect enqueue profile lock wait: %v", err)
		}
		if waiting > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("enqueue did not wait on the concurrent Game profile update: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}
