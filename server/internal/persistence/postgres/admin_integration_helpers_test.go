package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/taills/tbcg/server/internal/admin"
	"github.com/taills/tbcg/server/internal/persistence"
	"golang.org/x/crypto/bcrypt"
)

type seededAdminAccounts struct {
	author    testAdminCredential
	reviewer  testAdminCredential
	publisher testAdminCredential
	reader    testAdminCredential
}

type testAdminCredential struct {
	username string
	password string
}

func seedAdminTestAccounts(t *testing.T, ctx context.Context, db *sql.DB, gameID string) seededAdminAccounts {
	t.Helper()
	return seededAdminAccounts{
		author:    seedOneAdmin(t, ctx, db, gameID, "Author"),
		reviewer:  seedOneAdmin(t, ctx, db, gameID, "Reviewer"),
		publisher: seedOneAdmin(t, ctx, db, gameID, "Publisher"),
		reader:    seedOneAdmin(t, ctx, db, gameID, "Reader"),
	}
}

func seedOneAdmin(t *testing.T, ctx context.Context, db *sql.DB, gameID, role string) testAdminCredential {
	t.Helper()
	username := randomTestIdentifier(t, "admin-")
	var passwordBytes [32]byte
	if _, err := rand.Read(passwordBytes[:]); err != nil {
		t.Fatalf("generate temporary admin password: %v", err)
	}
	password := hex.EncodeToString(passwordBytes[:])
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		t.Fatalf("hash temporary admin password: %v", err)
	}
	var adminID string
	if err := db.QueryRowContext(ctx, `
INSERT INTO admin_users (username, password_hash) VALUES ($1, $2)
RETURNING admin_id::text`, username, string(hash)).Scan(&adminID); err != nil {
		t.Fatalf("seed temporary admin identity: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO admin_role_bindings (game_id, admin_id, role) VALUES ($1, $2, $3)`, gameID, adminID, role); err != nil {
		t.Fatalf("seed Game-bound %s role: %v", role, err)
	}
	return testAdminCredential{username: username, password: password}
}

func assertSessionRefreshRotatesMissingCSRFCookie(t *testing.T, server *httptest.Server) {
	t.Helper()
	client := server.Client()
	bootstrap, err := client.Get(server.URL + "/api/v1/session")
	if err != nil {
		t.Fatalf("establish anonymous session before CSRF refresh: %v", err)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range bootstrap.Cookies() {
		if cookie.Name == "tbcg_admin_session" {
			sessionCookie = cookie
		}
	}
	closeAdminResponse(t, bootstrap)
	if sessionCookie == nil {
		t.Fatal("anonymous session response omitted its HttpOnly session cookie")
	}
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/session", nil)
	if err != nil {
		t.Fatalf("create session refresh without CSRF cookie: %v", err)
	}
	request.AddCookie(sessionCookie)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("refresh anonymous session without CSRF cookie: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		body := readAdminBody(t, response)
		t.Fatalf("CSRF refresh status = %d: %s", response.StatusCode, body)
	}
	rotated := false
	for _, cookie := range response.Cookies() {
		if cookie.Name == "tbcg_admin_csrf" && cookie.Secure && !cookie.HttpOnly {
			rotated = true
		}
	}
	closeAdminResponse(t, response)
	if !rotated {
		t.Fatal("session refresh without CSRF cookie did not issue a secure double-submit token")
	}
}

func loginTestAccount(t *testing.T, server *httptest.Server, credential testAdminCredential) (*http.Client, string) {
	t.Helper()
	client := newTLSCookieClient(t, server)
	sessionResponse, err := client.Get(server.URL + "/api/v1/session")
	if err != nil {
		t.Fatalf("establish pre-login session: %v", err)
	}
	bootstrap := decodeAdminResponse(t, sessionResponse)
	closeAdminResponse(t, sessionResponse)
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(bootstrap.Data, &session); err != nil || session.CSRFToken == "" {
		t.Fatalf("decode anonymous CSRF session: %v", err)
	}
	response := sendAdminJSON(t, client, server.URL, http.MethodPost, "/api/v1/login", map[string]any{
		"username": credential.username,
		"password": credential.password,
	}, session.CSRFToken)
	if response.StatusCode != http.StatusOK {
		body := readAdminBody(t, response)
		t.Fatalf("login test account status = %d: %s", response.StatusCode, body)
	}
	envelope := decodeAdminResponse(t, response)
	closeAdminResponse(t, response)
	var authenticated struct {
		Authenticated bool   `json:"authenticated"`
		CSRFToken     string `json:"csrf_token"`
	}
	if err := json.Unmarshal(envelope.Data, &authenticated); err != nil || !authenticated.Authenticated || authenticated.CSRFToken == "" {
		t.Fatalf("decode authenticated session: %#v (%v)", authenticated, err)
	}
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse TLS management server URL: %v", err)
	}
	csrfCookieMatches := false
	for _, cookie := range client.Jar.Cookies(serverURL) {
		if cookie.Name == "tbcg_admin_csrf" && cookie.Value == authenticated.CSRFToken {
			csrfCookieMatches = true
		}
	}
	if !csrfCookieMatches {
		t.Fatal("rotated CSRF cookie does not match authenticated session response")
	}
	refreshedSession, err := client.Get(server.URL + "/api/v1/session")
	if err != nil {
		t.Fatalf("refresh authenticated session: %v", err)
	}
	refreshedEnvelope := decodeAdminResponse(t, refreshedSession)
	closeAdminResponse(t, refreshedSession)
	var refreshed struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(refreshedEnvelope.Data, &refreshed); err != nil || refreshed.CSRFToken != authenticated.CSRFToken {
		t.Fatal("session-bound CSRF token changed after authenticated session refresh")
	}
	csrfCookieMatches = false
	for _, cookie := range client.Jar.Cookies(serverURL) {
		if cookie.Name == "tbcg_admin_csrf" && cookie.Value == authenticated.CSRFToken {
			csrfCookieMatches = true
		}
	}
	if !csrfCookieMatches {
		t.Fatal("session refresh changed the browser's CSRF cookie unexpectedly")
	}
	return client, authenticated.CSRFToken
}

func newTLSCookieClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create isolated test cookie jar: %v", err)
	}
	return &http.Client{Transport: server.Client().Transport, Jar: jar}
}

func sendAdminJSON(t *testing.T, client *http.Client, origin, method, path string, payload any, csrfToken string) *http.Response {
	t.Helper()
	return sendAdminJSONRequest(t, client, origin, method, path, payload, csrfToken, true)
}

func sendAdminJSONWithoutCSRF(t *testing.T, client *http.Client, origin, method, path string, payload any) *http.Response {
	t.Helper()
	return sendAdminJSONRequest(t, client, origin, method, path, payload, "", false)
}

func sendAdminJSONRequest(t *testing.T, client *http.Client, origin, method, path string, payload any, csrfToken string, setCSRF bool) *http.Response {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal admin test request: %v", err)
		}
		body = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequest(method, origin+path, body)
	if err != nil {
		t.Fatalf("create admin API request: %v", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Origin", origin)
	if setCSRF && method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", csrfToken)
		cookieMatches := false
		for _, cookie := range client.Jar.Cookies(request.URL) {
			if cookie.Name == "tbcg_admin_csrf" && cookie.Value == csrfToken {
				cookieMatches = true
			}
		}
		if !cookieMatches {
			t.Fatal("test request CSRF header does not match the browser cookie jar")
		}
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send admin HTTPS request: %v", err)
	}
	return response
}

func decodeAdminResponse(t *testing.T, response *http.Response) adminAPIEnvelope {
	t.Helper()
	var envelope adminAPIEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode admin API envelope: %v", err)
	}
	return envelope
}

func readAdminBody(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read admin API response: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close admin API response body: %v", err)
	}
	return string(body)
}

func closeAdminResponse(t *testing.T, response *http.Response) {
	t.Helper()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatalf("drain admin API response: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close admin API response: %v", err)
	}
}

func loadAdminFixture(t *testing.T) map[string]any {
	t.Helper()
	fixturePath := filepath.Join(filepath.Dir(findServerRoot(t)), "tests", "fixtures", "content", "item.valid.json")
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read valid item fixture: %v", err)
	}
	var definition map[string]any
	if err := json.Unmarshal(data, &definition); err != nil {
		t.Fatalf("decode valid item fixture: %v", err)
	}
	public, ok := definition["public"].(map[string]any)
	if !ok {
		t.Fatal("valid item fixture has no public object")
	}
	public["icon_id"] = "demo:icon/placeholder"
	return definition
}

func itemDefinitionWithIcon(t *testing.T, source []byte, iconID string) []byte {
	t.Helper()
	var definition map[string]json.RawMessage
	if err := json.Unmarshal(source, &definition); err != nil {
		t.Fatalf("decode item Definition for asset test: %v", err)
	}
	var public map[string]json.RawMessage
	if err := json.Unmarshal(definition["public"], &public); err != nil {
		t.Fatalf("decode public projection for asset test: %v", err)
	}
	encodedIcon, err := json.Marshal(iconID)
	if err != nil {
		t.Fatalf("encode test icon ID: %v", err)
	}
	public["icon_id"] = encodedIcon
	definition["public"], err = json.Marshal(public)
	if err != nil {
		t.Fatalf("encode public projection for asset test: %v", err)
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("encode item Definition for asset test: %v", err)
	}
	return encoded
}

func cloneItemDefinition(t *testing.T, source map[string]any, displayName string) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("copy item definition: %v", err)
	}
	var copy map[string]any
	if err := json.Unmarshal(encoded, &copy); err != nil {
		t.Fatalf("decode copied item definition: %v", err)
	}
	public := copy["public"].(map[string]any)
	public["display_name"] = displayName
	return copy
}

func assertDraftCommitAcknowledgmentLoss(t *testing.T, ctx context.Context, superDB *sql.DB, appDSN string, repository *AdminRepository, gameID, username string) string {
	t.Helper()
	var actorID string
	if err := superDB.QueryRowContext(ctx, `SELECT admin_id::text FROM admin_users WHERE username = $1`, username).Scan(&actorID); err != nil {
		t.Fatalf("load Author identity for commit-acknowledgment regression: %v", err)
	}
	loseAcknowledgment := &atomic.Bool{}
	loseAcknowledgment.Store(true)
	ackLossDB := sql.OpenDB(&commitAcknowledgmentLossConnector{
		base: stdlib.GetDefaultDriver(), dsn: appDSN, loseAcknowledgment: loseAcknowledgment,
	})
	defer func() {
		if err := ackLossDB.Close(); err != nil {
			t.Errorf("close commit-acknowledgment-loss repository: %v", err)
		}
	}()
	ackLossRepository := newAdminRepository(ackLossDB)
	const contentID = "demo:item/commit-ack-lost"
	if _, err := ackLossRepository.CreateDraft(ctx, gameID, contentID, []byte(`{}`), actorID); !errors.Is(err, persistence.ErrUnknown) {
		t.Fatalf("draft creation after actual commit acknowledgment loss error = %v, want persistence.ErrUnknown", err)
	}
	var draftCount, auditCount int
	if err := superDB.QueryRowContext(ctx, `SELECT count(*) FROM content_drafts WHERE game_id = $1 AND content_id = $2`, gameID, contentID).Scan(&draftCount); err != nil {
		t.Fatalf("count committed draft after lost acknowledgment: %v", err)
	}
	if err := superDB.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs WHERE game_id = $1 AND action = 'item_draft.create' AND resource_id = $2 AND outcome = 'success'`, gameID, contentID).Scan(&auditCount); err != nil {
		t.Fatalf("count successful audit after lost acknowledgment: %v", err)
	}
	if draftCount != 1 || auditCount != 1 {
		t.Fatalf("committed draft/success-audit counts after lost acknowledgment = %d/%d, want 1/1", draftCount, auditCount)
	}
	drafts, total, err := repository.ListDrafts(ctx, gameID, contentID, 10, 0)
	if err != nil || total != 1 || len(drafts) != 1 {
		t.Fatalf("recover exact draft after lost acknowledgment = %d/%d, error %v; want one", len(drafts), total, err)
	}
	missingDrafts, missingTotal, err := repository.ListDrafts(ctx, gameID, "demo:item/not-created", 1, 0)
	if err != nil || missingTotal != 0 || len(missingDrafts) != 0 {
		t.Fatalf("list unknown exact content draft = %d/%d, error %v; want an empty result", len(missingDrafts), missingTotal, err)
	}
	return drafts[0].DraftID
}

func assertAdminRepositoryRejectsOverpoweredFixtures(t *testing.T, ctx context.Context, maintenanceDB *sql.DB, appDSN, appRole string) {
	t.Helper()
	dangerRole := randomTestIdentifier(t, "tbcg_test_bypass_")
	if _, err := maintenanceDB.ExecContext(ctx, "CREATE ROLE "+dangerRole+" NOLOGIN BYPASSRLS"); err != nil {
		t.Fatalf("create disposable over-privileged role fixture: %v", err)
	}
	t.Cleanup(func() {
		if _, err := maintenanceDB.ExecContext(context.Background(), "REVOKE "+dangerRole+" FROM "+appRole); err != nil {
			t.Errorf("revoke disposable over-privileged role fixture: %v", err)
		}
		if _, err := maintenanceDB.ExecContext(context.Background(), "DROP ROLE IF EXISTS "+dangerRole); err != nil {
			t.Errorf("drop disposable over-privileged role fixture: %v", err)
		}
	})
	if _, err := maintenanceDB.ExecContext(ctx, "GRANT "+dangerRole+" TO "+appRole); err != nil {
		t.Fatalf("grant disposable bypass-RLS fixture to application login: %v", err)
	}
	if repository, err := openAdminRepository(ctx, appDSN, true); err == nil {
		if closeErr := repository.Close(); closeErr != nil {
			t.Errorf("close repository accepted for over-privileged fixture: %v", closeErr)
		}
		t.Fatal("admin repository accepted a login inheriting BYPASSRLS")
	}
	if _, err := maintenanceDB.ExecContext(ctx, "REVOKE "+dangerRole+" FROM "+appRole); err != nil {
		t.Fatalf("remove disposable bypass-RLS fixture membership: %v", err)
	}
}

func assertAdminSessionCleanupIsBounded(t *testing.T, ctx context.Context, db *sql.DB, repository *AdminRepository) {
	t.Helper()
	now := time.Now().UTC()
	expiredHashes := make([][]byte, 3)
	for index := range expiredHashes {
		token := sha256.Sum256([]byte(randomTestIdentifier(t, "expired-session-")))
		csrf := sha256.Sum256([]byte(randomTestIdentifier(t, "expired-csrf-")))
		expiredHashes[index] = token[:]
		if _, err := db.ExecContext(ctx, `
INSERT INTO admin_sessions (token_hash, csrf_hash, created_at, expires_at, idle_expires_at)
VALUES ($1, $2, $3, $4, $5)`, token[:], csrf[:], now.Add(-3*time.Hour), now.Add(-time.Hour), now.Add(-2*time.Hour)); err != nil {
			t.Fatalf("seed expired admin session: %v", err)
		}
	}
	liveToken := sha256.Sum256([]byte(randomTestIdentifier(t, "live-session-")))
	liveCSRF := sha256.Sum256([]byte(randomTestIdentifier(t, "live-csrf-")))
	if _, err := db.ExecContext(ctx, `
INSERT INTO admin_sessions (token_hash, csrf_hash, expires_at, idle_expires_at)
VALUES ($1, $2, $3, $4)`, liveToken[:], liveCSRF[:], now.Add(time.Hour), now.Add(30*time.Minute)); err != nil {
		t.Fatalf("seed live admin session: %v", err)
	}

	var workers sync.WaitGroup
	results := make(chan int64, 2)
	errorsByWorker := make(chan error, 2)
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			deleted, err := repository.CleanupExpiredSessions(ctx, 1)
			results <- deleted
			errorsByWorker <- err
		}()
	}
	workers.Wait()
	close(results)
	close(errorsByWorker)
	var deletedTotal int64
	for deleted := range results {
		deletedTotal += deleted
	}
	for err := range errorsByWorker {
		if err != nil {
			t.Fatalf("run concurrent bounded admin session cleanup: %v", err)
		}
	}
	if deletedTotal != 2 {
		t.Fatalf("two concurrent cleanup batches deleted %d sessions, want 2", deletedTotal)
	}
	deleted, err := repository.CleanupExpiredSessions(ctx, 1)
	if err != nil || deleted != 1 {
		t.Fatalf("final bounded cleanup deleted %d sessions, error %v; want 1", deleted, err)
	}
	for _, tokenHash := range expiredHashes {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM admin_sessions WHERE token_hash = $1`, tokenHash).Scan(&count); err != nil || count != 0 {
			t.Fatalf("expired session remains after cleanup: count=%d error=%v", count, err)
		}
	}
	var liveCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM admin_sessions WHERE token_hash = $1`, liveToken[:]).Scan(&liveCount); err != nil || liveCount != 1 {
		t.Fatalf("live session cleanup count = %d, error %v; want 1", liveCount, err)
	}
	if _, err := repository.CleanupExpiredSessions(ctx, 0); err == nil {
		t.Fatal("session cleanup accepted an unbounded/empty batch size")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repository.CleanupExpiredSessions(cancelled, 1); err == nil {
		t.Fatal("session cleanup ignored context cancellation")
	}
	missingToken := sha256.Sum256([]byte(randomTestIdentifier(t, "missing-session-")))
	missingCSRF := sha256.Sum256([]byte(randomTestIdentifier(t, "missing-csrf-")))
	if _, err := repository.LoadSession(ctx, missingToken[:]); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("load missing session error = %v, want ErrNotFound", err)
	}
	if err := repository.UpdateCSRF(ctx, missingToken[:], missingCSRF[:]); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("rotate CSRF for missing session error = %v, want ErrNotFound", err)
	}
	if err := repository.TouchSession(ctx, missingToken[:], now.Add(time.Hour)); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("touch missing session error = %v, want ErrNotFound", err)
	}
	if err := repository.RevokeSession(ctx, missingToken[:]); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("revoke missing session error = %v, want ErrNotFound", err)
	}
}

func assertAdminSessionSecretsAreHashed(t *testing.T, ctx context.Context, db *sql.DB, client *http.Client, origin, username string) {
	t.Helper()
	parsedOrigin, err := url.Parse(origin)
	if err != nil {
		t.Fatalf("parse test management origin: %v", err)
	}
	var sessionToken, csrfToken string
	for _, cookie := range client.Jar.Cookies(parsedOrigin) {
		switch cookie.Name {
		case "tbcg_admin_session":
			sessionToken = cookie.Value
		case "tbcg_admin_csrf":
			csrfToken = cookie.Value
		}
	}
	decode := func(name, token string) []byte {
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || len(raw) != sha256.Size {
			t.Fatalf("decode %s browser token: invalid high-entropy token", name)
		}
		return raw
	}
	rawSession := decode("session", sessionToken)
	rawCSRF := decode("csrf", csrfToken)
	sessionHash := sha256.Sum256(rawSession)
	csrfHash := sha256.Sum256(rawCSRF)
	var adminID string
	if err := db.QueryRowContext(ctx, `SELECT admin_id::text FROM admin_users WHERE username = $1`, username).Scan(&adminID); err != nil {
		t.Fatalf("read test admin identity: %v", err)
	}
	var hashedMatch, rawSessionMatch, rawCSRFMatch int
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM admin_sessions WHERE admin_id = $1 AND token_hash = $2 AND csrf_hash = $3`, adminID, sessionHash[:], csrfHash[:]).Scan(&hashedMatch); err != nil {
		t.Fatalf("verify hashed admin session and CSRF credentials: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM admin_sessions WHERE admin_id = $1 AND token_hash = $2`, adminID, rawSession).Scan(&rawSessionMatch); err != nil {
		t.Fatalf("check for raw admin session token storage: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM admin_sessions WHERE admin_id = $1 AND csrf_hash = $2`, adminID, rawCSRF).Scan(&rawCSRFMatch); err != nil {
		t.Fatalf("check for raw admin CSRF token storage: %v", err)
	}
	if hashedMatch != 1 || rawSessionMatch != 0 || rawCSRFMatch != 0 {
		t.Fatalf("admin credential storage hashed/raw matches = %d/%d/%d, want 1/0/0", hashedMatch, rawSessionMatch, rawCSRFMatch)
	}
}
