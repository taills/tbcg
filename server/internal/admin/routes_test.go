package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

type routeTestStore struct {
	*loginTestStore
	allowRole    bool
	listedDrafts []Draft
	listContent  string
	listLimit    int
	listOffset   int
	listErr      error
}

func (s *routeTestStore) HasRole(context.Context, string, string, ...Role) (bool, error) {
	return s.allowRole, nil
}

func (s *routeTestStore) GetDraft(context.Context, string, string) (Draft, error) {
	return Draft{}, ErrNotFound
}

func (s *routeTestStore) ListDrafts(_ context.Context, _, contentID string, limit, offset int) ([]Draft, int, error) {
	s.listContent = contentID
	s.listLimit = limit
	s.listOffset = offset
	return s.listedDrafts, len(s.listedDrafts), s.listErr
}

func TestProtectedRouteErrorsUseConsistentEnvelopes(t *testing.T) {
	allowed := newAuthenticatedRouteStore(t, true)
	handler := NewHandler(allowed, secureTestConfig(), nil, nil)
	sessionCookie, csrfCookie := loginForRouteTest(t, handler)

	requests := []struct {
		name, method, path, body string
		want                     int
	}{
		{name: "missing draft read", method: http.MethodGet, path: "/api/v1/games/demo/item-drafts/0ad049e6-1234-4abc-8000-000000000001", want: http.StatusNotFound},
		{name: "missing draft update", method: http.MethodPut, path: "/api/v1/games/demo/item-drafts/0ad049e6-1234-4abc-8000-000000000001", body: `{"expected_draft_version":"1","definition":{}}`, want: http.StatusNotFound},
		{name: "zero update CAS rejected", method: http.MethodPut, path: "/api/v1/games/demo/item-drafts/0ad049e6-1234-4abc-8000-000000000001", body: `{"expected_draft_version":"0","definition":{}}`, want: http.StatusBadRequest},
		{name: "invalid create identity", method: http.MethodPost, path: "/api/v1/games/demo/item-drafts", body: `{"content_id":"Bad ID","expected_draft_version":"0","definition":{}}`, want: http.StatusBadRequest},
		{name: "invalid submit CAS", method: http.MethodPost, path: "/api/v1/games/demo/item-drafts/0ad049e6-1234-4abc-8000-000000000001/submit", body: `{"expected_draft_version":"0"}`, want: http.StatusBadRequest},
		{name: "missing submit draft", method: http.MethodPost, path: "/api/v1/games/demo/item-drafts/0ad049e6-1234-4abc-8000-000000000001/submit", body: `{"expected_draft_version":"1"}`, want: http.StatusNotFound},
		{name: "missing validation draft", method: http.MethodPost, path: "/api/v1/games/demo/item-drafts/0ad049e6-1234-4abc-8000-000000000001/validate", want: http.StatusNotFound},
		{name: "validation rejects unexpected body", method: http.MethodPost, path: "/api/v1/games/demo/item-drafts/0ad049e6-1234-4abc-8000-000000000001/validate", body: `{}`, want: http.StatusBadRequest},
		{name: "logout rejects unexpected body", method: http.MethodPost, path: "/api/v1/logout", body: `{}`, want: http.StatusBadRequest},
		{name: "invalid review decision", method: http.MethodPost, path: "/api/v1/games/demo/revision-approvals", body: `{"content_id":"demo:item/x","revision":0,"body_hash":"bad","decision":"pending"}`, want: http.StatusBadRequest},
		{name: "draft recovery requires exact content id", method: http.MethodGet, path: "/api/v1/games/demo/item-drafts", want: http.StatusBadRequest},
		{name: "draft recovery rejects duplicate content ids", method: http.MethodGet, path: "/api/v1/games/demo/item-drafts?content_id=demo:item/a&content_id=demo:item/b", want: http.StatusBadRequest},
		{name: "draft recovery rejects invalid limit", method: http.MethodGet, path: "/api/v1/games/demo/item-drafts?content_id=demo:item/a&limit=101", want: http.StatusBadRequest},
		{name: "draft recovery rejects invalid offset", method: http.MethodGet, path: "/api/v1/games/demo/item-drafts?content_id=demo:item/a&offset=bad", want: http.StatusBadRequest},
		{name: "review page limit out of range", method: http.MethodGet, path: "/api/v1/games/demo/review-queue?limit=101", want: http.StatusBadRequest},
		{name: "review offset out of range", method: http.MethodGet, path: "/api/v1/games/demo/review-queue?offset=-1", want: http.StatusBadRequest},
		{name: "unknown route", method: http.MethodGet, path: "/api/v1/unknown", want: http.StatusNotFound},
	}
	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			response := routeRequest(t, handler, sessionCookie, csrfCookie, test.method, test.path, test.body)
			if response.Code != test.want || !strings.Contains(response.Body.String(), `"success":false`) || !strings.Contains(response.Body.String(), `"error"`) {
				t.Fatalf("route response = %d %s", response.Code, response.Body.String())
			}
		})
	}

	listFailureStore := newAuthenticatedRouteStore(t, true)
	listFailureStore.listErr = errors.New("database unavailable")
	listFailureHandler := NewHandler(listFailureStore, secureTestConfig(), nil, nil)
	listFailureSession, listFailureCSRF := loginForRouteTest(t, listFailureHandler)
	listFailure := routeRequest(t, listFailureHandler, listFailureSession, listFailureCSRF, http.MethodGet, "/api/v1/games/demo/item-drafts?content_id=demo:item/a", "")
	if listFailure.Code != http.StatusInternalServerError {
		t.Fatalf("draft recovery storage failure status = %d, want 500", listFailure.Code)
	}

	deniedStore := newAuthenticatedRouteStore(t, false)
	deniedHandler := NewHandler(deniedStore, secureTestConfig(), nil, nil)
	deniedSession, deniedCSRF := loginForRouteTest(t, deniedHandler)
	denied := routeRequest(t, deniedHandler, deniedSession, deniedCSRF, http.MethodGet, "/api/v1/games/demo/review-queue", "")
	if denied.Code != http.StatusForbidden || len(deniedStore.auditEvents) < 1 {
		t.Fatalf("missing Reviewer permission response/audit = %d/%d", denied.Code, len(deniedStore.auditEvents))
	}
	deniedDraftList := routeRequest(t, deniedHandler, deniedSession, deniedCSRF, http.MethodGet, "/api/v1/games/demo/item-drafts?content_id=demo:item/a", "")
	if deniedDraftList.Code != http.StatusForbidden {
		t.Fatalf("unauthorized draft recovery status = %d, want 403", deniedDraftList.Code)
	}

	validationAuditStore := newAuthenticatedRouteStore(t, true)
	validationAuditStore.handlerTestStore.auditErr = errors.New("audit unavailable")
	validationHandler := NewHandler(validationAuditStore, secureTestConfig(), nil, nil)
	validationSession, validationCSRF := loginForRouteTest(t, validationHandler)
	candidate := `{"game_id":"demo","content_id":"demo:item/test","revision":1,"schema_version":1,"public":{"game_id":"demo","content_id":"demo:item/test","revision":1,"schema_version":1},"server":{}}`
	validationFailure := routeRequest(t, validationHandler, validationSession, validationCSRF, http.MethodPost, "/api/v1/games/demo/item-drafts", `{"content_id":"demo:item/test","expected_draft_version":"0","definition":`+candidate+`}`)
	if validationFailure.Code != http.StatusInternalServerError {
		t.Fatalf("validation audit failure status = %d, want 500", validationFailure.Code)
	}
}

func TestWriteFailureWithDataReturnsCurrentDraftEnvelope(t *testing.T) {
	latest := &Draft{DraftID: "0ad049e6-1234-4abc-8000-000000000001", ContentID: "demo:item/potion", DraftVersion: "2"}
	response := httptest.NewRecorder()
	writeFailureWithData(response, http.StatusConflict, "draft_version_conflict", "draft changed", latest, nil)
	var envelope struct {
		Success bool      `json:"success"`
		Data    Draft     `json:"data"`
		Error   *apiError `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode current-draft conflict envelope: %v", err)
	}
	if response.Code != http.StatusConflict || envelope.Success || envelope.Data.DraftVersion != "2" || envelope.Error == nil || envelope.Error.Code != "draft_version_conflict" {
		t.Fatalf("current-draft conflict response = %d %#v", response.Code, envelope)
	}
}

func TestLogoutClearsSecureScopedCookies(t *testing.T) {
	store := newAuthenticatedRouteStore(t, true)
	handler := NewHandler(store, secureTestConfig(), nil, nil)
	sessionCookie, csrfCookie := loginForRouteTest(t, handler)
	response := routeRequest(t, handler, sessionCookie, csrfCookie, http.MethodPost, "/api/v1/logout", "")
	if response.Code != http.StatusOK {
		t.Fatalf("logout status = %d, want 200: %s", response.Code, response.Body.String())
	}
	cookies := make(map[string]*http.Cookie)
	for _, cookie := range response.Result().Cookies() {
		cookies[cookie.Name] = cookie
	}
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		cookie := cookies[name]
		if cookie == nil || cookie.MaxAge >= 0 || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
			t.Errorf("cleared %s cookie attributes are unsafe: %#v", name, cookie)
		}
		if name == sessionCookieName && cookie != nil && !cookie.HttpOnly {
			t.Error("cleared session cookie lost HttpOnly attribute")
		}
	}
}

func TestListDraftsRequiresExactContentAndUsesBoundedPagination(t *testing.T) {
	store := newAuthenticatedRouteStore(t, true)
	store.listedDrafts = []Draft{{DraftID: "0ad049e6-1234-4abc-8000-000000000001", GameID: "demo", ContentID: "demo:item/potion", DraftVersion: "1"}}
	handler := NewHandler(store, secureTestConfig(), nil, nil)
	session, csrf := loginForRouteTest(t, handler)

	response := routeRequest(t, handler, session, csrf, http.MethodGet, "/api/v1/games/demo/item-drafts?content_id=demo:item/potion&limit=10&offset=5", "")
	if response.Code != http.StatusOK || store.listContent != "demo:item/potion" || store.listLimit != 10 || store.listOffset != 5 {
		t.Fatalf("draft recovery listing status/filter/page = %d/%q/%d/%d: %s", response.Code, store.listContent, store.listLimit, store.listOffset, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"total":1`) || !strings.Contains(response.Body.String(), `"draft_id":"0ad049e6-1234-4abc-8000-000000000001"`) {
		t.Fatalf("draft recovery listing omitted resource or pagination metadata: %s", response.Body.String())
	}
	invalid := routeRequest(t, handler, session, csrf, http.MethodGet, "/api/v1/games/demo/item-drafts?content_id=../secret", "")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid draft recovery content_id status = %d, want 400", invalid.Code)
	}
}

func newAuthenticatedRouteStore(t *testing.T, allowRole bool) *routeTestStore {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("route-test-password"), minimumBcryptCost)
	if err != nil {
		t.Fatalf("hash route test password: %v", err)
	}
	return &routeTestStore{
		loginTestStore: &loginTestStore{
			handlerTestStore: &handlerTestStore{},
			sessions:         make(map[string]Session),
			user:             User{ID: "f8ec170d-021a-4a11-99aa-133b8c797b07", Username: "route-user", Hash: string(hash)},
		},
		allowRole: allowRole,
	}
}

func loginForRouteTest(t *testing.T, handler http.Handler) (*http.Cookie, *http.Cookie) {
	t.Helper()
	bootstrap := httptest.NewRecorder()
	handler.ServeHTTP(bootstrap, httptest.NewRequest(http.MethodGet, "/api/v1/session", nil))
	anonymousSession, anonymousCSRF := cookieValues(t, bootstrap.Result().Cookies())
	login := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"username":"route-user","password":"route-test-password"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://127.0.0.1:8443")
	login.Header.Set("X-CSRF-Token", anonymousCSRF.Value)
	login.AddCookie(anonymousSession)
	login.AddCookie(anonymousCSRF)
	loggedIn := httptest.NewRecorder()
	handler.ServeHTTP(loggedIn, login)
	if loggedIn.Code != http.StatusOK {
		t.Fatalf("login route fixture status = %d: %s", loggedIn.Code, loggedIn.Body.String())
	}
	return cookieValues(t, loggedIn.Result().Cookies())
}

func routeRequest(t *testing.T, handler http.Handler, sessionCookie, csrfCookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Origin", "https://127.0.0.1:8443")
	request.AddCookie(sessionCookie)
	request.AddCookie(csrfCookie)
	if method != http.MethodGet {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrfCookie.Value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
