package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"example.com/tbcg/server/internal/content/buildworker"
	"example.com/tbcg/server/internal/persistence"
	"github.com/jackc/pgx/v5/pgconn"
)

type snapshotJobJSON struct {
	JobID          string `json:"job_id"`
	GameID         string `json:"game_id"`
	ReleaseID      string `json:"release_id"`
	ReleaseVersion int32  `json:"release_version"`
	Status         string `json:"status"`
}

type snapshotClaimJSON struct {
	Job           snapshotJobJSON `json:"job"`
	InputSnapshot json.RawMessage `json:"input_snapshot"`
	ClaimToken    string          `json:"claim_token"`
	ClaimOwner    string          `json:"claim_owner"`
	LeaseUntil    time.Time       `json:"lease_until"`
}

type snapshotWorkerQueue struct {
	repository *BuildWorkerRepository
}

// SnapshotQueue 暴露完整 Game 快照作业的窄队列端口。
func (r *BuildWorkerRepository) SnapshotQueue() buildworker.SnapshotQueue {
	return snapshotWorkerQueue{repository: r}
}

func (q snapshotWorkerQueue) Claim(ctx context.Context, workerID string, leaseSeconds int) (buildworker.SnapshotClaim, bool, error) {
	if !workerUUIDPattern.MatchString(workerID) || leaseSeconds < 5 || leaseSeconds > 300 {
		return buildworker.SnapshotClaim{}, false, errors.New("claim snapshot build: invalid worker instance or lease")
	}
	var encoded []byte
	err := q.repository.db.QueryRowContext(ctx, `SELECT public.admin_claim_snapshot_job($1::uuid, $2)`, workerID, leaseSeconds).Scan(&encoded)
	if err != nil {
		return buildworker.SnapshotClaim{}, false, buildWorkerStoreError("claim snapshot build job", err)
	}
	if len(encoded) == 0 {
		return buildworker.SnapshotClaim{}, false, nil
	}
	var claim snapshotClaimJSON
	if err := json.Unmarshal(encoded, &claim); err != nil {
		return buildworker.SnapshotClaim{}, false, fmt.Errorf("decode claimed snapshot job: %w", err)
	}
	if claim.Job.JobID == "" || claim.Job.Status != "running" || claim.ClaimOwner != workerID || !workerUUIDPattern.MatchString(claim.ClaimToken) || len(claim.InputSnapshot) == 0 {
		return buildworker.SnapshotClaim{}, false, errors.New("claim snapshot build: database returned an invalid claim")
	}
	return buildworker.SnapshotClaim{JobID: claim.Job.JobID, ClaimToken: claim.ClaimToken, GameID: claim.Job.GameID,
		ReleaseID: claim.Job.ReleaseID, ReleaseVersion: claim.Job.ReleaseVersion, InputSnapshot: claim.InputSnapshot}, true, nil
}

func (q snapshotWorkerQueue) Finish(ctx context.Context, jobID, workerID, claimToken string, manifest, publicSHA256, privateSHA256 []byte) (bool, error) {
	var encoded []byte
	err := q.repository.db.QueryRowContext(ctx, `SELECT public.admin_finish_snapshot_job($1::uuid, $2::uuid, $3::uuid, $4::jsonb, $5, $6)`,
		jobID, workerID, claimToken, manifest, publicSHA256, privateSHA256).Scan(&encoded)
	if err != nil {
		wrapped := buildWorkerStoreError("finish snapshot build", err)
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && !errors.Is(wrapped, persistence.ErrUnknown) {
			switch pgErr.Code {
			case "22023", "23505", "23514":
				return false, errors.Join(buildworker.ErrCompletionRejected, wrapped)
			}
		}
		return false, wrapped
	}
	if len(encoded) == 0 {
		return false, nil
	}
	var job snapshotJobJSON
	if err := json.Unmarshal(encoded, &job); err != nil || job.JobID != jobID || job.Status != "built" {
		return false, errors.New("finish snapshot build: unexpected job state")
	}
	return true, nil
}

func (q snapshotWorkerQueue) Fail(ctx context.Context, jobID, workerID, claimToken, errorCode string, retryable bool, backoffSeconds int) (bool, error) {
	var encoded []byte
	err := q.repository.db.QueryRowContext(ctx, `SELECT public.admin_fail_snapshot_job($1::uuid, $2::uuid, $3::uuid, $4, $5, $6)`,
		jobID, workerID, claimToken, errorCode, retryable, backoffSeconds).Scan(&encoded)
	if err != nil {
		return false, buildWorkerStoreError("fail snapshot build", err)
	}
	return len(encoded) != 0, nil
}
