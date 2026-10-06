package postgres

import (
	"context"
	"errors"

	"example.com/tbcg/server/internal/content/buildworker"
	"example.com/tbcg/server/internal/persistence"
	"github.com/jackc/pgx/v5/pgconn"
)

// buildWorkerQueue adapts the restricted worker repository to the worker's narrow queue port.
type buildWorkerQueue struct {
	repository *BuildWorkerRepository
}

// WorkerQueue exposes the repository through the build worker queue interface.
func (r *BuildWorkerRepository) WorkerQueue() buildworker.Queue {
	return buildWorkerQueue{repository: r}
}

func (q buildWorkerQueue) Claim(ctx context.Context, workerID string, leaseSeconds int) (buildworker.Claim, bool, error) {
	claim, found, err := q.repository.Claim(ctx, workerID, leaseSeconds)
	if err != nil || !found {
		return buildworker.Claim{}, found, err
	}
	return buildworker.Claim{
		JobID: claim.Job.JobID, ClaimToken: claim.ClaimToken, GameID: claim.Job.GameID,
		ContentID: claim.Job.ContentID, Revision: claim.Job.Revision, BodyHash: claim.Job.BodyHash,
		ReleaseID: claim.Job.ReleaseID, ReleaseVersion: claim.Job.ReleaseVersion, InputSnapshot: claim.InputSnapshot,
	}, true, nil
}

func (q buildWorkerQueue) Finish(ctx context.Context, jobID, workerID, claimToken string, manifest, publicSHA256, privateSHA256 []byte) (bool, error) {
	finished, err := q.repository.Finish(ctx, jobID, workerID, claimToken, manifest, publicSHA256, privateSHA256)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && !errors.Is(err, persistence.ErrUnknown) {
		switch pgErr.Code {
		case "22023", "23505", "23514":
			return false, errors.Join(buildworker.ErrCompletionRejected, err)
		}
	}
	return finished, err
}

func (q buildWorkerQueue) Fail(ctx context.Context, jobID, workerID, claimToken, errorCode string, retryable bool, backoffSeconds int) (bool, error) {
	return q.repository.Fail(ctx, jobID, workerID, claimToken, errorCode, retryable, backoffSeconds)
}
