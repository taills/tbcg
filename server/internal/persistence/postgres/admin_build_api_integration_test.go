package postgres

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/tbcg/server/internal/admin"
	contentbuild "example.com/tbcg/server/internal/content/build"
	"example.com/tbcg/server/internal/content/buildworker"
)

func TestPublisherBuildAPIRunsThroughRestrictedWorker(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	seed := seedApprovedBuild(t, fixture, "build_api_", "00000000-0000-4000-8000-000000000061", "demo:item/api-built")
	accounts := seedAdminTestAccounts(t, fixture.ctx, fixture.db, seed.gameID)
	gamesRoot := t.TempDir()
	if err := os.CopyFS(filepath.Join(gamesRoot, seed.gameID, "assets"), os.DirFS(filepath.Join("..", "..", "..", "..", "games", "demo", "assets"))); err != nil {
		t.Fatalf("stage original demo assets for the test Game: %v", err)
	}
	repository, err := openAdminRepository(fixture.ctx, fixture.appDSN, true)
	if err != nil {
		t.Fatalf("open restricted admin repository: %v", err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	origin := "https://" + server.Listener.Addr().String()
	server.Config.Handler = admin.NewHandler(repository, admin.Config{ExpectedOrigin: origin, SessionTTL: time.Hour, IdleTTL: 15 * time.Minute}, seed.validator, os.DirFS(gamesRoot))
	server.StartTLS()
	t.Cleanup(server.Close)

	publisher, publisherCSRF := loginTestAccount(t, server, accounts.publisher)
	candidatesResponse := sendAdminJSON(t, publisher, origin, http.MethodGet, "/api/v1/games/"+seed.gameID+"/build-candidates", nil, publisherCSRF)
	candidatesEnvelope := decodeAdminResponse(t, candidatesResponse)
	closeAdminResponse(t, candidatesResponse)
	var candidates []admin.BuildCandidate
	if err := json.Unmarshal(candidatesEnvelope.Data, &candidates); err != nil || len(candidates) != 1 || candidates[0].BodyHash != seed.request.BodyHash {
		t.Fatalf("Publisher build candidates = %s, %v", candidatesEnvelope.Data, err)
	}

	path := "/api/v1/games/" + seed.gameID + "/builds"
	body := map[string]any{"content_id": seed.request.ContentID, "revision": seed.request.Revision, "body_hash": seed.request.BodyHash}
	queued := sendBuildRequest(t, publisher, origin, path, body, publisherCSRF, "api-build-key-000001")
	if queued.status != http.StatusAccepted || queued.job.Status != admin.BuildStatusQueued {
		t.Fatalf("start build = %d %#v", queued.status, queued.job)
	}
	if replay := sendBuildRequest(t, publisher, origin, path, body, publisherCSRF, "api-build-key-000001"); replay.status != http.StatusAccepted || replay.job.JobID != queued.job.JobID {
		t.Fatalf("idempotent replay = %d %#v", replay.status, replay.job)
	}
	conflicting := map[string]any{"content_id": seed.request.ContentID, "revision": seed.request.Revision, "body_hash": strings.Repeat("0", 64)}
	if conflict := sendBuildRequest(t, publisher, origin, path, conflicting, publisherCSRF, "api-build-key-000001"); conflict.status != http.StatusConflict || conflict.code != "idempotency_conflict" {
		t.Fatalf("same key different request = %d %q", conflict.status, conflict.code)
	}
	for name, credential := range map[string]testAdminCredential{"author": accounts.author, "reviewer": accounts.reviewer, "reader": accounts.reader} {
		client, csrf := loginTestAccount(t, server, credential)
		if denied := sendBuildRequest(t, client, origin, path, body, csrf, "api-build-key-"+name+"-01"); denied.status != http.StatusForbidden {
			t.Fatalf("%s start build status = %d, want 403", name, denied.status)
		}
		readResponse := sendAdminJSON(t, client, origin, http.MethodGet, path+"/"+queued.job.JobID, nil, csrf)
		if readResponse.StatusCode != http.StatusForbidden {
			closeAdminResponse(t, readResponse)
			t.Fatalf("%s read build job status = %d, want 403", name, readResponse.StatusCode)
		}
		closeAdminResponse(t, readResponse)
	}

	workerRepository, err := openBuildWorkerRepository(fixture.ctx, fixture.workerDSN, true)
	if err != nil {
		t.Fatalf("open restricted worker repository: %v", err)
	}
	t.Cleanup(func() { _ = workerRepository.Close() })
	storeDir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(storeDir, 0o700); err != nil {
		t.Fatalf("create private artifact store: %v", err)
	}
	store, err := contentbuild.OpenArtifactStore(storeDir)
	if err != nil {
		t.Fatalf("open private artifact store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	worker, err := buildworker.New("00000000-0000-4000-8000-000000000062", 30, workerRepository.WorkerQueue(), store, seed.validator)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	if claimed, err := worker.RunOnce(fixture.ctx); err != nil || !claimed {
		t.Fatalf("worker RunOnce() = %t, %v", claimed, err)
	}

	statusResponse := sendAdminJSON(t, publisher, origin, http.MethodGet, path+"/"+queued.job.JobID, nil, publisherCSRF)
	rawStatus := readAdminBody(t, statusResponse)
	if statusResponse.StatusCode != http.StatusOK {
		t.Fatalf("read built job status = %d: %s", statusResponse.StatusCode, rawStatus)
	}
	for _, private := range []string{"input_snapshot", "private_artifact", "manifest_bytes", "claim_token", storeDir, `"server"`} {
		if strings.Contains(rawStatus, private) {
			t.Fatalf("Publisher job status exposed private data %q", private)
		}
	}
	var envelope struct {
		Data admin.BuildJob `json:"data"`
	}
	if err := json.Unmarshal([]byte(rawStatus), &envelope); err != nil {
		t.Fatalf("decode built job: %v", err)
	}
	if envelope.Data.Status != admin.BuildStatusBuilt || envelope.Data.PublicArtifactSHA256 == nil {
		t.Fatalf("built job = %#v", envelope.Data)
	}
	var activeRealms int
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT count(*) FROM realms WHERE game_id = $1 AND active_release_id IS NOT NULL`, seed.gameID).Scan(&activeRealms); err != nil || activeRealms != 0 {
		t.Fatalf("build changed a Realm active pointer: %d, %v", activeRealms, err)
	}
}

type buildResponse struct {
	status int
	code   string
	job    admin.BuildJob
}

func sendBuildRequest(t *testing.T, client *http.Client, origin, path string, body map[string]any, csrf, key string) buildResponse {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode build request: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, origin+path, strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatalf("create build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Idempotency-Key", key)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send build request: %v", err)
	}
	envelope := decodeAdminResponse(t, response)
	closeAdminResponse(t, response)
	result := buildResponse{status: response.StatusCode}
	if envelope.Error != nil {
		result.code = envelope.Error.Code
	}
	if envelope.Success {
		if err := json.Unmarshal(envelope.Data, &result.job); err != nil {
			t.Fatalf("decode build job envelope: %v", err)
		}
	}
	return result
}
