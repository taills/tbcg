package postgres

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/taills/tbcg/server/internal/admin"
	"github.com/taills/tbcg/server/internal/content/schema"
	"github.com/taills/tbcg/server/internal/persistence"
)

const (
	adminControlRole              = "tbcg_admin_control_plane"
	ownedLoopbackTestDatabasePort = 55432
)

// AdminRepository persists control-plane identities, drafts, revisions, approvals, and audit records.
type AdminRepository struct {
	db *sql.DB
}

func newAdminRepository(db *sql.DB) *AdminRepository {
	return &AdminRepository{db: db}
}

// OpenAdminRepository opens a pool and SET ROLEs each connection to the deployment-provisioned NOLOGIN role.
func OpenAdminRepository(ctx context.Context, dsn string) (*AdminRepository, error) {
	return openAdminRepository(ctx, dsn, false)
}

func openAdminRepository(ctx context.Context, dsn string, allowOwnedLoopbackTest bool) (*AdminRepository, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse admin PostgreSQL DSN: %w", err)
	}
	if err := validateAdminPostgresTLS(config, allowOwnedLoopbackTest); err != nil {
		return nil, fmt.Errorf("admin PostgreSQL TLS policy: %w", err)
	}
	config.AfterConnect = func(ctx context.Context, conn *pgconn.PgConn) error {
		if _, err := conn.Exec(ctx, "SET ROLE "+adminControlRole).ReadAll(); err != nil {
			return fmt.Errorf("assume restricted PostgreSQL control-plane role: %w", err)
		}
		result, err := conn.Exec(ctx, `
SELECT session_user, current_user,
       COALESCE((SELECT rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls FROM pg_roles WHERE rolname = current_user), true),
       COALESCE((SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls FROM pg_roles WHERE rolname = session_user), true),
       EXISTS (SELECT 1 FROM pg_roles r WHERE (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
               AND (pg_has_role(session_user, r.oid, 'MEMBER') OR pg_has_role(current_user, r.oid, 'MEMBER'))),
       has_schema_privilege(current_user, 'public', 'CREATE') OR has_schema_privilege(session_user, 'public', 'CREATE'),
       has_database_privilege(current_user, current_database(), 'CREATE') OR has_database_privilege(session_user, current_database(), 'CREATE'),
       EXISTS (SELECT 1 FROM pg_class c WHERE c.relnamespace = 'public'::regnamespace
               AND pg_has_role(session_user, c.relowner, 'MEMBER')),
       EXISTS (
         SELECT 1 FROM pg_class c
         CROSS JOIN unnest(ARRAY[current_user, session_user]) AS principals(role_name)
         WHERE c.relnamespace = 'public'::regnamespace
           AND c.relname = ANY(ARRAY['games','realms','owner_guards','players','characters','wallets','wallet_ledger','outbox','outbox_consumer_receipts','operation_results','releases','release_entries','admin_users','admin_role_bindings'])
           AND (has_any_column_privilege(principals.role_name, c.oid, 'INSERT')
             OR has_any_column_privilege(principals.role_name, c.oid, 'UPDATE')
             OR has_table_privilege(principals.role_name, c.oid, 'DELETE')
             OR has_table_privilege(principals.role_name, c.oid, 'TRUNCATE'))),
       has_column_privilege(current_user, 'public.realms', 'active_release_id', 'UPDATE'),
       EXISTS (
         SELECT 1 FROM unnest(ARRAY[current_user, session_user]) AS principals(role_name)
         CROSS JOIN unnest(ARRAY['public.content_revisions','public.audit_logs','public.revision_approvals']) AS immutable_relations(relation_name)
         WHERE has_any_column_privilege(principals.role_name, immutable_relations.relation_name, 'UPDATE')
            OR has_table_privilege(principals.role_name, immutable_relations.relation_name, 'DELETE, TRUNCATE')),
       EXISTS (
         SELECT 1 FROM pg_class c
         CROSS JOIN unnest(ARRAY[current_user, session_user]) AS principals(role_name)
         WHERE c.relnamespace = 'public'::regnamespace
           AND c.relname = ANY(ARRAY['game_build_profiles','admin_build_jobs','release_version_counters'])
           AND (has_any_column_privilege(principals.role_name, c.oid, 'SELECT')
             OR has_any_column_privilege(principals.role_name, c.oid, 'INSERT')
             OR has_any_column_privilege(principals.role_name, c.oid, 'UPDATE')
             OR has_table_privilege(principals.role_name, c.oid, 'DELETE')
             OR has_table_privilege(principals.role_name, c.oid, 'TRUNCATE'))),
       has_any_column_privilege(current_user, 'public.audit_logs', 'SELECT')
         OR has_any_column_privilege(session_user, 'public.audit_logs', 'SELECT'),
       EXISTS (
         SELECT 1 FROM pg_roles r
         CROSS JOIN unnest(ARRAY[current_user, session_user]) AS principals(role_name)
         WHERE r.rolname IN ('tbcg_content_build_owner', 'tbcg_content_build_worker')
           AND pg_has_role(principals.role_name, r.oid, 'MEMBER'))`).ReadAll()
		if err != nil {
			return fmt.Errorf("inspect admin PostgreSQL connection roles: %w", err)
		}
		if len(result) != 1 || len(result[0].Rows) != 1 || len(result[0].Rows[0]) != 14 {
			return errors.New("admin PostgreSQL role verification returned an unexpected result")
		}
		row := result[0].Rows[0]
		if string(row[1]) != adminControlRole {
			return errors.New("admin service did not assume the restricted control-plane role")
		}
		for _, value := range row[2:] {
			if string(value) != "f" {
				return errors.New("admin service database login or effective control-plane privileges are not restricted")
			}
		}
		return nil
	}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(defaultMaxOpenConnections)
	db.SetMaxIdleConns(defaultMaxIdleConnections)
	db.SetConnMaxLifetime(defaultConnectionMaxLifetime)
	db.SetConnMaxIdleTime(defaultConnectionMaxIdleTime)
	pingCtx, cancel := context.WithTimeout(ctx, defaultPingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		pingErr := fmt.Errorf("ping restricted admin PostgreSQL database: %w", err)
		if closeErr := db.Close(); closeErr != nil {
			return nil, errors.Join(pingErr, fmt.Errorf("close failed admin database pool: %w", closeErr))
		}
		return nil, pingErr
	}
	return newAdminRepository(db), nil
}

func validateAdminPostgresTLS(config *pgx.ConnConfig, allowOwnedLoopbackTest bool) error {
	if config == nil {
		return errors.New("PostgreSQL connection config is missing")
	}
	hostIP := net.ParseIP(config.Host)
	if allowOwnedLoopbackTest && hostIP != nil && hostIP.IsLoopback() && config.Port == ownedLoopbackTestDatabasePort && config.TLSConfig == nil {
		for _, fallback := range config.Fallbacks {
			if fallback == nil {
				return errors.New("owned loopback test database config has an empty fallback")
			}
			fallbackIP := net.ParseIP(fallback.Host)
			if fallbackIP == nil || !fallbackIP.IsLoopback() || fallback.Port != ownedLoopbackTestDatabasePort || fallback.TLSConfig != nil {
				return errors.New("owned loopback test database config has unsafe fallbacks")
			}
		}
		return nil
	}
	if !verifiedPostgresTLS(config.TLSConfig, config.Host) {
		return errors.New("remote PostgreSQL requires certificate-chain and hostname verification")
	}
	for _, fallback := range config.Fallbacks {
		if fallback == nil || !verifiedPostgresTLS(fallback.TLSConfig, fallback.Host) {
			return errors.New("every PostgreSQL fallback must verify its certificate chain and hostname")
		}
	}
	return nil
}

func verifiedPostgresTLS(config *tls.Config, host string) bool {
	return config != nil && !config.InsecureSkipVerify && config.ServerName == host &&
		config.VerifyPeerCertificate == nil && config.VerifyConnection == nil
}

// Close releases the admin repository's connection pool.
func (r *AdminRepository) Close() error {
	if err := r.db.Close(); err != nil {
		return fmt.Errorf("close admin PostgreSQL repository: %w", err)
	}
	return nil
}

func (r *AdminRepository) LoadSession(ctx context.Context, tokenHash []byte) (admin.Session, error) {
	var session admin.Session
	var adminID sql.NullString
	var username sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT s.session_id::text, s.admin_id::text, u.username, s.csrf_hash, s.expires_at, s.idle_expires_at
FROM admin_sessions s
LEFT JOIN admin_users u ON u.admin_id = s.admin_id
WHERE s.token_hash = $1
  AND s.expires_at > now()
  AND s.idle_expires_at > now()
  AND (s.admin_id IS NULL OR u.active)`, tokenHash).Scan(
		&session.ID, &adminID, &username, &session.CSRFHash, &session.ExpiresAt, &session.IdleExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return admin.Session{}, admin.ErrNotFound
	}
	if err != nil {
		return admin.Session{}, fmt.Errorf("load admin session: %w", err)
	}
	if adminID.Valid {
		session.AdminID = adminID.String
		session.Username = username.String
		roles, err := r.loadRoles(ctx, session.AdminID)
		if err != nil {
			return admin.Session{}, fmt.Errorf("load admin session roles: %w", err)
		}
		session.Roles = roles
	}
	return session, nil
}

func (r *AdminRepository) loadRoles(ctx context.Context, adminID string) ([]admin.GameRole, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT game_id, role
FROM admin_role_bindings
WHERE admin_id = $1
ORDER BY game_id, role`, adminID)
	if err != nil {
		return nil, fmt.Errorf("query admin Game roles: %w", err)
	}
	defer rows.Close()
	roles := make([]admin.GameRole, 0)
	for rows.Next() {
		var gameID, role string
		if err := rows.Scan(&gameID, &role); err != nil {
			return nil, fmt.Errorf("scan admin Game role: %w", err)
		}
		if len(roles) == 0 || roles[len(roles)-1].GameID != gameID {
			roles = append(roles, admin.GameRole{GameID: gameID, Roles: []string{role}})
			continue
		}
		last := len(roles) - 1
		roles[last].Roles = append(roles[last].Roles, role)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin Game roles: %w", err)
	}
	return roles, nil
}

func (r *AdminRepository) CreateSession(ctx context.Context, tokenHash, csrfHash []byte, expiresAt, idleExpiresAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO admin_sessions (token_hash, csrf_hash, expires_at, idle_expires_at)
VALUES ($1, $2, $3, $4)`, tokenHash, csrfHash, expiresAt, idleExpiresAt)
	if err != nil {
		return fmt.Errorf("create anonymous admin session: %w", err)
	}
	return nil
}

func (r *AdminRepository) UpdateCSRF(ctx context.Context, tokenHash, csrfHash []byte) error {
	result, err := r.db.ExecContext(ctx, `
UPDATE admin_sessions
SET csrf_hash = $2, last_seen_at = now()
WHERE token_hash = $1 AND expires_at > now() AND idle_expires_at > now()`, tokenHash, csrfHash)
	if err != nil {
		return fmt.Errorf("rotate admin CSRF hash: %w", err)
	}
	return requireOneRow(result, "rotate admin CSRF hash")
}

func (r *AdminRepository) TouchSession(ctx context.Context, tokenHash []byte, idleExpiresAt time.Time) error {
	result, err := r.db.ExecContext(ctx, `
UPDATE admin_sessions
SET idle_expires_at = LEAST($2, expires_at), last_seen_at = now()
WHERE token_hash = $1 AND expires_at > now() AND idle_expires_at > now()`, tokenHash, idleExpiresAt)
	if err != nil {
		return fmt.Errorf("touch admin session: %w", err)
	}
	return requireOneRow(result, "touch admin session")
}

func (r *AdminRepository) Authenticate(ctx context.Context, username string) (admin.User, error) {
	var user admin.User
	err := r.db.QueryRowContext(ctx, `
SELECT admin_id::text, username, password_hash
FROM admin_users
WHERE username = $1 AND active`, username).Scan(&user.ID, &user.Username, &user.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return admin.User{}, admin.ErrNotFound
	}
	if err != nil {
		return admin.User{}, fmt.Errorf("authenticate admin identity: %w", err)
	}
	return user, nil
}

func (r *AdminRepository) RotateSession(ctx context.Context, oldTokenHash, newTokenHash, csrfHash []byte, expiresAt, idleExpiresAt time.Time, adminID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin admin session rotation: %w", err)
	}
	defer rollbackAdminTransactionWithLog(tx, "control-plane transaction")
	result, err := tx.ExecContext(ctx, `
DELETE FROM admin_sessions
WHERE token_hash = $1 AND admin_id IS NULL AND expires_at > now() AND idle_expires_at > now()`, oldTokenHash)
	if err != nil {
		return fmt.Errorf("revoke anonymous session during login: %w", err)
	}
	if err := requireOneRow(result, "revoke anonymous session during login"); err != nil {
		return admin.ErrNotFound
	}
	result, err = tx.ExecContext(ctx, `
INSERT INTO admin_sessions (admin_id, token_hash, csrf_hash, expires_at, idle_expires_at)
SELECT admin_id, $2, $3, $4, $5
FROM admin_users
WHERE admin_id = $1 AND active`, adminID, newTokenHash, csrfHash, expiresAt, idleExpiresAt)
	if err != nil {
		return fmt.Errorf("create authenticated admin session: %w", err)
	}
	if err := requireOneRow(result, "create authenticated admin session"); err != nil {
		return admin.ErrNotFound
	}
	if err := insertAdminAudit(ctx, tx, admin.AuditEvent{
		ActorID: adminID, Action: "admin.login", Resource: "session", ResourceID: "login",
		Outcome: "success", DetailsJSON: []byte(`{}`),
	}); err != nil {
		return fmt.Errorf("audit successful admin login: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return classifyAdminCommitError(err)
	}
	return nil
}

func (r *AdminRepository) RevokeSession(ctx context.Context, tokenHash []byte) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin admin logout transaction: %w", err)
	}
	defer rollbackAdminTransactionWithLog(tx, "control-plane transaction")
	var adminID sql.NullString
	err = tx.QueryRowContext(ctx, `DELETE FROM admin_sessions WHERE token_hash = $1 RETURNING admin_id::text`, tokenHash).Scan(&adminID)
	if errors.Is(err, sql.ErrNoRows) {
		return admin.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("revoke admin session: %w", err)
	}
	actorID := ""
	if adminID.Valid {
		actorID = adminID.String
	}
	if err := insertAdminAudit(ctx, tx, admin.AuditEvent{
		ActorID: actorID, Action: "admin.logout", Resource: "session", ResourceID: "logout",
		Outcome: "success", DetailsJSON: []byte(`{}`),
	}); err != nil {
		return fmt.Errorf("audit admin logout: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return classifyAdminCommitError(err)
	}
	return nil
}

func (r *AdminRepository) HasRole(ctx context.Context, gameID, adminID string, roles ...admin.Role) (bool, error) {
	if len(roles) == 0 {
		return false, nil
	}
	placeholders := make([]string, len(roles))
	args := make([]any, 0, len(roles)+2)
	args = append(args, gameID, adminID)
	for index, role := range roles {
		placeholders[index] = fmt.Sprintf("$%d", index+3)
		args = append(args, string(role))
	}
	query := `SELECT EXISTS (
SELECT 1 FROM admin_role_bindings
WHERE game_id = $1 AND admin_id = $2 AND role IN (` + strings.Join(placeholders, ",") + `))`
	var allowed bool
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&allowed); err != nil {
		return false, fmt.Errorf("check Game-bound admin role: %w", err)
	}
	return allowed, nil
}

func (r *AdminRepository) WriteAudit(ctx context.Context, event admin.AuditEvent) error {
	if err := insertAdminAudit(ctx, r.db, event); err != nil {
		return fmt.Errorf("write independent admin audit: %w", err)
	}
	return nil
}

func insertAdminAudit(ctx context.Context, queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, event admin.AuditEvent) error {
	details := event.DetailsJSON
	if len(details) == 0 {
		details = []byte(`{}`)
	}
	_, err := queryer.ExecContext(ctx, `
INSERT INTO audit_logs (game_id, actor_id, action, resource_type, resource_id, outcome, details)
VALUES (
  CASE WHEN EXISTS (SELECT 1 FROM games WHERE game_id = NULLIF($1, '')) THEN NULLIF($1, '') ELSE NULL END,
  NULLIF($2, '')::uuid, $3, $4, $5, $6, $7::jsonb)`,
		event.GameID, event.ActorID, event.Action, event.Resource, event.ResourceID, event.Outcome, details)
	if err != nil {
		return fmt.Errorf("insert admin audit event: %w", err)
	}
	return nil
}

func (r *AdminRepository) CreateDraft(ctx context.Context, gameID, contentID string, definition []byte, actorID string) (admin.Draft, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return admin.Draft{}, fmt.Errorf("begin create item draft: %w", err)
	}
	defer rollbackAdminTransactionWithLog(tx, "control-plane transaction")
	draft, err := scanAdminDraft(tx.QueryRowContext(ctx, `
INSERT INTO content_drafts (game_id, content_id, definition, updated_by)
VALUES ($1, $2, $3::jsonb, $4)
RETURNING draft_id::text, game_id, content_id, draft_version::text, definition, updated_at`, gameID, contentID, definition, actorID))
	if err != nil {
		if isUniqueViolation(err) {
			if rollbackErr := rollbackAdminTransaction(tx); rollbackErr != nil {
				return admin.Draft{}, fmt.Errorf("rollback duplicate item draft insert: %w", rollbackErr)
			}
			if auditErr := r.writeFailureAudit(ctx, admin.AuditEvent{GameID: gameID, ActorID: actorID, Action: "item_draft.create", Resource: "item_draft", ResourceID: contentID, Outcome: "failure", DetailsJSON: []byte(`{}`)}); auditErr != nil {
				return admin.Draft{}, auditErr
			}
			return admin.Draft{}, admin.ErrConflict
		}
		return admin.Draft{}, fmt.Errorf("insert item draft: %w", err)
	}
	if err := insertAdminAudit(ctx, tx, admin.AuditEvent{
		GameID: gameID, ActorID: actorID, Action: "item_draft.create", Resource: "item_draft", ResourceID: contentID,
		Outcome: "success", DetailsJSON: []byte(`{}`),
	}); err != nil {
		return admin.Draft{}, fmt.Errorf("audit item draft creation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return admin.Draft{}, classifyAdminCommitError(err)
	}
	return draft, nil
}

func (r *AdminRepository) CompareAndSwapDraft(ctx context.Context, gameID, draftID string, expectedVersion int64, definition []byte, actorID string) (admin.Draft, *admin.Draft, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return admin.Draft{}, nil, fmt.Errorf("begin update item draft: %w", err)
	}
	defer rollbackAdminTransactionWithLog(tx, "control-plane transaction")
	draft, err := scanAdminDraft(tx.QueryRowContext(ctx, `
UPDATE content_drafts
SET definition = $4::jsonb, draft_version = draft_version + 1, updated_by = $5, updated_at = now()
WHERE game_id = $1 AND draft_id = $2::uuid AND draft_version = $3
RETURNING draft_id::text, game_id, content_id, draft_version::text, definition, updated_at`, gameID, draftID, expectedVersion, definition, actorID))
	if errors.Is(err, admin.ErrNotFound) {
		latest, readErr := scanAdminDraft(tx.QueryRowContext(ctx, `
SELECT draft_id::text, game_id, content_id, draft_version::text, definition, updated_at
FROM content_drafts WHERE game_id = $1 AND draft_id = $2::uuid`, gameID, draftID))
		if rollbackErr := rollbackAdminTransaction(tx); rollbackErr != nil {
			return admin.Draft{}, nil, fmt.Errorf("rollback stale item draft update: %w", rollbackErr)
		}
		if errors.Is(readErr, admin.ErrNotFound) {
			return admin.Draft{}, nil, admin.ErrNotFound
		}
		if readErr != nil {
			return admin.Draft{}, nil, fmt.Errorf("read current item draft after CAS miss: %w", readErr)
		}
		if auditErr := r.writeFailureAudit(ctx, admin.AuditEvent{GameID: gameID, ActorID: actorID, Action: "item_draft.update", Resource: "item_draft", ResourceID: latest.ContentID, Outcome: "failure", DetailsJSON: []byte(`{"reason":"cas_conflict"}`)}); auditErr != nil {
			return admin.Draft{}, nil, auditErr
		}
		return admin.Draft{}, &latest, admin.ErrConflict
	}
	if err != nil {
		return admin.Draft{}, nil, fmt.Errorf("compare-and-swap item draft: %w", err)
	}
	if err := insertAdminAudit(ctx, tx, admin.AuditEvent{
		GameID: gameID, ActorID: actorID, Action: "item_draft.update", Resource: "item_draft", ResourceID: draft.ContentID,
		Outcome: "success", DetailsJSON: []byte(`{}`),
	}); err != nil {
		return admin.Draft{}, nil, fmt.Errorf("audit item draft update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return admin.Draft{}, nil, classifyAdminCommitError(err)
	}
	return draft, nil, nil
}

func (r *AdminRepository) SubmitRevision(ctx context.Context, gameID, draftID string, expectedVersion int64, actorID string) (admin.Revision, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return admin.Revision{}, fmt.Errorf("begin submit item revision: %w", err)
	}
	defer rollbackAdminTransactionWithLog(tx, "control-plane transaction")
	var draft admin.Draft
	var currentVersion int64
	var submittedVersion sql.NullInt64
	err = tx.QueryRowContext(ctx, `
SELECT draft_id::text, game_id, content_id, draft_version, submitted_draft_version, definition, updated_at
FROM content_drafts
WHERE game_id = $1 AND draft_id = $2::uuid
FOR UPDATE`, gameID, draftID).Scan(&draft.DraftID, &draft.GameID, &draft.ContentID, &currentVersion, &submittedVersion, &draft.Definition, &draft.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return admin.Revision{}, admin.ErrNotFound
	}
	if err != nil {
		return admin.Revision{}, fmt.Errorf("lock item draft for revision submit: %w", err)
	}
	if currentVersion != expectedVersion || submittedVersion.Valid && submittedVersion.Int64 == expectedVersion {
		if err := rollbackAdminTransaction(tx); err != nil {
			return admin.Revision{}, fmt.Errorf("rollback stale item revision submit: %w", err)
		}
		if err := r.writeFailureAudit(ctx, admin.AuditEvent{GameID: gameID, ActorID: actorID, Action: "revision.submit", Resource: "item_draft", ResourceID: draft.ContentID, Outcome: "failure", DetailsJSON: []byte(`{"reason":"cas_conflict"}`)}); err != nil {
			return admin.Revision{}, err
		}
		return admin.Revision{}, admin.ErrConflict
	}
	var previousRevision int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(revision), 0) FROM content_revisions WHERE game_id = $1 AND content_id = $2`, gameID, draft.ContentID).Scan(&previousRevision); err != nil {
		return admin.Revision{}, fmt.Errorf("allocate item Definition revision: %w", err)
	}
	if previousRevision >= math.MaxInt32 {
		return admin.Revision{}, errors.New("item Definition revision exceeds PostgreSQL integer range")
	}
	// #nosec G115 -- previousRevision is bounded to [0, MaxInt32-1] above.
	revision := int32(previousRevision + 1)
	definition, err := setDefinitionRevision(draft.Definition, revision)
	if err != nil {
		return admin.Revision{}, fmt.Errorf("set server-assigned item revision: %w", err)
	}
	var schemaVersion int
	if err := json.Unmarshal(extractJSONField(definition, "schema_version"), &schemaVersion); err != nil || schemaVersion < 1 {
		return admin.Revision{}, errors.New("item Definition schema_version is invalid")
	}
	contentType, err := schema.ContentTypeOf(draft.ContentID)
	if err != nil {
		return admin.Revision{}, fmt.Errorf("derive content type: %w", err)
	}
	var storedBody []byte
	var submittedAt time.Time
	if err := tx.QueryRowContext(ctx, `
INSERT INTO content_revisions (game_id, content_id, revision, schema_version, content_type, body)
VALUES ($1, $2, $3, $4, $6, $5::jsonb)
RETURNING body, created_at`, gameID, draft.ContentID, revision, schemaVersion, definition, contentType).Scan(&storedBody, &submittedAt); err != nil {
		return admin.Revision{}, fmt.Errorf("insert immutable item revision: %w", err)
	}
	canonicalBody, err := canonicalJSON(storedBody)
	if err != nil {
		return admin.Revision{}, fmt.Errorf("canonicalize stored immutable item revision: %w", err)
	}
	hash := sha256.Sum256(canonicalBody)
	result, err := tx.ExecContext(ctx, `
UPDATE content_drafts
SET submitted_draft_version = $3
WHERE game_id = $1 AND draft_id = $2::uuid AND draft_version = $3
  AND submitted_draft_version IS DISTINCT FROM $3`, gameID, draftID, expectedVersion)
	if err != nil {
		return admin.Revision{}, fmt.Errorf("mark submitted draft version: %w", err)
	}
	if err := requireOneRow(result, "mark submitted draft version"); err != nil {
		return admin.Revision{}, admin.ErrConflict
	}
	if err := insertAdminAudit(ctx, tx, admin.AuditEvent{
		GameID: gameID, ActorID: actorID, Action: "revision.submit", Resource: "revision",
		ResourceID: draft.ContentID + ":" + strconv.Itoa(int(revision)), Outcome: "success",
		DetailsJSON: []byte(`{"body_hash":"` + hex.EncodeToString(hash[:]) + `"}`),
	}); err != nil {
		return admin.Revision{}, fmt.Errorf("audit immutable item revision submission: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return admin.Revision{}, classifyAdminCommitError(err)
	}
	return admin.Revision{
		GameID: gameID, ContentID: draft.ContentID, Revision: revision, BodyHash: hex.EncodeToString(hash[:]),
		Definition: append(json.RawMessage(nil), canonicalBody...), Submitted: submittedAt,
	}, nil
}

func (r *AdminRepository) ReviewQueue(ctx context.Context, gameID string, limit, offset int) ([]admin.Revision, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `
SELECT count(*)
FROM content_revisions c
LEFT JOIN revision_approvals a
  ON a.game_id = c.game_id AND a.content_id = c.content_id AND a.revision = c.revision
WHERE c.game_id = $1 AND a.revision IS NULL`, gameID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count pending item revisions: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT c.content_id, c.revision, c.body, c.created_at
FROM content_revisions c
LEFT JOIN revision_approvals a
  ON a.game_id = c.game_id AND a.content_id = c.content_id AND a.revision = c.revision
WHERE c.game_id = $1 AND a.revision IS NULL
ORDER BY c.created_at DESC, c.content_id, c.revision DESC
LIMIT $2 OFFSET $3`, gameID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query item revision review queue: %w", err)
	}
	defer rows.Close()
	revisions := make([]admin.Revision, 0)
	for rows.Next() {
		var item admin.Revision
		var body []byte
		if err := rows.Scan(&item.ContentID, &item.Revision, &body, &item.Submitted); err != nil {
			return nil, 0, fmt.Errorf("scan pending item revision: %w", err)
		}
		canonical, err := canonicalJSON(body)
		if err != nil {
			return nil, 0, fmt.Errorf("canonicalize pending item revision: %w", err)
		}
		hash := sha256.Sum256(canonical)
		item.GameID = gameID
		item.BodyHash = hex.EncodeToString(hash[:])
		item.Definition = append(json.RawMessage(nil), canonical...)
		revisions = append(revisions, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate item revision review queue: %w", err)
	}
	return revisions, total, nil
}

func (r *AdminRepository) ReviewRevision(ctx context.Context, gameID, contentID string, revision int32, bodyHash []byte, decision, comment, actorID string) (admin.Approval, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return admin.Approval{}, fmt.Errorf("begin exact revision review: %w", err)
	}
	defer rollbackAdminTransactionWithLog(tx, "control-plane transaction")
	var body []byte
	err = tx.QueryRowContext(ctx, `
SELECT body FROM content_revisions
WHERE game_id = $1 AND content_id = $2 AND revision = $3`, gameID, contentID, revision).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		if rollbackErr := rollbackAdminTransaction(tx); rollbackErr != nil {
			return admin.Approval{}, fmt.Errorf("rollback missing revision review: %w", rollbackErr)
		}
		if auditErr := r.writeFailureAudit(ctx, admin.AuditEvent{GameID: gameID, ActorID: actorID, Action: "revision.review", Resource: "revision", ResourceID: contentID + ":" + strconv.Itoa(int(revision)), Outcome: "denied", DetailsJSON: []byte(`{"reason":"not_found"}`)}); auditErr != nil {
			return admin.Approval{}, auditErr
		}
		return admin.Approval{}, admin.ErrNotFound
	}
	if err != nil {
		return admin.Approval{}, fmt.Errorf("lock immutable item revision for review: %w", err)
	}
	canonical, err := canonicalJSON(body)
	if err != nil {
		return admin.Approval{}, fmt.Errorf("canonicalize revision body for review: %w", err)
	}
	actualHash := sha256.Sum256(canonical)
	if subtle.ConstantTimeCompare(actualHash[:], bodyHash) != 1 {
		if rollbackErr := rollbackAdminTransaction(tx); rollbackErr != nil {
			return admin.Approval{}, fmt.Errorf("rollback mismatched revision review: %w", rollbackErr)
		}
		if auditErr := r.writeFailureAudit(ctx, admin.AuditEvent{GameID: gameID, ActorID: actorID, Action: "revision.review", Resource: "revision", ResourceID: contentID + ":" + strconv.Itoa(int(revision)), Outcome: "denied", DetailsJSON: []byte(`{"reason":"body_hash_mismatch"}`)}); auditErr != nil {
			return admin.Approval{}, auditErr
		}
		return admin.Approval{}, admin.ErrConflict
	}
	var reviewer bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM admin_role_bindings WHERE game_id = $1 AND admin_id = $2 AND role = 'Reviewer')`, gameID, actorID).Scan(&reviewer); err != nil {
		return admin.Approval{}, fmt.Errorf("recheck Reviewer role in approval transaction: %w", err)
	}
	if !reviewer {
		if rollbackErr := rollbackAdminTransaction(tx); rollbackErr != nil {
			return admin.Approval{}, fmt.Errorf("rollback unauthorized revision review: %w", rollbackErr)
		}
		if auditErr := r.writeFailureAudit(ctx, admin.AuditEvent{GameID: gameID, ActorID: actorID, Action: "revision.review", Resource: "revision", ResourceID: contentID + ":" + strconv.Itoa(int(revision)), Outcome: "denied", DetailsJSON: []byte(`{"reason":"reviewer_role_missing"}`)}); auditErr != nil {
			return admin.Approval{}, auditErr
		}
		return admin.Approval{}, admin.ErrForbidden
	}
	var approval admin.Approval
	approval.GameID = gameID
	approval.ContentID = contentID
	approval.Revision = revision
	approval.BodyHash = hex.EncodeToString(actualHash[:])
	approval.Decision = decision
	approval.Reviewer = actorID
	approval.Comment = comment
	err = tx.QueryRowContext(ctx, `
INSERT INTO revision_approvals (game_id, content_id, revision, body_hash, decision, reviewer_id, comment)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING reviewed_at`, gameID, contentID, revision, actualHash[:], decision, actorID, comment).Scan(&approval.Reviewed)
	if err != nil {
		if isUniqueViolation(err) {
			if rollbackErr := rollbackAdminTransaction(tx); rollbackErr != nil {
				return admin.Approval{}, fmt.Errorf("rollback duplicate revision approval: %w", rollbackErr)
			}
			if auditErr := r.writeFailureAudit(ctx, admin.AuditEvent{GameID: gameID, ActorID: actorID, Action: "revision.review", Resource: "revision", ResourceID: contentID + ":" + strconv.Itoa(int(revision)), Outcome: "denied", DetailsJSON: []byte(`{"reason":"already_reviewed"}`)}); auditErr != nil {
				return admin.Approval{}, auditErr
			}
			return admin.Approval{}, admin.ErrConflict
		}
		return admin.Approval{}, fmt.Errorf("insert exact revision approval: %w", err)
	}
	if err := insertAdminAudit(ctx, tx, admin.AuditEvent{
		GameID: gameID, ActorID: actorID, Action: "revision.review", Resource: "revision",
		ResourceID: contentID + ":" + strconv.Itoa(int(revision)), Outcome: "success",
		DetailsJSON: []byte(`{"decision":"` + decision + `","body_hash":"` + approval.BodyHash + `"}`),
	}); err != nil {
		return admin.Approval{}, fmt.Errorf("audit exact revision approval: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return admin.Approval{}, classifyAdminCommitError(err)
	}
	return approval, nil
}

func scanAdminDraft(row *sql.Row) (admin.Draft, error) {
	var draft admin.Draft
	err := row.Scan(&draft.DraftID, &draft.GameID, &draft.ContentID, &draft.DraftVersion, &draft.Definition, &draft.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return admin.Draft{}, admin.ErrNotFound
	}
	if err != nil {
		return admin.Draft{}, fmt.Errorf("scan item draft: %w", err)
	}
	return draft, nil
}

func setDefinitionRevision(input []byte, revision int32) ([]byte, error) {
	var definition map[string]json.RawMessage
	if err := json.Unmarshal(input, &definition); err != nil {
		return nil, fmt.Errorf("decode item Definition: %w", err)
	}
	var public map[string]json.RawMessage
	if err := json.Unmarshal(definition["public"], &public); err != nil || public == nil {
		return nil, errors.New("item Definition public projection is invalid")
	}
	value, _ := json.Marshal(revision)
	definition["revision"] = value
	public["revision"] = append(json.RawMessage(nil), value...)
	encodedPublic, err := json.Marshal(public)
	if err != nil {
		return nil, fmt.Errorf("encode item public projection: %w", err)
	}
	definition["public"] = encodedPublic
	encoded, err := json.Marshal(definition)
	if err != nil {
		return nil, fmt.Errorf("encode item Definition: %w", err)
	}
	return encoded, nil
}

func extractJSONField(input []byte, field string) []byte {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(input, &object); err != nil {
		return nil
	}
	return object[field]
}

func canonicalJSON(input []byte) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(input)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode canonical JSON: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical JSON: %w", err)
	}
	return canonical, nil
}

func (r *AdminRepository) writeFailureAudit(ctx context.Context, event admin.AuditEvent) error {
	if err := r.WriteAudit(ctx, event); err != nil {
		return fmt.Errorf("persist failed-operation audit: %w", err)
	}
	return nil
}

func rollbackAdminTransaction(tx *sql.Tx) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return err
	}
	return nil
}

func rollbackAdminTransactionWithLog(tx *sql.Tx, operation string) {
	if err := rollbackAdminTransaction(tx); err != nil {
		log.Printf("admin database rollback failed operation=%q error=%v", operation, err)
	}
}

func requireOneRow(result sql.Result, action string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read %s row count: %w", action, err)
	}
	if rows != 1 {
		return admin.ErrNotFound
	}
	return nil
}

func classifyAdminCommitError(err error) error {
	if !persistence.IsCommitOutcomeUnknown(err) {
		return fmt.Errorf("admin PostgreSQL transaction commit rejected: %w", err)
	}
	return fmt.Errorf("admin PostgreSQL transaction commit outcome unknown: %w", errors.Join(persistence.ErrUnknown, err))
}
