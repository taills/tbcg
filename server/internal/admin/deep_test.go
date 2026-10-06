package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"example.com/tbcg/server/internal/content/schema"
)

type failingAnonymousSessionStore struct {
	handlerTestStore
}

func (*failingAnonymousSessionStore) CreateSession(context.Context, []byte, []byte, time.Time, time.Time) error {
	return errors.New("temporary database failure")
}

type sessionBoundaryStore struct {
	handlerTestStore
	session  Session
	loadErr  error
	csrfErr  error
	touchErr error
	csrf     string
}

func (s *sessionBoundaryStore) LoadSession(context.Context, []byte) (Session, error) {
	return s.session, s.loadErr
}

func (s *sessionBoundaryStore) UpdateCSRF(context.Context, []byte, []byte) error {
	return s.csrfErr
}

func (s *sessionBoundaryStore) TouchSession(context.Context, []byte, time.Time) error {
	return s.touchErr
}

func TestSessionRefreshRotatesMissingCSRFWithoutChangingSession(t *testing.T) {
	store := &loginTestStore{handlerTestStore: &handlerTestStore{}, sessions: make(map[string]Session)}
	handler := NewHandler(store, secureTestConfig(), nil, nil)
	initial := httptest.NewRecorder()
	handler.ServeHTTP(initial, httptest.NewRequest(http.MethodGet, "/api/v1/session", nil))
	oldSession, oldCSRF := cookieValues(t, initial.Result().Cookies())

	request := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	request.AddCookie(oldSession)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("session refresh status = %d, want 200", response.Code)
	}
	var newCSRF *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == csrfCookieName {
			newCSRF = cookie
		}
	}
	if newCSRF == nil || oldCSRF.Value == newCSRF.Value {
		t.Fatal("missing CSRF cookie did not rotate only the CSRF token")
	}
	storedSession, err := store.LoadSession(context.Background(), store.createdTokenHash)
	if err != nil {
		t.Fatalf("load refreshed anonymous session: %v", err)
	}
	if subtle.ConstantTimeCompare(storedSession.CSRFHash, hashToken(t, newCSRF.Value)) != 1 {
		t.Fatal("rotated CSRF token was not bound to the server session hash")
	}
}

func TestSessionReadSurfacesLoadCSRFAndIdleExpiryFailures(t *testing.T) {
	for _, test := range []struct {
		name  string
		store *sessionBoundaryStore
	}{
		{name: "session load", store: &sessionBoundaryStore{loadErr: errors.New("database unavailable")}},
		{name: "CSRF hash rotation", store: &sessionBoundaryStore{
			session: Session{CSRFHash: make([]byte, sha256.Size), ExpiresAt: time.Now().Add(time.Hour)},
			csrfErr: errors.New("database unavailable"),
		}},
		{name: "idle expiry update", store: func() *sessionBoundaryStore {
			raw := bytes.Repeat([]byte{7}, sha256.Size)
			hash := sha256.Sum256(raw)
			return &sessionBoundaryStore{
				session:  Session{CSRFHash: hash[:], ExpiresAt: time.Now().Add(time.Hour)},
				touchErr: errors.New("database unavailable"),
				csrf:     base64.RawURLEncoding.EncodeToString(raw),
			}
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(test.store, secureTestConfig(), nil, nil)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
			rawSession := make([]byte, sha256.Size)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: base64.RawURLEncoding.EncodeToString(rawSession)})
			if test.store.csrf != "" {
				request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: test.store.csrf})
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("session error status = %d, want 500: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRejectedOriginAuditFailureReturnsServerError(t *testing.T) {
	store := &handlerTestStore{auditErr: errors.New("audit database unavailable")}
	handler := NewHandler(store, secureTestConfig(), nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/logout", nil)
	request.Header.Set("Origin", "https://attacker.invalid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("Origin audit failure status = %d, want 500", response.Code)
	}
}

func TestAnonymousSessionDatabaseFailureReturnsEnvelope(t *testing.T) {
	handler := NewHandler(&failingAnonymousSessionStore{}, secureTestConfig(), nil, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/session", nil))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"success":false`) {
		t.Fatalf("anonymous session database failure response = %d %s", response.Code, response.Body.String())
	}
}

func TestPrepareDefinitionEnforcesServerIdentityAndCanonicalSchema(t *testing.T) {
	validator, err := schema.New(os.DirFS("../../../contracts"))
	if err != nil {
		t.Fatalf("compile shared item schema: %v", err)
	}
	icon := []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>")
	assetHash := sha256.Sum256(icon)
	assetFS := fstest.MapFS{
		"demo/assets/manifest.json":         {Data: []byte(`{"schema_version":1,"assets":[{"asset_id":"demo:icon/placeholder","path":"icons/placeholder.svg","sha256":"` + hex.EncodeToString(assetHash[:]) + `","source":"Original test asset","license":"TBCG original"}]}`)},
		"demo/assets/icons/placeholder.svg": {Data: icon},
	}
	server := &handler{validator: validator, assets: assetFS}
	fixturePath := filepath.Join("../../../tests/fixtures/content", "item.valid.json")
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read canonical item fixture: %v", err)
	}
	fixture = []byte(strings.Replace(string(fixture), `"item/potion-small"`, `"demo:icon/placeholder"`, 1))
	if _, issues := server.prepareDefinition("demo", "demo:item/healing-potion", 1, fixture); len(issues) != 0 {
		t.Fatalf("valid definition issues = %#v", issues)
	}

	wrongIdentity := []byte(strings.Replace(string(fixture), `"content_id": "demo:item/healing-potion"`, `"content_id": "demo:item/other"`, 1))
	if _, issues := server.prepareDefinition("demo", "demo:item/healing-potion", 1, wrongIdentity); len(issues) == 0 || issues[0].Pointer != "/content_id" {
		t.Fatalf("client-owned content identity was not rejected: %#v", issues)
	}
	badJSON, issues := server.prepareDefinition("demo", "demo:item/healing-potion", 1, []byte("{"))
	if badJSON != nil || len(issues) != 1 || issues[0].Code != "invalid_json" {
		t.Fatalf("malformed definition result = %#v, %#v", badJSON, issues)
	}
	missingPublic, issues := server.prepareDefinition("demo", "demo:item/healing-potion", 1, []byte(`{"game_id":"demo"}`))
	if missingPublic != nil || len(issues) != 1 || issues[0].Pointer != "/public" {
		t.Fatalf("missing public object result = %#v, %#v", missingPublic, issues)
	}

	var definition map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &definition); err != nil {
		t.Fatalf("decode item fixture: %v", err)
	}
	var public map[string]json.RawMessage
	if err := json.Unmarshal(definition["public"], &public); err != nil {
		t.Fatalf("decode public fixture: %v", err)
	}
	public["unknown/field"] = json.RawMessage(`true`)
	encodedPublic, err := json.Marshal(public)
	if err != nil {
		t.Fatalf("encode public Definition with unknown field: %v", err)
	}
	definition["public"] = encodedPublic
	withUnknown, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("encode Definition with unknown field: %v", err)
	}
	if _, issues := server.prepareDefinition("demo", "demo:item/healing-potion", 1, withUnknown); len(issues) == 0 || validationMessage(issues[0].Code) != "字段不受支持" {
		t.Fatalf("unknown field did not receive its controlled message: %#v", issues)
	}

	for code, want := range map[string]string{
		schema.CodeMissingField: "必填字段缺失",
		schema.CodeUnknownField: "字段不受支持",
		"invalid_reference":     "引用不存在或缺少来源许可",
		"server_owned_field":    "该身份字段由服务端管理",
		"other":                 "字段值不符合内容 Schema",
	} {
		if got := validationMessage(code); got != want {
			t.Errorf("validationMessage(%q) = %q, want %q", code, got, want)
		}
	}
}
