package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/taills/tbcg/server/internal/admin"
	"github.com/taills/tbcg/server/internal/persistence"
)

const buildWorkerRole = "tbcg_content_build_worker"

var (
	workerUUIDPattern      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	workerErrorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

type BuildWorkerRepository struct {
	db *sql.DB
}

type BuildWorkerClaim struct {
	Job           admin.BuildJob  `json:"job"`
	InputSnapshot json.RawMessage `json:"input_snapshot"`
	ClaimToken    string          `json:"claim_token"`
	ClaimOwner    string          `json:"claim_owner"`
	LeaseUntil    time.Time       `json:"lease_until"`
}

// OpenBuildWorkerRepository opens a TLS-verified pool with only the worker function role.
func OpenBuildWorkerRepository(ctx context.Context, dsn string) (*BuildWorkerRepository, error) {
	return openBuildWorkerRepository(ctx, dsn, false)
}

func openBuildWorkerRepository(ctx context.Context, dsn string, allowOwnedLoopbackTest bool) (*BuildWorkerRepository, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse build worker PostgreSQL DSN: %w", err)
	}
	if err := validateAdminPostgresTLS(config, allowOwnedLoopbackTest); err != nil {
		return nil, fmt.Errorf("build worker PostgreSQL TLS policy: %w", err)
	}
	config.AfterConnect = func(ctx context.Context, conn *pgconn.PgConn) error {
		if _, err := conn.Exec(ctx, "SET ROLE "+buildWorkerRole).ReadAll(); err != nil {
			return fmt.Errorf("assume restricted content build worker role: %w", err)
		}
		results, err := conn.Exec(ctx, `
SELECT session_user, current_user,
       COALESCE((SELECT rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls FROM pg_roles WHERE rolname = current_user), true),
       COALESCE((SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls FROM pg_roles WHERE rolname = session_user), true),
       pg_has_role(session_user, current_user, 'MEMBER'),
       EXISTS (SELECT 1 FROM pg_roles r WHERE (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
               AND (pg_has_role(session_user, r.oid, 'MEMBER') OR pg_has_role(current_user, r.oid, 'MEMBER'))),
       has_schema_privilege(current_user, 'public', 'CREATE') OR has_schema_privilege(session_user, 'public', 'CREATE'),
       has_database_privilege(current_user, current_database(), 'CREATE') OR has_database_privilege(session_user, current_database(), 'CREATE'),
       EXISTS (SELECT 1 FROM pg_class c WHERE c.relnamespace = 'public'::regnamespace
               AND pg_has_role(session_user, c.relowner, 'MEMBER')),
       EXISTS (
         SELECT 1 FROM pg_class c
         CROSS JOIN unnest(ARRAY[current_user, session_user]) AS principals(role_name)
         WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r','p','v','m')
           AND (has_any_column_privilege(principals.role_name, c.oid, 'SELECT')
             OR has_any_column_privilege(principals.role_name, c.oid, 'INSERT')
             OR has_any_column_privilege(principals.role_name, c.oid, 'UPDATE')
             OR has_table_privilege(principals.role_name, c.oid, 'DELETE')
             OR has_table_privilege(principals.role_name, c.oid, 'TRUNCATE'))),
       EXISTS (
         SELECT 1 FROM pg_roles r
         CROSS JOIN unnest(ARRAY[current_user, session_user]) AS principals(role_name)
         WHERE r.rolname IN ('tbcg_content_build_owner', 'tbcg_admin_control_plane')
           AND pg_has_role(principals.role_name, r.oid, 'MEMBER')),
       has_function_privilege(current_user, 'public.admin_claim_build_job(uuid,integer)', 'EXECUTE'),
       has_function_privilege(current_user, 'public.admin_renew_build_job_lease(uuid,uuid,uuid,integer)', 'EXECUTE')`).ReadAll()
		if err != nil {
			return fmt.Errorf("inspect build worker PostgreSQL role: %w", err)
		}
		if len(results) != 1 || len(results[0].Rows) != 1 || len(results[0].Rows[0]) != 13 {
			return errors.New("build worker PostgreSQL role verification returned an unexpected result")
		}
		row := results[0].Rows[0]
		if string(row[1]) != buildWorkerRole || string(row[4]) != "t" {
			return errors.New("build worker did not assume its restricted NOLOGIN role")
		}
		if string(row[11]) != "t" || string(row[12]) != "t" {
			return errors.New("build worker role cannot execute the claim and lease functions")
		}
		for _, value := range append(append([][]byte(nil), row[2:4]...), row[5:11]...) {
			if string(value) != "f" {
				return errors.New("build worker PostgreSQL privileges are not restricted to worker functions")
			}
		}
		return nil
	}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(defaultConnectionMaxLifetime)
	db.SetConnMaxIdleTime(defaultConnectionMaxIdleTime)
	pingCtx, cancel := context.WithTimeout(ctx, defaultPingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		pingErr := fmt.Errorf("ping restricted content build worker database: %w", err)
		if closeErr := db.Close(); closeErr != nil {
			return nil, errors.Join(pingErr, fmt.Errorf("close failed worker database pool: %w", closeErr))
		}
		return nil, pingErr
	}
	return &BuildWorkerRepository{db: db}, nil
}

func (r *BuildWorkerRepository) Claim(ctx context.Context, workerInstanceID string, leaseSeconds int) (BuildWorkerClaim, bool, error) {
	if !workerUUIDPattern.MatchString(workerInstanceID) || leaseSeconds < 5 || leaseSeconds > 300 {
		return BuildWorkerClaim{}, false, errors.New("claim content build: invalid worker instance or lease")
	}
	var encoded []byte
	err := r.db.QueryRowContext(ctx, `SELECT public.admin_claim_build_job($1::uuid, $2)`, workerInstanceID, leaseSeconds).Scan(&encoded)
	if err != nil {
		return BuildWorkerClaim{}, false, buildWorkerStoreError("claim content build job", err)
	}
	if len(encoded) == 0 {
		return BuildWorkerClaim{}, false, nil
	}
	var claim BuildWorkerClaim
	if err := json.Unmarshal(encoded, &claim); err != nil {
		return BuildWorkerClaim{}, false, fmt.Errorf("decode claimed build job: %w", err)
	}
	if claim.Job.JobID == "" || claim.Job.Status != admin.BuildStatusRunning || claim.ClaimOwner != workerInstanceID ||
		!workerUUIDPattern.MatchString(claim.ClaimToken) || len(claim.InputSnapshot) == 0 || !claim.LeaseUntil.After(time.Now()) {
		return BuildWorkerClaim{}, false, errors.New("claim content build: database returned an invalid or out-of-scope claim")
	}
	return claim, true, nil
}

func (r *BuildWorkerRepository) RenewLease(ctx context.Context, jobID, workerInstanceID, claimToken string, leaseSeconds int) (bool, error) {
	if !workerUUIDPattern.MatchString(jobID) || !workerUUIDPattern.MatchString(workerInstanceID) ||
		!workerUUIDPattern.MatchString(claimToken) || leaseSeconds < 5 || leaseSeconds > 300 {
		return false, errors.New("renew content build lease: invalid job, worker, claim token, or lease")
	}
	var renewed bool
	err := r.db.QueryRowContext(ctx, `
SELECT public.admin_renew_build_job_lease($1::uuid, $2::uuid, $3::uuid, $4)`, jobID, workerInstanceID, claimToken, leaseSeconds).Scan(&renewed)
	if err != nil {
		if persistence.IsCommitOutcomeUnknown(err) {
			return false, fmt.Errorf("renew build lease outcome unknown: %w", errors.Join(persistence.ErrUnknown, err))
		}
		return false, buildWorkerStoreError("renew content build lease", err)
	}
	return renewed, nil
}

// Finish registers the built Release, its single entry, and job completion in one transaction.
// It returns false when this worker no longer holds the claim (stale token or expired lease).
func (r *BuildWorkerRepository) Finish(ctx context.Context, jobID, workerInstanceID, claimToken string, manifest []byte, publicSHA256, privateSHA256 []byte) (bool, error) {
	if !workerUUIDPattern.MatchString(jobID) || !workerUUIDPattern.MatchString(workerInstanceID) || !workerUUIDPattern.MatchString(claimToken) ||
		len(publicSHA256) != 32 || len(privateSHA256) != 32 || !json.Valid(manifest) {
		return false, errors.New("finish content build: invalid job, claim, manifest, or artifact hash")
	}
	var encoded []byte
	err := r.db.QueryRowContext(ctx, `
SELECT public.admin_finish_build_job($1::uuid, $2::uuid, $3::uuid, $4::jsonb, $5, $6)`,
		jobID, workerInstanceID, claimToken, manifest, publicSHA256, privateSHA256).Scan(&encoded)
	if err != nil {
		return false, buildWorkerStoreError("finish content build", err)
	}
	if len(encoded) == 0 {
		return false, nil
	}
	return decodeWorkerJobResult(encoded, jobID, admin.BuildStatusBuilt)
}

// Fail records a bounded, safe error code and either schedules a retry or ends the job.
func (r *BuildWorkerRepository) Fail(ctx context.Context, jobID, workerInstanceID, claimToken, errorCode string, retryable bool, backoffSeconds int) (bool, error) {
	if !workerUUIDPattern.MatchString(jobID) || !workerUUIDPattern.MatchString(workerInstanceID) || !workerUUIDPattern.MatchString(claimToken) ||
		!workerErrorCodePattern.MatchString(errorCode) || backoffSeconds < 0 || backoffSeconds > 3600 {
		return false, errors.New("fail content build: invalid job, claim, error code, or backoff")
	}
	var encoded []byte
	err := r.db.QueryRowContext(ctx, `
SELECT public.admin_fail_build_job($1::uuid, $2::uuid, $3::uuid, $4, $5, $6)`,
		jobID, workerInstanceID, claimToken, errorCode, retryable, backoffSeconds).Scan(&encoded)
	if err != nil {
		return false, buildWorkerStoreError("fail content build", err)
	}
	if len(encoded) == 0 {
		return false, nil
	}
	var job admin.BuildJob
	if err := json.Unmarshal(encoded, &job); err != nil {
		return false, fmt.Errorf("decode failed build job: %w", err)
	}
	if job.JobID != jobID || (job.Status != admin.BuildStatusRetryWait && job.Status != admin.BuildStatusFailed) {
		return false, errors.New("fail content build: database returned an unexpected job state")
	}
	return true, nil
}

func decodeWorkerJobResult(encoded []byte, jobID string, want admin.BuildStatus) (bool, error) {
	var job admin.BuildJob
	if err := json.Unmarshal(encoded, &job); err != nil {
		return false, fmt.Errorf("decode build job result: %w", err)
	}
	if job.JobID != jobID || job.Status != want {
		return false, fmt.Errorf("build job result state = %q, want %q", job.Status, want)
	}
	return true, nil
}

func (r *BuildWorkerRepository) Close() error {
	if err := r.db.Close(); err != nil {
		return fmt.Errorf("close build worker repository: %w", err)
	}
	return nil
}

func buildWorkerStoreError(operation string, err error) error {
	if persistence.IsCommitOutcomeUnknown(err) {
		return fmt.Errorf("%s outcome unknown: %w", operation, errors.Join(persistence.ErrUnknown, err))
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42501" {
		return fmt.Errorf("%s: %w", operation, errors.Join(admin.ErrForbidden, err))
	}
	return fmt.Errorf("%s: %w", operation, err)
}
