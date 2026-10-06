package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	contentassets "github.com/taills/tbcg/server/internal/content/assets"
	contentbuild "github.com/taills/tbcg/server/internal/content/build"
	"github.com/taills/tbcg/server/internal/content/schema"
)

type buildRouteStore struct {
	*routeTestStore
	candidates   []BuildCandidate
	preparation  BuildPreparation
	readErr      error
	job          BuildJob
	getErr       error
	found        bool
	findErr      error
	enqueueErr   error
	listCalls    int
	readCalls    int
	findCalls    int
	enqueueCalls int
	key          string
	fingerprint  []byte
	asset        contentassets.FrozenSnapshot
	schemaHash   string
	profileHash  string
}

func (s *buildRouteStore) ListBuildCandidates(_ context.Context, _, _ string, _, _ int) ([]BuildCandidate, int, error) {
	s.listCalls++
	return s.candidates, len(s.candidates), nil
}

func (s *buildRouteStore) ReadApprovedBuildInput(context.Context, string, string, string, int32, []byte) (BuildPreparation, error) {
	s.readCalls++
	return s.preparation, s.readErr
}

func (s *buildRouteStore) FindBuildJobByKey(_ context.Context, _, _, key string, fingerprint []byte) (BuildJob, bool, error) {
	s.findCalls++
	s.key = key
	s.fingerprint = append([]byte(nil), fingerprint...)
	return s.job, s.found, s.findErr
}

func (s *buildRouteStore) EnqueueBuild(_ context.Context, _, _ string, _ BuildRequest, key string, fingerprint []byte, _ int64, profileHash string, asset contentassets.FrozenSnapshot, schemaFingerprint string) (BuildJob, error) {
	s.enqueueCalls++
	s.key = key
	s.fingerprint = append([]byte(nil), fingerprint...)
	s.asset = asset
	s.schemaHash = schemaFingerprint
	s.profileHash = profileHash
	return s.job, s.enqueueErr
}

func (s *buildRouteStore) GetBuildJob(context.Context, string, string, string) (BuildJob, error) {
	return s.job, s.getErr
}

func newBuildRouteStore(t *testing.T, allowRole bool) *buildRouteStore {
	t.Helper()
	definition, err := os.ReadFile("../../../tests/fixtures/content/item.valid.json")
	if err != nil {
		t.Fatalf("read approved build Definition fixture: %v", err)
	}
	definition = bytes.Replace(definition, []byte(`"item/potion-small"`), []byte(`"demo:icon/placeholder"`), 1)
	asset, err := contentassets.ReadIcon(os.DirFS("../../../games/demo/assets"), "demo:icon/placeholder")
	if err != nil {
		t.Fatalf("load trusted icon fixture: %v", err)
	}
	manifestHash := sha256.Sum256(asset.Frozen().Manifest)
	definitionHash := sha256.Sum256(definition)
	created := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	return &buildRouteStore{
		routeTestStore: newAuthenticatedRouteStore(t, allowRole),
		preparation: BuildPreparation{
			GameID: "demo", ContentID: "demo:item/healing-potion", Revision: 1,
			BodyHash: hex.EncodeToString(definitionHash[:]), Definition: definition,
			Profile: contentbuild.CompatibilityProfile{
				ProfileVersion: 2, ContentSchemaVersion: 1, InstanceSchemaVersion: 1, RulesetVersion: 1,
				ProtocolVersion: 1, ClientMinBuild: "1.0.0", AssetManifestSHA256: hex.EncodeToString(manifestHash[:]),
			},
		},
		job: BuildJob{
			JobID: "00000000-0000-4000-8000-000000000041", GameID: "demo", ContentID: "demo:item/healing-potion", Revision: 1,
			BodyHash: hex.EncodeToString(definitionHash[:]), ReleaseID: "build-0000000000000000000000000000004a", ReleaseVersion: 1,
			Status: BuildStatusQueued, CreatedAt: created, UpdatedAt: created,
		},
	}
}

func newBuildRouteHandler(t *testing.T, store Store) http.Handler {
	t.Helper()
	validator, err := schema.New(os.DirFS("../../../contracts"))
	if err != nil {
		t.Fatalf("compile build route schema: %v", err)
	}
	return NewHandler(store, secureTestConfig(), validator, os.DirFS("../../../games"))
}

func buildWriteRequest(handler http.Handler, session, csrf *http.Cookie, path, body, idempotencyKey string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://127.0.0.1:8443")
	request.Header.Set("X-CSRF-Token", csrf.Value)
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	request.AddCookie(session)
	request.AddCookie(csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeEnvelope[T any](t *testing.T, response *httptest.ResponseRecorder) (bool, T, string) {
	t.Helper()
	var payload struct {
		Success bool `json:"success"`
		Data    T    `json:"data"`
		Error   *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response envelope: %v (%s)", err, response.Body.String())
	}
	code := ""
	if payload.Error != nil {
		code = payload.Error.Code
	}
	return payload.Success, payload.Data, code
}

const validBuildBody = `{"content_id":"demo:item/healing-potion","revision":1,"body_hash":"`

func buildBody(store *buildRouteStore) string {
	return validBuildBody + store.preparation.BodyHash + `"}`
}

func TestBuildCandidateRouteRequiresPublisherAndBoundsPage(t *testing.T) {
	store := newBuildRouteStore(t, true)
	store.candidates = []BuildCandidate{{GameID: "demo", ContentID: "demo:item/potion", Revision: 2, BodyHash: strings.Repeat("a", 64), SubmittedAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)}}
	handler := newBuildRouteHandler(t, store)
	session, csrf := loginForRouteTest(t, handler)
	response := routeRequest(t, handler, session, csrf, http.MethodGet, "/api/v1/games/demo/build-candidates?limit=20&offset=10", "")
	if response.Code != http.StatusOK || store.listCalls != 1 {
		t.Fatalf("candidate list status/calls = %d/%d: %s", response.Code, store.listCalls, response.Body.String())
	}
	ok, candidates, _ := decodeEnvelope[[]BuildCandidate](t, response)
	if !ok || len(candidates) != 1 || candidates[0].Revision != 2 {
		t.Fatalf("candidate payload = %#v", candidates)
	}
	if response := routeRequest(t, handler, session, csrf, http.MethodGet, "/api/v1/games/demo/build-candidates?limit=101", ""); response.Code != http.StatusBadRequest {
		t.Fatalf("oversized candidate page status = %d", response.Code)
	}

	denied := newBuildRouteStore(t, false)
	deniedHandler := newBuildRouteHandler(t, denied)
	deniedSession, deniedCSRF := loginForRouteTest(t, deniedHandler)
	response = routeRequest(t, deniedHandler, deniedSession, deniedCSRF, http.MethodGet, "/api/v1/games/demo/build-candidates", "")
	if response.Code != http.StatusForbidden || denied.listCalls != 0 {
		t.Fatalf("non-Publisher candidate status/calls = %d/%d", response.Code, denied.listCalls)
	}
}

func TestStartBuildFreezesTrustedInputsAndReplaysIdempotently(t *testing.T) {
	store := newBuildRouteStore(t, true)
	handler := newBuildRouteHandler(t, store)
	session, csrf := loginForRouteTest(t, handler)
	response := buildWriteRequest(handler, session, csrf, "/api/v1/games/demo/builds", buildBody(store), "stable-build-request-key")
	if response.Code != http.StatusAccepted {
		t.Fatalf("start build status = %d: %s", response.Code, response.Body.String())
	}
	ok, job, _ := decodeEnvelope[BuildJob](t, response)
	if !ok || job.Status != BuildStatusQueued || job.JobID != store.job.JobID {
		t.Fatalf("start build payload = %#v", job)
	}
	if store.findCalls != 1 || store.readCalls != 1 || store.enqueueCalls != 1 || store.key != "stable-build-request-key" ||
		len(store.fingerprint) != sha256.Size || store.asset.AssetID != "demo:icon/placeholder" ||
		store.profileHash != store.preparation.Profile.AssetManifestSHA256 || len(store.schemaHash) != 64 {
		t.Fatalf("enqueue inputs: find/read/enqueue=%d/%d/%d key=%q asset=%q", store.findCalls, store.readCalls, store.enqueueCalls, store.key, store.asset.AssetID)
	}

	store.found = true
	response = buildWriteRequest(handler, session, csrf, "/api/v1/games/demo/builds", buildBody(store), "stable-build-request-key")
	if response.Code != http.StatusAccepted || store.enqueueCalls != 1 || store.readCalls != 1 {
		t.Fatalf("replay status/read/enqueue = %d/%d/%d", response.Code, store.readCalls, store.enqueueCalls)
	}
}

func TestStartBuildRejectsInvalidUnapprovedAndConflictingRequests(t *testing.T) {
	for _, test := range []struct {
		name       string
		key        string
		body       func(*buildRouteStore) string
		setup      func(*buildRouteStore)
		wantStatus int
		wantCode   string
	}{
		{name: "missing idempotency key", key: "", body: buildBody, wantStatus: http.StatusBadRequest, wantCode: "invalid_idempotency_key"},
		{name: "short idempotency key", key: "short", body: buildBody, wantStatus: http.StatusBadRequest, wantCode: "invalid_idempotency_key"},
		{name: "uppercase hash", key: "stable-build-request-key", body: func(*buildRouteStore) string {
			return validBuildBody + strings.Repeat("A", 64) + `"}`
		}, wantStatus: http.StatusBadRequest, wantCode: "invalid_build_request"},
		{name: "unknown field", key: "stable-build-request-key", body: func(s *buildRouteStore) string {
			return strings.TrimSuffix(buildBody(s), "}") + `,"profile":{"protocol_version":9}}`
		}, wantStatus: http.StatusBadRequest},
		{name: "key bound to another request", key: "stable-build-request-key", body: buildBody, setup: func(s *buildRouteStore) { s.findErr = ErrConflict }, wantStatus: http.StatusConflict, wantCode: "idempotency_conflict"},
		{name: "not approved", key: "stable-build-request-key", body: buildBody, setup: func(s *buildRouteStore) { s.readErr = ErrNotFound }, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "missing profile", key: "stable-build-request-key", body: buildBody, setup: func(s *buildRouteStore) { s.readErr = ErrBuildProfileNotConfigured }, wantStatus: http.StatusConflict, wantCode: "build_profile_unavailable"},
		{name: "asset manifest drift", key: "stable-build-request-key", body: buildBody, setup: func(s *buildRouteStore) {
			s.preparation.Profile.AssetManifestSHA256 = strings.Repeat("0", 64)
		}, wantStatus: http.StatusConflict, wantCode: "asset_manifest_mismatch"},
		{name: "profile changed during enqueue", key: "stable-build-request-key", body: buildBody, setup: func(s *buildRouteStore) { s.enqueueErr = ErrConflict }, wantStatus: http.StatusConflict, wantCode: "build_conflict"},
		{name: "revision already building", key: "stable-build-request-key", body: buildBody, setup: func(s *buildRouteStore) { s.enqueueErr = ErrBuildAlreadyActive }, wantStatus: http.StatusConflict, wantCode: "build_already_active"},
		{name: "Game queue full", key: "stable-build-request-key", body: buildBody, setup: func(s *buildRouteStore) { s.enqueueErr = ErrBuildQueueFull }, wantStatus: http.StatusTooManyRequests, wantCode: "build_queue_full"},
		{name: "commit outcome unknown", key: "stable-build-request-key", body: buildBody, setup: func(s *buildRouteStore) { s.enqueueErr = ErrBuildOutcomeUnknown }, wantStatus: http.StatusServiceUnavailable, wantCode: "build_outcome_unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newBuildRouteStore(t, true)
			if test.setup != nil {
				test.setup(store)
			}
			handler := newBuildRouteHandler(t, store)
			session, csrf := loginForRouteTest(t, handler)
			response := buildWriteRequest(handler, session, csrf, "/api/v1/games/demo/builds", test.body(store), test.key)
			_, _, code := decodeEnvelope[json.RawMessage](t, response)
			if response.Code != test.wantStatus || (test.wantCode != "" && code != test.wantCode) {
				t.Fatalf("status/code = %d/%q, want %d/%q: %s", response.Code, code, test.wantStatus, test.wantCode, response.Body.String())
			}
			if test.wantStatus != http.StatusServiceUnavailable && test.wantStatus != http.StatusConflict && test.wantStatus != http.StatusTooManyRequests && store.enqueueCalls != 0 {
				t.Fatalf("rejected request reached enqueue %d times", store.enqueueCalls)
			}
		})
	}
}

func TestStartBuildRequiresPublisherOriginAndCSRF(t *testing.T) {
	denied := newBuildRouteStore(t, false)
	handler := newBuildRouteHandler(t, denied)
	session, csrf := loginForRouteTest(t, handler)
	if response := buildWriteRequest(handler, session, csrf, "/api/v1/games/demo/builds", buildBody(denied), "stable-build-request-key"); response.Code != http.StatusForbidden || denied.findCalls != 0 {
		t.Fatalf("non-Publisher start build status/find = %d/%d", response.Code, denied.findCalls)
	}

	store := newBuildRouteStore(t, true)
	handler = newBuildRouteHandler(t, store)
	session, csrf = loginForRouteTest(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/games/demo/builds", strings.NewReader(buildBody(store)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("X-CSRF-Token", csrf.Value)
	request.Header.Set("Idempotency-Key", "stable-build-request-key")
	request.AddCookie(session)
	request.AddCookie(csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || store.findCalls != 0 {
		t.Fatalf("cross-origin start build status/find = %d/%d", response.Code, store.findCalls)
	}
	wrongCSRF := &http.Cookie{Name: csrf.Name, Value: strings.Repeat("b", len(csrf.Value))}
	if response := buildWriteRequest(handler, session, wrongCSRF, "/api/v1/games/demo/builds", buildBody(store), "stable-build-request-key"); response.Code != http.StatusForbidden || store.findCalls != 0 {
		t.Fatalf("wrong-CSRF start build status/find = %d/%d", response.Code, store.findCalls)
	}
}

func TestGetBuildJobValidatesIdentityAndHidesUnknownJobs(t *testing.T) {
	store := newBuildRouteStore(t, true)
	handler := newBuildRouteHandler(t, store)
	session, csrf := loginForRouteTest(t, handler)
	response := routeRequest(t, handler, session, csrf, http.MethodGet, "/api/v1/games/demo/builds/"+store.job.JobID, "")
	ok, job, _ := decodeEnvelope[BuildJob](t, response)
	if response.Code != http.StatusOK || !ok || job.JobID != store.job.JobID {
		t.Fatalf("get build job status = %d: %s", response.Code, response.Body.String())
	}
	if response := routeRequest(t, handler, session, csrf, http.MethodGet, "/api/v1/games/demo/builds/not-a-uuid", ""); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid job ID status = %d", response.Code)
	}
	store.getErr = ErrNotFound
	if response := routeRequest(t, handler, session, csrf, http.MethodGet, "/api/v1/games/demo/builds/"+store.job.JobID, ""); response.Code != http.StatusNotFound {
		t.Fatalf("unknown job status = %d", response.Code)
	}
}

func TestBuildRoutesAreAbsentWithoutDurableBuildStore(t *testing.T) {
	plain := newAuthenticatedRouteStore(t, true)
	handler := newBuildRouteHandler(t, plain)
	session, csrf := loginForRouteTest(t, handler)
	response := buildWriteRequest(handler, session, csrf, "/api/v1/games/demo/builds", `{}`, "stable-build-request-key")
	if response.Code != http.StatusNotFound {
		t.Fatalf("build route without durable store status = %d, want 404", response.Code)
	}
}
