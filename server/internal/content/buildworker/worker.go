// Package buildworker runs one trusted single-item build at a time from the durable job queue.
// It is a restricted process boundary, not a sandbox for untrusted code.
package buildworker

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	contentassets "example.com/tbcg/server/internal/content/assets"
	contentbuild "example.com/tbcg/server/internal/content/build"
	"example.com/tbcg/server/internal/content/release"
	"example.com/tbcg/server/internal/content/schema"
)

const (
	codeInvalidInput       = "invalid_frozen_input"
	codeDeploymentMismatch = "worker_deployment_mismatch"
	codeArtifactConflict   = "artifact_conflict"
	codeArtifactStore      = "artifact_store_unavailable"
	codeCompletionRejected = "completion_rejected"
	deploymentBackoff      = 300
	storeBackoff           = 30
)

// ErrLeaseLost means another worker or the expiry sweep owns the job now; no completion was written.
var ErrLeaseLost = errors.New("build worker: claim lease lost before completion")

// ErrCompletionRejected marks a definitive refusal of a completion report (not an unknown outcome).
// Queue adapters wrap deterministic database rejections with it so the job fails instead of looping.
var ErrCompletionRejected = errors.New("build worker: completion report rejected")

// Claim is the private worker view of a running job and its frozen input.
type Claim struct {
	JobID          string
	ClaimToken     string
	GameID         string
	ContentID      string
	Revision       int32
	BodyHash       string
	ReleaseID      string
	ReleaseVersion int32
	InputSnapshot  json.RawMessage
}

// Queue is the narrow database boundary the worker needs.
type Queue interface {
	Claim(ctx context.Context, workerID string, leaseSeconds int) (Claim, bool, error)
	Finish(ctx context.Context, jobID, workerID, claimToken string, manifest, publicSHA256, privateSHA256 []byte) (bool, error)
	Fail(ctx context.Context, jobID, workerID, claimToken, errorCode string, retryable bool, backoffSeconds int) (bool, error)
}

// ArtifactInstaller durably installs artifact bytes before the job is marked built.
type ArtifactInstaller interface {
	Install(gameID, releaseID string, artifacts contentbuild.Artifacts) (contentbuild.StoredArtifacts, error)
}

// Worker processes claimed jobs with a fixed identity and lease.
type Worker struct {
	id           string
	leaseSeconds int
	queue        Queue
	store        ArtifactInstaller
	validator    *schema.Validator
	snapshots    *snapshotSupport
}

// New validates the worker configuration. The worker ID must be stable across restarts so a
// restarted process recovers its own unexpired claim instead of waiting for lease expiry.
func New(id string, leaseSeconds int, queue Queue, store ArtifactInstaller, validator *schema.Validator) (*Worker, error) {
	if id == "" || leaseSeconds < 5 || leaseSeconds > 300 || queue == nil || store == nil || validator == nil {
		return nil, errors.New("build worker: invalid identity, lease, queue, store, or validator")
	}
	return &Worker{id: id, leaseSeconds: leaseSeconds, queue: queue, store: store, validator: validator}, nil
}

// RunOnce claims and processes at most one job. It reports whether a job was claimed.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	claim, found, err := w.queue.Claim(ctx, w.id, w.leaseSeconds)
	if err != nil {
		return false, fmt.Errorf("claim build job: %w", err)
	}
	if !found {
		if w.snapshots != nil {
			return w.runSnapshotOnce(ctx)
		}
		return false, nil
	}
	// Finish well inside the lease so a slow build cannot complete after another claim.
	jobCtx, cancel := context.WithTimeout(ctx, time.Duration(w.leaseSeconds)*time.Second/2)
	defer cancel()
	return true, w.process(jobCtx, claim)
}

func (w *Worker) process(ctx context.Context, claim Claim) error {
	artifacts, code, retryable, err := w.build(claim)
	if err != nil {
		return w.fail(ctx, claim, code, retryable, err)
	}
	stored, err := w.store.Install(claim.GameID, claim.ReleaseID, artifacts)
	if err != nil {
		if errors.Is(err, release.ErrReleaseConflict) {
			return w.fail(ctx, claim, codeArtifactConflict, false, err)
		}
		return w.fail(ctx, claim, codeArtifactStore, true, err)
	}
	finished, err := w.queue.Finish(ctx, claim.JobID, w.id, claim.ClaimToken, artifacts.Manifest, stored.PublicSHA256, stored.PrivateSHA256)
	if errors.Is(err, ErrCompletionRejected) {
		return w.fail(ctx, claim, codeCompletionRejected, false, err)
	}
	if err != nil {
		// Outcome may be unknown; the same worker ID reclaims its running job and replays deterministically.
		return fmt.Errorf("finish build job %s: %w", claim.JobID, err)
	}
	if !finished {
		return ErrLeaseLost
	}
	return nil
}

func (w *Worker) fail(ctx context.Context, claim Claim, code string, retryable bool, cause error) error {
	backoff := 0
	if retryable {
		backoff = storeBackoff
		if code == codeDeploymentMismatch {
			backoff = deploymentBackoff
		}
	}
	recorded, err := w.queue.Fail(ctx, claim.JobID, w.id, claim.ClaimToken, code, retryable, backoff)
	if err != nil {
		return errors.Join(fmt.Errorf("build job %s failed with %s: %w", claim.JobID, code, cause), fmt.Errorf("record build failure: %w", err))
	}
	if !recorded {
		return errors.Join(ErrLeaseLost, cause)
	}
	return nil
}

type frozenInput struct {
	Definition        json.RawMessage                   `json:"definition"`
	Profile           contentbuild.CompatibilityProfile `json:"profile"`
	SchemaFingerprint string                            `json:"schema_fingerprint"`
	BuilderVersion    string                            `json:"builder_version"`
	Asset             contentassets.FrozenSnapshot      `json:"asset"`
}

func (w *Worker) build(claim Claim) (contentbuild.Artifacts, string, bool, error) {
	var input frozenInput
	if err := json.Unmarshal(claim.InputSnapshot, &input); err != nil {
		return contentbuild.Artifacts{}, codeInvalidInput, false, fmt.Errorf("decode frozen build input: %w", err)
	}
	if input.BuilderVersion != contentbuild.BuilderVersion {
		return contentbuild.Artifacts{}, codeDeploymentMismatch, true, fmt.Errorf("frozen builder version %q differs from %q", input.BuilderVersion, contentbuild.BuilderVersion)
	}
	bodyHash, err := hex.DecodeString(claim.BodyHash)
	if err != nil {
		return contentbuild.Artifacts{}, codeInvalidInput, false, fmt.Errorf("decode frozen body hash: %w", err)
	}
	asset, err := contentassets.NewFrozen(input.Asset)
	if err != nil {
		return contentbuild.Artifacts{}, codeInvalidInput, false, fmt.Errorf("restore frozen asset snapshot: %w", err)
	}
	artifacts, err := contentbuild.Build(w.validator, contentbuild.Input{
		GameID: claim.GameID, ContentID: claim.ContentID, Revision: claim.Revision, BodyHash: bodyHash,
		Definition: input.Definition, ReleaseID: claim.ReleaseID, ReleaseVersion: int(claim.ReleaseVersion),
		Profile: input.Profile, SchemaFingerprint: input.SchemaFingerprint, Asset: asset,
	})
	if errors.Is(err, contentbuild.ErrSchemaFingerprintMismatch) {
		return contentbuild.Artifacts{}, codeDeploymentMismatch, true, err
	}
	if err != nil {
		return contentbuild.Artifacts{}, codeInvalidInput, false, err
	}
	return artifacts, "", false, nil
}
