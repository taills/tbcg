package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/taills/tbcg/server/internal/admin"
)

func TestBuildMigrationRejectsAndPreservesPreexistingOwnerACL(t *testing.T) {
	fixture := newAdminACLFixture(t, "3")
	execACLStatement(t, fixture.ctx, fixture.db, `GRANT SELECT (display_name) ON games TO tbcg_content_build_owner`)
	t.Cleanup(func() {
		cleanupACLStatement(t, fixture.db, `REVOKE SELECT (display_name) ON games FROM tbcg_content_build_owner`)
	})
	output, err := fixture.migrationResult("4")
	if err == nil || !bytes.Contains(output, []byte("build owner and worker roles must have no pre-existing public table privileges")) {
		t.Fatal("004 accepted a build owner role with unrelated pre-existing table privileges")
	}
	var version int
	var profileTable sql.NullString
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT version, to_regclass('public.game_build_profiles') FROM schema_version`).Scan(&version, &profileTable); err != nil {
		t.Fatalf("inspect failed 004 migration state: %v", err)
	}
	if version != 3 || profileTable.Valid {
		t.Fatalf("failed 004 migration changed schema: version=%d profile_table=%t", version, profileTable.Valid)
	}
	var preserved bool
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT has_column_privilege('tbcg_content_build_owner', 'public.games', 'display_name', 'SELECT')`).Scan(&preserved); err != nil {
		t.Fatalf("inspect pre-existing build owner ACL after failed migration: %v", err)
	}
	if !preserved {
		t.Fatal("failed 004 migration revoked unrelated pre-existing ACL")
	}
}

func TestBuildMigrationRejectsPreexistingAdminReleaseEntryWrite(t *testing.T) {
	fixture := newAdminACLFixture(t, "3")
	for _, inherited := range []bool{false, true} {
		name := "direct"
		if inherited {
			name = "inherited"
		}
		t.Run(name, func(t *testing.T) {
			fixture.grantColumn(t, "release_entries", "revision", "INSERT", inherited)
			output, err := fixture.migrationResult("4")
			if err == nil || !bytes.Contains(output, []byte("tbcg_admin_control_plane has pre-existing Release entry write privileges")) {
				t.Fatal("004 did not reject a pre-existing Release entry write grant")
			}
			var version int
			var profileTable sql.NullString
			if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT version, to_regclass('public.game_build_profiles') FROM schema_version`).Scan(&version, &profileTable); err != nil {
				t.Fatalf("inspect failed 004 migration state: %v", err)
			}
			if version != 3 || profileTable.Valid {
				t.Fatalf("failed 004 migration changed schema: version=%d profile_table=%t", version, profileTable.Valid)
			}
		})
	}
}

func TestBuildFunctionOwnerCannotInsertActivatedRelease(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	gameID := randomTestIdentifier(t, "build_activation_guard_")
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO games (game_id, display_name) VALUES ($1, 'Build activation guard')`, gameID); err != nil {
		t.Fatalf("insert build guard Game: %v", err)
	}
	contentID := "demo:item/activation-guard"
	definition := fmt.Appendf(nil, `{"game_id":%q,"content_id":%q,"revision":1,"schema_version":1,"public":{"game_id":%q,"content_id":%q,"revision":1,"schema_version":1}}`, gameID, contentID, gameID, contentID)
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO content_revisions (game_id, content_id, revision, schema_version, content_type, body) VALUES ($1, $2, 1, 1, 'item', $3::jsonb)`, gameID, contentID, definition); err != nil {
		t.Fatalf("insert immutable activation-guard revision: %v", err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO releases (game_id, release_id, release_version, content_schema_version, instance_schema_version, ruleset_version, protocol_version, client_min_build, status, manifest, activated_at) VALUES ($1, 'existing-active', 1, 1, 1, 1, 1, '1.0.0', 'activated', '{}'::jsonb, now())`, gameID); err != nil {
		t.Fatalf("insert pre-existing activated Release: %v", err)
	}
	conn, err := fixture.db.Conn(fixture.ctx)
	if err != nil {
		t.Fatalf("get dedicated build-owner test connection: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close dedicated build-owner test connection: %v", err)
		}
	})
	if _, err := conn.ExecContext(fixture.ctx, `SET ROLE tbcg_content_build_owner`); err != nil {
		t.Fatalf("assume restricted build function owner role: %v", err)
	}
	_, err = conn.ExecContext(fixture.ctx, `
INSERT INTO releases (game_id, release_id, release_version, content_schema_version,
    instance_schema_version, ruleset_version, protocol_version, client_min_build, status, manifest)
VALUES ($1, 'forged-activated', 1, 1, 1, 1, 1, '1.0.0', 'activated', '{}'::jsonb)`, gameID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("build owner activated Release error = %v, want check violation", err)
	}
	_, err = conn.ExecContext(fixture.ctx, `INSERT INTO release_entries (game_id, release_id, content_id, revision) VALUES ($1, 'existing-active', $2, 1)`, gameID, contentID)
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("build owner changed activated Release closure error = %v, want check violation", err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO releases (game_id, release_id, release_version, content_schema_version, instance_schema_version, ruleset_version, protocol_version, client_min_build, status, manifest) VALUES ($1, 'built-immutable', 2, 1, 1, 1, 1, '1.0.0', 'built', '{}'::jsonb)`, gameID); err != nil {
		t.Fatalf("insert built Release for immutable-entry test: %v", err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO release_entries (game_id, release_id, content_id, revision) VALUES ($1, 'built-immutable', $2, 1)`, gameID, contentID); err != nil {
		t.Fatalf("insert built Release entry: %v", err)
	}
	for _, mutation := range []struct {
		name  string
		query string
	}{
		{name: "update", query: `UPDATE release_entries SET revision = 1 WHERE game_id = $1 AND release_id = 'built-immutable'`},
		{name: "delete", query: `DELETE FROM release_entries WHERE game_id = $1 AND release_id = 'built-immutable'`},
		{name: "truncate", query: `TRUNCATE release_entries`},
	} {
		t.Run("release entry "+mutation.name, func(t *testing.T) {
			var err error
			if mutation.name == "truncate" {
				_, err = fixture.db.ExecContext(fixture.ctx, mutation.query)
			} else {
				_, err = fixture.db.ExecContext(fixture.ctx, mutation.query, gameID)
			}
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Fatalf("release entry %s error = %v, want check violation", mutation.name, err)
			}
		})
	}
}

func TestGameBuildProfileIsTrustedVersionedConfiguration(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	gameID := randomTestIdentifier(t, "build_profile_")
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO games (game_id, display_name) VALUES ($1, 'Build profile test')`, gameID); err != nil {
		t.Fatalf("insert profile Game: %v", err)
	}
	manifestHash := bytes.Repeat([]byte{0x7a}, 32)
	if _, err := fixture.db.ExecContext(fixture.ctx, `
INSERT INTO game_build_profiles (game_id, content_schema_version, instance_schema_version,
    ruleset_version, protocol_version, client_min_build, asset_manifest_sha256)
VALUES ($1, 1, 2, 3, 1, '1.0.0', $2)`, gameID, manifestHash); err != nil {
		t.Fatalf("provision trusted Game build profile: %v", err)
	}
	var profileVersion int64
	var storedManifestHash []byte
	if err := fixture.db.QueryRowContext(fixture.ctx, `
SELECT profile_version, asset_manifest_sha256 FROM game_build_profiles WHERE game_id = $1`, gameID).Scan(&profileVersion, &storedManifestHash); err != nil {
		t.Fatalf("read provisioned build profile: %v", err)
	}
	if profileVersion != 1 || !bytes.Equal(storedManifestHash, manifestHash) {
		t.Fatalf("initial profile version/hash = %d/%x", profileVersion, storedManifestHash)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `UPDATE game_build_profiles SET client_min_build = '1.0.1' WHERE game_id = $1`, gameID); err != nil {
		t.Fatalf("update trusted build profile: %v", err)
	}
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT profile_version FROM game_build_profiles WHERE game_id = $1`, gameID).Scan(&profileVersion); err != nil {
		t.Fatalf("read updated profile version: %v", err)
	}
	if profileVersion != 2 {
		t.Fatalf("profile_version after trusted change = %d, want 2", profileVersion)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `
INSERT INTO game_build_profiles (game_id, content_schema_version, instance_schema_version,
    ruleset_version, protocol_version, client_min_build, asset_manifest_sha256)
VALUES ($1, 0, 1, 1, 1, '1.0.0', $2)`, gameID, manifestHash); err == nil {
		t.Fatal("profile accepted a nonpositive content schema version")
	}
}

func TestReleaseVersionCounterOnlyMovesForward(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	gameID := randomTestIdentifier(t, "build_counter_")
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO games (game_id, display_name) VALUES ($1, 'Build counter test')`, gameID); err != nil {
		t.Fatalf("insert counter Game: %v", err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO release_version_counters (game_id, next_release_version) VALUES ($1, 5)`, gameID); err != nil {
		t.Fatalf("insert release version counter: %v", err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `UPDATE release_version_counters SET next_release_version = 6 WHERE game_id = $1`, gameID); err != nil {
		t.Fatalf("advance release version counter: %v", err)
	}
	_, err := fixture.db.ExecContext(fixture.ctx, `UPDATE release_version_counters SET next_release_version = 4 WHERE game_id = $1`, gameID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("regress release version counter error = %v, want check violation", err)
	}
}

func TestBuildWorkerClaimsWithLeaseTokenWithoutDirectTableAccess(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	gameID := randomTestIdentifier(t, "build_worker_")
	adminID := "00000000-0000-4000-8000-000000000031"
	contentID := "demo:item/worker-claim"
	workerID := "00000000-0000-4000-8000-000000000032"
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO games (game_id, display_name) VALUES ($1, 'Worker claim test')`, gameID); err != nil {
		t.Fatalf("insert worker Game: %v", err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO admin_users (admin_id, username, password_hash) VALUES ($1, $2, 'test-hash')`, adminID, gameID+"-worker-admin"); err != nil {
		t.Fatalf("insert worker job requester: %v", err)
	}
	definition := fmt.Appendf(nil, `{"game_id":%q,"content_id":%q,"revision":1,"schema_version":1,"public":{"game_id":%q,"content_id":%q,"revision":1,"schema_version":1}}`, gameID, contentID, gameID, contentID)
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO content_revisions (game_id, content_id, revision, schema_version, content_type, body) VALUES ($1, $2, 1, 1, 'item', $3::jsonb)`, gameID, contentID, definition); err != nil {
		t.Fatalf("insert worker immutable revision: %v", err)
	}
	zeroHash := bytes.Repeat([]byte{0}, 32)
	if _, err := fixture.db.ExecContext(fixture.ctx, `
INSERT INTO admin_build_jobs (game_id, requested_by, idempotency_key, request_fingerprint,
    content_id, revision, body_hash, release_id, release_version, input_snapshot,
    status, attempt_count, claim_token, claim_owner, lease_until)
VALUES ($1, $2, 'worker-claim-key01', $3, $4, 1, $5, 'worker-claim-release', 1, '{}'::jsonb,
    'running', 1, gen_random_uuid(), '00000000-0000-4000-8000-000000000099', now() - interval '1 second')`, gameID, adminID, zeroHash, contentID, zeroHash); err != nil {
		t.Fatalf("insert queued worker job: %v", err)
	}
	workerRepository, err := openBuildWorkerRepository(fixture.ctx, fixture.workerDSN, true)
	if err != nil {
		t.Fatalf("open restricted build worker repository: %v", err)
	}
	t.Cleanup(func() {
		if err := workerRepository.Close(); err != nil {
			t.Errorf("close build worker repository: %v", err)
		}
	})
	claim, found, err := workerRepository.Claim(fixture.ctx, workerID, 30)
	if err != nil || !found {
		t.Fatalf("claim expired queued job as worker = found %t, err %v", found, err)
	}
	if claim.Job.Status != admin.BuildStatusRunning || len(claim.InputSnapshot) == 0 || claim.ClaimToken == "" || !claim.LeaseUntil.After(time.Now()) {
		t.Fatalf("worker claim state is incomplete: status=%q lease=%t input=%t token=%t", claim.Job.Status, claim.LeaseUntil.After(time.Now()), len(claim.InputSnapshot) != 0, claim.ClaimToken != "")
	}
	var attemptCount int
	var claimedStatus string
	var claimedOwner string
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT attempt_count, status, claim_owner::text FROM admin_build_jobs WHERE job_id = $1::uuid`, claim.Job.JobID).Scan(&attemptCount, &claimedStatus, &claimedOwner); err != nil {
		t.Fatalf("inspect expired lease recovery state: %v", err)
	}
	if attemptCount != 2 || claimedStatus != "running" || claimedOwner != workerID {
		t.Fatalf("expired claim recovery attempt/status/owner = %d/%s/%s", attemptCount, claimedStatus, claimedOwner)
	}
	recovered, found, err := workerRepository.Claim(fixture.ctx, workerID, 30)
	if err != nil || !found || recovered.Job.JobID != claim.Job.JobID || recovered.ClaimToken != claim.ClaimToken {
		t.Fatalf("worker claim recovery = %#v/%t/%v, want same job/token", recovered, found, err)
	}
	if renewed, err := workerRepository.RenewLease(fixture.ctx, claim.Job.JobID, "00000000-0000-4000-8000-000000000099", claim.ClaimToken, 30); err != nil || renewed {
		t.Fatalf("renew claim with different worker ID = %t, err=%v, want false", renewed, err)
	}
	if renewed, err := workerRepository.RenewLease(fixture.ctx, claim.Job.JobID, workerID, claim.ClaimToken, 30); err != nil || !renewed {
		t.Fatalf("renew active worker lease = %t, err=%v", renewed, err)
	}
	var leaseBefore, leaseAfter time.Time
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT lease_until FROM admin_build_jobs WHERE job_id = $1::uuid`, claim.Job.JobID).Scan(&leaseBefore); err != nil {
		t.Fatalf("read lease before shorter renewal: %v", err)
	}
	if renewed, err := workerRepository.RenewLease(fixture.ctx, claim.Job.JobID, workerID, claim.ClaimToken, 5); err != nil || !renewed {
		t.Fatalf("shorter renewal of a held lease = %t, err=%v, want true without shrinking", renewed, err)
	}
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT lease_until FROM admin_build_jobs WHERE job_id = $1::uuid`, claim.Job.JobID).Scan(&leaseAfter); err != nil {
		t.Fatalf("read lease after shorter renewal: %v", err)
	}
	if !leaseAfter.Equal(leaseBefore) {
		t.Fatalf("shorter renewal changed lease from %v to %v", leaseBefore, leaseAfter)
	}
	if renewed, err := workerRepository.RenewLease(fixture.ctx, claim.Job.JobID, workerID, "00000000-0000-4000-8000-000000000099", 30); err != nil || renewed {
		t.Fatalf("renew with stale claim token = %t, err=%v, want false", renewed, err)
	}
	var directJobID string
	if err := workerRepository.db.QueryRowContext(fixture.ctx, `SELECT job_id::text FROM admin_build_jobs LIMIT 1`).Scan(&directJobID); err == nil {
		t.Fatal("worker role read build job table directly instead of using the claim function")
	}
}

func TestBuildJobFrozenInputAndTerminalStateAreGuarded(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	gameID := randomTestIdentifier(t, "build_job_")
	adminID := "00000000-0000-4000-8000-000000000001"
	contentID := "demo:item/build-test"
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO games (game_id, display_name) VALUES ($1, 'Build job test')`, gameID); err != nil {
		t.Fatalf("insert job Game: %v", err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO admin_users (admin_id, username, password_hash) VALUES ($1, $2, 'test-hash')`, adminID, gameID+"-admin"); err != nil {
		t.Fatalf("insert job requester: %v", err)
	}
	definition := fmt.Appendf(nil, `{"game_id":%q,"content_id":%q,"revision":1,"schema_version":1,"public":{"game_id":%q,"content_id":%q,"revision":1,"schema_version":1}}`, gameID, contentID, gameID, contentID)
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO content_revisions (game_id, content_id, revision, schema_version, content_type, body) VALUES ($1, $2, 1, 1, 'item', $3::jsonb)`, gameID, contentID, definition); err != nil {
		t.Fatalf("insert immutable test revision: %v", err)
	}
	zeroHash := bytes.Repeat([]byte{0}, 32)
	if _, err := fixture.db.ExecContext(fixture.ctx, `
INSERT INTO admin_build_jobs (game_id, requested_by, idempotency_key, request_fingerprint,
    content_id, revision, body_hash, release_id, release_version, input_snapshot)
VALUES ($1, $2, '0123456789abcdef', $3, $4, 1, $5, 'build-test-release', 1, '{}'::jsonb)`, gameID, adminID, zeroHash, contentID, zeroHash); err != nil {
		t.Fatalf("insert queued test build job: %v", err)
	}
	assertBuildJobCheckViolation(t, fixture, `UPDATE admin_build_jobs SET input_snapshot = '{"tampered":true}'::jsonb WHERE game_id = $1`, gameID)
	assertBuildJobCheckViolation(t, fixture, `UPDATE admin_build_jobs SET status = 'built' WHERE game_id = $1`, gameID)
	if _, err := fixture.db.ExecContext(fixture.ctx, `UPDATE admin_build_jobs SET status = 'running', attempt_count = 1, claim_token = gen_random_uuid(), claim_owner = gen_random_uuid(), lease_until = now() + interval '30 seconds' WHERE game_id = $1`, gameID); err != nil {
		t.Fatalf("claim queued build job: %v", err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `UPDATE admin_build_jobs SET status = 'built', claim_token = NULL, claim_owner = NULL, lease_until = NULL, manifest = '{}'::jsonb, public_artifact_sha256 = decode(repeat('11', 32), 'hex'), private_artifact_sha256 = decode(repeat('22', 32), 'hex') WHERE game_id = $1`, gameID); err != nil {
		t.Fatalf("mark complete build job: %v", err)
	}
	assertBuildJobCheckViolation(t, fixture, `UPDATE admin_build_jobs SET public_artifact_sha256 = decode(repeat('33', 32), 'hex') WHERE game_id = $1`, gameID)
	assertBuildJobCheckViolation(t, fixture, `UPDATE admin_build_jobs SET status = 'failed', error_code = 'late_failure' WHERE game_id = $1`, gameID)
}

func assertBuildJobCheckViolation(t *testing.T, fixture *adminACLFixture, query string, gameID string) {
	t.Helper()
	_, err := fixture.db.ExecContext(fixture.ctx, query, gameID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("build job mutation error = %v, want check violation", err)
	}
}

func TestBuildProfileAndJobsAreProvisionedOutsideControlPlane(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	var profileTable, jobsTable, countersTable bool
	if err := fixture.db.QueryRowContext(fixture.ctx, `
SELECT to_regclass('public.game_build_profiles') IS NOT NULL,
       to_regclass('public.admin_build_jobs') IS NOT NULL,
       to_regclass('public.release_version_counters') IS NOT NULL`).Scan(&profileTable, &jobsTable, &countersTable); err != nil {
		t.Fatalf("inspect task 0008 build schema: %v", err)
	}
	if !profileTable || !jobsTable || !countersTable {
		t.Fatalf("build tables profile/jobs/counters = %t/%t/%t", profileTable, jobsTable, countersTable)
	}

	repository, err := openAdminRepository(fixture.ctx, fixture.appDSN, true)
	if err != nil {
		t.Fatalf("open restricted control-plane repository: %v", err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close restricted control-plane repository: %v", err)
		}
	})
	for _, table := range []string{"game_build_profiles", "admin_build_jobs", "release_version_counters"} {
		var canSelect, canInsert, canUpdate, canDelete bool
		if err := repository.db.QueryRowContext(fixture.ctx, `
SELECT has_any_column_privilege(current_user, 'public.' || $1, 'SELECT'),
       has_any_column_privilege(current_user, 'public.' || $1, 'INSERT'),
       has_any_column_privilege(current_user, 'public.' || $1, 'UPDATE'),
       has_table_privilege(current_user, 'public.' || $1, 'DELETE')`, table).Scan(&canSelect, &canInsert, &canUpdate, &canDelete); err != nil {
			t.Fatalf("inspect control-plane access to build table %q: %v", table, err)
		}
		if canSelect || canInsert || canUpdate || canDelete {
			t.Errorf("control-plane role has direct table privileges on %s: SELECT/INSERT/UPDATE/DELETE=%t/%t/%t/%t", table, canSelect, canInsert, canUpdate, canDelete)
		}
	}
	workerConn, err := fixture.db.Conn(fixture.ctx)
	if err != nil {
		t.Fatalf("get dedicated build-worker ACL connection: %v", err)
	}
	t.Cleanup(func() {
		if err := workerConn.Close(); err != nil {
			t.Errorf("close build-worker ACL connection: %v", err)
		}
	})
	if _, err := workerConn.ExecContext(fixture.ctx, `SET ROLE tbcg_content_build_worker`); err != nil {
		t.Fatalf("assume restricted build-worker role: %v", err)
	}
	for _, table := range []string{"game_build_profiles", "admin_build_jobs", "release_version_counters", "releases", "release_entries"} {
		var canSelect, canInsert, canUpdate, canDelete bool
		if err := workerConn.QueryRowContext(fixture.ctx, `
SELECT has_any_column_privilege(current_user, 'public.' || $1, 'SELECT'),
       has_any_column_privilege(current_user, 'public.' || $1, 'INSERT'),
       has_any_column_privilege(current_user, 'public.' || $1, 'UPDATE'),
       has_table_privilege(current_user, 'public.' || $1, 'DELETE')`, table).Scan(&canSelect, &canInsert, &canUpdate, &canDelete); err != nil {
			t.Fatalf("inspect worker direct access to table %q: %v", table, err)
		}
		if canSelect || canInsert || canUpdate || canDelete {
			t.Errorf("worker role has direct table privileges on %s: SELECT/INSERT/UPDATE/DELETE=%t/%t/%t/%t", table, canSelect, canInsert, canUpdate, canDelete)
		}
	}
}

func TestBuildMigrationRejectsLoginMemberOfBuildOwner(t *testing.T) {
	fixture := newAdminACLFixture(t, "3")
	appRole := pgx.Identifier{fixture.roles.appRole}.Sanitize()
	execACLStatement(t, fixture.ctx, fixture.db, "GRANT tbcg_content_build_owner TO "+appRole)
	t.Cleanup(func() {
		cleanupACLStatement(t, fixture.db, "REVOKE tbcg_content_build_owner FROM "+appRole)
	})
	output, err := fixture.migrationResult("4")
	if err == nil || !bytes.Contains(output, []byte("tbcg_content_build_owner must not have members other than the migration executor")) {
		t.Fatal("004 accepted a login role that can SET ROLE to the build function owner")
	}
}

func TestBuildRolesRejectCrossRoleLoginMembership(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	appRole := pgx.Identifier{fixture.roles.appRole}.Sanitize()
	workerRole := pgx.Identifier{fixture.roles.workerRole}.Sanitize()
	for _, groupRole := range []string{"tbcg_content_build_owner", "tbcg_content_build_worker"} {
		t.Run("admin/"+groupRole, func(t *testing.T) {
			execACLStatement(t, fixture.ctx, fixture.db, "GRANT "+groupRole+" TO "+appRole)
			t.Cleanup(func() { cleanupACLStatement(t, fixture.db, "REVOKE "+groupRole+" FROM "+appRole) })
			assertAdminStartupRejected(t, fixture)
		})
	}
	for _, groupRole := range []string{"tbcg_content_build_owner", "tbcg_admin_control_plane"} {
		t.Run("worker/"+groupRole, func(t *testing.T) {
			execACLStatement(t, fixture.ctx, fixture.db, "GRANT "+groupRole+" TO "+workerRole)
			t.Cleanup(func() { cleanupACLStatement(t, fixture.db, "REVOKE "+groupRole+" FROM "+workerRole) })
			repository, err := openBuildWorkerRepository(fixture.ctx, fixture.workerDSN, true)
			if err == nil {
				_ = repository.Close()
				t.Fatalf("build worker repository accepted a login that is also a member of %s", groupRole)
			}
		})
	}
	assertAdminStartupAllowed(t, fixture)
	repository, err := openBuildWorkerRepository(fixture.ctx, fixture.workerDSN, true)
	if err != nil {
		t.Fatalf("restricted build worker repository rejected a correctly provisioned login: %v", err)
	}
	if err := repository.Close(); err != nil {
		t.Errorf("close build worker repository: %v", err)
	}
}

func TestReleaseEntryInsertSerializesWithActivation(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	gameID := randomTestIdentifier(t, "entry_activation_")
	contentID := "demo:item/entry-activation"
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO games (game_id, display_name) VALUES ($1, 'Entry activation race')`, gameID); err != nil {
		t.Fatalf("insert race Game: %v", err)
	}
	definition := fmt.Appendf(nil, `{"game_id":%q,"content_id":%q,"revision":1,"schema_version":1,"public":{"game_id":%q,"content_id":%q,"revision":1,"schema_version":1}}`, gameID, contentID, gameID, contentID)
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO content_revisions (game_id, content_id, revision, schema_version, content_type, body) VALUES ($1, $2, 1, 1, 'item', $3::jsonb)`, gameID, contentID, definition); err != nil {
		t.Fatalf("insert race revision: %v", err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO releases (game_id, release_id, release_version, content_schema_version, instance_schema_version, ruleset_version, protocol_version, client_min_build, status, manifest) VALUES ($1, 'race-built', 1, 1, 1, 1, 1, '1.0.0', 'built', '{}'::jsonb)`, gameID); err != nil {
		t.Fatalf("insert race built Release: %v", err)
	}
	entryTx, err := fixture.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatalf("begin entry transaction: %v", err)
	}
	t.Cleanup(func() { _ = entryTx.Rollback() })
	if _, err := entryTx.ExecContext(fixture.ctx, `INSERT INTO release_entries (game_id, release_id, content_id, revision) VALUES ($1, 'race-built', $2, 1)`, gameID, contentID); err != nil {
		t.Fatalf("insert entry while Release is built: %v", err)
	}
	activationCtx, cancel := context.WithTimeout(fixture.ctx, 10*time.Second)
	defer cancel()
	activated := make(chan error, 1)
	go func() {
		_, activateErr := fixture.db.ExecContext(activationCtx, `UPDATE releases SET status = 'activated', activated_at = now() WHERE game_id = $1 AND release_id = 'race-built' /* entry-activation-race */`, gameID)
		activated <- activateErr
	}()
	waitForLockedStatement(t, fixture, activationCtx, "entry-activation-race")
	if err := entryTx.Commit(); err != nil {
		t.Fatalf("commit Release entry before activation: %v", err)
	}
	if err := <-activated; err != nil {
		t.Fatalf("activation after serialized entry insert: %v", err)
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO release_entries (game_id, release_id, content_id, revision) VALUES ($1, 'race-built', 'demo:item/late', 1)`, gameID); err == nil {
		t.Fatal("entry was added after the Release was activated")
	}
}

func waitForLockedStatement(t *testing.T, fixture *adminACLFixture, ctx context.Context, marker string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		if err := fixture.db.QueryRowContext(ctx, `
SELECT count(*) FROM pg_stat_activity
WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%' || $1 || '%'`, marker).Scan(&waiting); err != nil {
			t.Fatalf("inspect lock wait for %s: %v", marker, err)
		}
		if waiting > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("statement %s never waited on the expected row lock: %v", marker, ctx.Err())
		case <-ticker.C:
		}
	}
}
