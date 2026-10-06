package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/taills/tbcg/server/internal/admin"
	contentbuild "github.com/taills/tbcg/server/internal/content/build"
	"github.com/taills/tbcg/server/internal/content/buildworker"
)

func TestBuildWorkerTurnsApprovedJobIntoUnactivatedBuiltRelease(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	seed := seedApprovedBuild(t, fixture, "build_worker_e2e_", "00000000-0000-4000-8000-000000000051", "demo:item/worker-built")
	adminRepository, err := openAdminRepository(fixture.ctx, fixture.appDSN, true)
	if err != nil {
		t.Fatalf("open Publisher repository: %v", err)
	}
	t.Cleanup(func() { _ = adminRepository.Close() })
	preparation, err := adminRepository.ReadApprovedBuildInput(fixture.ctx, seed.gameID, seed.publisherID, seed.contentID, 1, seed.bodyHash[:])
	if err != nil {
		t.Fatalf("read approved build input: %v", err)
	}
	enqueue := func(key string) admin.BuildJob {
		t.Helper()
		fingerprint := sha256.Sum256([]byte(key))
		job, err := adminRepository.EnqueueBuild(fixture.ctx, seed.gameID, seed.publisherID, seed.request, key, fingerprint[:],
			preparation.Profile.ProfileVersion, preparation.Profile.AssetManifestSHA256, seed.frozenAsset, seed.validator.Fingerprint())
		if err != nil {
			t.Fatalf("enqueue approved build %s: %v", key, err)
		}
		return job
	}
	job := enqueue("worker-e2e-key-0001")

	workerRepository, err := openBuildWorkerRepository(fixture.ctx, fixture.workerDSN, true)
	if err != nil {
		t.Fatalf("open restricted worker repository: %v", err)
	}
	t.Cleanup(func() { _ = workerRepository.Close() })
	storeDir := filepath.Join(t.TempDir(), "private-artifacts")
	if err := os.Mkdir(storeDir, 0o700); err != nil {
		t.Fatalf("create private artifact store: %v", err)
	}
	store, err := contentbuild.OpenArtifactStore(storeDir)
	if err != nil {
		t.Fatalf("open private artifact store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const workerID = "00000000-0000-4000-8000-000000000052"
	worker, err := buildworker.New(workerID, 30, workerRepository.WorkerQueue(), store, seed.validator)
	if err != nil {
		t.Fatalf("create build worker: %v", err)
	}
	if claimed, err := worker.RunOnce(fixture.ctx); err != nil || !claimed {
		t.Fatalf("worker RunOnce() = %t, %v", claimed, err)
	}

	built, err := adminRepository.GetBuildJob(fixture.ctx, seed.gameID, seed.publisherID, job.JobID)
	if err != nil {
		t.Fatalf("read built job as Publisher: %v", err)
	}
	publicBytes, err := os.ReadFile(filepath.Join(storeDir, seed.gameID, job.ReleaseID+".public.json"))
	if err != nil {
		t.Fatalf("read installed public artifact: %v", err)
	}
	publicHash := sha256.Sum256(publicBytes)
	if built.Status != admin.BuildStatusBuilt || built.PublicArtifactSHA256 == nil || *built.PublicArtifactSHA256 != hex.EncodeToString(publicHash[:]) || built.ErrorCode != nil {
		t.Fatalf("built job state = %#v", built)
	}
	var status string
	var activated bool
	var releaseVersion int32
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT status, activated_at IS NOT NULL, release_version FROM releases WHERE game_id = $1 AND release_id = $2`, seed.gameID, job.ReleaseID).Scan(&status, &activated, &releaseVersion); err != nil {
		t.Fatalf("read registered Release: %v", err)
	}
	if status != "built" || activated || releaseVersion != job.ReleaseVersion {
		t.Fatalf("registered Release status/activated/version = %s/%t/%d", status, activated, releaseVersion)
	}
	var entries, completions int
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT count(*) FROM release_entries WHERE game_id = $1 AND release_id = $2 AND content_id = $3 AND revision = 1`, seed.gameID, job.ReleaseID, seed.contentID).Scan(&entries); err != nil {
		t.Fatalf("count Release entries: %v", err)
	}
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT count(*) FROM audit_logs WHERE game_id = $1 AND action = 'release.build.completed' AND resource_id = $2`, seed.gameID, job.ReleaseID).Scan(&completions); err != nil {
		t.Fatalf("count completion audit rows: %v", err)
	}
	if entries != 1 || completions != 1 {
		t.Fatalf("Release entries/completion audits = %d/%d, want 1/1", entries, completions)
	}
	for _, suffix := range []string{".manifest.json", ".public.json", ".private.json"} {
		info, err := os.Lstat(filepath.Join(storeDir, seed.gameID, job.ReleaseID+suffix))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("private artifact %s mode/err = %v/%v", suffix, info, err)
		}
	}
	if claimed, err := worker.RunOnce(fixture.ctx); err != nil || claimed {
		t.Fatalf("worker claimed again after the only job was built: %t, %v", claimed, err)
	}

	manifest, err := os.ReadFile(filepath.Join(storeDir, seed.gameID, job.ReleaseID+".manifest.json"))
	if err != nil {
		t.Fatalf("read installed manifest: %v", err)
	}
	privateBytes, err := os.ReadFile(filepath.Join(storeDir, seed.gameID, job.ReleaseID+".private.json"))
	if err != nil {
		t.Fatalf("read installed private artifact: %v", err)
	}
	privateHash := sha256.Sum256(privateBytes)
	const staleToken = "00000000-0000-4000-8000-0000000000ff"
	if replayed, err := workerRepository.Finish(fixture.ctx, job.JobID, workerID, staleToken, manifest, publicHash[:], privateHash[:]); err != nil || !replayed {
		t.Fatalf("commit-unknown finish replay with identical artifacts = %t, %v", replayed, err)
	}
	otherHash := sha256.Sum256([]byte("different private artifact"))
	if _, err := workerRepository.Finish(fixture.ctx, job.JobID, workerID, staleToken, manifest, publicHash[:], otherHash[:]); err == nil {
		t.Fatal("finish replay accepted different artifacts for an already built job")
	}

	second := enqueue("worker-e2e-key-0002")
	claim, found, err := workerRepository.Claim(fixture.ctx, workerID, 30)
	if err != nil || !found || claim.Job.JobID != second.JobID {
		t.Fatalf("claim second job = %#v/%t/%v", claim.Job, found, err)
	}
	if finished, err := workerRepository.Finish(fixture.ctx, second.JobID, workerID, staleToken, manifest, publicHash[:], privateHash[:]); err != nil || finished {
		t.Fatalf("stale claim token finished a running job = %t, %v", finished, err)
	}
	if failed, err := workerRepository.Fail(fixture.ctx, second.JobID, workerID, staleToken, "invalid_frozen_input", false, 0); err != nil || failed {
		t.Fatalf("stale claim token failed a running job = %t, %v", failed, err)
	}
	if finished, err := workerRepository.WorkerQueue().Finish(fixture.ctx, second.JobID, workerID, claim.ClaimToken, manifest, publicHash[:], privateHash[:]); !errors.Is(err, buildworker.ErrCompletionRejected) || finished {
		t.Fatalf("finish with another job's manifest = %t, %v, want ErrCompletionRejected", finished, err)
	}
	if failed, err := workerRepository.Fail(fixture.ctx, second.JobID, workerID, claim.ClaimToken, "artifact_store_unavailable", true, 0); err != nil || !failed {
		t.Fatalf("record retryable failure = %t, %v", failed, err)
	}
	retry, err := adminRepository.GetBuildJob(fixture.ctx, seed.gameID, seed.publisherID, second.JobID)
	if err != nil || retry.Status != admin.BuildStatusRetryWait || retry.ErrorCode == nil || *retry.ErrorCode != "artifact_store_unavailable" {
		t.Fatalf("retryable failure state = %#v, %v", retry, err)
	}
	if claimed, err := worker.RunOnce(fixture.ctx); err != nil || !claimed {
		t.Fatalf("worker did not reclaim the retry_wait job: %t, %v", claimed, err)
	}
	rebuilt, err := adminRepository.GetBuildJob(fixture.ctx, seed.gameID, seed.publisherID, second.JobID)
	if err != nil || rebuilt.Status != admin.BuildStatusBuilt || rebuilt.ReleaseID == job.ReleaseID {
		t.Fatalf("retried job state = %#v, %v", rebuilt, err)
	}
}
