CREATE TABLE admin_users (
    admin_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT admin_users_username_nonblank CHECK (length(btrim(username)) > 0),
    CONSTRAINT admin_users_password_hash_nonblank CHECK (length(password_hash) > 0)
);

CREATE TABLE admin_sessions (
    session_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_id uuid REFERENCES admin_users (admin_id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    csrf_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    idle_expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT admin_sessions_token_hash_length CHECK (octet_length(token_hash) = 32),
    CONSTRAINT admin_sessions_csrf_hash_length CHECK (octet_length(csrf_hash) = 32),
    CONSTRAINT admin_sessions_expiry_after_create CHECK (expires_at > created_at),
    CONSTRAINT admin_sessions_idle_expiry_valid CHECK (idle_expires_at > created_at AND idle_expires_at <= expires_at)
);

CREATE TABLE admin_role_bindings (
    game_id text NOT NULL,
    admin_id uuid NOT NULL,
    role text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, admin_id, role),
    CONSTRAINT admin_role_bindings_game_fk FOREIGN KEY (game_id) REFERENCES games (game_id) ON DELETE CASCADE,
    CONSTRAINT admin_role_bindings_admin_fk FOREIGN KEY (admin_id) REFERENCES admin_users (admin_id) ON DELETE CASCADE,
    CONSTRAINT admin_role_bindings_role_valid CHECK (role IN ('Reader', 'Author', 'Reviewer', 'Publisher'))
);

CREATE TABLE content_drafts (
    draft_id uuid NOT NULL DEFAULT gen_random_uuid(),
    game_id text NOT NULL,
    content_id text NOT NULL,
    draft_version bigint NOT NULL DEFAULT 1,
    submitted_draft_version bigint,
    definition jsonb NOT NULL,
    updated_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, content_id),
    CONSTRAINT content_drafts_scope_id_unique UNIQUE (game_id, draft_id),
    CONSTRAINT content_drafts_game_fk FOREIGN KEY (game_id) REFERENCES games (game_id) ON DELETE CASCADE,
    CONSTRAINT content_drafts_updated_by_fk FOREIGN KEY (updated_by) REFERENCES admin_users (admin_id),
    CONSTRAINT content_drafts_version_positive CHECK (draft_version > 0),
    CONSTRAINT content_drafts_submitted_version_valid CHECK (
        submitted_draft_version IS NULL OR
        (submitted_draft_version > 0 AND submitted_draft_version <= draft_version)
    ),
    CONSTRAINT content_drafts_definition_object CHECK (jsonb_typeof(definition) = 'object'),
    CONSTRAINT content_drafts_content_id_nonblank CHECK (length(btrim(content_id)) > 0)
);

CREATE TABLE revision_approvals (
    game_id text NOT NULL,
    content_id text NOT NULL,
    revision integer NOT NULL,
    body_hash bytea NOT NULL,
    decision text NOT NULL,
    reviewer_id uuid NOT NULL,
    comment text NOT NULL DEFAULT '',
    reviewed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, content_id, revision),
    CONSTRAINT revision_approvals_revision_fk FOREIGN KEY (game_id, content_id, revision)
        REFERENCES content_revisions (game_id, content_id, revision),
    CONSTRAINT revision_approvals_reviewer_fk FOREIGN KEY (reviewer_id) REFERENCES admin_users (admin_id),
    CONSTRAINT revision_approvals_body_hash_length CHECK (octet_length(body_hash) = 32),
    CONSTRAINT revision_approvals_decision_valid CHECK (decision IN ('approved', 'rejected')),
    CONSTRAINT revision_approvals_comment_length CHECK (length(comment) <= 2000)
);

CREATE TABLE audit_logs (
    audit_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    game_id text REFERENCES games (game_id),
    actor_id uuid REFERENCES admin_users (admin_id),
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    outcome text NOT NULL,
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT audit_logs_action_nonblank CHECK (length(btrim(action)) > 0),
    CONSTRAINT audit_logs_resource_type_nonblank CHECK (length(btrim(resource_type)) > 0),
    CONSTRAINT audit_logs_resource_id_nonblank CHECK (length(btrim(resource_id)) > 0),
    CONSTRAINT audit_logs_outcome_valid CHECK (outcome IN ('success', 'denied', 'failure')),
    CONSTRAINT audit_logs_details_object CHECK (jsonb_typeof(details) = 'object')
);

CREATE FUNCTION public.validate_content_revision_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.body->'game_id' IS DISTINCT FROM to_jsonb(NEW.game_id)
       OR NEW.body->'content_id' IS DISTINCT FROM to_jsonb(NEW.content_id)
       OR NEW.body->'revision' IS DISTINCT FROM to_jsonb(NEW.revision)
       OR NEW.body->'schema_version' IS DISTINCT FROM to_jsonb(NEW.schema_version)
       OR NEW.body->'public'->'game_id' IS DISTINCT FROM to_jsonb(NEW.game_id)
       OR NEW.body->'public'->'content_id' IS DISTINCT FROM to_jsonb(NEW.content_id)
       OR NEW.body->'public'->'revision' IS DISTINCT FROM to_jsonb(NEW.revision)
       OR NEW.body->'public'->'schema_version' IS DISTINCT FROM to_jsonb(NEW.schema_version) THEN
        RAISE EXCEPTION 'content revision body identity must match its database scope'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER content_revisions_identity_matches_scope
BEFORE INSERT ON content_revisions
FOR EACH ROW
EXECUTE FUNCTION public.validate_content_revision_identity();

CREATE FUNCTION public.prevent_content_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'content revisions are immutable'
        USING ERRCODE = '23514';
END;
$$;

CREATE TRIGGER content_revisions_immutable_body
BEFORE UPDATE OR DELETE ON content_revisions
FOR EACH ROW
EXECUTE FUNCTION public.prevent_content_revision_mutation();

CREATE TRIGGER content_revisions_no_truncate
BEFORE TRUNCATE ON content_revisions
FOR EACH STATEMENT
EXECUTE FUNCTION public.prevent_content_revision_mutation();

CREATE INDEX admin_sessions_admin_id_idx ON admin_sessions (admin_id);
CREATE INDEX admin_sessions_expiry_idx ON admin_sessions (expires_at);
CREATE INDEX admin_sessions_idle_expiry_idx ON admin_sessions (idle_expires_at);
CREATE INDEX admin_sessions_cleanup_idx ON admin_sessions ((LEAST(expires_at, idle_expires_at)), session_id);
CREATE INDEX admin_role_bindings_admin_game_idx ON admin_role_bindings (admin_id, game_id);
CREATE INDEX content_drafts_updated_by_idx ON content_drafts (updated_by);
CREATE INDEX revision_approvals_reviewer_id_idx ON revision_approvals (reviewer_id);
CREATE INDEX revision_approvals_reviewed_idx ON revision_approvals (game_id, reviewed_at DESC);
CREATE INDEX audit_logs_actor_id_idx ON audit_logs (actor_id);
CREATE INDEX audit_logs_game_created_idx ON audit_logs (game_id, created_at DESC);

-- tbcg_admin_control_plane is a deployment-provisioned, dedicated NOLOGIN group.
-- Refuse to adopt a principal that already has overlapping or dangerous privileges.
DO $$
DECLARE
    role_info record;
    role_oid oid;
BEGIN
    SELECT oid INTO role_oid FROM pg_roles WHERE rolname = 'tbcg_admin_control_plane';
    IF role_oid IS NULL THEN
        RAISE EXCEPTION 'tbcg_admin_control_plane must be pre-provisioned outside this migration';
    END IF;
    SELECT rolcanlogin, rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls
    INTO role_info
    FROM pg_roles
    WHERE oid = role_oid;
    IF role_info.rolcanlogin OR role_info.rolsuper OR role_info.rolcreatedb OR role_info.rolcreaterole OR role_info.rolreplication OR role_info.rolbypassrls THEN
        RAISE EXCEPTION 'tbcg_admin_control_plane must be a pre-provisioned restricted NOLOGIN role';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_roles r
        WHERE (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
          AND pg_has_role(role_oid, r.oid, 'MEMBER')
    ) OR has_database_privilege(role_oid, current_database(), 'CREATE')
      OR has_schema_privilege(role_oid, 'public', 'CREATE') THEN
        RAISE EXCEPTION 'tbcg_admin_control_plane has inherited administrative or DDL privileges';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_class c
        WHERE c.relnamespace = 'public'::regnamespace
          AND pg_has_role(role_oid, c.relowner, 'MEMBER')
    ) THEN
        RAISE EXCEPTION 'tbcg_admin_control_plane can assume an existing public object owner';
    END IF;
    IF has_column_privilege(role_oid, 'public.games', 'game_id', 'SELECT')
      OR has_any_column_privilege(role_oid, 'public.content_revisions', 'SELECT')
      OR EXISTS (
          SELECT 1 FROM pg_attribute a
          WHERE a.attrelid = 'public.content_revisions'::regclass
            AND a.attname = ANY(ARRAY['game_id','content_id','revision','schema_version','content_type','body'])
            AND has_column_privilege(role_oid, a.attrelid, a.attnum, 'INSERT')
      ) THEN
        RAISE EXCEPTION 'tbcg_admin_control_plane already has overlapping control-plane table privileges';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_class c
        WHERE c.relnamespace = 'public'::regnamespace
          AND c.relname = ANY(ARRAY['games','realms','owner_guards','players','characters','wallets','wallet_ledger','outbox','outbox_consumer_receipts','operation_results','releases'])
          AND (has_any_column_privilege(role_oid, c.oid, 'INSERT')
            OR has_any_column_privilege(role_oid, c.oid, 'UPDATE')
            OR has_table_privilege(role_oid, c.oid, 'DELETE')
            OR has_table_privilege(role_oid, c.oid, 'TRUNCATE'))
    ) OR has_column_privilege(role_oid, 'public.realms', 'active_release_id', 'UPDATE') THEN
        RAISE EXCEPTION 'tbcg_admin_control_plane already has runtime write privileges';
    END IF;
END;
$$;

GRANT SELECT (game_id) ON games TO tbcg_admin_control_plane;
GRANT SELECT (admin_id, username, password_hash, active) ON admin_users TO tbcg_admin_control_plane;
GRANT SELECT (session_id, admin_id, token_hash, csrf_hash, expires_at, idle_expires_at) ON admin_sessions TO tbcg_admin_control_plane;
GRANT INSERT (admin_id, token_hash, csrf_hash, expires_at, idle_expires_at) ON admin_sessions TO tbcg_admin_control_plane;
GRANT UPDATE (csrf_hash, idle_expires_at, last_seen_at) ON admin_sessions TO tbcg_admin_control_plane;
GRANT DELETE ON admin_sessions TO tbcg_admin_control_plane;
GRANT SELECT (game_id, admin_id, role) ON admin_role_bindings TO tbcg_admin_control_plane;
GRANT SELECT (draft_id, game_id, content_id, draft_version, submitted_draft_version, definition, updated_at) ON content_drafts TO tbcg_admin_control_plane;
GRANT INSERT (game_id, content_id, definition, updated_by) ON content_drafts TO tbcg_admin_control_plane;
GRANT UPDATE (definition, draft_version, submitted_draft_version, updated_by, updated_at) ON content_drafts TO tbcg_admin_control_plane;
GRANT SELECT ON content_revisions TO tbcg_admin_control_plane;
GRANT INSERT (game_id, content_id, revision, schema_version, content_type, body) ON content_revisions TO tbcg_admin_control_plane;
GRANT SELECT, INSERT ON revision_approvals TO tbcg_admin_control_plane;
GRANT INSERT (game_id, actor_id, action, resource_type, resource_id, outcome, details) ON audit_logs TO tbcg_admin_control_plane;
GRANT USAGE ON SEQUENCE audit_logs_audit_id_seq TO tbcg_admin_control_plane;

---- create above / drop below ----

DROP TRIGGER content_revisions_no_truncate ON content_revisions;
DROP TRIGGER content_revisions_immutable_body ON content_revisions;
DROP TRIGGER content_revisions_identity_matches_scope ON content_revisions;
DROP FUNCTION public.prevent_content_revision_mutation();
DROP FUNCTION public.validate_content_revision_identity();
DROP TABLE audit_logs;
DROP TABLE revision_approvals;
DROP TABLE content_drafts;
DROP TABLE admin_role_bindings;
DROP TABLE admin_sessions;
DROP TABLE admin_users;
REVOKE SELECT (game_id) ON games FROM tbcg_admin_control_plane;
REVOKE SELECT ON content_revisions FROM tbcg_admin_control_plane;
REVOKE INSERT (game_id, content_id, revision, schema_version, content_type, body) ON content_revisions FROM tbcg_admin_control_plane;
