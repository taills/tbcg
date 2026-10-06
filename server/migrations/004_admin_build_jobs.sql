CREATE TABLE game_build_profiles (
    game_id text PRIMARY KEY,
    content_schema_version integer NOT NULL,
    instance_schema_version integer NOT NULL,
    ruleset_version integer NOT NULL,
    protocol_version integer NOT NULL,
    client_min_build text NOT NULL,
    asset_manifest_sha256 bytea NOT NULL,
    profile_version bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT game_build_profiles_game_fk FOREIGN KEY (game_id) REFERENCES games (game_id) ON DELETE CASCADE,
    CONSTRAINT game_build_profiles_content_schema_positive CHECK (content_schema_version > 0),
    CONSTRAINT game_build_profiles_instance_schema_positive CHECK (instance_schema_version > 0),
    CONSTRAINT game_build_profiles_ruleset_positive CHECK (ruleset_version > 0),
    CONSTRAINT game_build_profiles_protocol_positive CHECK (protocol_version > 0),
    CONSTRAINT game_build_profiles_client_build_nonblank CHECK (length(btrim(client_min_build)) > 0),
    CONSTRAINT game_build_profiles_manifest_hash_length CHECK (octet_length(asset_manifest_sha256) = 32),
    CONSTRAINT game_build_profiles_version_positive CHECK (profile_version > 0)
);

CREATE TABLE release_version_counters (
    game_id text PRIMARY KEY,
    next_release_version bigint NOT NULL,
    CONSTRAINT release_version_counters_game_fk FOREIGN KEY (game_id) REFERENCES games (game_id) ON DELETE CASCADE,
    CONSTRAINT release_version_counters_positive CHECK (next_release_version > 0)
);

CREATE TABLE admin_build_jobs (
    job_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id text NOT NULL,
    requested_by uuid NOT NULL,
    idempotency_key text NOT NULL,
    request_fingerprint bytea NOT NULL,
    content_id text NOT NULL,
    revision integer NOT NULL,
    body_hash bytea NOT NULL,
    release_id text NOT NULL,
    release_version integer NOT NULL,
    input_snapshot jsonb NOT NULL,
    status text NOT NULL DEFAULT 'queued',
    attempt_count integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 5,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    claim_token uuid,
    claim_owner uuid,
    lease_until timestamptz,
    manifest jsonb,
    public_artifact_sha256 bytea,
    private_artifact_sha256 bytea,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT admin_build_jobs_scope_fk FOREIGN KEY (game_id) REFERENCES games (game_id),
    CONSTRAINT admin_build_jobs_requester_fk FOREIGN KEY (requested_by) REFERENCES admin_users (admin_id),
    CONSTRAINT admin_build_jobs_revision_fk FOREIGN KEY (game_id, content_id, revision)
        REFERENCES content_revisions (game_id, content_id, revision),
    CONSTRAINT admin_build_jobs_identity_unique UNIQUE (game_id, release_id),
    CONSTRAINT admin_build_jobs_version_unique UNIQUE (game_id, release_version),
    CONSTRAINT admin_build_jobs_idempotency_unique UNIQUE (game_id, requested_by, idempotency_key),
    CONSTRAINT admin_build_jobs_game_id_format CHECK (game_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    CONSTRAINT admin_build_jobs_content_id_format CHECK (length(content_id) BETWEEN 1 AND 128 AND content_id ~ '^[a-z0-9][a-z0-9._:/-]*$'),
    CONSTRAINT admin_build_jobs_release_id_format CHECK (release_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    CONSTRAINT admin_build_jobs_key_format CHECK (length(idempotency_key) BETWEEN 16 AND 128 AND idempotency_key ~ '^[A-Za-z0-9._~-]+$'),
    CONSTRAINT admin_build_jobs_request_hash_length CHECK (octet_length(request_fingerprint) = 32),
    CONSTRAINT admin_build_jobs_body_hash_length CHECK (octet_length(body_hash) = 32),
    CONSTRAINT admin_build_jobs_revision_positive CHECK (revision > 0),
    CONSTRAINT admin_build_jobs_release_version_positive CHECK (release_version > 0),
    CONSTRAINT admin_build_jobs_snapshot_object CHECK (jsonb_typeof(input_snapshot) = 'object' AND pg_column_size(input_snapshot) <= 8388608),
    CONSTRAINT admin_build_jobs_status_valid CHECK (status IN ('queued', 'running', 'retry_wait', 'built', 'failed')),
    CONSTRAINT admin_build_jobs_attempt_range CHECK (attempt_count >= 0 AND max_attempts BETWEEN 1 AND 10 AND attempt_count <= max_attempts),
    CONSTRAINT admin_build_jobs_claim_state_valid CHECK (
        (status = 'running' AND claim_token IS NOT NULL AND claim_owner IS NOT NULL AND lease_until IS NOT NULL)
        OR (status <> 'running' AND claim_token IS NULL AND claim_owner IS NULL AND lease_until IS NULL)
    ),
    CONSTRAINT admin_build_jobs_built_artifacts_valid CHECK (
        (status = 'built' AND manifest IS NOT NULL AND jsonb_typeof(manifest) = 'object'
            AND public_artifact_sha256 IS NOT NULL AND octet_length(public_artifact_sha256) = 32
            AND private_artifact_sha256 IS NOT NULL AND octet_length(private_artifact_sha256) = 32 AND error_code IS NULL)
        OR (status <> 'built' AND manifest IS NULL AND public_artifact_sha256 IS NULL AND private_artifact_sha256 IS NULL)
    ),
    CONSTRAINT admin_build_jobs_error_code_nonblank CHECK (error_code IS NULL OR length(btrim(error_code)) BETWEEN 1 AND 64),
    CONSTRAINT admin_build_jobs_failed_has_error CHECK (status <> 'failed' OR error_code IS NOT NULL)
);

CREATE INDEX admin_build_jobs_claim_idx ON admin_build_jobs (next_attempt_at, created_at, job_id)
    WHERE status IN ('queued', 'retry_wait');
CREATE INDEX admin_build_jobs_game_created_idx ON admin_build_jobs (game_id, created_at DESC, job_id);
CREATE INDEX admin_build_jobs_requested_by_idx ON admin_build_jobs (requested_by);
CREATE INDEX admin_build_jobs_active_game_idx ON admin_build_jobs (game_id, content_id, revision)
    WHERE status IN ('queued', 'running', 'retry_wait');
CREATE INDEX admin_build_jobs_expired_lease_idx ON admin_build_jobs (lease_until, job_id) WHERE status = 'running';
CREATE UNIQUE INDEX admin_build_jobs_one_claim_per_worker_idx ON admin_build_jobs (claim_owner) WHERE status = 'running';

CREATE FUNCTION public.bump_game_build_profile_version()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.game_id IS DISTINCT FROM OLD.game_id THEN
        RAISE EXCEPTION 'game build profile Game identity is immutable'
            USING ERRCODE = '23514';
    END IF;
    NEW.profile_version := OLD.profile_version + 1;
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER game_build_profiles_version_increases
BEFORE UPDATE ON game_build_profiles
FOR EACH ROW
EXECUTE FUNCTION public.bump_game_build_profile_version();

CREATE FUNCTION public.prevent_release_version_counter_regression()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.game_id IS DISTINCT FROM OLD.game_id OR NEW.next_release_version <= OLD.next_release_version THEN
        RAISE EXCEPTION 'release version counter must preserve Game identity and increase monotonically'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER release_version_counters_monotonic
BEFORE UPDATE ON release_version_counters
FOR EACH ROW
EXECUTE FUNCTION public.prevent_release_version_counter_regression();

CREATE FUNCTION public.prevent_admin_build_job_input_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status IN ('built', 'failed') AND (to_jsonb(NEW) - 'updated_at') IS DISTINCT FROM (to_jsonb(OLD) - 'updated_at') THEN
        RAISE EXCEPTION 'built and failed build jobs are immutable terminal states'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.job_id IS DISTINCT FROM OLD.job_id
       OR NEW.game_id IS DISTINCT FROM OLD.game_id
       OR NEW.requested_by IS DISTINCT FROM OLD.requested_by
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.request_fingerprint IS DISTINCT FROM OLD.request_fingerprint
       OR NEW.content_id IS DISTINCT FROM OLD.content_id
       OR NEW.revision IS DISTINCT FROM OLD.revision
       OR NEW.body_hash IS DISTINCT FROM OLD.body_hash
       OR NEW.release_id IS DISTINCT FROM OLD.release_id
       OR NEW.release_version IS DISTINCT FROM OLD.release_version
       OR NEW.input_snapshot IS DISTINCT FROM OLD.input_snapshot
       OR NEW.max_attempts IS DISTINCT FROM OLD.max_attempts
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'admin build job identity and frozen input are immutable'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
        (OLD.status = 'queued' AND NEW.status IN ('running', 'failed'))
        OR (OLD.status = 'retry_wait' AND NEW.status IN ('running', 'failed'))
        OR (OLD.status = 'running' AND NEW.status IN ('retry_wait', 'built', 'failed'))
    ) THEN
        RAISE EXCEPTION 'invalid admin build job status transition from % to %', OLD.status, NEW.status
            USING ERRCODE = '23514';
    END IF;
    IF OLD.status IN ('queued', 'retry_wait') AND NEW.status = 'running' THEN
        IF NEW.attempt_count <> OLD.attempt_count + 1 OR NEW.claim_token IS NULL OR NEW.claim_owner IS NULL OR NEW.lease_until <= now() OR NEW.error_code IS NOT NULL THEN
            RAISE EXCEPTION 'claiming a build job requires a fresh token, lease, and single attempt increment'
                USING ERRCODE = '23514';
        END IF;
    ELSIF OLD.status = 'running' AND NEW.status = 'running' THEN
        IF NEW.attempt_count <> OLD.attempt_count OR NEW.claim_token IS DISTINCT FROM OLD.claim_token
           OR NEW.claim_owner IS DISTINCT FROM OLD.claim_owner OR NEW.next_attempt_at IS DISTINCT FROM OLD.next_attempt_at OR NEW.lease_until <= OLD.lease_until OR NEW.lease_until <= now()
           OR NEW.manifest IS DISTINCT FROM OLD.manifest OR NEW.public_artifact_sha256 IS DISTINCT FROM OLD.public_artifact_sha256
           OR NEW.private_artifact_sha256 IS DISTINCT FROM OLD.private_artifact_sha256 OR NEW.error_code IS DISTINCT FROM OLD.error_code THEN
            RAISE EXCEPTION 'build job lease renewal may only extend the current claim lease'
                USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.status IS NOT DISTINCT FROM OLD.status AND OLD.status <> 'running'
       AND (to_jsonb(NEW) - 'updated_at') IS DISTINCT FROM (to_jsonb(OLD) - 'updated_at') THEN
        RAISE EXCEPTION 'non-running build jobs cannot be modified without a state transition'
            USING ERRCODE = '23514';
    ELSIF OLD.status = 'running' AND NEW.status IN ('retry_wait', 'built', 'failed') THEN
        IF NEW.attempt_count <> OLD.attempt_count OR NEW.claim_token IS NOT NULL OR NEW.claim_owner IS NOT NULL OR NEW.lease_until IS NOT NULL THEN
            RAISE EXCEPTION 'finishing a claimed build job must retain its attempt and release its lease'
                USING ERRCODE = '23514';
        END IF;
    END IF;
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER admin_build_jobs_identity_is_immutable
BEFORE UPDATE ON admin_build_jobs
FOR EACH ROW
EXECUTE FUNCTION public.prevent_admin_build_job_input_mutation();

CREATE FUNCTION public.restrict_build_owner_release_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF current_user = 'tbcg_content_build_owner'
       AND (NEW.status <> 'built' OR NEW.activated_at IS NOT NULL) THEN
        RAISE EXCEPTION 'content build owner may only register an unactivated built Release'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER releases_build_owner_must_register_built_only
BEFORE INSERT ON releases
FOR EACH ROW
EXECUTE FUNCTION public.restrict_build_owner_release_insert();

CREATE FUNCTION public.guard_release_entry_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    release_status text;
    release_activated_at timestamptz;
BEGIN
    IF current_user = 'tbcg_content_build_owner' THEN
        -- The owner only registers entries for the uncommitted Release it inserted in the same transaction.
        SELECT status, activated_at INTO release_status, release_activated_at
        FROM public.releases WHERE game_id = NEW.game_id AND release_id = NEW.release_id;
    ELSE
        -- Serialize with activation: FOR SHARE blocks a concurrent status/activated_at UPDATE until commit.
        SELECT status, activated_at INTO release_status, release_activated_at
        FROM public.releases WHERE game_id = NEW.game_id AND release_id = NEW.release_id FOR SHARE;
    END IF;
    IF NOT FOUND OR release_status <> 'built' OR release_activated_at IS NOT NULL THEN
        RAISE EXCEPTION 'Release entries may only be added to a complete unactivated built Release'
            USING ERRCODE = '23514';
    END IF;
    IF current_user = 'tbcg_content_build_owner' AND EXISTS (
        SELECT 1 FROM public.release_entries
        WHERE game_id = NEW.game_id AND release_id = NEW.release_id
    ) THEN
        RAISE EXCEPTION 'single-item build may register exactly one Release entry'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER release_entries_only_extend_built_release
BEFORE INSERT ON release_entries
FOR EACH ROW
EXECUTE FUNCTION public.guard_release_entry_insert();

CREATE FUNCTION public.prevent_release_entry_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'release entries are immutable'
        USING ERRCODE = '23514';
END;
$$;

CREATE TRIGGER release_entries_immutable_rows
BEFORE UPDATE OR DELETE ON release_entries
FOR EACH ROW
EXECUTE FUNCTION public.prevent_release_entry_mutation();

CREATE TRIGGER release_entries_no_truncate
BEFORE TRUNCATE ON release_entries
FOR EACH STATEMENT
EXECUTE FUNCTION public.prevent_release_entry_mutation();

-- The control plane has no direct access to build profiles, counters, or job snapshots.
-- Dedicated function-owner code will expose only the state transitions added by the build phase.
DO $$
DECLARE
    role_info record;
    owner_oid oid;
    worker_oid oid;
    admin_oid oid;
BEGIN
    SELECT oid INTO owner_oid FROM pg_roles WHERE rolname = 'tbcg_content_build_owner';
    IF owner_oid IS NULL THEN
        RAISE EXCEPTION 'tbcg_content_build_owner must be pre-provisioned outside this migration';
    END IF;
    SELECT oid INTO worker_oid FROM pg_roles WHERE rolname = 'tbcg_content_build_worker';
    IF worker_oid IS NULL THEN
        RAISE EXCEPTION 'tbcg_content_build_worker must be pre-provisioned outside this migration';
    END IF;
    SELECT oid INTO admin_oid FROM pg_roles WHERE rolname = 'tbcg_admin_control_plane';
    IF admin_oid IS NULL THEN
        RAISE EXCEPTION 'tbcg_admin_control_plane must be pre-provisioned outside this migration';
    END IF;
    SELECT rolcanlogin, rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls
      INTO role_info FROM pg_roles WHERE oid = owner_oid;
    IF role_info.rolcanlogin OR role_info.rolsuper OR role_info.rolcreatedb OR role_info.rolcreaterole OR role_info.rolreplication OR role_info.rolbypassrls THEN
        RAISE EXCEPTION 'tbcg_content_build_owner must be a restricted NOLOGIN role';
    END IF;
    SELECT rolcanlogin, rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls
      INTO role_info FROM pg_roles WHERE oid = worker_oid;
    IF role_info.rolcanlogin OR role_info.rolsuper OR role_info.rolcreatedb OR role_info.rolcreaterole OR role_info.rolreplication OR role_info.rolbypassrls THEN
        RAISE EXCEPTION 'tbcg_content_build_worker must be a restricted NOLOGIN role';
    END IF;
    IF pg_has_role(admin_oid, owner_oid, 'MEMBER') OR pg_has_role(admin_oid, worker_oid, 'MEMBER')
       OR pg_has_role(worker_oid, owner_oid, 'MEMBER') THEN
        RAISE EXCEPTION 'admin and worker roles must not inherit build function owner privileges';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_auth_members m
        WHERE m.roleid = owner_oid
          AND m.member <> (SELECT oid FROM pg_roles WHERE rolname = current_user)
    ) THEN
        RAISE EXCEPTION 'tbcg_content_build_owner must not have members other than the migration executor';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_roles r
        WHERE NOT r.rolsuper
          AND r.oid NOT IN (owner_oid, worker_oid, admin_oid)
          AND pg_has_role(r.oid, worker_oid, 'MEMBER')
          AND (pg_has_role(r.oid, admin_oid, 'MEMBER') OR pg_has_role(r.oid, owner_oid, 'MEMBER')
            OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
    ) THEN
        RAISE EXCEPTION 'build worker members must not also hold control-plane, owner, or administrative roles';
    END IF;
    IF has_any_column_privilege(admin_oid, 'public.release_entries', 'INSERT')
       OR has_any_column_privilege(admin_oid, 'public.release_entries', 'UPDATE')
       OR has_table_privilege(admin_oid, 'public.release_entries', 'DELETE')
       OR has_table_privilege(admin_oid, 'public.release_entries', 'TRUNCATE') THEN
        RAISE EXCEPTION 'tbcg_admin_control_plane has pre-existing Release entry write privileges';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_class c
        WHERE c.relnamespace = 'public'::regnamespace
          AND c.relname = ANY(ARRAY['game_build_profiles','release_version_counters','admin_build_jobs'])
          AND (has_any_column_privilege(admin_oid, c.oid, 'SELECT')
            OR has_any_column_privilege(admin_oid, c.oid, 'INSERT')
            OR has_any_column_privilege(admin_oid, c.oid, 'UPDATE')
            OR has_table_privilege(admin_oid, c.oid, 'DELETE')
            OR has_table_privilege(admin_oid, c.oid, 'TRUNCATE'))
    ) THEN
        RAISE EXCEPTION 'tbcg_admin_control_plane has pre-existing direct or inherited build table privileges';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_class c
        WHERE c.relnamespace = 'public'::regnamespace
          AND c.relname = ANY(ARRAY['games','realms','owner_guards','players','characters','wallets','wallet_ledger','outbox','outbox_consumer_receipts','operation_results','releases','release_entries','content_revisions','revision_approvals','audit_logs','admin_users','admin_sessions','admin_role_bindings','game_build_profiles','release_version_counters','admin_build_jobs'])
          AND (has_any_column_privilege(worker_oid, c.oid, 'SELECT')
            OR has_any_column_privilege(worker_oid, c.oid, 'INSERT')
            OR has_any_column_privilege(worker_oid, c.oid, 'UPDATE')
            OR has_table_privilege(worker_oid, c.oid, 'DELETE')
            OR has_table_privilege(worker_oid, c.oid, 'TRUNCATE'))
    ) THEN
        RAISE EXCEPTION 'tbcg_content_build_worker has pre-existing direct or inherited table privileges';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_roles r
        WHERE (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
          AND (pg_has_role(owner_oid, r.oid, 'MEMBER') OR pg_has_role(worker_oid, r.oid, 'MEMBER'))
    ) THEN
        RAISE EXCEPTION 'build owner and worker roles must not inherit PostgreSQL administrative privileges';
    END IF;
    IF has_database_privilege(owner_oid, current_database(), 'CREATE')
       OR has_schema_privilege(owner_oid, 'public', 'CREATE')
       OR has_database_privilege(worker_oid, current_database(), 'CREATE')
       OR has_schema_privilege(worker_oid, 'public', 'CREATE') THEN
        RAISE EXCEPTION 'build owner and worker roles must not have DDL privileges';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_class c
        WHERE c.relnamespace = 'public'::regnamespace
          AND c.relkind IN ('r', 'p', 'v', 'm', 'S')
          AND (pg_has_role(owner_oid, c.relowner, 'MEMBER') OR pg_has_role(worker_oid, c.relowner, 'MEMBER'))
    ) THEN
        RAISE EXCEPTION 'build owner and worker roles must not assume existing public object owners';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_class c
        WHERE c.relnamespace = 'public'::regnamespace
          AND c.relkind IN ('r', 'p', 'v', 'm')
          AND (has_any_column_privilege(owner_oid, c.oid, 'SELECT')
            OR has_any_column_privilege(owner_oid, c.oid, 'INSERT')
            OR has_any_column_privilege(owner_oid, c.oid, 'UPDATE')
            OR has_table_privilege(owner_oid, c.oid, 'DELETE')
            OR has_table_privilege(owner_oid, c.oid, 'TRUNCATE')
            OR has_any_column_privilege(worker_oid, c.oid, 'SELECT')
            OR has_any_column_privilege(worker_oid, c.oid, 'INSERT')
            OR has_any_column_privilege(worker_oid, c.oid, 'UPDATE')
            OR has_table_privilege(worker_oid, c.oid, 'DELETE')
            OR has_table_privilege(worker_oid, c.oid, 'TRUNCATE'))
    ) THEN
        RAISE EXCEPTION 'build owner and worker roles must have no pre-existing public table privileges';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_class c
        WHERE c.relnamespace = 'public'::regnamespace AND c.relkind = 'S'
          AND (has_sequence_privilege(owner_oid, c.oid, 'USAGE') OR has_sequence_privilege(owner_oid, c.oid, 'SELECT') OR has_sequence_privilege(owner_oid, c.oid, 'UPDATE')
            OR has_sequence_privilege(worker_oid, c.oid, 'USAGE') OR has_sequence_privilege(worker_oid, c.oid, 'SELECT') OR has_sequence_privilege(worker_oid, c.oid, 'UPDATE'))
    ) THEN
        RAISE EXCEPTION 'build owner and worker roles must have no pre-existing public sequence privileges';
    END IF;
END;
$$;

REVOKE ALL ON game_build_profiles, release_version_counters, admin_build_jobs FROM PUBLIC;
REVOKE ALL ON game_build_profiles, release_version_counters, admin_build_jobs FROM tbcg_admin_control_plane, tbcg_content_build_worker;
GRANT SELECT ON game_build_profiles TO tbcg_content_build_owner;
GRANT UPDATE (profile_version) ON game_build_profiles TO tbcg_content_build_owner;
GRANT SELECT, INSERT, UPDATE ON release_version_counters TO tbcg_content_build_owner;
GRANT SELECT, INSERT, UPDATE ON admin_build_jobs TO tbcg_content_build_owner;
GRANT SELECT (game_id) ON games TO tbcg_content_build_owner;
GRANT SELECT (game_id, admin_id, role) ON admin_role_bindings TO tbcg_content_build_owner;
GRANT SELECT ON content_revisions, revision_approvals TO tbcg_content_build_owner;
GRANT SELECT ON releases, release_entries TO tbcg_content_build_owner;
GRANT INSERT (game_id, release_id, release_version, content_schema_version, instance_schema_version, ruleset_version, protocol_version, client_min_build, status, manifest)
    ON releases TO tbcg_content_build_owner;
GRANT INSERT (game_id, release_id, content_id, revision) ON release_entries TO tbcg_content_build_owner;
GRANT INSERT (game_id, actor_id, action, resource_type, resource_id, outcome, details) ON audit_logs TO tbcg_content_build_owner;
GRANT USAGE ON SEQUENCE audit_logs_audit_id_seq TO tbcg_content_build_owner;

CREATE FUNCTION public.assert_game_publisher(p_game_id text, p_actor_id uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM public.admin_role_bindings
        WHERE game_id = p_game_id AND admin_id = p_actor_id AND role = 'Publisher'
    ) THEN
        RAISE EXCEPTION 'Publisher role is required for this Game'
            USING ERRCODE = '42501';
    END IF;
END;
$$;

CREATE FUNCTION public.assert_build_worker(p_worker_instance_id uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF p_worker_instance_id IS NULL OR NOT EXISTS (
        SELECT 1 FROM pg_roles r
        WHERE r.rolname = 'tbcg_content_build_worker'
          AND pg_has_role(session_user, r.oid, 'MEMBER')
    ) THEN
        RAISE EXCEPTION 'restricted content build worker identity is required'
            USING ERRCODE = '42501';
    END IF;
END;
$$;

CREATE FUNCTION public.build_job_public_json(p_job_id uuid)
RETURNS jsonb
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
    SELECT jsonb_build_object(
        'job_id', job_id,
        'game_id', game_id,
        'content_id', content_id,
        'revision', revision,
        'body_hash', encode(body_hash, 'hex'),
        'release_id', release_id,
        'release_version', release_version,
        'status', status,
        'created_at', created_at,
        'updated_at', updated_at,
        'public_artifact_sha256', CASE WHEN public_artifact_sha256 IS NULL THEN NULL ELSE encode(public_artifact_sha256, 'hex') END,
        'error_code', error_code
    )
    FROM public.admin_build_jobs WHERE job_id = p_job_id
$$;

CREATE FUNCTION public.admin_list_build_candidates(p_game_id text, p_actor_id uuid, p_limit integer, p_offset integer)
RETURNS TABLE(items jsonb, total bigint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    PERFORM public.assert_game_publisher(p_game_id, p_actor_id);
    IF p_limit IS NULL OR p_offset IS NULL OR p_limit < 1 OR p_limit > 100 OR p_offset < 0 OR p_offset > 1000000 THEN
        RAISE EXCEPTION 'invalid build candidate pagination'
            USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    WITH approved AS MATERIALIZED (
        SELECT c.game_id, c.content_id, c.revision, a.body_hash, c.created_at
        FROM public.content_revisions c
        JOIN public.revision_approvals a USING (game_id, content_id, revision)
        JOIN public.game_build_profiles profile ON profile.game_id = c.game_id
        WHERE c.game_id = p_game_id AND c.content_type = 'item' AND a.decision = 'approved'
          AND c.schema_version = profile.content_schema_version
    ), page AS (
        SELECT * FROM approved
        ORDER BY created_at DESC, content_id, revision DESC
        LIMIT p_limit OFFSET p_offset
    )
    SELECT COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'game_id', page.game_id,
            'content_id', page.content_id,
            'revision', page.revision,
            'body_hash', encode(page.body_hash, 'hex'),
            'submitted_at', page.created_at
        ) ORDER BY page.created_at DESC, page.content_id, page.revision DESC)
        FROM page
    ), '[]'::jsonb), (SELECT count(*) FROM approved)::bigint;
END;
$$;

CREATE FUNCTION public.admin_read_build_input(
    p_game_id text, p_actor_id uuid, p_content_id text, p_revision integer, p_body_hash bytea
)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    approved_body jsonb;
    profile public.game_build_profiles%ROWTYPE;
BEGIN
    PERFORM public.assert_game_publisher(p_game_id, p_actor_id);
    IF p_body_hash IS NULL OR octet_length(p_body_hash) <> 32 OR p_revision IS NULL OR p_revision < 1 THEN
        RAISE EXCEPTION 'invalid approved build identity'
            USING ERRCODE = '22023';
    END IF;
    SELECT * INTO profile FROM public.game_build_profiles WHERE game_id = p_game_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Game has no trusted build profile'
            USING ERRCODE = '55000';
    END IF;
    SELECT c.body INTO approved_body
    FROM public.content_revisions c
    JOIN public.revision_approvals a USING (game_id, content_id, revision)
    WHERE c.game_id = p_game_id AND c.content_id = p_content_id AND c.revision = p_revision
      AND c.content_type = 'item' AND a.decision = 'approved' AND a.body_hash = p_body_hash;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    IF (approved_body->>'schema_version')::integer IS DISTINCT FROM profile.content_schema_version THEN
        RAISE EXCEPTION 'approved revision schema is incompatible with the Game build profile'
            USING ERRCODE = '55000';
    END IF;
    RETURN jsonb_build_object(
        'definition', approved_body,
        'profile', jsonb_build_object(
            'profile_version', profile.profile_version,
            'content_schema_version', profile.content_schema_version,
            'instance_schema_version', profile.instance_schema_version,
            'ruleset_version', profile.ruleset_version,
            'protocol_version', profile.protocol_version,
            'client_min_build', profile.client_min_build,
            'asset_manifest_sha256', encode(profile.asset_manifest_sha256, 'hex')
        )
    );
END;
$$;

CREATE FUNCTION public.admin_find_build_job(
    p_game_id text, p_actor_id uuid, p_idempotency_key text, p_request_fingerprint bytea
)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    existing_job public.admin_build_jobs%ROWTYPE;
BEGIN
    PERFORM public.assert_game_publisher(p_game_id, p_actor_id);
    IF p_idempotency_key IS NULL OR length(p_idempotency_key) NOT BETWEEN 16 AND 128
       OR p_idempotency_key !~ '^[A-Za-z0-9._~-]+$' OR p_request_fingerprint IS NULL OR octet_length(p_request_fingerprint) <> 32 THEN
        RAISE EXCEPTION 'invalid build idempotency input'
            USING ERRCODE = '22023';
    END IF;
    SELECT * INTO existing_job FROM public.admin_build_jobs
    WHERE game_id = p_game_id AND requested_by = p_actor_id AND idempotency_key = p_idempotency_key;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    IF existing_job.request_fingerprint <> p_request_fingerprint THEN
        RAISE EXCEPTION 'idempotency key is bound to a different build request'
            USING ERRCODE = '23505', CONSTRAINT = 'admin_build_jobs_idempotency_unique';
    END IF;
    RETURN public.build_job_public_json(existing_job.job_id);
END;
$$;

CREATE FUNCTION public.admin_get_build_job(p_game_id text, p_actor_id uuid, p_job_id uuid)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    found_job uuid;
BEGIN
    PERFORM public.assert_game_publisher(p_game_id, p_actor_id);
    SELECT job_id INTO found_job FROM public.admin_build_jobs WHERE game_id = p_game_id AND job_id = p_job_id;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    RETURN public.build_job_public_json(found_job);
END;
$$;

CREATE FUNCTION public.admin_enqueue_build_job(
    p_game_id text, p_actor_id uuid, p_content_id text, p_revision integer, p_body_hash bytea,
    p_idempotency_key text, p_request_fingerprint bytea, p_expected_profile_version bigint,
    p_asset_snapshot jsonb, p_schema_fingerprint text
)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    existing_job public.admin_build_jobs%ROWTYPE;
    profile public.game_build_profiles%ROWTYPE;
    approved_body jsonb;
    expected_icon_id text;
    next_version bigint;
    reserved_version integer;
    new_job_id uuid;
    snapshot jsonb;
    active_job_id uuid;
    active_jobs bigint;
BEGIN
    PERFORM public.assert_game_publisher(p_game_id, p_actor_id);
    IF p_body_hash IS NULL OR octet_length(p_body_hash) <> 32
       OR p_request_fingerprint IS NULL OR octet_length(p_request_fingerprint) <> 32 OR p_revision IS NULL OR p_revision < 1
       OR p_idempotency_key IS NULL OR length(p_idempotency_key) NOT BETWEEN 16 AND 128 OR p_idempotency_key !~ '^[A-Za-z0-9._~-]+$'
       OR p_expected_profile_version IS NULL OR p_expected_profile_version < 1
       OR p_schema_fingerprint IS NULL OR p_schema_fingerprint !~ '^[a-f0-9]{64}$'
       OR jsonb_typeof(p_asset_snapshot) IS DISTINCT FROM 'object'
       OR jsonb_typeof(p_asset_snapshot->'asset_id') IS DISTINCT FROM 'string'
       OR jsonb_typeof(p_asset_snapshot->'manifest_bytes') IS DISTINCT FROM 'string'
       OR jsonb_typeof(p_asset_snapshot->'asset_bytes') IS DISTINCT FROM 'string'
       OR length(p_asset_snapshot->>'manifest_bytes') NOT BETWEEN 1 AND 1400000
       OR length(p_asset_snapshot->>'asset_bytes') NOT BETWEEN 1 AND 1400000 THEN
        RAISE EXCEPTION 'invalid immutable build input'
            USING ERRCODE = '22023';
    END IF;

    SELECT * INTO existing_job FROM public.admin_build_jobs
    WHERE game_id = p_game_id AND requested_by = p_actor_id AND idempotency_key = p_idempotency_key;
    IF FOUND THEN
        IF existing_job.request_fingerprint <> p_request_fingerprint THEN
            RAISE EXCEPTION 'idempotency key is bound to a different build request'
                USING ERRCODE = '23505', CONSTRAINT = 'admin_build_jobs_idempotency_unique';
        END IF;
        RETURN public.build_job_public_json(existing_job.job_id);
    END IF;

    SELECT * INTO profile FROM public.game_build_profiles WHERE game_id = p_game_id FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Game has no trusted build profile'
            USING ERRCODE = '55000';
    END IF;
    IF profile.profile_version <> p_expected_profile_version THEN
        RAISE EXCEPTION 'Game build profile changed while preparing the build'
            USING ERRCODE = '40001';
    END IF;
    SELECT c.body INTO approved_body
    FROM public.content_revisions c
    JOIN public.revision_approvals a USING (game_id, content_id, revision)
    WHERE c.game_id = p_game_id AND c.content_id = p_content_id AND c.revision = p_revision
      AND c.content_type = 'item' AND a.decision = 'approved' AND a.body_hash = p_body_hash;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'requested exact revision is not approved'
            USING ERRCODE = 'P0002';
    END IF;
    IF (approved_body->>'schema_version')::integer IS DISTINCT FROM profile.content_schema_version THEN
        RAISE EXCEPTION 'approved revision schema is incompatible with the Game build profile'
            USING ERRCODE = '55000';
    END IF;
    expected_icon_id := approved_body->'public'->>'icon_id';
    IF p_asset_snapshot->>'asset_id' IS DISTINCT FROM expected_icon_id
       OR sha256(decode(p_asset_snapshot->>'manifest_bytes', 'base64')) <> profile.asset_manifest_sha256 THEN
        RAISE EXCEPTION 'frozen licensed asset does not match approved Definition or trusted Game profile'
            USING ERRCODE = '23514';
    END IF;

    INSERT INTO public.release_version_counters (game_id, next_release_version)
    SELECT p_game_id, COALESCE(MAX(r.release_version::bigint), 0) + 1
    FROM public.releases r WHERE r.game_id = p_game_id
    ON CONFLICT (game_id) DO NOTHING;
    SELECT next_release_version INTO next_version FROM public.release_version_counters
    WHERE game_id = p_game_id FOR UPDATE;
    IF NOT FOUND OR next_version > 2147483647 THEN
        RAISE EXCEPTION 'Game Release version range is exhausted'
            USING ERRCODE = '22003';
    END IF;
    -- The counter row lock serializes enqueues per Game, so dedup and the active-job cap cannot race.
    -- Re-read the idempotency key first: a concurrent same-key request may have committed while we waited.
    SELECT * INTO existing_job FROM public.admin_build_jobs
    WHERE game_id = p_game_id AND requested_by = p_actor_id AND idempotency_key = p_idempotency_key;
    IF FOUND THEN
        IF existing_job.request_fingerprint <> p_request_fingerprint THEN
            RAISE EXCEPTION 'idempotency key is bound to a different build request'
                USING ERRCODE = '23505', CONSTRAINT = 'admin_build_jobs_idempotency_unique';
        END IF;
        RETURN public.build_job_public_json(existing_job.job_id);
    END IF;
    SELECT job_id INTO active_job_id FROM public.admin_build_jobs
    WHERE game_id = p_game_id AND content_id = p_content_id AND revision = p_revision
      AND body_hash = p_body_hash AND status IN ('queued', 'running', 'retry_wait')
    ORDER BY created_at, job_id LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'exact revision already has an active build job %', active_job_id
            USING ERRCODE = '55006';
    END IF;
    SELECT count(*) INTO active_jobs FROM public.admin_build_jobs
    WHERE game_id = p_game_id AND status IN ('queued', 'running', 'retry_wait');
    IF active_jobs >= 20 THEN
        RAISE EXCEPTION 'Game build queue is full'
            USING ERRCODE = '53400';
    END IF;
    reserved_version := next_version::integer;
    UPDATE public.release_version_counters SET next_release_version = next_version + 1 WHERE game_id = p_game_id;

    snapshot := jsonb_build_object(
        'definition', approved_body,
        'profile', jsonb_build_object(
            'profile_version', profile.profile_version,
            'content_schema_version', profile.content_schema_version,
            'instance_schema_version', profile.instance_schema_version,
            'ruleset_version', profile.ruleset_version,
            'protocol_version', profile.protocol_version,
            'client_min_build', profile.client_min_build,
            'asset_manifest_sha256', encode(profile.asset_manifest_sha256, 'hex')
        ),
        'schema_fingerprint', p_schema_fingerprint,
        'builder_version', 'item-builder-v1',
        'asset', p_asset_snapshot
    );
    new_job_id := gen_random_uuid();
    INSERT INTO public.admin_build_jobs (
        job_id, game_id, requested_by, idempotency_key, request_fingerprint, content_id, revision,
        body_hash, release_id, release_version, input_snapshot
    ) VALUES (
        new_job_id, p_game_id, p_actor_id, p_idempotency_key, p_request_fingerprint, p_content_id, p_revision,
        p_body_hash, 'build-' || replace(gen_random_uuid()::text, '-', ''), reserved_version, snapshot
    ) ON CONFLICT (game_id, requested_by, idempotency_key) DO NOTHING
    RETURNING job_id INTO new_job_id;
    IF new_job_id IS NULL THEN
        SELECT * INTO existing_job FROM public.admin_build_jobs
        WHERE game_id = p_game_id AND requested_by = p_actor_id AND idempotency_key = p_idempotency_key;
        IF existing_job.request_fingerprint <> p_request_fingerprint THEN
            RAISE EXCEPTION 'idempotency key is bound to a different build request'
                USING ERRCODE = '23505', CONSTRAINT = 'admin_build_jobs_idempotency_unique';
        END IF;
        RETURN public.build_job_public_json(existing_job.job_id);
    END IF;

    INSERT INTO public.audit_logs (game_id, actor_id, action, resource_type, resource_id, outcome, details)
    VALUES (p_game_id, p_actor_id, 'release.build.request', 'build_job', new_job_id::text, 'success',
        jsonb_build_object('content_id', p_content_id, 'revision', p_revision, 'body_hash', encode(p_body_hash, 'hex')));
    RETURN public.build_job_public_json(new_job_id);
END;
$$;

CREATE FUNCTION public.build_job_claim_json(p_job_id uuid)
RETURNS jsonb
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
    SELECT jsonb_build_object(
        'job', public.build_job_public_json(job_id),
        'input_snapshot', input_snapshot,
        'claim_token', claim_token,
        'claim_owner', claim_owner,
        'lease_until', lease_until
    )
    FROM public.admin_build_jobs WHERE job_id = p_job_id AND status = 'running'
$$;

CREATE FUNCTION public.admin_renew_build_job_lease(
    p_job_id uuid, p_worker_instance_id uuid, p_claim_token uuid, p_lease_seconds integer
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    PERFORM public.assert_build_worker(p_worker_instance_id);
    IF p_job_id IS NULL OR p_claim_token IS NULL OR p_lease_seconds IS NULL OR p_lease_seconds NOT BETWEEN 5 AND 300 THEN
        RAISE EXCEPTION 'invalid build lease renewal input'
            USING ERRCODE = '22023';
    END IF;
    -- Extend only when it lengthens the lease; a shorter request keeps the current lease unchanged.
    UPDATE public.admin_build_jobs
    SET lease_until = now() + make_interval(secs => p_lease_seconds)
    WHERE job_id = p_job_id AND status = 'running' AND claim_owner = p_worker_instance_id
      AND claim_token = p_claim_token AND lease_until > now()
      AND lease_until < now() + make_interval(secs => p_lease_seconds);
    RETURN EXISTS (
        SELECT 1 FROM public.admin_build_jobs
        WHERE job_id = p_job_id AND status = 'running' AND claim_owner = p_worker_instance_id
          AND claim_token = p_claim_token AND lease_until > now()
    );
END;
$$;

CREATE FUNCTION public.admin_claim_build_job(p_worker_instance_id uuid, p_lease_seconds integer)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    job public.admin_build_jobs%ROWTYPE;
    available_job_id uuid;
BEGIN
    PERFORM public.assert_build_worker(p_worker_instance_id);
    IF p_lease_seconds IS NULL OR p_lease_seconds NOT BETWEEN 5 AND 300 THEN
        RAISE EXCEPTION 'build worker lease duration must be between 5 and 300 seconds'
            USING ERRCODE = '22023';
    END IF;

    SELECT * INTO job FROM public.admin_build_jobs
    WHERE status = 'running' AND claim_owner = p_worker_instance_id AND lease_until > now()
    ORDER BY created_at, job_id LIMIT 1 FOR UPDATE SKIP LOCKED;
    IF FOUND THEN
        -- Recovering an unknown claim result: give the restarted worker a full lease, never a shorter one.
        UPDATE public.admin_build_jobs
        SET lease_until = now() + make_interval(secs => p_lease_seconds)
        WHERE job_id = job.job_id AND lease_until < now() + make_interval(secs => p_lease_seconds);
        RETURN public.build_job_claim_json(job.job_id);
    END IF;

    WITH expired AS (
        SELECT job_id, attempt_count, max_attempts
        FROM public.admin_build_jobs
        WHERE status = 'running' AND lease_until <= now()
        ORDER BY lease_until, job_id
        LIMIT 50 FOR UPDATE SKIP LOCKED
    )
    UPDATE public.admin_build_jobs target
    SET status = CASE WHEN expired.attempt_count >= expired.max_attempts THEN 'failed' ELSE 'retry_wait' END,
        error_code = CASE WHEN expired.attempt_count >= expired.max_attempts THEN 'attempt_limit' ELSE 'worker_lease_expired' END,
        next_attempt_at = now(), claim_token = NULL, claim_owner = NULL, lease_until = NULL
    FROM expired WHERE target.job_id = expired.job_id;

    SELECT job_id INTO available_job_id FROM public.admin_build_jobs
    WHERE status IN ('queued', 'retry_wait') AND next_attempt_at <= now() AND attempt_count < max_attempts
    ORDER BY next_attempt_at, created_at, job_id
    LIMIT 1 FOR UPDATE SKIP LOCKED;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    UPDATE public.admin_build_jobs
    SET status = 'running', attempt_count = attempt_count + 1,
        claim_token = gen_random_uuid(), claim_owner = p_worker_instance_id,
        lease_until = now() + make_interval(secs => p_lease_seconds), error_code = NULL
    WHERE job_id = available_job_id
    RETURNING * INTO job;
    RETURN public.build_job_claim_json(job.job_id);
END;
$$;

CREATE FUNCTION public.admin_fail_build_job(
    p_job_id uuid, p_worker_instance_id uuid, p_claim_token uuid, p_error_code text,
    p_retryable boolean, p_backoff_seconds integer
)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    job public.admin_build_jobs%ROWTYPE;
    next_status text;
BEGIN
    PERFORM public.assert_build_worker(p_worker_instance_id);
    IF p_job_id IS NULL OR p_claim_token IS NULL OR p_error_code IS NULL
       OR p_error_code !~ '^[a-z][a-z0-9_]{0,63}$' OR p_retryable IS NULL
       OR p_backoff_seconds IS NULL OR p_backoff_seconds NOT BETWEEN 0 AND 3600 THEN
        RAISE EXCEPTION 'invalid build failure report'
            USING ERRCODE = '22023';
    END IF;
    SELECT * INTO job FROM public.admin_build_jobs WHERE job_id = p_job_id FOR UPDATE;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    IF job.status IN ('retry_wait', 'failed') AND job.claim_token IS NULL AND job.error_code = p_error_code THEN
        RETURN public.build_job_public_json(job.job_id);
    END IF;
    IF job.status <> 'running' OR job.claim_owner <> p_worker_instance_id
       OR job.claim_token <> p_claim_token OR job.lease_until <= now() THEN
        RETURN NULL;
    END IF;
    next_status := CASE WHEN p_retryable AND job.attempt_count < job.max_attempts THEN 'retry_wait' ELSE 'failed' END;
    UPDATE public.admin_build_jobs
    SET status = next_status,
        error_code = p_error_code,
        next_attempt_at = CASE WHEN next_status = 'retry_wait' THEN now() + make_interval(secs => p_backoff_seconds) ELSE now() END,
        claim_token = NULL, claim_owner = NULL, lease_until = NULL
    WHERE job_id = p_job_id;
    INSERT INTO public.audit_logs (game_id, actor_id, action, resource_type, resource_id, outcome, details)
    VALUES (job.game_id, job.requested_by,
        CASE WHEN next_status = 'retry_wait' THEN 'release.build.retry' ELSE 'release.build.failed' END,
        'build_job', job.job_id::text, 'failure', jsonb_build_object('error_code', p_error_code, 'attempt', job.attempt_count));
    RETURN public.build_job_public_json(job.job_id);
END;
$$;

CREATE FUNCTION public.admin_finish_build_job(
    p_job_id uuid, p_worker_instance_id uuid, p_claim_token uuid,
    p_manifest jsonb, p_public_artifact_sha256 bytea, p_private_artifact_sha256 bytea
)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    job public.admin_build_jobs%ROWTYPE;
    profile jsonb;
    expected_revision jsonb;
    expected_asset_id text;
    expected_asset jsonb;
    manifest_keys text[];
    release_status text;
    release_manifest jsonb;
    entries_match boolean;
    entries_count bigint;
BEGIN
    PERFORM public.assert_build_worker(p_worker_instance_id);
    IF p_job_id IS NULL OR p_claim_token IS NULL OR p_manifest IS NULL
       OR jsonb_typeof(p_manifest) IS DISTINCT FROM 'object'
       OR pg_column_size(p_manifest) > 65536
       OR p_public_artifact_sha256 IS NULL OR octet_length(p_public_artifact_sha256) <> 32
       OR p_private_artifact_sha256 IS NULL OR octet_length(p_private_artifact_sha256) <> 32 THEN
        RAISE EXCEPTION 'invalid build completion report'
            USING ERRCODE = '22023';
    END IF;
    SELECT * INTO job FROM public.admin_build_jobs WHERE job_id = p_job_id FOR UPDATE;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    IF job.status = 'built' THEN
        IF job.manifest IS DISTINCT FROM p_manifest
           OR job.public_artifact_sha256 IS DISTINCT FROM p_public_artifact_sha256
           OR job.private_artifact_sha256 IS DISTINCT FROM p_private_artifact_sha256 THEN
            RAISE EXCEPTION 'built job retry has different immutable artifacts'
                USING ERRCODE = '23505', CONSTRAINT = 'admin_build_jobs_built_artifacts_valid';
        END IF;
        SELECT status, manifest INTO release_status, release_manifest
        FROM public.releases WHERE game_id = job.game_id AND release_id = job.release_id;
        SELECT count(*) INTO entries_count FROM public.release_entries
        WHERE game_id = job.game_id AND release_id = job.release_id;
        SELECT EXISTS (
            SELECT 1 FROM public.release_entries
            WHERE game_id = job.game_id AND release_id = job.release_id
              AND content_id = job.content_id AND revision = job.revision
        ) INTO entries_match;
        IF release_status NOT IN ('built', 'staged', 'activated', 'deprecated')
           OR release_manifest IS DISTINCT FROM p_manifest OR entries_count <> 1 OR NOT entries_match THEN
            RAISE EXCEPTION 'built Release registration does not reconcile with immutable job output'
                USING ERRCODE = '23505', CONSTRAINT = 'releases_game_id_release_id_key';
        END IF;
        RETURN public.build_job_public_json(job.job_id);
    END IF;
    IF job.status <> 'running' OR job.claim_owner <> p_worker_instance_id
       OR job.claim_token <> p_claim_token OR job.lease_until <= now() THEN
        RETURN NULL;
    END IF;
    profile := job.input_snapshot->'profile';
    expected_revision := jsonb_build_array(jsonb_build_object(
        'content_id', job.content_id, 'revision', job.revision, 'body_hash', encode(job.body_hash, 'hex')));
    expected_asset_id := job.input_snapshot->'asset'->>'asset_id';
    -- The expected asset row comes from the manifest bytes frozen by the admin trust domain at enqueue.
    SELECT entry INTO expected_asset
    FROM jsonb_array_elements(
        convert_from(decode(job.input_snapshot->'asset'->>'manifest_bytes', 'base64'), 'UTF8')::jsonb->'assets'
    ) AS entries(entry)
    WHERE entry->>'asset_id' = expected_asset_id;
    SELECT array_agg(key COLLATE "C" ORDER BY key COLLATE "C") INTO manifest_keys FROM jsonb_object_keys(p_manifest) AS keys(key);
    IF expected_asset IS NULL OR manifest_keys IS DISTINCT FROM ARRAY[
        'assets', 'builder_version', 'capabilities', 'compatibility', 'content_revisions', 'format_version',
        'game_id', 'private_artifact_sha256', 'public_artifact_sha256', 'release_id', 'release_version',
        'schema_fingerprint', 'scope'
    ]::text[] THEN
        RAISE EXCEPTION 'build completion manifest has an unexpected shape'
            USING ERRCODE = '23514';
    END IF;
    IF jsonb_typeof(p_manifest->'assets') IS DISTINCT FROM 'array' THEN
        RAISE EXCEPTION 'build completion manifest must contain an asset array'
            USING ERRCODE = '23514';
    END IF;
    IF jsonb_array_length(p_manifest->'assets') <> 1 THEN
        RAISE EXCEPTION 'single-item build manifest must contain exactly one asset'
            USING ERRCODE = '23514';
    END IF;
    IF p_manifest->'format_version' IS DISTINCT FROM '1'::jsonb
       OR p_manifest->'scope' IS DISTINCT FROM to_jsonb('single_item'::text)
       OR p_manifest->'game_id' IS DISTINCT FROM to_jsonb(job.game_id)
       OR p_manifest->'release_id' IS DISTINCT FROM to_jsonb(job.release_id)
       OR p_manifest->'release_version' IS DISTINCT FROM to_jsonb(job.release_version)
       OR p_manifest->'builder_version' IS DISTINCT FROM job.input_snapshot->'builder_version'
       OR p_manifest->'schema_fingerprint' IS DISTINCT FROM job.input_snapshot->'schema_fingerprint'
       OR p_manifest->'compatibility' IS DISTINCT FROM profile
       OR p_manifest->'capabilities' IS DISTINCT FROM '[]'::jsonb
       OR p_manifest->'content_revisions' IS DISTINCT FROM expected_revision
       OR p_manifest->'public_artifact_sha256' IS DISTINCT FROM to_jsonb(encode(p_public_artifact_sha256, 'hex'))
       OR p_manifest->'private_artifact_sha256' IS DISTINCT FROM to_jsonb(encode(p_private_artifact_sha256, 'hex'))
       OR p_manifest->'assets'->0 IS DISTINCT FROM expected_asset THEN
        RAISE EXCEPTION 'build completion manifest does not match frozen job input'
            USING ERRCODE = '23514';
    END IF;

    INSERT INTO public.releases (
        game_id, release_id, release_version, content_schema_version, instance_schema_version,
        ruleset_version, protocol_version, client_min_build, status, manifest
    ) VALUES (
        job.game_id, job.release_id, job.release_version,
        (profile->>'content_schema_version')::integer,
        (profile->>'instance_schema_version')::integer,
        (profile->>'ruleset_version')::integer,
        (profile->>'protocol_version')::integer,
        profile->>'client_min_build', 'built', p_manifest
    );
    INSERT INTO public.release_entries (game_id, release_id, content_id, revision)
    VALUES (job.game_id, job.release_id, job.content_id, job.revision);
    UPDATE public.admin_build_jobs
    SET status = 'built', claim_token = NULL, claim_owner = NULL, lease_until = NULL,
        manifest = p_manifest, public_artifact_sha256 = p_public_artifact_sha256,
        private_artifact_sha256 = p_private_artifact_sha256, error_code = NULL
    WHERE job_id = job.job_id;
    INSERT INTO public.audit_logs (game_id, actor_id, action, resource_type, resource_id, outcome, details)
    VALUES (job.game_id, job.requested_by, 'release.build.completed', 'release', job.release_id, 'success',
        jsonb_build_object('job_id', job.job_id, 'content_id', job.content_id, 'revision', job.revision,
            'public_artifact_sha256', encode(p_public_artifact_sha256, 'hex')));
    RETURN public.build_job_public_json(job.job_id);
END;
$$;

-- ALTER FUNCTION ... OWNER requires the new owner to hold CREATE on the schema; grant it only for
-- the duration of this transaction so the owner never keeps DDL rights.
GRANT CREATE ON SCHEMA public TO tbcg_content_build_owner;
ALTER FUNCTION public.build_job_public_json(uuid) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.build_job_claim_json(uuid) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.assert_game_publisher(text, uuid) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.assert_build_worker(uuid) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_claim_build_job(uuid, integer) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_renew_build_job_lease(uuid, uuid, uuid, integer) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_fail_build_job(uuid, uuid, uuid, text, boolean, integer) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_finish_build_job(uuid, uuid, uuid, jsonb, bytea, bytea) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_list_build_candidates(text, uuid, integer, integer) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_read_build_input(text, uuid, text, integer, bytea) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_find_build_job(text, uuid, text, bytea) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_get_build_job(text, uuid, uuid) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_enqueue_build_job(text, uuid, text, integer, bytea, text, bytea, bigint, jsonb, text) OWNER TO tbcg_content_build_owner;
REVOKE CREATE ON SCHEMA public FROM tbcg_content_build_owner;
REVOKE ALL ON FUNCTION public.build_job_public_json(uuid), public.build_job_claim_json(uuid),
    public.assert_game_publisher(text, uuid), public.assert_build_worker(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.admin_claim_build_job(uuid, integer), public.admin_renew_build_job_lease(uuid, uuid, uuid, integer),
    public.admin_fail_build_job(uuid, uuid, uuid, text, boolean, integer), public.admin_finish_build_job(uuid, uuid, uuid, jsonb, bytea, bytea) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.admin_list_build_candidates(text, uuid, integer, integer), public.admin_read_build_input(text, uuid, text, integer, bytea), public.admin_find_build_job(text, uuid, text, bytea), public.admin_get_build_job(text, uuid, uuid), public.admin_enqueue_build_job(text, uuid, text, integer, bytea, text, bytea, bigint, jsonb, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.admin_list_build_candidates(text, uuid, integer, integer), public.admin_read_build_input(text, uuid, text, integer, bytea), public.admin_find_build_job(text, uuid, text, bytea), public.admin_get_build_job(text, uuid, uuid), public.admin_enqueue_build_job(text, uuid, text, integer, bytea, text, bytea, bigint, jsonb, text) TO tbcg_admin_control_plane;
GRANT EXECUTE ON FUNCTION public.admin_claim_build_job(uuid, integer), public.admin_renew_build_job_lease(uuid, uuid, uuid, integer),
    public.admin_fail_build_job(uuid, uuid, uuid, text, boolean, integer), public.admin_finish_build_job(uuid, uuid, uuid, jsonb, bytea, bytea) TO tbcg_content_build_worker;

---- create above / drop below ----

REVOKE ALL ON FUNCTION public.admin_enqueue_build_job(text, uuid, text, integer, bytea, text, bytea, bigint, jsonb, text),
    public.admin_get_build_job(text, uuid, uuid), public.admin_find_build_job(text, uuid, text, bytea),
    public.admin_read_build_input(text, uuid, text, integer, bytea), public.admin_list_build_candidates(text, uuid, integer, integer)
    FROM PUBLIC, tbcg_admin_control_plane;
DROP FUNCTION public.admin_enqueue_build_job(text, uuid, text, integer, bytea, text, bytea, bigint, jsonb, text);
DROP FUNCTION public.admin_get_build_job(text, uuid, uuid);
DROP FUNCTION public.admin_find_build_job(text, uuid, text, bytea);
DROP FUNCTION public.admin_read_build_input(text, uuid, text, integer, bytea);
DROP FUNCTION public.admin_list_build_candidates(text, uuid, integer, integer);
REVOKE ALL ON FUNCTION public.admin_claim_build_job(uuid, integer), public.admin_renew_build_job_lease(uuid, uuid, uuid, integer),
    public.admin_fail_build_job(uuid, uuid, uuid, text, boolean, integer), public.admin_finish_build_job(uuid, uuid, uuid, jsonb, bytea, bytea)
    FROM PUBLIC, tbcg_content_build_worker;
DROP FUNCTION public.admin_finish_build_job(uuid, uuid, uuid, jsonb, bytea, bytea);
DROP FUNCTION public.admin_fail_build_job(uuid, uuid, uuid, text, boolean, integer);
DROP FUNCTION public.admin_renew_build_job_lease(uuid, uuid, uuid, integer);
DROP FUNCTION public.admin_claim_build_job(uuid, integer);
REVOKE ALL ON FUNCTION public.build_job_claim_json(uuid), public.build_job_public_json(uuid),
    public.assert_game_publisher(text, uuid), public.assert_build_worker(uuid) FROM PUBLIC;
DROP FUNCTION public.build_job_claim_json(uuid);
DROP FUNCTION public.build_job_public_json(uuid);
DROP FUNCTION public.assert_game_publisher(text, uuid);
DROP FUNCTION public.assert_build_worker(uuid);
REVOKE ALL ON game_build_profiles, release_version_counters, admin_build_jobs FROM tbcg_content_build_owner;
REVOKE UPDATE (profile_version) ON game_build_profiles FROM tbcg_content_build_owner;
REVOKE SELECT (game_id) ON games FROM tbcg_content_build_owner;
REVOKE SELECT (game_id, admin_id, role) ON admin_role_bindings FROM tbcg_content_build_owner;
REVOKE SELECT ON content_revisions, revision_approvals, releases, release_entries FROM tbcg_content_build_owner;
REVOKE INSERT (game_id, release_id, release_version, content_schema_version, instance_schema_version, ruleset_version, protocol_version, client_min_build, status, manifest)
    ON releases FROM tbcg_content_build_owner;
REVOKE INSERT (game_id, release_id, content_id, revision) ON release_entries FROM tbcg_content_build_owner;
REVOKE INSERT (game_id, actor_id, action, resource_type, resource_id, outcome, details) ON audit_logs FROM tbcg_content_build_owner;
REVOKE USAGE ON SEQUENCE audit_logs_audit_id_seq FROM tbcg_content_build_owner;
DROP TRIGGER release_entries_no_truncate ON release_entries;
DROP TRIGGER release_entries_immutable_rows ON release_entries;
DROP TRIGGER release_entries_only_extend_built_release ON release_entries;
DROP FUNCTION public.prevent_release_entry_mutation();
DROP FUNCTION public.guard_release_entry_insert();
DROP TRIGGER releases_build_owner_must_register_built_only ON releases;
DROP FUNCTION public.restrict_build_owner_release_insert();
DROP TRIGGER admin_build_jobs_identity_is_immutable ON admin_build_jobs;
DROP FUNCTION public.prevent_admin_build_job_input_mutation();
DROP TRIGGER release_version_counters_monotonic ON release_version_counters;
DROP FUNCTION public.prevent_release_version_counter_regression();
DROP TRIGGER game_build_profiles_version_increases ON game_build_profiles;
DROP FUNCTION public.bump_game_build_profile_version();
DROP TABLE admin_build_jobs;
DROP TABLE release_version_counters;
DROP TABLE game_build_profiles;
