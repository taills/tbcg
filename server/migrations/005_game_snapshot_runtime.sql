-- 005：完整 Game 快照构建、runtime owner 激活请求与游戏运行面表。
-- 快照作业复用 004 的 owner/worker 角色模型：admin 与 worker 只能通过 SECURITY DEFINER 函数访问。

CREATE TABLE admin_snapshot_jobs (
    job_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id text NOT NULL,
    requested_by uuid NOT NULL,
    idempotency_key text NOT NULL,
    request_fingerprint bytea NOT NULL,
    release_id text NOT NULL,
    release_version integer NOT NULL,
    entry_count integer NOT NULL,
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
    CONSTRAINT admin_snapshot_jobs_game_fk FOREIGN KEY (game_id) REFERENCES games (game_id),
    CONSTRAINT admin_snapshot_jobs_requester_fk FOREIGN KEY (requested_by) REFERENCES admin_users (admin_id),
    CONSTRAINT admin_snapshot_jobs_release_unique UNIQUE (game_id, release_id),
    CONSTRAINT admin_snapshot_jobs_version_unique UNIQUE (game_id, release_version),
    CONSTRAINT admin_snapshot_jobs_idempotency_unique UNIQUE (game_id, requested_by, idempotency_key),
    CONSTRAINT admin_snapshot_jobs_key_format CHECK (length(idempotency_key) BETWEEN 16 AND 128 AND idempotency_key ~ '^[A-Za-z0-9._~-]+$'),
    CONSTRAINT admin_snapshot_jobs_fingerprint_length CHECK (octet_length(request_fingerprint) = 32),
    CONSTRAINT admin_snapshot_jobs_entry_count CHECK (entry_count BETWEEN 1 AND 2000),
    CONSTRAINT admin_snapshot_jobs_snapshot_object CHECK (jsonb_typeof(input_snapshot) = 'object' AND pg_column_size(input_snapshot) <= 33554432),
    CONSTRAINT admin_snapshot_jobs_status_valid CHECK (status IN ('queued', 'running', 'retry_wait', 'built', 'failed')),
    CONSTRAINT admin_snapshot_jobs_attempt_range CHECK (attempt_count >= 0 AND max_attempts BETWEEN 1 AND 10 AND attempt_count <= max_attempts),
    CONSTRAINT admin_snapshot_jobs_claim_state CHECK (
        (status = 'running' AND claim_token IS NOT NULL AND claim_owner IS NOT NULL AND lease_until IS NOT NULL)
        OR (status <> 'running' AND claim_token IS NULL AND claim_owner IS NULL AND lease_until IS NULL)
    ),
    CONSTRAINT admin_snapshot_jobs_built_valid CHECK (
        (status = 'built' AND manifest IS NOT NULL AND public_artifact_sha256 IS NOT NULL AND private_artifact_sha256 IS NOT NULL
            AND octet_length(public_artifact_sha256) = 32 AND octet_length(private_artifact_sha256) = 32 AND error_code IS NULL)
        OR (status <> 'built' AND manifest IS NULL AND public_artifact_sha256 IS NULL AND private_artifact_sha256 IS NULL)
    ),
    CONSTRAINT admin_snapshot_jobs_failed_has_error CHECK (status <> 'failed' OR error_code IS NOT NULL)
);

CREATE TABLE admin_snapshot_job_entries (
    job_id uuid NOT NULL REFERENCES admin_snapshot_jobs (job_id),
    game_id text NOT NULL,
    content_id text NOT NULL,
    revision integer NOT NULL,
    body_hash bytea NOT NULL,
    PRIMARY KEY (job_id, content_id),
    CONSTRAINT admin_snapshot_job_entries_revision_fk FOREIGN KEY (game_id, content_id, revision)
        REFERENCES content_revisions (game_id, content_id, revision),
    CONSTRAINT admin_snapshot_job_entries_hash_length CHECK (octet_length(body_hash) = 32)
);

CREATE INDEX admin_snapshot_jobs_claim_idx ON admin_snapshot_jobs (next_attempt_at, created_at, job_id)
    WHERE status IN ('queued', 'retry_wait');
CREATE INDEX admin_snapshot_jobs_game_idx ON admin_snapshot_jobs (game_id, created_at DESC);
CREATE INDEX admin_snapshot_jobs_requester_idx ON admin_snapshot_jobs (requested_by);
CREATE UNIQUE INDEX admin_snapshot_jobs_one_claim_per_worker_idx ON admin_snapshot_jobs (claim_owner) WHERE status = 'running';
CREATE INDEX admin_snapshot_job_entries_revision_idx ON admin_snapshot_job_entries (game_id, content_id, revision);

CREATE FUNCTION public.prevent_snapshot_table_truncate()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'snapshot build and activation records cannot be truncated' USING ERRCODE = '23514';
END;
$$;

CREATE TRIGGER admin_snapshot_jobs_no_truncate BEFORE TRUNCATE ON admin_snapshot_jobs
FOR EACH STATEMENT EXECUTE FUNCTION public.prevent_snapshot_table_truncate();
CREATE TRIGGER admin_snapshot_job_entries_no_truncate BEFORE TRUNCATE ON admin_snapshot_job_entries
FOR EACH STATEMENT EXECUTE FUNCTION public.prevent_snapshot_table_truncate();

CREATE FUNCTION public.prevent_admin_snapshot_job_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status IN ('built', 'failed') AND (to_jsonb(NEW) - 'updated_at') IS DISTINCT FROM (to_jsonb(OLD) - 'updated_at') THEN
        RAISE EXCEPTION 'built and failed snapshot jobs are immutable' USING ERRCODE = '23514';
    END IF;
    IF NEW.job_id IS DISTINCT FROM OLD.job_id OR NEW.game_id IS DISTINCT FROM OLD.game_id
       OR NEW.requested_by IS DISTINCT FROM OLD.requested_by OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.request_fingerprint IS DISTINCT FROM OLD.request_fingerprint OR NEW.release_id IS DISTINCT FROM OLD.release_id
       OR NEW.release_version IS DISTINCT FROM OLD.release_version OR NEW.input_snapshot IS DISTINCT FROM OLD.input_snapshot
       OR NEW.entry_count IS DISTINCT FROM OLD.entry_count OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.max_attempts IS DISTINCT FROM OLD.max_attempts THEN
        RAISE EXCEPTION 'snapshot job identity and frozen input are immutable' USING ERRCODE = '23514';
    END IF;
    IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
        (OLD.status IN ('queued', 'retry_wait') AND NEW.status IN ('running', 'failed'))
        OR (OLD.status = 'running' AND NEW.status IN ('retry_wait', 'built', 'failed'))
    ) THEN
        RAISE EXCEPTION 'invalid snapshot job transition from % to %', OLD.status, NEW.status USING ERRCODE = '23514';
    END IF;
    IF OLD.status = 'running' AND NEW.status = 'running'
       AND (NEW.claim_token IS DISTINCT FROM OLD.claim_token OR NEW.claim_owner IS DISTINCT FROM OLD.claim_owner
            OR NEW.attempt_count <> OLD.attempt_count OR NEW.lease_until <= OLD.lease_until) THEN
        RAISE EXCEPTION 'snapshot lease renewal may only extend the current claim' USING ERRCODE = '23514';
    END IF;
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER admin_snapshot_jobs_immutable_input
BEFORE UPDATE ON admin_snapshot_jobs
FOR EACH ROW EXECUTE FUNCTION public.prevent_admin_snapshot_job_mutation();

CREATE FUNCTION public.prevent_admin_snapshot_entry_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'snapshot job entries are immutable' USING ERRCODE = '23514';
END;
$$;

CREATE TRIGGER admin_snapshot_job_entries_immutable
BEFORE UPDATE OR DELETE ON admin_snapshot_job_entries
FOR EACH ROW EXECUTE FUNCTION public.prevent_admin_snapshot_entry_mutation();

-- 快照 Release 由 owner 一次登记多条 entry；仅当 entry 精确属于该 Release 的冻结快照作业时放行。
CREATE OR REPLACE FUNCTION public.guard_release_entry_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    release_status text;
    release_activated_at timestamptz;
    snapshot_job uuid;
BEGIN
    IF current_user = 'tbcg_content_build_owner' THEN
        SELECT status, activated_at INTO release_status, release_activated_at
        FROM public.releases WHERE game_id = NEW.game_id AND release_id = NEW.release_id;
    ELSE
        SELECT status, activated_at INTO release_status, release_activated_at
        FROM public.releases WHERE game_id = NEW.game_id AND release_id = NEW.release_id FOR SHARE;
    END IF;
    IF NOT FOUND OR release_status <> 'built' OR release_activated_at IS NOT NULL THEN
        RAISE EXCEPTION 'Release entries may only be added to a complete unactivated built Release'
            USING ERRCODE = '23514';
    END IF;
    IF current_user = 'tbcg_content_build_owner' THEN
        SELECT j.job_id INTO snapshot_job FROM public.admin_snapshot_jobs j
        WHERE j.game_id = NEW.game_id AND j.release_id = NEW.release_id;
        IF FOUND THEN
            IF NOT EXISTS (
                SELECT 1 FROM public.admin_snapshot_job_entries e
                WHERE e.job_id = snapshot_job AND e.content_id = NEW.content_id AND e.revision = NEW.revision
            ) THEN
                RAISE EXCEPTION 'snapshot Release entry is not part of the frozen job input' USING ERRCODE = '23514';
            END IF;
        ELSIF EXISTS (
            SELECT 1 FROM public.release_entries
            WHERE game_id = NEW.game_id AND release_id = NEW.release_id
        ) THEN
            RAISE EXCEPTION 'single-item build may register exactly one Release entry'
                USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TABLE release_activation_requests (
    request_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id text NOT NULL,
    realm_id text NOT NULL,
    release_id text NOT NULL,
    requested_by uuid NOT NULL,
    idempotency_key text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    error_code text,
    previous_release_id text,
    owner_epoch bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    CONSTRAINT release_activation_requests_realm_fk FOREIGN KEY (game_id, realm_id) REFERENCES realms (game_id, realm_id),
    CONSTRAINT release_activation_requests_release_fk FOREIGN KEY (game_id, release_id) REFERENCES releases (game_id, release_id),
    CONSTRAINT release_activation_requests_requester_fk FOREIGN KEY (requested_by) REFERENCES admin_users (admin_id),
    CONSTRAINT release_activation_requests_key_unique UNIQUE (game_id, requested_by, idempotency_key),
    CONSTRAINT release_activation_requests_key_format CHECK (length(idempotency_key) BETWEEN 16 AND 128 AND idempotency_key ~ '^[A-Za-z0-9._~-]+$'),
    CONSTRAINT release_activation_requests_status_valid CHECK (status IN ('pending', 'activated', 'rejected')),
    CONSTRAINT release_activation_requests_decision CHECK ((status = 'pending') = (decided_at IS NULL)),
    CONSTRAINT release_activation_requests_rejected_code CHECK (status <> 'rejected' OR error_code IS NOT NULL)
);
CREATE UNIQUE INDEX release_activation_requests_one_pending_idx ON release_activation_requests (game_id, realm_id) WHERE status = 'pending';
CREATE INDEX release_activation_requests_release_idx ON release_activation_requests (game_id, release_id);
CREATE INDEX release_activation_requests_requester_idx ON release_activation_requests (requested_by);
CREATE INDEX release_activation_requests_game_created_idx ON release_activation_requests (game_id, created_at DESC);

CREATE FUNCTION public.prevent_activation_request_rewrite()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status <> 'pending' OR NEW.request_id IS DISTINCT FROM OLD.request_id OR NEW.game_id IS DISTINCT FROM OLD.game_id
       OR NEW.realm_id IS DISTINCT FROM OLD.realm_id OR NEW.release_id IS DISTINCT FROM OLD.release_id
       OR NEW.requested_by IS DISTINCT FROM OLD.requested_by OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key THEN
        RAISE EXCEPTION 'activation requests are decided once and otherwise immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER release_activation_requests_decide_once
BEFORE UPDATE ON release_activation_requests
FOR EACH ROW EXECUTE FUNCTION public.prevent_activation_request_rewrite();
CREATE TRIGGER release_activation_requests_no_truncate BEFORE TRUNCATE ON release_activation_requests
FOR EACH STATEMENT EXECUTE FUNCTION public.prevent_snapshot_table_truncate();

-- Release 行只允许改变 status/activated_at，且状态只能流向 activated 或 deprecated。
CREATE FUNCTION public.restrict_release_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF (to_jsonb(NEW) - 'status' - 'activated_at') IS DISTINCT FROM (to_jsonb(OLD) - 'status' - 'activated_at') THEN
        RAISE EXCEPTION 'Release identity and manifest are immutable' USING ERRCODE = '23514';
    END IF;
    IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
        NEW.status IN ('activated', 'deprecated') AND OLD.status IN ('built', 'activated', 'deprecated')
    ) THEN
        RAISE EXCEPTION 'invalid Release status transition from % to %', OLD.status, NEW.status USING ERRCODE = '23514';
    END IF;
    IF OLD.activated_at IS NOT NULL AND NEW.activated_at IS DISTINCT FROM OLD.activated_at THEN
        RAISE EXCEPTION 'Release activated_at is write-once' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER releases_restricted_update BEFORE UPDATE ON releases
FOR EACH ROW EXECUTE FUNCTION public.restrict_release_update();

-- ===== 游戏运行面（runtime owner 独占写入） =====

CREATE TABLE player_accounts (
    account_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id text NOT NULL REFERENCES games (game_id),
    username text NOT NULL,
    password_hash text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT player_accounts_username_unique UNIQUE (game_id, username),
    CONSTRAINT player_accounts_username_format CHECK (username ~ '^[A-Za-z0-9_]{3,24}$'),
    CONSTRAINT player_accounts_hash_nonblank CHECK (length(password_hash) BETWEEN 20 AND 200)
);

CREATE TABLE player_sessions (
    token_hash bytea PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES player_accounts (account_id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT player_sessions_hash_length CHECK (octet_length(token_hash) = 32)
);
CREATE INDEX player_sessions_account_idx ON player_sessions (account_id);
CREATE INDEX player_sessions_expiry_idx ON player_sessions (expires_at);

CREATE UNIQUE INDEX characters_name_unique_idx ON characters (game_id, realm_id, (attributes->>'name'));
CREATE INDEX characters_owner_idx ON characters (game_id, realm_id, owner_subject);

CREATE TABLE inventory_items (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    character_id text NOT NULL,
    item_id text NOT NULL,
    quantity integer NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, character_id, item_id),
    CONSTRAINT inventory_items_character_fk FOREIGN KEY (game_id, realm_id, character_id) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT inventory_items_quantity_range CHECK (quantity BETWEEN 1 AND 9999)
);

CREATE TABLE quest_states (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    character_id text NOT NULL,
    quest_id text NOT NULL,
    status text NOT NULL,
    stage_index integer NOT NULL DEFAULT 0,
    stage_progress integer NOT NULL DEFAULT 0,
    completed_count integer NOT NULL DEFAULT 0,
    release_id text NOT NULL,
    accepted_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, character_id, quest_id),
    CONSTRAINT quest_states_character_fk FOREIGN KEY (game_id, realm_id, character_id) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT quest_states_status_valid CHECK (status IN ('active', 'objectives_done', 'completed')),
    CONSTRAINT quest_states_progress_nonnegative CHECK (stage_index >= 0 AND stage_progress >= 0 AND completed_count >= 0)
);

CREATE TABLE progress_facts (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    character_id text NOT NULL,
    scope text NOT NULL,
    fact_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, character_id, scope, fact_key),
    CONSTRAINT progress_facts_character_fk FOREIGN KEY (game_id, realm_id, character_id) REFERENCES characters (game_id, realm_id, character_id)
);

CREATE TABLE battle_records (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    battle_id uuid NOT NULL,
    encounter_id text NOT NULL,
    release_id text NOT NULL,
    seed bigint NOT NULL,
    outcome text NOT NULL,
    rounds integer NOT NULL,
    participants jsonb NOT NULL,
    context jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, battle_id),
    CONSTRAINT battle_records_realm_fk FOREIGN KEY (game_id, realm_id) REFERENCES realms (game_id, realm_id),
    CONSTRAINT battle_records_outcome_valid CHECK (outcome IN ('victory', 'defeat', 'fled', 'timeout')),
    CONSTRAINT battle_records_participants_array CHECK (jsonb_typeof(participants) = 'array')
);

CREATE TABLE pets (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    pet_uid uuid NOT NULL,
    character_id text NOT NULL,
    pet_id text NOT NULL,
    name text NOT NULL,
    level integer NOT NULL DEFAULT 1,
    exp bigint NOT NULL DEFAULT 0,
    active boolean NOT NULL DEFAULT false,
    state_version bigint NOT NULL DEFAULT 1,
    source_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, pet_uid),
    CONSTRAINT pets_character_fk FOREIGN KEY (game_id, realm_id, character_id) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT pets_source_unique UNIQUE (game_id, realm_id, character_id, source_key),
    CONSTRAINT pets_name_length CHECK (length(name) BETWEEN 1 AND 24),
    CONSTRAINT pets_level_range CHECK (level BETWEEN 1 AND 200 AND exp >= 0)
);
CREATE UNIQUE INDEX pets_one_active_idx ON pets (game_id, realm_id, character_id) WHERE active;

CREATE TABLE partners (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    character_id text NOT NULL,
    partner_id text NOT NULL,
    in_formation boolean NOT NULL DEFAULT false,
    recruited_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, character_id, partner_id),
    CONSTRAINT partners_character_fk FOREIGN KEY (game_id, realm_id, character_id) REFERENCES characters (game_id, realm_id, character_id)
);

CREATE TABLE mounts (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    character_id text NOT NULL,
    mount_id text NOT NULL,
    equipped boolean NOT NULL DEFAULT false,
    acquired_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, character_id, mount_id),
    CONSTRAINT mounts_character_fk FOREIGN KEY (game_id, realm_id, character_id) REFERENCES characters (game_id, realm_id, character_id)
);
CREATE UNIQUE INDEX mounts_one_equipped_idx ON mounts (game_id, realm_id, character_id) WHERE equipped;

CREATE TABLE shop_purchases (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    character_id text NOT NULL,
    shop_id text NOT NULL,
    item_id text NOT NULL,
    quantity integer NOT NULL,
    PRIMARY KEY (game_id, realm_id, character_id, shop_id, item_id),
    CONSTRAINT shop_purchases_character_fk FOREIGN KEY (game_id, realm_id, character_id) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT shop_purchases_quantity_positive CHECK (quantity > 0)
);

CREATE TABLE shop_stock (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    shop_id text NOT NULL,
    item_id text NOT NULL,
    remaining bigint NOT NULL,
    PRIMARY KEY (game_id, realm_id, shop_id, item_id),
    CONSTRAINT shop_stock_realm_fk FOREIGN KEY (game_id, realm_id) REFERENCES realms (game_id, realm_id),
    CONSTRAINT shop_stock_nonnegative CHECK (remaining >= 0)
);

CREATE TABLE friend_requests (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    from_id text NOT NULL,
    to_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, from_id, to_id),
    CONSTRAINT friend_requests_from_fk FOREIGN KEY (game_id, realm_id, from_id) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT friend_requests_to_fk FOREIGN KEY (game_id, realm_id, to_id) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT friend_requests_not_self CHECK (from_id <> to_id)
);
CREATE INDEX friend_requests_to_idx ON friend_requests (game_id, realm_id, to_id);

CREATE TABLE friendships (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    low_id text NOT NULL,
    high_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, low_id, high_id),
    CONSTRAINT friendships_low_fk FOREIGN KEY (game_id, realm_id, low_id) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT friendships_high_fk FOREIGN KEY (game_id, realm_id, high_id) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT friendships_ordered CHECK (low_id < high_id)
);
CREATE INDEX friendships_high_idx ON friendships (game_id, realm_id, high_id);

CREATE TABLE guilds (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    guild_id uuid NOT NULL,
    name text NOT NULL,
    leader_id text NOT NULL,
    notice text NOT NULL DEFAULT '',
    state_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, guild_id),
    CONSTRAINT guilds_name_unique UNIQUE (game_id, realm_id, name),
    CONSTRAINT guilds_leader_fk FOREIGN KEY (game_id, realm_id, leader_id) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT guilds_name_length CHECK (length(name) BETWEEN 2 AND 24),
    CONSTRAINT guilds_notice_length CHECK (length(notice) <= 1024)
);

CREATE TABLE guild_members (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    character_id text NOT NULL,
    guild_id uuid NOT NULL,
    role text NOT NULL,
    joined_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, character_id),
    CONSTRAINT guild_members_guild_fk FOREIGN KEY (game_id, realm_id, guild_id) REFERENCES guilds (game_id, realm_id, guild_id) ON DELETE CASCADE,
    CONSTRAINT guild_members_character_fk FOREIGN KEY (game_id, realm_id, character_id) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT guild_members_role_valid CHECK (role IN ('leader', 'officer', 'member'))
);
CREATE INDEX guild_members_guild_idx ON guild_members (game_id, realm_id, guild_id);
CREATE INDEX guilds_leader_idx ON guilds (game_id, realm_id, leader_id);
CREATE UNIQUE INDEX guild_members_one_leader_idx ON guild_members (game_id, realm_id, guild_id) WHERE role = 'leader';

CREATE TABLE guild_invites (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    guild_id uuid NOT NULL,
    character_id text NOT NULL,
    kind text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, guild_id, character_id),
    CONSTRAINT guild_invites_guild_fk FOREIGN KEY (game_id, realm_id, guild_id) REFERENCES guilds (game_id, realm_id, guild_id) ON DELETE CASCADE,
    CONSTRAINT guild_invites_character_fk FOREIGN KEY (game_id, realm_id, character_id) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT guild_invites_kind_valid CHECK (kind IN ('invite', 'application'))
);
CREATE INDEX guild_invites_character_idx ON guild_invites (game_id, realm_id, character_id);

CREATE TABLE trade_records (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    trade_id uuid NOT NULL,
    party_a text NOT NULL,
    party_b text NOT NULL,
    offer_a jsonb NOT NULL,
    offer_b jsonb NOT NULL,
    tax bigint NOT NULL DEFAULT 0,
    release_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, trade_id),
    CONSTRAINT trade_records_a_fk FOREIGN KEY (game_id, realm_id, party_a) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT trade_records_b_fk FOREIGN KEY (game_id, realm_id, party_b) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT trade_records_distinct CHECK (party_a <> party_b),
    CONSTRAINT trade_records_tax_nonnegative CHECK (tax >= 0)
);
CREATE INDEX trade_records_party_a_idx ON trade_records (game_id, realm_id, party_a);
CREATE INDEX trade_records_party_b_idx ON trade_records (game_id, realm_id, party_b);

CREATE TABLE dungeon_runs (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    run_id uuid NOT NULL,
    dungeon_id text NOT NULL,
    release_id text NOT NULL,
    definition jsonb NOT NULL,
    members jsonb NOT NULL,
    stage_index integer NOT NULL DEFAULT 0,
    status text NOT NULL DEFAULT 'active',
    state_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, run_id),
    CONSTRAINT dungeon_runs_realm_fk FOREIGN KEY (game_id, realm_id) REFERENCES realms (game_id, realm_id),
    CONSTRAINT dungeon_runs_status_valid CHECK (status IN ('active', 'completed', 'failed', 'abandoned')),
    CONSTRAINT dungeon_runs_members_array CHECK (jsonb_typeof(members) = 'array')
);
CREATE INDEX dungeon_runs_active_idx ON dungeon_runs (game_id, realm_id) WHERE status = 'active';

CREATE TABLE dungeon_entries (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    character_id text NOT NULL,
    dungeon_id text NOT NULL,
    run_id uuid NOT NULL,
    entry_day date NOT NULL,
    PRIMARY KEY (game_id, realm_id, character_id, run_id),
    CONSTRAINT dungeon_entries_run_fk FOREIGN KEY (game_id, realm_id, run_id) REFERENCES dungeon_runs (game_id, realm_id, run_id),
    CONSTRAINT dungeon_entries_character_fk FOREIGN KEY (game_id, realm_id, character_id) REFERENCES characters (game_id, realm_id, character_id)
);
CREATE INDEX dungeon_entries_daily_idx ON dungeon_entries (game_id, realm_id, character_id, dungeon_id, entry_day);

CREATE TABLE dungeon_rewards (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    run_id uuid NOT NULL,
    character_id text NOT NULL,
    claimed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, run_id, character_id),
    CONSTRAINT dungeon_rewards_entry_fk FOREIGN KEY (game_id, realm_id, character_id, run_id)
        REFERENCES dungeon_entries (game_id, realm_id, character_id, run_id)
);

CREATE TABLE event_progress (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    event_id text NOT NULL,
    window_start timestamptz NOT NULL,
    character_id text NOT NULL,
    progress integer NOT NULL DEFAULT 0,
    score bigint NOT NULL DEFAULT 0,
    claims integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, event_id, window_start, character_id),
    CONSTRAINT event_progress_character_fk FOREIGN KEY (game_id, realm_id, character_id) REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT event_progress_nonnegative CHECK (progress >= 0 AND score >= 0 AND claims >= 0)
);
CREATE INDEX event_progress_rank_idx ON event_progress (game_id, realm_id, event_id, window_start, score DESC, updated_at);

-- ===== 快照构建函数（由 build owner 持有） =====

REVOKE ALL ON admin_snapshot_jobs, admin_snapshot_job_entries, release_activation_requests FROM PUBLIC;
REVOKE ALL ON admin_snapshot_jobs, admin_snapshot_job_entries, release_activation_requests, player_accounts, player_sessions,
    inventory_items, quest_states, progress_facts, battle_records, pets, partners, mounts, shop_purchases, shop_stock,
    friend_requests, friendships, guilds, guild_members, guild_invites, trade_records, dungeon_runs, dungeon_entries,
    dungeon_rewards, event_progress FROM PUBLIC, tbcg_admin_control_plane, tbcg_content_build_worker;
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_class c
        WHERE c.relnamespace = 'public'::regnamespace
          AND c.relname = ANY(ARRAY['player_accounts','player_sessions','inventory_items','quest_states','pets','guilds','trade_records','dungeon_rewards'])
          AND (has_any_column_privilege('tbcg_admin_control_plane', c.oid, 'SELECT') OR has_any_column_privilege('tbcg_content_build_worker', c.oid, 'SELECT'))
    ) THEN
        RAISE EXCEPTION 'admin or worker roles inherit privileges on runtime player tables';
    END IF;
END;
$$;
GRANT SELECT, INSERT, UPDATE ON admin_snapshot_jobs TO tbcg_content_build_owner;
GRANT SELECT, INSERT ON admin_snapshot_job_entries TO tbcg_content_build_owner;
GRANT SELECT, INSERT ON release_activation_requests TO tbcg_content_build_owner;
GRANT SELECT (game_id, realm_id, display_name, active_release_id) ON realms TO tbcg_content_build_owner;

CREATE FUNCTION public.assert_game_role(p_game_id text, p_actor_id uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.admin_role_bindings WHERE game_id = p_game_id AND admin_id = p_actor_id) THEN
        RAISE EXCEPTION 'a Game role is required' USING ERRCODE = '42501';
    END IF;
END;
$$;

CREATE FUNCTION public.snapshot_job_public_json(p_job_id uuid)
RETURNS jsonb
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
    SELECT jsonb_build_object(
        'job_id', job_id, 'game_id', game_id, 'kind', 'game_snapshot', 'release_id', release_id,
        'release_version', release_version, 'entry_count', entry_count, 'status', status,
        'created_at', created_at, 'updated_at', updated_at,
        'public_artifact_sha256', CASE WHEN public_artifact_sha256 IS NULL THEN NULL ELSE encode(public_artifact_sha256, 'hex') END,
        'error_code', error_code)
    FROM public.admin_snapshot_jobs WHERE job_id = p_job_id
$$;

-- 返回将被冻结的最新已批准 revision 集合与需要的图标，供 admin 从受信目录读取资产。
CREATE FUNCTION public.admin_preview_snapshot(p_game_id text, p_actor_id uuid)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    profile public.game_build_profiles%ROWTYPE;
    result jsonb;
BEGIN
    PERFORM public.assert_game_publisher(p_game_id, p_actor_id);
    SELECT * INTO profile FROM public.game_build_profiles WHERE game_id = p_game_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Game has no trusted build profile' USING ERRCODE = '55000';
    END IF;
    WITH latest AS (
        SELECT DISTINCT ON (c.content_id) c.content_id, c.content_type, c.revision, a.body_hash, c.body
        FROM public.content_revisions c
        JOIN public.revision_approvals a USING (game_id, content_id, revision)
        WHERE c.game_id = p_game_id AND a.decision = 'approved' AND c.schema_version = profile.content_schema_version
        ORDER BY c.content_id, c.revision DESC
    )
    SELECT jsonb_build_object(
        'profile_version', profile.profile_version,
        'entries', COALESCE(jsonb_agg(jsonb_build_object('content_id', content_id, 'content_type', content_type,
            'revision', revision, 'body_hash', encode(body_hash, 'hex')) ORDER BY content_id), '[]'::jsonb),
        'icon_ids', COALESCE((SELECT jsonb_agg(DISTINCT l2.body->'public'->>'icon_id') FROM latest l2
            WHERE l2.content_type = 'item' AND l2.body->'public'->>'icon_id' IS NOT NULL), '[]'::jsonb))
    INTO result FROM latest;
    RETURN result;
END;
$$;

CREATE FUNCTION public.admin_enqueue_snapshot_build(
    p_game_id text, p_actor_id uuid, p_idempotency_key text, p_request_fingerprint bytea,
    p_expected_profile_version bigint, p_assets jsonb, p_schema_fingerprint text
)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    existing public.admin_snapshot_jobs%ROWTYPE;
    profile public.game_build_profiles%ROWTYPE;
    next_version bigint;
    new_job_id uuid;
    entries jsonb;
    entry_total integer;
    asset jsonb;
    active_jobs bigint;
BEGIN
    PERFORM public.assert_game_publisher(p_game_id, p_actor_id);
    IF p_request_fingerprint IS NULL OR octet_length(p_request_fingerprint) <> 32
       OR p_idempotency_key IS NULL OR length(p_idempotency_key) NOT BETWEEN 16 AND 128 OR p_idempotency_key !~ '^[A-Za-z0-9._~-]+$'
       OR p_expected_profile_version IS NULL OR p_expected_profile_version < 1
       OR p_schema_fingerprint IS NULL OR p_schema_fingerprint !~ '^[a-f0-9]{64}$'
       OR jsonb_typeof(p_assets) IS DISTINCT FROM 'array' OR jsonb_array_length(p_assets) > 256 THEN
        RAISE EXCEPTION 'invalid snapshot build input' USING ERRCODE = '22023';
    END IF;
    SELECT * INTO existing FROM public.admin_snapshot_jobs
    WHERE game_id = p_game_id AND requested_by = p_actor_id AND idempotency_key = p_idempotency_key;
    IF FOUND THEN
        IF existing.request_fingerprint <> p_request_fingerprint THEN
            RAISE EXCEPTION 'idempotency key is bound to a different snapshot request'
                USING ERRCODE = '23505', CONSTRAINT = 'admin_snapshot_jobs_idempotency_unique';
        END IF;
        RETURN public.snapshot_job_public_json(existing.job_id);
    END IF;
    SELECT * INTO profile FROM public.game_build_profiles WHERE game_id = p_game_id FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Game has no trusted build profile' USING ERRCODE = '55000';
    END IF;
    IF profile.profile_version <> p_expected_profile_version THEN
        RAISE EXCEPTION 'Game build profile changed while preparing the build' USING ERRCODE = '40001';
    END IF;
    FOR asset IN SELECT value FROM jsonb_array_elements(p_assets) LOOP
        IF jsonb_typeof(asset->'asset_id') IS DISTINCT FROM 'string'
           OR jsonb_typeof(asset->'manifest_bytes') IS DISTINCT FROM 'string'
           OR jsonb_typeof(asset->'asset_bytes') IS DISTINCT FROM 'string'
           OR sha256(decode(asset->>'manifest_bytes', 'base64')) <> profile.asset_manifest_sha256 THEN
            RAISE EXCEPTION 'frozen licensed asset does not match trusted Game profile' USING ERRCODE = '23514';
        END IF;
    END LOOP;

    INSERT INTO public.release_version_counters (game_id, next_release_version)
    SELECT p_game_id, COALESCE(MAX(r.release_version::bigint), 0) + 1 FROM public.releases r WHERE r.game_id = p_game_id
    ON CONFLICT (game_id) DO NOTHING;
    SELECT next_release_version INTO next_version FROM public.release_version_counters WHERE game_id = p_game_id FOR UPDATE;
    IF next_version > 2147483647 THEN
        RAISE EXCEPTION 'Game Release version range is exhausted' USING ERRCODE = '22003';
    END IF;
    SELECT * INTO existing FROM public.admin_snapshot_jobs
    WHERE game_id = p_game_id AND requested_by = p_actor_id AND idempotency_key = p_idempotency_key;
    IF FOUND THEN
        IF existing.request_fingerprint <> p_request_fingerprint THEN
            RAISE EXCEPTION 'idempotency key is bound to a different snapshot request'
                USING ERRCODE = '23505', CONSTRAINT = 'admin_snapshot_jobs_idempotency_unique';
        END IF;
        RETURN public.snapshot_job_public_json(existing.job_id);
    END IF;
    SELECT count(*) INTO active_jobs FROM public.admin_snapshot_jobs
    WHERE game_id = p_game_id AND status IN ('queued', 'running', 'retry_wait');
    IF active_jobs >= 5 THEN
        RAISE EXCEPTION 'Game snapshot build queue is full' USING ERRCODE = '53400';
    END IF;

    WITH latest AS (
        SELECT DISTINCT ON (c.content_id) c.content_id, c.content_type, c.revision, a.body_hash, c.body
        FROM public.content_revisions c
        JOIN public.revision_approvals a USING (game_id, content_id, revision)
        WHERE c.game_id = p_game_id AND a.decision = 'approved' AND c.schema_version = profile.content_schema_version
        ORDER BY c.content_id, c.revision DESC
    )
    SELECT jsonb_agg(jsonb_build_object('content_id', content_id, 'content_type', content_type, 'revision', revision,
        'body_hash', encode(body_hash, 'hex'), 'definition', body) ORDER BY content_id), count(*)
    INTO entries, entry_total FROM latest;
    IF entry_total IS NULL OR entry_total < 1 THEN
        RAISE EXCEPTION 'Game has no approved content to snapshot' USING ERRCODE = 'P0002';
    END IF;
    IF entry_total > 2000 THEN
        RAISE EXCEPTION 'Game snapshot exceeds 2000 entries' USING ERRCODE = '54000';
    END IF;
    UPDATE public.release_version_counters SET next_release_version = next_version + 1 WHERE game_id = p_game_id;
    new_job_id := gen_random_uuid();
    INSERT INTO public.admin_snapshot_jobs (job_id, game_id, requested_by, idempotency_key, request_fingerprint,
        release_id, release_version, entry_count, input_snapshot)
    VALUES (new_job_id, p_game_id, p_actor_id, p_idempotency_key, p_request_fingerprint,
        'snapshot-' || replace(gen_random_uuid()::text, '-', ''), next_version::integer, entry_total,
        jsonb_build_object(
            'profile', jsonb_build_object(
                'profile_version', profile.profile_version,
                'content_schema_version', profile.content_schema_version,
                'instance_schema_version', profile.instance_schema_version,
                'ruleset_version', profile.ruleset_version,
                'protocol_version', profile.protocol_version,
                'client_min_build', profile.client_min_build,
                'asset_manifest_sha256', encode(profile.asset_manifest_sha256, 'hex')),
            'schema_fingerprint', p_schema_fingerprint,
            'builder_version', 'game-snapshot-builder-v1',
            'entries', entries,
            'assets', p_assets));
    INSERT INTO public.admin_snapshot_job_entries (job_id, game_id, content_id, revision, body_hash)
    SELECT new_job_id, p_game_id, e->>'content_id', (e->>'revision')::integer, decode(e->>'body_hash', 'hex')
    FROM jsonb_array_elements(entries) AS e;
    INSERT INTO public.audit_logs (game_id, actor_id, action, resource_type, resource_id, outcome, details)
    VALUES (p_game_id, p_actor_id, 'release.snapshot.request', 'snapshot_job', new_job_id::text, 'success',
        jsonb_build_object('entry_count', entry_total, 'release_version', next_version));
    RETURN public.snapshot_job_public_json(new_job_id);
END;
$$;

CREATE FUNCTION public.admin_get_snapshot_job(p_game_id text, p_actor_id uuid, p_job_id uuid)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    PERFORM public.assert_game_publisher(p_game_id, p_actor_id);
    IF NOT EXISTS (SELECT 1 FROM public.admin_snapshot_jobs WHERE job_id = p_job_id AND game_id = p_game_id) THEN
        RAISE EXCEPTION 'snapshot job not found' USING ERRCODE = 'P0002';
    END IF;
    RETURN public.snapshot_job_public_json(p_job_id);
END;
$$;

CREATE FUNCTION public.admin_list_releases(p_game_id text, p_actor_id uuid)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    PERFORM public.assert_game_role(p_game_id, p_actor_id);
    RETURN jsonb_build_object(
        'releases', COALESCE((SELECT jsonb_agg(jsonb_build_object(
            'release_id', r.release_id, 'release_version', r.release_version, 'status', r.status,
            'scope', r.manifest->>'scope', 'created_at', r.created_at, 'activated_at', r.activated_at,
            'entry_count', (SELECT count(*) FROM public.release_entries e WHERE e.game_id = r.game_id AND e.release_id = r.release_id),
            'public_artifact_sha256', r.manifest->>'public_artifact_sha256') ORDER BY r.release_version DESC)
            FROM (SELECT * FROM public.releases WHERE game_id = p_game_id ORDER BY release_version DESC LIMIT 50) r), '[]'::jsonb),
        'realms', COALESCE((SELECT jsonb_agg(jsonb_build_object('realm_id', realm_id, 'display_name', display_name,
            'active_release_id', active_release_id) ORDER BY realm_id) FROM public.realms WHERE game_id = p_game_id), '[]'::jsonb),
        'snapshot_jobs', COALESCE((SELECT jsonb_agg(public.snapshot_job_public_json(j.job_id) ORDER BY j.created_at DESC)
            FROM (SELECT job_id, created_at FROM public.admin_snapshot_jobs WHERE game_id = p_game_id ORDER BY created_at DESC LIMIT 20) j), '[]'::jsonb),
        'activations', COALESCE((SELECT jsonb_agg(jsonb_build_object('request_id', a.request_id, 'realm_id', a.realm_id,
            'release_id', a.release_id, 'status', a.status, 'error_code', a.error_code, 'previous_release_id', a.previous_release_id,
            'created_at', a.created_at, 'decided_at', a.decided_at) ORDER BY a.created_at DESC)
            FROM (SELECT * FROM public.release_activation_requests WHERE game_id = p_game_id ORDER BY created_at DESC LIMIT 20) a), '[]'::jsonb));
END;
$$;

CREATE FUNCTION public.admin_request_activation(
    p_game_id text, p_realm_id text, p_actor_id uuid, p_release_id text, p_idempotency_key text
)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    existing public.release_activation_requests%ROWTYPE;
    release_scope text;
    release_status text;
    new_id uuid;
BEGIN
    PERFORM public.assert_game_publisher(p_game_id, p_actor_id);
    IF p_idempotency_key IS NULL OR length(p_idempotency_key) NOT BETWEEN 16 AND 128 OR p_idempotency_key !~ '^[A-Za-z0-9._~-]+$'
       OR p_realm_id IS NULL OR p_release_id IS NULL THEN
        RAISE EXCEPTION 'invalid activation idempotency key' USING ERRCODE = '22023';
    END IF;
    -- 同一 Realm 的激活请求串行化：幂等重放、待确认唯一性检查与插入之间没有竞态窗口。
    PERFORM pg_advisory_xact_lock(hashtextextended('tbcg-activation:' || p_game_id || ':' || p_realm_id, 0));
    SELECT * INTO existing FROM public.release_activation_requests
    WHERE game_id = p_game_id AND requested_by = p_actor_id AND idempotency_key = p_idempotency_key;
    IF FOUND THEN
        IF existing.realm_id <> p_realm_id OR existing.release_id <> p_release_id THEN
            RAISE EXCEPTION 'idempotency key is bound to a different activation'
                USING ERRCODE = '23505', CONSTRAINT = 'release_activation_requests_key_unique';
        END IF;
        RETURN jsonb_build_object('request_id', existing.request_id, 'status', existing.status, 'release_id', existing.release_id, 'realm_id', existing.realm_id);
    END IF;
    SELECT manifest->>'scope', status INTO release_scope, release_status FROM public.releases
    WHERE game_id = p_game_id AND release_id = p_release_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Release not found' USING ERRCODE = 'P0002';
    END IF;
    IF release_scope IS DISTINCT FROM 'game_snapshot' OR release_status NOT IN ('built', 'activated', 'deprecated') THEN
        RAISE EXCEPTION 'only built full Game snapshots can be activated' USING ERRCODE = '55000';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.realms WHERE game_id = p_game_id AND realm_id = p_realm_id) THEN
        RAISE EXCEPTION 'Realm not found' USING ERRCODE = 'P0002';
    END IF;
    IF EXISTS (SELECT 1 FROM public.release_activation_requests WHERE game_id = p_game_id AND realm_id = p_realm_id AND status = 'pending') THEN
        RAISE EXCEPTION 'Realm already has a pending activation' USING ERRCODE = '55006';
    END IF;
    INSERT INTO public.release_activation_requests (game_id, realm_id, release_id, requested_by, idempotency_key)
    VALUES (p_game_id, p_realm_id, p_release_id, p_actor_id, p_idempotency_key) RETURNING request_id INTO new_id;
    INSERT INTO public.audit_logs (game_id, actor_id, action, resource_type, resource_id, outcome, details)
    VALUES (p_game_id, p_actor_id, 'release.activation.request', 'release', p_release_id, 'success',
        jsonb_build_object('realm_id', p_realm_id, 'request_id', new_id));
    RETURN jsonb_build_object('request_id', new_id, 'status', 'pending', 'release_id', p_release_id, 'realm_id', p_realm_id);
END;
$$;

CREATE FUNCTION public.snapshot_job_claim_json(p_job_id uuid)
RETURNS jsonb
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
    SELECT jsonb_build_object('job', public.snapshot_job_public_json(job_id), 'input_snapshot', input_snapshot,
        'claim_token', claim_token, 'claim_owner', claim_owner, 'lease_until', lease_until)
    FROM public.admin_snapshot_jobs WHERE job_id = p_job_id AND status = 'running'
$$;

CREATE FUNCTION public.admin_claim_snapshot_job(p_worker_instance_id uuid, p_lease_seconds integer)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    job public.admin_snapshot_jobs%ROWTYPE;
    available uuid;
BEGIN
    PERFORM public.assert_build_worker(p_worker_instance_id);
    IF p_lease_seconds IS NULL OR p_lease_seconds NOT BETWEEN 5 AND 300 THEN
        RAISE EXCEPTION 'invalid snapshot lease duration' USING ERRCODE = '22023';
    END IF;
    SELECT * INTO job FROM public.admin_snapshot_jobs
    WHERE status = 'running' AND claim_owner = p_worker_instance_id AND lease_until > now()
    ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED;
    IF FOUND THEN
        UPDATE public.admin_snapshot_jobs SET lease_until = now() + make_interval(secs => p_lease_seconds)
        WHERE job_id = job.job_id AND lease_until < now() + make_interval(secs => p_lease_seconds);
        RETURN public.snapshot_job_claim_json(job.job_id);
    END IF;
    -- 先回收调用者自己已过期的作业，避免与“每个 worker 一个 running 作业”唯一索引冲突。
    UPDATE public.admin_snapshot_jobs
    SET status = CASE WHEN attempt_count >= max_attempts THEN 'failed' ELSE 'retry_wait' END,
        error_code = CASE WHEN attempt_count >= max_attempts THEN 'attempt_limit' ELSE 'worker_lease_expired' END,
        next_attempt_at = now(), claim_token = NULL, claim_owner = NULL, lease_until = NULL
    WHERE status = 'running' AND claim_owner = p_worker_instance_id AND lease_until <= now();
    WITH expired AS (
        SELECT job_id, attempt_count, max_attempts FROM public.admin_snapshot_jobs
        WHERE status = 'running' AND lease_until <= now() ORDER BY lease_until LIMIT 20 FOR UPDATE SKIP LOCKED
    )
    UPDATE public.admin_snapshot_jobs t
    SET status = CASE WHEN expired.attempt_count >= expired.max_attempts THEN 'failed' ELSE 'retry_wait' END,
        error_code = CASE WHEN expired.attempt_count >= expired.max_attempts THEN 'attempt_limit' ELSE 'worker_lease_expired' END,
        next_attempt_at = now(), claim_token = NULL, claim_owner = NULL, lease_until = NULL
    FROM expired WHERE t.job_id = expired.job_id;
    SELECT job_id INTO available FROM public.admin_snapshot_jobs
    WHERE status IN ('queued', 'retry_wait') AND next_attempt_at <= now() AND attempt_count < max_attempts
    ORDER BY next_attempt_at, created_at LIMIT 1 FOR UPDATE SKIP LOCKED;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    UPDATE public.admin_snapshot_jobs
    SET status = 'running', attempt_count = attempt_count + 1, claim_token = gen_random_uuid(),
        claim_owner = p_worker_instance_id, lease_until = now() + make_interval(secs => p_lease_seconds), error_code = NULL
    WHERE job_id = available;
    RETURN public.snapshot_job_claim_json(available);
END;
$$;

CREATE FUNCTION public.admin_renew_snapshot_job_lease(p_job_id uuid, p_worker_instance_id uuid, p_claim_token uuid, p_lease_seconds integer)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    PERFORM public.assert_build_worker(p_worker_instance_id);
    IF p_lease_seconds IS NULL OR p_lease_seconds NOT BETWEEN 5 AND 300 THEN
        RAISE EXCEPTION 'invalid snapshot lease renewal' USING ERRCODE = '22023';
    END IF;
    UPDATE public.admin_snapshot_jobs SET lease_until = now() + make_interval(secs => p_lease_seconds)
    WHERE job_id = p_job_id AND status = 'running' AND claim_owner = p_worker_instance_id AND claim_token = p_claim_token
      AND lease_until > now() AND lease_until < now() + make_interval(secs => p_lease_seconds);
    RETURN EXISTS (SELECT 1 FROM public.admin_snapshot_jobs WHERE job_id = p_job_id AND status = 'running'
        AND claim_owner = p_worker_instance_id AND claim_token = p_claim_token AND lease_until > now());
END;
$$;

CREATE FUNCTION public.admin_fail_snapshot_job(
    p_job_id uuid, p_worker_instance_id uuid, p_claim_token uuid, p_error_code text, p_retryable boolean, p_backoff_seconds integer
)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    job public.admin_snapshot_jobs%ROWTYPE;
    next_status text;
BEGIN
    PERFORM public.assert_build_worker(p_worker_instance_id);
    IF p_job_id IS NULL OR p_claim_token IS NULL
       OR p_error_code IS NULL OR p_error_code !~ '^[a-z][a-z0-9_]{0,63}$' OR p_retryable IS NULL
       OR p_backoff_seconds IS NULL OR p_backoff_seconds NOT BETWEEN 0 AND 3600 THEN
        RAISE EXCEPTION 'invalid snapshot failure report' USING ERRCODE = '22023';
    END IF;
    SELECT * INTO job FROM public.admin_snapshot_jobs WHERE job_id = p_job_id FOR UPDATE;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    IF job.status IN ('retry_wait', 'failed') AND job.error_code = p_error_code THEN
        RETURN public.snapshot_job_public_json(job.job_id);
    END IF;
    IF job.status <> 'running' OR job.claim_owner IS DISTINCT FROM p_worker_instance_id
       OR job.claim_token IS DISTINCT FROM p_claim_token OR job.lease_until <= now() THEN
        RETURN NULL;
    END IF;
    next_status := CASE WHEN p_retryable AND job.attempt_count < job.max_attempts THEN 'retry_wait' ELSE 'failed' END;
    UPDATE public.admin_snapshot_jobs
    SET status = next_status, error_code = p_error_code,
        next_attempt_at = CASE WHEN next_status = 'retry_wait' THEN now() + make_interval(secs => p_backoff_seconds) ELSE now() END,
        claim_token = NULL, claim_owner = NULL, lease_until = NULL
    WHERE job_id = p_job_id;
    INSERT INTO public.audit_logs (game_id, actor_id, action, resource_type, resource_id, outcome, details)
    VALUES (job.game_id, job.requested_by, 'release.snapshot.' || CASE WHEN next_status = 'retry_wait' THEN 'retry' ELSE 'failed' END,
        'snapshot_job', job.job_id::text, 'failure', jsonb_build_object('error_code', p_error_code, 'attempt', job.attempt_count));
    RETURN public.snapshot_job_public_json(job.job_id);
END;
$$;

CREATE FUNCTION public.admin_finish_snapshot_job(
    p_job_id uuid, p_worker_instance_id uuid, p_claim_token uuid, p_manifest jsonb,
    p_public_artifact_sha256 bytea, p_private_artifact_sha256 bytea
)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    job public.admin_snapshot_jobs%ROWTYPE;
    profile jsonb;
    expected_revisions jsonb;
BEGIN
    PERFORM public.assert_build_worker(p_worker_instance_id);
    IF p_job_id IS NULL OR p_claim_token IS NULL OR p_manifest IS NULL
       OR jsonb_typeof(p_manifest) IS DISTINCT FROM 'object' OR pg_column_size(p_manifest) > 1048576
       OR p_public_artifact_sha256 IS NULL OR p_private_artifact_sha256 IS NULL
       OR octet_length(p_public_artifact_sha256) <> 32 OR octet_length(p_private_artifact_sha256) <> 32 THEN
        RAISE EXCEPTION 'invalid snapshot completion report' USING ERRCODE = '22023';
    END IF;
    SELECT * INTO job FROM public.admin_snapshot_jobs WHERE job_id = p_job_id FOR UPDATE;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    IF job.status = 'built' THEN
        IF job.manifest IS DISTINCT FROM p_manifest OR job.public_artifact_sha256 IS DISTINCT FROM p_public_artifact_sha256
           OR job.private_artifact_sha256 IS DISTINCT FROM p_private_artifact_sha256 THEN
            RAISE EXCEPTION 'built snapshot retry has different artifacts'
                USING ERRCODE = '23505', CONSTRAINT = 'admin_snapshot_jobs_built_valid';
        END IF;
        RETURN public.snapshot_job_public_json(job.job_id);
    END IF;
    IF job.status <> 'running' OR job.claim_owner IS DISTINCT FROM p_worker_instance_id
       OR job.claim_token IS DISTINCT FROM p_claim_token OR job.lease_until <= now() THEN
        RETURN NULL;
    END IF;
    profile := job.input_snapshot->'profile';
    SELECT jsonb_agg(jsonb_build_object('content_id', content_id, 'revision', revision, 'body_hash', encode(body_hash, 'hex'))
        ORDER BY content_id COLLATE "C")
    INTO expected_revisions FROM public.admin_snapshot_job_entries WHERE job_id = job.job_id;
    IF p_manifest->'format_version' IS DISTINCT FROM '1'::jsonb
       OR p_manifest->'scope' IS DISTINCT FROM '"game_snapshot"'::jsonb
       OR p_manifest->'game_id' IS DISTINCT FROM to_jsonb(job.game_id)
       OR p_manifest->'release_id' IS DISTINCT FROM to_jsonb(job.release_id)
       OR p_manifest->'release_version' IS DISTINCT FROM to_jsonb(job.release_version)
       OR p_manifest->'builder_version' IS DISTINCT FROM job.input_snapshot->'builder_version'
       OR p_manifest->'schema_fingerprint' IS DISTINCT FROM job.input_snapshot->'schema_fingerprint'
       OR p_manifest->'compatibility' IS DISTINCT FROM profile
       OR p_manifest->'content_revisions' IS DISTINCT FROM expected_revisions
       OR p_manifest->'public_artifact_sha256' IS DISTINCT FROM to_jsonb(encode(p_public_artifact_sha256, 'hex'))
       OR p_manifest->'private_artifact_sha256' IS DISTINCT FROM to_jsonb(encode(p_private_artifact_sha256, 'hex'))
       OR jsonb_typeof(p_manifest->'capabilities') IS DISTINCT FROM 'array'
       OR jsonb_typeof(p_manifest->'assets') IS DISTINCT FROM 'array'
       OR EXISTS (
            SELECT 1 FROM jsonb_array_elements(p_manifest->'assets') AS m(asset)
            WHERE NOT EXISTS (
                SELECT 1 FROM jsonb_array_elements(job.input_snapshot->'assets') AS f(frozen),
                     jsonb_array_elements(convert_from(decode(f.frozen->>'manifest_bytes', 'base64'), 'UTF8')::jsonb->'assets') AS r(row_entry)
                WHERE f.frozen->>'asset_id' = m.asset->>'asset_id' AND r.row_entry = m.asset))
       OR EXISTS (SELECT 1 FROM jsonb_array_elements(p_manifest->'capabilities') AS c(cap) WHERE jsonb_typeof(c.cap) <> 'string') THEN
        RAISE EXCEPTION 'snapshot completion manifest does not match frozen job input' USING ERRCODE = '23514';
    END IF;
    INSERT INTO public.releases (game_id, release_id, release_version, content_schema_version, instance_schema_version,
        ruleset_version, protocol_version, client_min_build, status, manifest)
    VALUES (job.game_id, job.release_id, job.release_version, (profile->>'content_schema_version')::integer,
        (profile->>'instance_schema_version')::integer, (profile->>'ruleset_version')::integer,
        (profile->>'protocol_version')::integer, profile->>'client_min_build', 'built', p_manifest);
    INSERT INTO public.release_entries (game_id, release_id, content_id, revision)
    SELECT job.game_id, job.release_id, content_id, revision FROM public.admin_snapshot_job_entries WHERE job_id = job.job_id;
    UPDATE public.admin_snapshot_jobs
    SET status = 'built', claim_token = NULL, claim_owner = NULL, lease_until = NULL, manifest = p_manifest,
        public_artifact_sha256 = p_public_artifact_sha256, private_artifact_sha256 = p_private_artifact_sha256, error_code = NULL
    WHERE job_id = job.job_id;
    INSERT INTO public.audit_logs (game_id, actor_id, action, resource_type, resource_id, outcome, details)
    VALUES (job.game_id, job.requested_by, 'release.snapshot.completed', 'release', job.release_id, 'success',
        jsonb_build_object('job_id', job.job_id, 'entry_count', job.entry_count, 'public_artifact_sha256', encode(p_public_artifact_sha256, 'hex')));
    RETURN public.snapshot_job_public_json(job.job_id);
END;
$$;

GRANT CREATE ON SCHEMA public TO tbcg_content_build_owner;
ALTER FUNCTION public.assert_game_role(text, uuid) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.snapshot_job_public_json(uuid) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.snapshot_job_claim_json(uuid) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_preview_snapshot(text, uuid) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_enqueue_snapshot_build(text, uuid, text, bytea, bigint, jsonb, text) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_get_snapshot_job(text, uuid, uuid) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_list_releases(text, uuid) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_request_activation(text, text, uuid, text, text) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_claim_snapshot_job(uuid, integer) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_renew_snapshot_job_lease(uuid, uuid, uuid, integer) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_fail_snapshot_job(uuid, uuid, uuid, text, boolean, integer) OWNER TO tbcg_content_build_owner;
ALTER FUNCTION public.admin_finish_snapshot_job(uuid, uuid, uuid, jsonb, bytea, bytea) OWNER TO tbcg_content_build_owner;
REVOKE CREATE ON SCHEMA public FROM tbcg_content_build_owner;
REVOKE ALL ON FUNCTION public.assert_game_role(text, uuid), public.snapshot_job_public_json(uuid), public.snapshot_job_claim_json(uuid),
    public.admin_preview_snapshot(text, uuid), public.admin_enqueue_snapshot_build(text, uuid, text, bytea, bigint, jsonb, text),
    public.admin_get_snapshot_job(text, uuid, uuid), public.admin_list_releases(text, uuid),
    public.admin_request_activation(text, text, uuid, text, text), public.admin_claim_snapshot_job(uuid, integer),
    public.admin_renew_snapshot_job_lease(uuid, uuid, uuid, integer), public.admin_fail_snapshot_job(uuid, uuid, uuid, text, boolean, integer),
    public.admin_finish_snapshot_job(uuid, uuid, uuid, jsonb, bytea, bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.admin_preview_snapshot(text, uuid), public.admin_enqueue_snapshot_build(text, uuid, text, bytea, bigint, jsonb, text),
    public.admin_get_snapshot_job(text, uuid, uuid), public.admin_list_releases(text, uuid),
    public.admin_request_activation(text, text, uuid, text, text) TO tbcg_admin_control_plane;
GRANT EXECUTE ON FUNCTION public.admin_claim_snapshot_job(uuid, integer), public.admin_renew_snapshot_job_lease(uuid, uuid, uuid, integer),
    public.admin_fail_snapshot_job(uuid, uuid, uuid, text, boolean, integer), public.admin_finish_snapshot_job(uuid, uuid, uuid, jsonb, bytea, bytea)
    TO tbcg_content_build_worker;

-- ===== runtime owner 权限组（存在时授予；由部署预置 NOLOGIN 组） =====
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tbcg_game_runtime') THEN
        GRANT SELECT ON games, releases, release_entries, realms, owner_guards TO tbcg_game_runtime;
        GRANT INSERT ON realms, owner_guards TO tbcg_game_runtime;
        GRANT UPDATE (active_release_id) ON realms TO tbcg_game_runtime;
        GRANT UPDATE (status, activated_at) ON releases TO tbcg_game_runtime;
        GRANT UPDATE (owner_epoch) ON owner_guards TO tbcg_game_runtime;
        GRANT SELECT, UPDATE ON release_activation_requests TO tbcg_game_runtime;
        GRANT SELECT, INSERT, UPDATE ON characters, wallets TO tbcg_game_runtime;
        GRANT SELECT, INSERT ON wallet_ledger, outbox_consumer_receipts TO tbcg_game_runtime;
        GRANT SELECT, INSERT, UPDATE ON operation_results, outbox TO tbcg_game_runtime;
        GRANT SELECT, INSERT, UPDATE, DELETE ON player_accounts, player_sessions, inventory_items, quest_states,
            pets, partners, mounts, shop_purchases, shop_stock, friend_requests, friendships, guilds,
            guild_members, guild_invites, dungeon_runs, event_progress TO tbcg_game_runtime;
        -- 追加型证据表：只读与插入，删除即可能绕过防重复领奖。
        GRANT SELECT, INSERT ON progress_facts, battle_records, trade_records, dungeon_entries, dungeon_rewards TO tbcg_game_runtime;
    ELSE
        RAISE NOTICE 'tbcg_game_runtime is not provisioned; game-server privileges were not granted';
    END IF;
END;
$$;

---- create above / drop below ----

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.releases WHERE manifest->>'scope' = 'game_snapshot') THEN
        RAISE EXCEPTION 'cannot roll back 005 while immutable full Game snapshot Releases exist';
    END IF;
END;
$$;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tbcg_game_runtime') THEN
        REVOKE SELECT ON games, releases, release_entries, realms, owner_guards FROM tbcg_game_runtime;
        REVOKE INSERT ON realms, owner_guards FROM tbcg_game_runtime;
        REVOKE UPDATE (active_release_id) ON realms FROM tbcg_game_runtime;
        REVOKE UPDATE (status, activated_at) ON releases FROM tbcg_game_runtime;
        REVOKE UPDATE (owner_epoch) ON owner_guards FROM tbcg_game_runtime;
        REVOKE SELECT, INSERT, UPDATE ON characters, wallets, operation_results, outbox FROM tbcg_game_runtime;
        REVOKE SELECT, INSERT ON wallet_ledger, outbox_consumer_receipts FROM tbcg_game_runtime;
    END IF;
END;
$$;
REVOKE ALL ON FUNCTION public.admin_preview_snapshot(text, uuid), public.admin_enqueue_snapshot_build(text, uuid, text, bytea, bigint, jsonb, text),
    public.admin_get_snapshot_job(text, uuid, uuid), public.admin_list_releases(text, uuid),
    public.admin_request_activation(text, text, uuid, text, text) FROM tbcg_admin_control_plane;
REVOKE ALL ON FUNCTION public.admin_claim_snapshot_job(uuid, integer), public.admin_renew_snapshot_job_lease(uuid, uuid, uuid, integer),
    public.admin_fail_snapshot_job(uuid, uuid, uuid, text, boolean, integer), public.admin_finish_snapshot_job(uuid, uuid, uuid, jsonb, bytea, bytea)
    FROM tbcg_content_build_worker;
DROP FUNCTION public.admin_finish_snapshot_job(uuid, uuid, uuid, jsonb, bytea, bytea);
DROP FUNCTION public.admin_fail_snapshot_job(uuid, uuid, uuid, text, boolean, integer);
DROP FUNCTION public.admin_renew_snapshot_job_lease(uuid, uuid, uuid, integer);
DROP FUNCTION public.admin_claim_snapshot_job(uuid, integer);
DROP FUNCTION public.snapshot_job_claim_json(uuid);
DROP FUNCTION public.admin_request_activation(text, text, uuid, text, text);
DROP FUNCTION public.admin_list_releases(text, uuid);
DROP FUNCTION public.admin_get_snapshot_job(text, uuid, uuid);
DROP FUNCTION public.admin_enqueue_snapshot_build(text, uuid, text, bytea, bigint, jsonb, text);
DROP FUNCTION public.admin_preview_snapshot(text, uuid);
DROP FUNCTION public.snapshot_job_public_json(uuid);
DROP FUNCTION public.assert_game_role(text, uuid);
REVOKE SELECT (game_id, realm_id, display_name, active_release_id) ON realms FROM tbcg_content_build_owner;
REVOKE ALL ON admin_snapshot_jobs, admin_snapshot_job_entries, release_activation_requests FROM tbcg_content_build_owner;
DROP TABLE event_progress;
DROP TABLE dungeon_rewards;
DROP TABLE dungeon_entries;
DROP TABLE dungeon_runs;
DROP TABLE trade_records;
DROP TABLE guild_invites;
DROP TABLE guild_members;
DROP TABLE guilds;
DROP TABLE friendships;
DROP TABLE friend_requests;
DROP TABLE shop_stock;
DROP TABLE shop_purchases;
DROP TABLE mounts;
DROP TABLE partners;
DROP TABLE pets;
DROP TABLE battle_records;
DROP TABLE progress_facts;
DROP TABLE quest_states;
DROP TABLE inventory_items;
DROP INDEX characters_owner_idx;
DROP INDEX characters_name_unique_idx;
DROP TABLE player_sessions;
DROP TABLE player_accounts;
DROP TRIGGER releases_restricted_update ON releases;
DROP FUNCTION public.restrict_release_update();
DROP TRIGGER release_activation_requests_no_truncate ON release_activation_requests;
DROP TRIGGER release_activation_requests_decide_once ON release_activation_requests;
DROP FUNCTION public.prevent_activation_request_rewrite();
DROP TABLE release_activation_requests;
CREATE OR REPLACE FUNCTION public.guard_release_entry_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    release_status text;
    release_activated_at timestamptz;
BEGIN
    IF current_user = 'tbcg_content_build_owner' THEN
        SELECT status, activated_at INTO release_status, release_activated_at
        FROM public.releases WHERE game_id = NEW.game_id AND release_id = NEW.release_id;
    ELSE
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
DROP TRIGGER admin_snapshot_job_entries_immutable ON admin_snapshot_job_entries;
DROP FUNCTION public.prevent_admin_snapshot_entry_mutation();
DROP TRIGGER admin_snapshot_job_entries_no_truncate ON admin_snapshot_job_entries;
DROP TRIGGER admin_snapshot_jobs_no_truncate ON admin_snapshot_jobs;
DROP TRIGGER admin_snapshot_jobs_immutable_input ON admin_snapshot_jobs;
DROP FUNCTION public.prevent_admin_snapshot_job_mutation();
DROP TABLE admin_snapshot_job_entries;
DROP TABLE admin_snapshot_jobs;
DROP FUNCTION public.prevent_snapshot_table_truncate();
