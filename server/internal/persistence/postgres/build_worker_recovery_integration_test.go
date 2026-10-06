package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	contentbuild "example.com/tbcg/server/internal/content/build"
	"example.com/tbcg/server/internal/content/buildworker"
)

// lostFinishQueue simulates a process crash after artifacts were installed but before completion committed.
type lostFinishQueue struct {
	buildworker.Queue
}

func (lostFinishQueue) Finish(context.Context, string, string, string, []byte, []byte, []byte) (bool, error) {
	return false, errors.New("connection reset before completion was sent")
}

func TestBuildWorkerRecoversAfterCrashAndFencesStaleClaims(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	seed := seedApprovedBuild(t, fixture, "build_recovery_", "00000000-0000-4000-8000-000000000081", "demo:item/recovered")
	adminRepository, err := openAdminRepository(fixture.ctx, fixture.appDSN, true)
	if err != nil {
		t.Fatalf("open Publisher repository: %v", err)
	}
	t.Cleanup(func() { _ = adminRepository.Close() })
	preparation, err := adminRepository.ReadApprovedBuildInput(fixture.ctx, seed.gameID, seed.publisherID, seed.contentID, 1, seed.bodyHash[:])
	if err != nil {
		t.Fatalf("read approved build input: %v", err)
	}
	enqueue := func(key string) string {
		t.Helper()
		fingerprint := sha256.Sum256([]byte(key))
		job, err := adminRepository.EnqueueBuild(fixture.ctx, seed.gameID, seed.publisherID, seed.request, key, fingerprint[:],
			preparation.Profile.ProfileVersion, preparation.Profile.AssetManifestSHA256, seed.frozenAsset, seed.validator.Fingerprint())
		if err != nil {
			t.Fatalf("enqueue %s: %v", key, err)
		}
		return job.JobID
	}
	workerRepository, err := openBuildWorkerRepository(fixture.ctx, fixture.workerDSN, true)
	if err != nil {
		t.Fatalf("open worker repository: %v", err)
	}
	t.Cleanup(func() { _ = workerRepository.Close() })
	storeDir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(storeDir, 0o700); err != nil {
		t.Fatalf("create private store: %v", err)
	}
	store, err := contentbuild.OpenArtifactStore(storeDir)
	if err != nil {
		t.Fatalf("open private store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	newWorker := func(id string, lease int, queue buildworker.Queue) *buildworker.Worker {
		t.Helper()
		worker, err := buildworker.New(id, lease, queue, store, seed.validator)
		if err != nil {
			t.Fatalf("create worker %s: %v", id, err)
		}
		return worker
	}
	jobStatus := func(jobID string) (string, int) {
		t.Helper()
		var status string
		var attempts int
		if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT status, attempt_count FROM admin_build_jobs WHERE job_id = $1::uuid`, jobID).Scan(&status, &attempts); err != nil {
			t.Fatalf("read job %s: %v", jobID, err)
		}
		return status, attempts
	}

	const crashingWorker = "00000000-0000-4000-8000-000000000082"
	crashedJob := enqueue("recovery-key-crash-01")
	if _, err := newWorker(crashingWorker, 30, lostFinishQueue{Queue: workerRepository.WorkerQueue()}).RunOnce(fixture.ctx); err == nil {
		t.Fatal("simulated lost completion was reported as success")
	}
	if status, attempts := jobStatus(crashedJob); status != "running" || attempts != 1 {
		t.Fatalf("job after crash = %s/%d, want running/1", status, attempts)
	}
	if claimed, err := newWorker(crashingWorker, 30, workerRepository.WorkerQueue()).RunOnce(fixture.ctx); err != nil || !claimed {
		t.Fatalf("restarted worker recovery = %t, %v", claimed, err)
	}
	if status, attempts := jobStatus(crashedJob); status != "built" || attempts != 1 {
		t.Fatalf("recovered job = %s/%d, want built/1 without a new attempt", status, attempts)
	}

	const staleWorker, takeoverWorker = "00000000-0000-4000-8000-000000000083", "00000000-0000-4000-8000-000000000084"
	staleJob := enqueue("recovery-key-stale-01")
	stale, found, err := workerRepository.Claim(fixture.ctx, staleWorker, 5)
	if err != nil || !found || stale.Job.JobID != staleJob {
		t.Fatalf("stale worker claim = %#v/%t/%v", stale.Job, found, err)
	}
	time.Sleep(6 * time.Second)
	takeover, found, err := workerRepository.Claim(fixture.ctx, takeoverWorker, 30)
	if err != nil || !found || takeover.Job.JobID != staleJob || takeover.ClaimToken == stale.ClaimToken {
		t.Fatalf("takeover claim after lease expiry = %#v/%t/%v", takeover.Job, found, err)
	}
	if status, attempts := jobStatus(staleJob); status != "running" || attempts != 2 {
		t.Fatalf("taken-over job = %s/%d, want running/2", status, attempts)
	}
	manifest := []byte(`{}`)
	hash := sha256.Sum256(manifest)
	if finished, err := workerRepository.Finish(fixture.ctx, staleJob, staleWorker, stale.ClaimToken, manifest, hash[:], hash[:]); err != nil || finished {
		t.Fatalf("stale worker finish after takeover = %t, %v", finished, err)
	}
	if failed, err := workerRepository.Fail(fixture.ctx, staleJob, staleWorker, stale.ClaimToken, "artifact_store_unavailable", true, 0); err != nil || failed {
		t.Fatalf("stale worker fail after takeover = %t, %v", failed, err)
	}
	if claimed, err := newWorker(takeoverWorker, 30, workerRepository.WorkerQueue()).RunOnce(fixture.ctx); err != nil || !claimed {
		t.Fatalf("takeover worker completion = %t, %v", claimed, err)
	}
	if status, attempts := jobStatus(staleJob); status != "built" || attempts != 2 {
		t.Fatalf("job after takeover = %s/%d, want built/2", status, attempts)
	}
	var releases int
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT count(*) FROM releases WHERE game_id = $1`, seed.gameID).Scan(&releases); err != nil || releases != 2 {
		t.Fatalf("registered Releases = %d, %v, want exactly one per job", releases, err)
	}
}
