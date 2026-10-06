package buildworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	contentbuild "example.com/tbcg/server/internal/content/build"
	"example.com/tbcg/server/internal/content/mapdata"
	"example.com/tbcg/server/internal/content/release"
	"example.com/tbcg/server/internal/content/schema"
)

// SnapshotClaim 是 worker 视角的完整 Game 快照作业。
type SnapshotClaim struct {
	JobID          string
	ClaimToken     string
	GameID         string
	ReleaseID      string
	ReleaseVersion int32
	InputSnapshot  json.RawMessage
}

// SnapshotQueue 是快照作业的窄数据库边界。
type SnapshotQueue interface {
	Claim(ctx context.Context, workerID string, leaseSeconds int) (SnapshotClaim, bool, error)
	Finish(ctx context.Context, jobID, workerID, claimToken string, manifest, publicSHA256, privateSHA256 []byte) (bool, error)
	Fail(ctx context.Context, jobID, workerID, claimToken, errorCode string, retryable bool, backoffSeconds int) (bool, error)
}

type snapshotSupport struct {
	queue    SnapshotQueue
	registry *schema.Registry
	geometry *mapdata.Validator
}

// WithSnapshots 让同一单执行槽 worker 在没有单物品作业时处理完整 Game 快照。
func (w *Worker) WithSnapshots(queue SnapshotQueue, registry *schema.Registry, geometry *mapdata.Validator) (*Worker, error) {
	if queue == nil || registry == nil || geometry == nil {
		return nil, errors.New("build worker: snapshot queue and trusted validators are required")
	}
	copyWorker := *w
	copyWorker.snapshots = &snapshotSupport{queue: queue, registry: registry, geometry: geometry}
	return &copyWorker, nil
}

func (w *Worker) runSnapshotOnce(ctx context.Context) (bool, error) {
	claim, found, err := w.snapshots.queue.Claim(ctx, w.id, w.leaseSeconds)
	if err != nil {
		return false, fmt.Errorf("claim snapshot job: %w", err)
	}
	if !found {
		return false, nil
	}
	jobCtx, cancel := context.WithTimeout(ctx, time.Duration(w.leaseSeconds)*time.Second/2)
	defer cancel()
	return true, w.processSnapshot(jobCtx, claim)
}

func (w *Worker) processSnapshot(ctx context.Context, claim SnapshotClaim) error {
	fail := func(code string, retryable bool, cause error) error {
		backoff := 0
		if retryable {
			backoff = storeBackoff
			if code == codeDeploymentMismatch {
				backoff = deploymentBackoff
			}
		}
		recorded, err := w.snapshots.queue.Fail(ctx, claim.JobID, w.id, claim.ClaimToken, code, retryable, backoff)
		if err != nil {
			return errors.Join(cause, fmt.Errorf("record snapshot failure: %w", err))
		}
		if !recorded {
			return errors.Join(ErrLeaseLost, cause)
		}
		return nil
	}
	var input contentbuild.SnapshotInput
	if err := json.Unmarshal(claim.InputSnapshot, &input); err != nil {
		return fail(codeInvalidInput, false, fmt.Errorf("decode frozen snapshot input: %w", err))
	}
	var frozen struct {
		BuilderVersion string `json:"builder_version"`
	}
	_ = json.Unmarshal(claim.InputSnapshot, &frozen)
	if frozen.BuilderVersion != contentbuild.SnapshotBuilderVersion {
		return fail(codeDeploymentMismatch, true, fmt.Errorf("frozen snapshot builder %q differs", frozen.BuilderVersion))
	}
	input.GameID = claim.GameID
	input.ReleaseID = claim.ReleaseID
	input.ReleaseVersion = int(claim.ReleaseVersion)
	artifacts, err := contentbuild.BuildSnapshot(w.snapshots.registry, w.snapshots.geometry, input)
	if errors.Is(err, contentbuild.ErrSchemaFingerprintMismatch) {
		return fail(codeDeploymentMismatch, true, err)
	}
	if err != nil {
		return fail(codeInvalidInput, false, err)
	}
	stored, err := w.store.Install(claim.GameID, claim.ReleaseID, artifacts)
	if err != nil {
		if errors.Is(err, release.ErrReleaseConflict) {
			return fail(codeArtifactConflict, false, err)
		}
		return fail(codeArtifactStore, true, err)
	}
	finished, err := w.snapshots.queue.Finish(ctx, claim.JobID, w.id, claim.ClaimToken, artifacts.Manifest, stored.PublicSHA256, stored.PrivateSHA256)
	if errors.Is(err, ErrCompletionRejected) {
		return fail(codeCompletionRejected, false, err)
	}
	if err != nil {
		return fmt.Errorf("finish snapshot job %s: %w", claim.JobID, err)
	}
	if !finished {
		return ErrLeaseLost
	}
	return nil
}
