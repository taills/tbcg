package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestFailedLoginIsGenericAndWritesOnlyDeniedAudit(t *testing.T) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("known-password"), minimumBcryptCost)
	if err != nil {
		t.Fatalf("hash test account password: %v", err)
	}
	makeStore := func() *loginTestStore {
		return &loginTestStore{
			handlerTestStore: &handlerTestStore{},
			sessions:         make(map[string]Session),
			user:             User{ID: "f8ec170d-021a-4a11-99aa-133b8c797b07", Username: "known-user", Hash: string(passwordHash)},
		}
	}
	unknownStore := makeStore()
	unknownHandler := NewHandler(unknownStore, secureTestConfig(), nil, nil)
	unknownResponse := postLoginAfterAnonymousSession(t, unknownHandler, "missing-user", "secret-a")
	wrongPasswordStore := makeStore()
	wrongPasswordHandler := NewHandler(wrongPasswordStore, secureTestConfig(), nil, nil)
	wrongPasswordResponse := postLoginAfterAnonymousSession(t, wrongPasswordHandler, "known-user", "secret-b")
	if unknownResponse.Code != http.StatusUnauthorized || wrongPasswordResponse.Code != http.StatusUnauthorized {
		t.Fatalf("credential rejection statuses = %d/%d, want 401/401", unknownResponse.Code, wrongPasswordResponse.Code)
	}
	if unknownResponse.Body.String() != wrongPasswordResponse.Body.String() {
		t.Fatalf("credential rejection response differs by account existence: %s / %s", unknownResponse.Body.String(), wrongPasswordResponse.Body.String())
	}
	if strings.Contains(unknownResponse.Body.String(), "missing-user") || strings.Contains(wrongPasswordResponse.Body.String(), "secret-b") {
		t.Fatal("credential rejection response leaked an account name or password")
	}
	if len(unknownStore.auditEvents) != 1 || unknownStore.auditEvents[0].Outcome != "denied" || strings.Contains(string(unknownStore.auditEvents[0].DetailsJSON), "secret") {
		t.Fatalf("failed-login audit was missing or contained credentials: %#v", unknownStore.auditEvents)
	}
}

func TestLoginUsesUniformBcryptComparisonPolicy(t *testing.T) {
	validHash, err := bcrypt.GenerateFromPassword([]byte("known-password"), minimumBcryptCost)
	if err != nil {
		t.Fatalf("hash test account password: %v", err)
	}
	higherCostHash, err := bcrypt.GenerateFromPassword([]byte("known-password"), minimumBcryptCost+1)
	if err != nil {
		t.Fatalf("hash higher-cost test account password: %v", err)
	}
	malformedHash := "not-a-bcrypt-hash"
	for _, test := range []struct {
		name            string
		username        string
		password        string
		user            User
		wantRealCalls   int
		wantDummyCalls  int
		wantCalls       int
		wantPlaceholder bool
	}{
		{name: "known wrong password compares real once", username: "known-user", password: "wrong-password", user: User{ID: "f8ec170d-021a-4a11-99aa-133b8c797b07", Username: "known-user", Hash: string(validHash)}, wantRealCalls: 1, wantCalls: 1},
		{name: "unknown user compares dummy once", username: "missing-user", password: "wrong-password", wantDummyCalls: 1, wantCalls: 1},
		{name: "malformed stored hash compares dummy once", username: "known-user", password: "wrong-password", user: User{ID: "f8ec170d-021a-4a11-99aa-133b8c797b07", Username: "known-user", Hash: malformedHash}, wantDummyCalls: 1, wantCalls: 1},
		{name: "higher-cost stored hash uses dummy once", username: "known-user", password: "wrong-password", user: User{ID: "f8ec170d-021a-4a11-99aa-133b8c797b07", Username: "known-user", Hash: string(higherCostHash)}, wantDummyCalls: 1, wantCalls: 1},
		{name: "overlong password compares dummy placeholder once", username: "known-user", password: strings.Repeat("x", 73), user: User{ID: "f8ec170d-021a-4a11-99aa-133b8c797b07", Username: "known-user", Hash: string(validHash)}, wantDummyCalls: 1, wantCalls: 1, wantPlaceholder: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dummyHash, err := bcrypt.GenerateFromPassword([]byte("test-dummy-password"), minimumBcryptCost)
			if err != nil {
				t.Fatalf("hash test dummy password: %v", err)
			}
			var comparedHashes []string
			var comparedPasswords []string
			found := test.user.ID != ""
			valid := verifyPasswordCandidate(found, []byte(test.user.Hash), []byte(test.password), dummyHash, func(hash, password []byte) error {
				comparedHashes = append(comparedHashes, string(hash))
				comparedPasswords = append(comparedPasswords, string(password))
				return bcrypt.CompareHashAndPassword(hash, password)
			})
			if valid {
				t.Fatal("unexpectedly accepted an invalid credential")
			}
			if len(comparedHashes) != test.wantCalls {
				t.Fatalf("bcrypt comparisons = %d, want %d", len(comparedHashes), test.wantCalls)
			}
			realCalls, dummyCalls, usedPlaceholder := 0, 0, false
			for index, hash := range comparedHashes {
				if hash == string(validHash) {
					realCalls++
				}
				if hash == string(dummyHash) {
					dummyCalls++
				}
				if comparedPasswords[index] == "invalid-password" {
					usedPlaceholder = true
				}
				if len(comparedPasswords[index]) > 72 {
					t.Fatal("bcrypt received an overlong password instead of a valid placeholder")
				}
			}
			if realCalls != test.wantRealCalls || dummyCalls != test.wantDummyCalls {
				t.Fatalf("real/dummy hash comparisons = %d/%d, want %d/%d", realCalls, dummyCalls, test.wantRealCalls, test.wantDummyCalls)
			}
			if usedPlaceholder != test.wantPlaceholder {
				t.Fatalf("used valid password placeholder = %t, want %t", usedPlaceholder, test.wantPlaceholder)
			}
		})
	}
}

func TestLoginSuccessfulCredentialComparesOnlyExactCostRealHash(t *testing.T) {
	const password = "known-password"
	realHash, err := bcrypt.GenerateFromPassword([]byte(password), minimumBcryptCost)
	if err != nil {
		t.Fatalf("hash successful test credential: %v", err)
	}
	dummyHash, err := bcrypt.GenerateFromPassword([]byte("test-dummy-password"), minimumBcryptCost)
	if err != nil {
		t.Fatalf("hash test dummy credential: %v", err)
	}
	comparisons := 0
	valid := verifyPasswordCandidate(true, realHash, []byte(password), dummyHash, func(hash, candidate []byte) error {
		comparisons++
		return bcrypt.CompareHashAndPassword(hash, candidate)
	})
	if !valid || comparisons != 1 {
		t.Fatalf("successful password validation/comparison count = %t/%d, want true/1", valid, comparisons)
	}
}

func TestLoginReportsDatabaseFailureWithoutMislabelingItAsBadCredentials(t *testing.T) {
	store := &loginTestStore{
		handlerTestStore: &handlerTestStore{},
		sessions:         make(map[string]Session),
		authErr:          errors.New("database unavailable"),
	}
	handler := NewHandler(store, secureTestConfig(), nil, nil)
	response := postLoginAfterAnonymousSession(t, handler, "author", "wrong-password")
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "wrong-password") {
		t.Fatalf("authentication database failure response = %d %s", response.Code, response.Body.String())
	}
}

func TestLoginRateLimitAndAuditFailureAreExplicit(t *testing.T) {
	store := &loginTestStore{handlerTestStore: &handlerTestStore{}, sessions: make(map[string]Session)}
	config := secureTestConfig()
	config.LoginsPerIPPerMinute = 1
	handler := NewHandler(store, config, nil, nil)
	first := postLoginAfterAnonymousSession(t, handler, "missing-user", "not-a-real-secret")
	second := postLoginAfterAnonymousSession(t, handler, "missing-user", "not-a-real-secret")
	if first.Code != http.StatusUnauthorized || second.Code != http.StatusTooManyRequests {
		t.Fatalf("limited login statuses = %d/%d, want 401/429", first.Code, second.Code)
	}
	if len(store.auditEvents) != 2 || store.auditEvents[1].Outcome != "denied" {
		t.Fatalf("login denial/rate-limit audits = %#v", store.auditEvents)
	}

	auditFailureStore := &loginTestStore{
		handlerTestStore: &handlerTestStore{auditErr: errors.New("audit store unavailable")},
		sessions:         make(map[string]Session),
	}
	auditFailureHandler := NewHandler(auditFailureStore, secureTestConfig(), nil, nil)
	auditFailure := postLoginAfterAnonymousSession(t, auditFailureHandler, "missing-user", "not-a-real-secret")
	if auditFailure.Code != http.StatusInternalServerError {
		t.Fatalf("failed audit persistence status = %d, want 500", auditFailure.Code)
	}
	if strings.Contains(auditFailure.Body.String(), "not-a-real-secret") {
		t.Fatal("audit persistence error response leaked the password")
	}
}

func TestRateLimitedLoginRequiresAuditWrite(t *testing.T) {
	store := &handlerTestStore{auditErr: errors.New("audit store unavailable")}
	api := &handler{store: store}
	response := httptest.NewRecorder()
	api.rateLimitedLogin(response, httptest.NewRequest(http.MethodPost, "/api/v1/login", nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "audit store unavailable") {
		t.Fatalf("rate-limit audit failure status/body = %d %s", response.Code, response.Body.String())
	}
}

func TestOriginRateLimiterAndDraftVersionBoundaries(t *testing.T) {
	for _, test := range []struct {
		name   string
		config Config
		valid  bool
	}{
		{name: "https with secure cookie", config: secureTestConfig(), valid: true},
		{name: "https uses secure cookies unconditionally", config: Config{ExpectedOrigin: "https://localhost:8443"}, valid: true},
		{name: "explicit insecure loopback", config: Config{ExpectedOrigin: "http://localhost:8443"}},
		{name: "insecure remote origin", config: Config{ExpectedOrigin: "http://192.0.2.10:8443"}},
		{name: "origin path rejected", config: Config{ExpectedOrigin: "https://localhost:8443/path"}},
		{name: "idle expiry exceeds absolute session lifetime", config: Config{ExpectedOrigin: "https://localhost:8443", SessionTTL: time.Minute, IdleTTL: time.Hour}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseExpectedOrigin(normalizeConfig(test.config))
			if (err == nil) != test.valid {
				t.Fatalf("parse origin error = %v, want valid %t", err, test.valid)
			}
		})
	}

	if !isLoopbackHost("localhost") || !isLoopbackHost("127.0.0.1") || isLoopbackHost("192.0.2.10") {
		t.Fatal("loopback host classification failed")
	}
	limiter := &requestLimiter{windows: make(map[string]limitWindow)}
	start := time.Now()
	if !limiter.Allow("ip:test", 1, time.Minute, start) || limiter.Allow("ip:test", 1, time.Minute, start.Add(time.Second)) {
		t.Fatal("rate limiter did not reject a second in-window request")
	}
	if !limiter.Allow("ip:test", 1, time.Minute, start.Add(time.Minute)) || !limiter.Allow("ip:other", 1, time.Minute, start) {
		t.Fatal("rate limiter failed to reset a window or isolate keys")
	}

	for _, value := range []string{"0", "1", "9007199254740993", "9223372036854775807"} {
		if _, err := parseDraftVersion(value); err != nil {
			t.Errorf("parse supported decimal draft version %q: %v", value, err)
		}
	}
	for _, value := range []string{"", "00", "+1", "-1", "9223372036854775808"} {
		if _, err := parseDraftVersion(value); err == nil {
			t.Errorf("accepted invalid decimal draft version %q", value)
		}
	}
	for _, value := range []string{"demo:item/potion", "a/b", "Demo:item/x", "../escape", "", strings.Repeat("a", 129)} {
		valid := validContentID(value)
		want := value == "demo:item/potion" || value == "a/b"
		if valid != want {
			t.Errorf("validContentID(%q) = %t, want %t", value, valid, want)
		}
	}
}

func TestRequestLimiterFailsClosedAtCapacityAndReclaimsExpiredWindows(t *testing.T) {
	start := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	limiter := &requestLimiter{
		windows: map[string]limitWindow{
			"active-a": {start: start, count: 1},
			"active-b": {start: start, count: 1},
		},
		maxKeys:    2,
		pruneBatch: 2,
	}
	if limiter.Allow("new", 1, time.Minute, start) {
		t.Fatal("full limiter evicted an active bucket instead of failing closed")
	}
	if len(limiter.windows) != 2 {
		t.Fatalf("limiter size after rejected new key = %d, want 2", len(limiter.windows))
	}
	if !limiter.Allow("new", 1, time.Minute, start.Add(time.Minute)) {
		t.Fatal("limiter did not reclaim expired buckets for a new key")
	}
	if len(limiter.windows) != 1 || limiter.windows["new"].count != 1 {
		t.Fatalf("limiter buckets after bounded expiry sweep = %#v", limiter.windows)
	}
}

func TestDecodeJSONRejectsOversizeUnknownTrailingAndWrongMedia(t *testing.T) {
	for _, test := range []struct {
		name        string
		body        string
		contentType string
		limit       int64
		wantStatus  int
	}{
		{name: "valid", body: `{"name":"ok"}`, contentType: "application/json", limit: 100, wantStatus: http.StatusOK},
		{name: "invalid json", body: "{", contentType: "application/json", limit: 100, wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: `{"name":"ok","extra":true}`, contentType: "application/json", limit: 100, wantStatus: http.StatusBadRequest},
		{name: "trailing json", body: `{"name":"ok"} {}`, contentType: "application/json", limit: 100, wantStatus: http.StatusBadRequest},
		{name: "too large", body: `{"name":"too long"}`, contentType: "application/json", limit: 2, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "wrong media", body: `{"name":"ok"}`, contentType: "text/plain", limit: 100, wantStatus: http.StatusUnsupportedMediaType},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			request.Body = http.MaxBytesReader(response, request.Body, test.limit)
			var target struct {
				Name string `json:"name"`
			}
			ok := decodeJSON(response, request, &target)
			if test.wantStatus == http.StatusOK {
				if !ok || target.Name != "ok" {
					t.Fatalf("valid decode = %t %#v", ok, target)
				}
				return
			}
			if ok || response.Code != test.wantStatus {
				t.Fatalf("decode result/status = %t/%d, want false/%d", ok, response.Code, test.wantStatus)
			}
		})
	}
}

func TestUnknownAPIRouteUsesStandardEnvelope(t *testing.T) {
	handler := NewHandler(&handlerTestStore{}, secureTestConfig(), nil, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil))
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"success":false`) {
		t.Fatalf("unknown API response = %d %s", response.Code, response.Body.String())
	}
}

func secureTestConfig() Config {
	return Config{ExpectedOrigin: "https://127.0.0.1:8443"}
}

func postLoginAfterAnonymousSession(t *testing.T, handler http.Handler, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	bootstrap := httptest.NewRecorder()
	handler.ServeHTTP(bootstrap, httptest.NewRequest(http.MethodGet, "/api/v1/session", nil))
	sessionCookie, csrfCookie := cookieValues(t, bootstrap.Result().Cookies())
	requestBody := `{"username":"` + username + `","password":"` + password + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://127.0.0.1:8443")
	request.Header.Set("X-CSRF-Token", csrfCookie.Value)
	request.AddCookie(sessionCookie)
	request.AddCookie(csrfCookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
