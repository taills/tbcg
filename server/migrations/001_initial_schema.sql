CREATE TABLE games (
    game_id text PRIMARY KEY,
    display_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT games_id_not_blank CHECK (length(btrim(game_id)) > 0)
);

CREATE TABLE content_revisions (
    game_id text NOT NULL,
    content_id text NOT NULL,
    revision integer NOT NULL,
    schema_version integer NOT NULL,
    content_type text NOT NULL,
    body jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, content_id, revision),
    CONSTRAINT content_revisions_game_fk
        FOREIGN KEY (game_id) REFERENCES games (game_id),
    CONSTRAINT content_revisions_revision_positive CHECK (revision > 0),
    CONSTRAINT content_revisions_schema_version_positive CHECK (schema_version > 0),
    CONSTRAINT content_revisions_body_object CHECK (jsonb_typeof(body) = 'object')
);

CREATE TABLE releases (
    game_id text NOT NULL,
    release_id text NOT NULL,
    release_version integer NOT NULL,
    content_schema_version integer NOT NULL,
    instance_schema_version integer NOT NULL,
    ruleset_version integer NOT NULL,
    protocol_version integer NOT NULL,
    client_min_build text NOT NULL,
    status text NOT NULL,
    manifest jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    activated_at timestamptz,
    PRIMARY KEY (game_id, release_id),
    CONSTRAINT releases_game_fk
        FOREIGN KEY (game_id) REFERENCES games (game_id),
    CONSTRAINT releases_version_unique UNIQUE (game_id, release_version),
    CONSTRAINT releases_version_positive CHECK (release_version > 0),
    CONSTRAINT releases_schema_versions_positive CHECK (
        content_schema_version > 0
        AND instance_schema_version > 0
        AND ruleset_version > 0
        AND protocol_version > 0
    ),
    CONSTRAINT releases_client_min_build_nonblank CHECK (length(btrim(client_min_build)) > 0),
    CONSTRAINT releases_status_valid
        CHECK (status IN ('draft', 'validated', 'reviewed', 'built', 'staged', 'activated', 'deprecated')),
    CONSTRAINT releases_manifest_object CHECK (jsonb_typeof(manifest) = 'object')
);

CREATE TABLE realms (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    display_name text NOT NULL,
    active_release_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id),
    CONSTRAINT realms_game_fk
        FOREIGN KEY (game_id) REFERENCES games (game_id),
    CONSTRAINT realms_active_release_fk
        FOREIGN KEY (game_id, active_release_id) REFERENCES releases (game_id, release_id)
);

CREATE TABLE release_entries (
    game_id text NOT NULL,
    release_id text NOT NULL,
    content_id text NOT NULL,
    revision integer NOT NULL,
    PRIMARY KEY (game_id, release_id, content_id),
    CONSTRAINT release_entries_release_fk
        FOREIGN KEY (game_id, release_id) REFERENCES releases (game_id, release_id),
    CONSTRAINT release_entries_revision_fk
        FOREIGN KEY (game_id, content_id, revision)
        REFERENCES content_revisions (game_id, content_id, revision)
);

CREATE TABLE owner_guards (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    owner_epoch bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (game_id, realm_id),
    CONSTRAINT owner_guards_realm_fk
        FOREIGN KEY (game_id, realm_id) REFERENCES realms (game_id, realm_id),
    CONSTRAINT owner_guards_epoch_nonnegative CHECK (owner_epoch >= 0)
);

CREATE FUNCTION public.prevent_owner_epoch_regression()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.game_id IS DISTINCT FROM OLD.game_id OR NEW.realm_id IS DISTINCT FROM OLD.realm_id THEN
        RAISE EXCEPTION 'owner_guard scope is immutable'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.owner_epoch <= OLD.owner_epoch THEN
        RAISE EXCEPTION 'owner_epoch must strictly increase from % to %', OLD.owner_epoch, NEW.owner_epoch
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER owner_guards_epoch_must_increase
BEFORE UPDATE ON owner_guards
FOR EACH ROW
EXECUTE FUNCTION public.prevent_owner_epoch_regression();

CREATE FUNCTION public.prevent_owner_guard_removal()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'owner_guards rows cannot be deleted or truncated'
        USING ERRCODE = '23514';
END;
$$;

CREATE TRIGGER owner_guards_no_delete
BEFORE DELETE ON owner_guards
FOR EACH ROW
EXECUTE FUNCTION public.prevent_owner_guard_removal();

CREATE TRIGGER owner_guards_no_truncate
BEFORE TRUNCATE ON owner_guards
FOR EACH STATEMENT
EXECUTE FUNCTION public.prevent_owner_guard_removal();

CREATE TABLE characters (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    character_id text NOT NULL,
    owner_subject text NOT NULL,
    state_version bigint NOT NULL DEFAULT 1,
    instance_schema_version integer NOT NULL,
    attributes jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, character_id),
    CONSTRAINT characters_realm_fk
        FOREIGN KEY (game_id, realm_id) REFERENCES realms (game_id, realm_id),
    CONSTRAINT characters_state_version_positive CHECK (state_version > 0),
    CONSTRAINT characters_schema_version_positive CHECK (instance_schema_version > 0),
    CONSTRAINT characters_attributes_object CHECK (jsonb_typeof(attributes) = 'object')
);

CREATE TABLE operation_results (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    actor_subject text NOT NULL,
    operation_name text NOT NULL,
    idempotency_key text NOT NULL,
    payload_hash bytea NOT NULL,
    result jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, actor_subject, operation_name, idempotency_key),
    CONSTRAINT operation_results_realm_fk
        FOREIGN KEY (game_id, realm_id) REFERENCES realms (game_id, realm_id),
    CONSTRAINT operation_results_payload_hash_sha256 CHECK (octet_length(payload_hash) = 32),
    CONSTRAINT operation_results_result_object CHECK (jsonb_typeof(result) = 'object')
);

CREATE TABLE wallets (
    game_id text NOT NULL,
    realm_id text NOT NULL,
    character_id text NOT NULL,
    currency_code text NOT NULL,
    balance bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, realm_id, character_id, currency_code),
    CONSTRAINT wallets_character_fk
        FOREIGN KEY (game_id, realm_id, character_id)
        REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT wallets_balance_nonnegative CHECK (balance >= 0)
);

CREATE TABLE wallet_ledger (
    ledger_id uuid PRIMARY KEY,
    game_id text NOT NULL,
    realm_id text NOT NULL,
    character_id text NOT NULL,
    currency_code text NOT NULL,
    actor_subject text NOT NULL,
    operation_name text NOT NULL,
    idempotency_key text NOT NULL,
    line_item_id text NOT NULL,
    amount bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT wallet_ledger_wallet_fk
        FOREIGN KEY (game_id, realm_id, character_id, currency_code)
        REFERENCES wallets (game_id, realm_id, character_id, currency_code),
    CONSTRAINT wallet_ledger_character_fk
        FOREIGN KEY (game_id, realm_id, character_id)
        REFERENCES characters (game_id, realm_id, character_id),
    CONSTRAINT wallet_ledger_operation_fk
        FOREIGN KEY (game_id, realm_id, actor_subject, operation_name, idempotency_key)
        REFERENCES operation_results (game_id, realm_id, actor_subject, operation_name, idempotency_key),
    CONSTRAINT wallet_ledger_operation_line_unique
        UNIQUE (game_id, realm_id, actor_subject, operation_name, idempotency_key, line_item_id),
    CONSTRAINT wallet_ledger_amount_nonzero CHECK (amount <> 0)
);

CREATE TABLE outbox (
    event_id uuid PRIMARY KEY,
    game_id text NOT NULL,
    realm_id text NOT NULL,
    actor_subject text NOT NULL,
    operation_name text NOT NULL,
    idempotency_key text NOT NULL,
    event_key text NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    available_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    lease_owner text,
    lease_expires_at timestamptz,
    CONSTRAINT outbox_operation_fk
        FOREIGN KEY (game_id, realm_id, actor_subject, operation_name, idempotency_key)
        REFERENCES operation_results (game_id, realm_id, actor_subject, operation_name, idempotency_key),
    CONSTRAINT outbox_event_idempotency_unique
        UNIQUE (game_id, realm_id, actor_subject, operation_name, idempotency_key, event_key),
    CONSTRAINT outbox_payload_object CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT outbox_attempts_nonnegative CHECK (attempts >= 0),
    CONSTRAINT outbox_lease_pair CHECK ((lease_owner IS NULL) = (lease_expires_at IS NULL))
);

CREATE INDEX realms_active_release_fk_idx
    ON realms (game_id, active_release_id)
    WHERE active_release_id IS NOT NULL;

CREATE INDEX release_entries_revision_fk_idx
    ON release_entries (game_id, content_id, revision);

CREATE INDEX wallet_ledger_character_history_idx
    ON wallet_ledger (game_id, realm_id, character_id, currency_code, created_at DESC);

CREATE INDEX outbox_pending_delivery_idx
    ON outbox (available_at, created_at)
    WHERE delivered_at IS NULL;

---- create above / drop below ----

DROP TABLE outbox;
DROP TABLE wallet_ledger;
DROP TABLE wallets;
DROP TABLE operation_results;
DROP TABLE characters;
DROP TABLE owner_guards;
DROP FUNCTION public.prevent_owner_guard_removal();
DROP FUNCTION public.prevent_owner_epoch_regression();
DROP TABLE release_entries;
DROP TABLE realms;
DROP TABLE releases;
DROP TABLE content_revisions;
DROP TABLE games;
