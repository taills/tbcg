package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type handlerTestStore struct {
	createdTokenHash []byte
	createdCSRFHash  []byte
	authCalls        int
	auditErr         error
}

func (s *handlerTestStore) LoadSession(context.Context, []byte) (Session, error) {
	return Session{}, ErrNotFound
}

func (s *handlerTestStore) CreateSession(_ context.Context, tokenHash, csrfHash []byte, _, _ time.Time) error {
	s.createdTokenHash = append([]byte(nil), tokenHash...)
	s.createdCSRFHash = append([]byte(nil), csrfHash...)
	return nil
}

func (*handlerTestStore) UpdateCSRF(context.Context, []byte, []byte) error { return nil }

func (*handlerTestStore) TouchSession(context.Context, []byte, time.Time) error { return nil }

func (s *handlerTestStore) Authenticate(context.Context, string) (User, error) {
	s.authCalls++
	return User{}, errors.New("unexpected authentication call")
}

func (*handlerTestStore) RotateSession(context.Context, []byte, []byte, []byte, time.Time, time.Time, string) error {
	return errors.New("unexpected session rotation")
}

func (*handlerTestStore) RevokeSession(context.Context, []byte) error { return nil }

func (*handlerTestStore) HasRole(context.Context, string, string, ...Role) (bool, error) {
	return false, nil
}

func (s *handlerTestStore) WriteAudit(context.Context, AuditEvent) error { return s.auditErr }

func (*handlerTestStore) GetDraft(context.Context, string, string) (Draft, error) {
	return Draft{}, ErrNotFound
}

func (*handlerTestStore) ListDrafts(context.Context, string, string, int, int) ([]Draft, int, error) {
	return nil, 0, nil
}

func (*handlerTestStore) CreateDraft(context.Context, string, string, []byte, string) (Draft, error) {
	return Draft{}, ErrNotFound
}

func (*handlerTestStore) CompareAndSwapDraft(context.Context, string, string, int64, []byte, string) (Draft, *Draft, error) {
	return Draft{}, nil, ErrNotFound
}

func (*handlerTestStore) SubmitRevision(context.Context, string, string, int64, string) (Revision, error) {
	return Revision{}, ErrNotFound
}

func (*handlerTestStore) ReviewQueue(context.Context, string, int, int) ([]Revision, int, error) {
	return nil, 0, nil
}

func (*handlerTestStore) ReviewRevision(context.Context, string, string, int32, []byte, string, string, string) (Approval, error) {
	return Approval{}, ErrNotFound
}

func TestAnonymousSessionSetsSecureCookieAndBoundCSRFToken(t *testing.T) {
	store := &handlerTestStore{}
	handler := NewHandler(store, Config{
		ExpectedOrigin: "https://127.0.0.1:8443",
		SessionTTL:     time.Hour,
		IdleTTL:        15 * time.Minute,
	}, nil, nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/session", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("session status = %d, want 200: %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("session cookies = %d, want session and CSRF cookies", len(cookies))
	}
	cookieByName := make(map[string]*http.Cookie, len(cookies))
	for _, cookie := range cookies {
		cookieByName[cookie.Name] = cookie
	}
	sessionCookie := cookieByName[sessionCookieName]
	csrfCookie := cookieByName[csrfCookieName]
	if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure || sessionCookie.Domain != "" || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie attributes are unsafe: %#v", sessionCookie)
	}
	if csrfCookie == nil || csrfCookie.HttpOnly || !csrfCookie.Secure || csrfCookie.Domain != "" || csrfCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("CSRF cookie attributes are unsafe: %#v", csrfCookie)
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Authenticated bool   `json:"authenticated"`
			CSRFToken     string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if !envelope.Success || envelope.Data.Authenticated || len(envelope.Data.CSRFToken) < 32 {
		t.Fatalf("anonymous session data is incomplete: %#v", envelope)
	}
	if csrfCookie.Value != envelope.Data.CSRFToken {
		t.Fatal("session CSRF cookie and response token do not match")
	}
	if len(store.createdTokenHash) != sha256.Size || len(store.createdCSRFHash) != sha256.Size {
		t.Fatalf("stored hashes have lengths token=%d csrf=%d; want 32-byte digests", len(store.createdTokenHash), len(store.createdCSRFHash))
	}
	if string(store.createdTokenHash) == envelope.Data.CSRFToken {
		t.Fatal("CSRF token was stored in plaintext instead of as a digest")
	}
}

func TestLoginRejectsNonExactOriginBeforeCredentialLookup(t *testing.T) {
	store := &handlerTestStore{}
	handler := NewHandler(store, Config{ExpectedOrigin: "https://127.0.0.1:8443"}, nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"username":"editor","password":"test"}`))
	request.Header.Set("Origin", "https://127.0.0.1.attacker.invalid:8443")
	request.Header.Set("X-CSRF-Token", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("login with non-exact Origin status = %d, want 403", response.Code)
	}
	if store.authCalls != 0 {
		t.Fatalf("credential lookup called %d times after bad Origin, want 0", store.authCalls)
	}
}

func TestValidatePasswordLimitUsesUTF8Bytes(t *testing.T) {
	if err := validatePasswordBytes(""); err == nil {
		t.Fatal("empty password was accepted")
	}
	if err := validatePasswordBytes(strings.Repeat("a", 72)); err != nil {
		t.Fatalf("72-byte password rejected: %v", err)
	}
	if err := validatePasswordBytes(strings.Repeat("a", 73)); err == nil {
		t.Fatal("73-byte password was not rejected")
	}
	if err := validatePasswordBytes(strings.Repeat("é", 36)); err != nil {
		t.Fatalf("72-byte UTF-8 password rejected: %v", err)
	}
}

type loginTestStore struct {
	*handlerTestStore
	sessions    map[string]Session
	user        User
	authErr     error
	auditEvents []AuditEvent
}

func (s *loginTestStore) LoadSession(_ context.Context, tokenHash []byte) (Session, error) {
	session, ok := s.sessions[hex.EncodeToString(tokenHash)]
	if !ok {
		return Session{}, ErrNotFound
	}
	return session, nil
}

func (s *loginTestStore) CreateSession(_ context.Context, tokenHash, csrfHash []byte, expiresAt, idleExpiresAt time.Time) error {
	s.handlerTestStore.createdTokenHash = append([]byte(nil), tokenHash...)
	s.handlerTestStore.createdCSRFHash = append([]byte(nil), csrfHash...)
	s.sessions[hex.EncodeToString(tokenHash)] = Session{CSRFHash: append([]byte(nil), csrfHash...), ExpiresAt: expiresAt, IdleExpiresAt: idleExpiresAt}
	return nil
}

func (s *loginTestStore) UpdateCSRF(_ context.Context, tokenHash, csrfHash []byte) error {
	key := hex.EncodeToString(tokenHash)
	session, ok := s.sessions[key]
	if !ok {
		return ErrNotFound
	}
	session.CSRFHash = append([]byte(nil), csrfHash...)
	s.sessions[key] = session
	return nil
}

func (s *loginTestStore) TouchSession(_ context.Context, tokenHash []byte, idleExpiresAt time.Time) error {
	key := hex.EncodeToString(tokenHash)
	session, ok := s.sessions[key]
	if !ok {
		return ErrNotFound
	}
	session.IdleExpiresAt = idleExpiresAt
	s.sessions[key] = session
	return nil
}

func (s *loginTestStore) Authenticate(ctx context.Context, username string) (User, error) {
	s.handlerTestStore.authCalls++
	if s.authErr != nil {
		return User{}, s.authErr
	}
	if username != s.user.Username {
		return User{}, ErrNotFound
	}
	return s.user, nil
}

func (s *loginTestStore) RotateSession(_ context.Context, oldTokenHash, newTokenHash, csrfHash []byte, expiresAt, idleExpiresAt time.Time, adminID string) error {
	delete(s.sessions, hex.EncodeToString(oldTokenHash))
	s.sessions[hex.EncodeToString(newTokenHash)] = Session{AdminID: adminID, Username: s.user.Username, CSRFHash: append([]byte(nil), csrfHash...), ExpiresAt: expiresAt, IdleExpiresAt: idleExpiresAt}
	s.auditEvents = append(s.auditEvents, AuditEvent{Action: "admin.login", Outcome: "success"})
	return nil
}

func (s *loginTestStore) WriteAudit(_ context.Context, event AuditEvent) error {
	if s.handlerTestStore.auditErr != nil {
		return s.handlerTestStore.auditErr
	}
	s.auditEvents = append(s.auditEvents, event)
	return nil
}

func TestLoginRequiresSessionBoundCSRFAndRotatesBothTokens(t *testing.T) {
	password := "correct horse battery staple"
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), minimumBcryptCost)
	if err != nil {
		t.Fatalf("create test password hash: %v", err)
	}
	store := &loginTestStore{
		handlerTestStore: &handlerTestStore{},
		sessions:         make(map[string]Session),
		user:             User{ID: "f8ec170d-021a-4a11-99aa-133b8c797b07", Username: "editor", Hash: string(passwordHash)},
	}
	handler := NewHandler(store, Config{ExpectedOrigin: "https://127.0.0.1:8443"}, nil, nil)

	bootstrapResponse := httptest.NewRecorder()
	handler.ServeHTTP(bootstrapResponse, httptest.NewRequest(http.MethodGet, "/api/v1/session", nil))
	cookies := bootstrapResponse.Result().Cookies()
	oldSession, oldCSRF := cookieValues(t, cookies)

	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"username":"editor","password":"correct horse battery staple"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginRequest.Header.Set("Origin", "https://127.0.0.1:8443")
	loginRequest.Header.Set("X-CSRF-Token", oldCSRF.Value)
	loginRequest.AddCookie(oldSession)
	loginRequest.AddCookie(oldCSRF)
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("valid login status = %d, want 200: %s", loginResponse.Code, loginResponse.Body.String())
	}
	newSession, newCSRF := cookieValues(t, loginResponse.Result().Cookies())
	if newSession.Value == oldSession.Value || newCSRF.Value == oldCSRF.Value {
		t.Fatal("successful login did not rotate both session and CSRF tokens")
	}
	if _, ok := store.sessions[hex.EncodeToString(store.createdTokenHash)]; ok {
		t.Fatal("anonymous session remained valid after login rotation")
	}
	newToken, err := base64.RawURLEncoding.DecodeString(newSession.Value)
	if err != nil {
		t.Fatalf("decode rotated session token: %v", err)
	}
	newTokenHash := sha256.Sum256(newToken)
	stored, ok := store.sessions[hex.EncodeToString(newTokenHash[:])]
	if !ok || stored.AdminID != store.user.ID || subtle.ConstantTimeCompare(stored.CSRFHash, hashToken(t, newCSRF.Value)) != 1 {
		t.Fatal("rotated session was not stored with the authenticated user and CSRF digest")
	}
}

func TestLoginRejectsMissingSessionBoundCSRF(t *testing.T) {
	store := &loginTestStore{handlerTestStore: &handlerTestStore{}, sessions: make(map[string]Session)}
	handler := NewHandler(store, Config{ExpectedOrigin: "https://127.0.0.1:8443"}, nil, nil)
	bootstrapResponse := httptest.NewRecorder()
	handler.ServeHTTP(bootstrapResponse, httptest.NewRequest(http.MethodGet, "/api/v1/session", nil))
	cookies := bootstrapResponse.Result().Cookies()
	sessionCookie, csrfCookie := cookieValues(t, cookies)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"username":"editor","password":"password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://127.0.0.1:8443")
	request.Header.Set("X-CSRF-Token", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	request.AddCookie(sessionCookie)
	request.AddCookie(csrfCookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || store.authCalls != 0 {
		t.Fatalf("invalid CSRF login status/auth calls = %d/%d, want 403/0", response.Code, store.authCalls)
	}
}

type failingBodyReader struct{}

func (failingBodyReader) Read([]byte) (int, error) {
	return 0, errors.New("injected body read failure")
}

func TestEnsureEmptyBodyMapsReadErrorsAndOversizeBody(t *testing.T) {
	readFailure := httptest.NewRecorder()
	failedRequest := httptest.NewRequest(http.MethodPost, "/logout", nil)
	failedRequest.Body = io.NopCloser(failingBodyReader{})
	if ensureEmptyBody(readFailure, failedRequest) || readFailure.Code != http.StatusBadRequest {
		t.Fatalf("body read failure response = %d, want 400", readFailure.Code)
	}

	oversizeResponse := httptest.NewRecorder()
	oversizeRequest := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader("body"))
	oversizeRequest.Body = http.MaxBytesReader(oversizeResponse, oversizeRequest.Body, 1)
	if ensureEmptyBody(oversizeResponse, oversizeRequest) || oversizeResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize empty-body response = %d, want 413", oversizeResponse.Code)
	}
}

func cookieValues(t *testing.T, cookies []*http.Cookie) (*http.Cookie, *http.Cookie) {
	t.Helper()
	values := make(map[string]*http.Cookie)
	for _, cookie := range cookies {
		values[cookie.Name] = cookie
	}
	session, csrf := values[sessionCookieName], values[csrfCookieName]
	if session == nil || csrf == nil {
		t.Fatalf("missing session/CSRF cookie in response: %#v", cookies)
	}
	return session, csrf
}

func hashToken(t *testing.T, token string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("decode test CSRF token: %v", err)
	}
	hash := sha256.Sum256(raw)
	return hash[:]
}
