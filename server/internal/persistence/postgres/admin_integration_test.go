package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/tbcg/server/internal/admin"
	"example.com/tbcg/server/internal/content/schema"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type adminAPIEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Fields  []struct {
			Pointer string `json:"pointer"`
			Code    string `json:"code"`
		} `json:"fields"`
	} `json:"error"`
}

func TestAdminControlPlaneHTTPSPostgresUserJourney(t *testing.T) {
	maintenanceURL := os.Getenv("TBCG_TEST_DATABASE_URL")
	if maintenanceURL == "" {
		if os.Getenv("TBCG_REQUIRE_TEST_DATABASE") == "1" {
			t.Fatal("CI requires TBCG_TEST_DATABASE_URL")
		}
		t.Skip("set TBCG_TEST_DATABASE_URL to a disposable PostgreSQL maintenance database URL")
	}
	ternPath, err := exec.LookPath("tern")
	if err != nil {
		t.Fatalf("find standalone tern v2.4.3 CLI on PATH: %v", err)
	}
	versionOutput, err := exec.Command(ternPath, "version").CombinedOutput()
	if err != nil || !strings.Contains(string(versionOutput), "v2.4.3") {
		t.Fatalf("require tern v2.4.3, got %q (%v)", strings.TrimSpace(string(versionOutput)), err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	maintenanceConfig := parseIsolatedTestDatabase(t, maintenanceURL)
	maintenanceDB := stdlib.OpenDB(*maintenanceConfig)
	t.Cleanup(func() { _ = maintenanceDB.Close() })
	if err := maintenanceDB.PingContext(ctx); err != nil {
		t.Fatalf("connect to disposable PostgreSQL maintenance database: %v", err)
	}
	assertPostgreSQL18(t, ctx, maintenanceDB)
	roleFixture := prepareAdminRoleFixture(t, ctx, maintenanceDB, maintenanceConfig)

	databaseName, err := randomTestDatabaseName()
	if err != nil {
		t.Fatalf("create isolated admin test database name: %v", err)
	}
	if _, err := maintenanceDB.ExecContext(ctx, "CREATE DATABASE "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
		t.Fatalf("create isolated admin API database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := maintenanceDB.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
			t.Errorf("drop isolated admin API database: %v", err)
		}
	})
	migrationDir := filepath.Join(findServerRoot(t), "migrations")
	env, err := ternEnvironment(maintenanceConfig, databaseName, migrationDir)
	if err != nil {
		t.Fatalf("prepare isolated migration environment: %v", err)
	}
	runTernMigration(t, ctx, ternPath, findServerRoot(t), env, "migrate")

	superConfig := maintenanceConfig.Copy()
	superConfig.Database = databaseName
	superDB := stdlib.OpenDB(*superConfig)
	t.Cleanup(func() { _ = superDB.Close() })
	if err := superDB.PingContext(ctx); err != nil {
		t.Fatalf("connect to isolated admin database as migration owner: %v", err)
	}
	gameID := "demo"
	if _, err := superDB.ExecContext(ctx, `INSERT INTO games (game_id, display_name) VALUES ($1, $2)`, gameID, "TBCG demo"); err != nil {
		t.Fatalf("seed test Game: %v", err)
	}
	accounts := seedAdminTestAccounts(t, ctx, superDB, gameID)
	assertAdminRolePermissions(t, ctx, maintenanceConfig, databaseName, roleFixture)

	validator, err := schema.New(os.DirFS(filepath.Join(filepath.Dir(findServerRoot(t)), "contracts")))
	if err != nil {
		t.Fatalf("compile trusted item schemas for admin API: %v", err)
	}
	assets := os.DirFS(filepath.Join(filepath.Dir(findServerRoot(t)), "games"))
	appURL, err := url.Parse(maintenanceURL)
	if err != nil {
		t.Fatalf("parse disposable admin test DSN: %v", err)
	}
	appURL.Path = "/" + databaseName
	appURL.User = url.UserPassword(roleFixture.appRole, roleFixture.appPassword)
	assertAdminRepositoryRejectsOverpoweredFixtures(t, ctx, maintenanceDB, appURL.String(), roleFixture.appRole)
	ownerURL, err := url.Parse(maintenanceURL)
	if err != nil {
		t.Fatalf("parse migration-owner test DSN: %v", err)
	}
	ownerURL.Path = "/" + databaseName
	if ownerRepository, err := openAdminRepository(ctx, ownerURL.String(), true); err == nil {
		if closeErr := ownerRepository.Close(); closeErr != nil {
			t.Errorf("close unexpectedly accepted superuser repository: %v", closeErr)
		}
		t.Fatal("admin repository accepted the PostgreSQL superuser as its session identity")
	}
	repository, err := openAdminRepository(ctx, appURL.String(), true)
	if err != nil {
		t.Fatalf("open admin repository using restricted database login: %v", err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close admin API repository: %v", err)
		}
	})
	assertAdminSessionCleanupIsBounded(t, ctx, superDB, repository)

	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	origin := "https://" + server.Listener.Addr().String()
	server.Config.Handler = admin.NewHandler(repository, admin.Config{
		ExpectedOrigin: origin,
		SessionTTL:     time.Hour,
		IdleTTL:        15 * time.Minute,
	}, validator, assets)
	server.StartTLS()
	t.Cleanup(server.Close)
	if !strings.HasPrefix(server.URL, "https://") {
		t.Fatalf("test management service did not use TLS: %s", server.URL)
	}
	assertSessionRefreshRotatesMissingCSRFCookie(t, server)

	unauthenticatedClient := newTLSCookieClient(t, server)
	unauthResponse := sendAdminJSONWithoutCSRF(t, unauthenticatedClient, server.URL, http.MethodPost, "/api/v1/games/demo/item-drafts", map[string]any{"content_id": "demo:item/blocked", "expected_draft_version": "0", "definition": map[string]any{}})
	if unauthResponse.StatusCode != http.StatusUnauthorized {
		closeAdminResponse(t, unauthResponse)
		t.Fatalf("unauthenticated create status = %d, want 401", unauthResponse.StatusCode)
	}
	closeAdminResponse(t, unauthResponse)

	authorClient, authorCSRF := loginTestAccount(t, server, accounts.author)
	authorClientB, authorCSRFB := loginTestAccount(t, server, accounts.author)
	reviewerClient, reviewerCSRF := loginTestAccount(t, server, accounts.reviewer)
	publisherClient, publisherCSRF := loginTestAccount(t, server, accounts.publisher)
	readerClient, readerCSRF := loginTestAccount(t, server, accounts.reader)
	assertAdminSessionSecretsAreHashed(t, ctx, superDB, authorClient, server.URL, accounts.author.username)
	ackLostDraftID := assertDraftCommitAcknowledgmentLoss(t, ctx, superDB, appURL.String(), repository, gameID, accounts.author.username)
	ackLostContentID := "demo:item/commit-ack-lost"
	recoveredAfterAckLoss := sendAdminJSON(t, authorClient, server.URL, http.MethodGet, "/api/v1/games/demo/item-drafts?content_id="+url.QueryEscape(ackLostContentID), nil, "")
	var recoveredEnvelope struct {
		Data []struct {
			DraftID string `json:"draft_id"`
		} `json:"data"`
	}
	if recoveredAfterAckLoss.StatusCode != http.StatusOK {
		body := readAdminBody(t, recoveredAfterAckLoss)
		t.Fatalf("API recovery after committed create acknowledgment loss status = %d: %s", recoveredAfterAckLoss.StatusCode, body)
	}
	recoveredEnvelopeData := decodeAdminResponse(t, recoveredAfterAckLoss)
	closeAdminResponse(t, recoveredAfterAckLoss)
	if err := json.Unmarshal(recoveredEnvelopeData.Data, &recoveredEnvelope.Data); err != nil {
		t.Fatalf("decode API recovery after create acknowledgment loss: %v", err)
	}
	if len(recoveredEnvelope.Data) != 1 || recoveredEnvelope.Data[0].DraftID != ackLostDraftID {
		t.Fatalf("recovered draft IDs = %#v, want the uniquely committed %q", recoveredEnvelope.Data, ackLostDraftID)
	}
	crossGameRecovery := sendAdminJSON(t, authorClient, server.URL, http.MethodGet, "/api/v1/games/other/item-drafts?content_id="+url.QueryEscape(ackLostContentID), nil, "")
	if crossGameRecovery.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-Game draft recovery status = %d, want 403", crossGameRecovery.StatusCode)
	}
	closeAdminResponse(t, crossGameRecovery)

	contentID := "demo:item/healing-potion"
	definition := loadAdminFixture(t)
	createPayload := map[string]any{
		"content_id":             contentID,
		"expected_draft_version": "0",
		"definition":             definition,
	}
	createResponse := sendAdminJSON(t, authorClient, server.URL, http.MethodPost, "/api/v1/games/demo/item-drafts", createPayload, authorCSRF)
	if createResponse.StatusCode != http.StatusCreated {
		body := readAdminBody(t, createResponse)
		t.Fatalf("author create status = %d, want 201: %s", createResponse.StatusCode, body)
	}
	var created struct {
		DraftID      string          `json:"draft_id"`
		GameID       string          `json:"game_id"`
		ContentID    string          `json:"content_id"`
		DraftVersion string          `json:"draft_version"`
		Definition   json.RawMessage `json:"definition"`
	}
	createEnvelope := decodeAdminResponse(t, createResponse)
	if err := json.Unmarshal(createEnvelope.Data, &created); err != nil {
		t.Fatalf("decode created draft: %v", err)
	}
	if created.DraftID == "" || created.GameID != gameID || created.ContentID != contentID || created.DraftVersion != "1" {
		t.Fatalf("created draft identity/version = %#v", created)
	}
	recoveredDrafts := sendAdminJSON(t, authorClient, server.URL, http.MethodGet, "/api/v1/games/demo/item-drafts?content_id="+url.QueryEscape(contentID)+"&limit=10&offset=0", nil, "")
	if recoveredDrafts.StatusCode != http.StatusOK {
		body := readAdminBody(t, recoveredDrafts)
		t.Fatalf("recover draft after create response loss status = %d: %s", recoveredDrafts.StatusCode, body)
	}
	var recoveryEnvelope struct {
		Data []struct {
			DraftID string `json:"draft_id"`
		} `json:"data"`
		Meta struct {
			Total  int `json:"total"`
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(recoveredDrafts.Body).Decode(&recoveryEnvelope); err != nil {
		t.Fatalf("decode recovered item drafts: %v", err)
	}
	closeAdminResponse(t, recoveredDrafts)
	if len(recoveryEnvelope.Data) != 1 || recoveryEnvelope.Data[0].DraftID != created.DraftID || recoveryEnvelope.Meta.Total != 1 || recoveryEnvelope.Meta.Limit != 10 || recoveryEnvelope.Meta.Offset != 0 {
		t.Fatalf("exact draft recovery result = %#v, want one matching draft and bounded pagination", recoveryEnvelope)
	}
	duplicateDraft := sendAdminJSON(t, authorClient, server.URL, http.MethodPost, "/api/v1/games/demo/item-drafts", createPayload, authorCSRF)
	if duplicateDraft.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate content draft status = %d, want 409", duplicateDraft.StatusCode)
	}
	closeAdminResponse(t, duplicateDraft)

	unknownAPI := sendAdminJSON(t, authorClient, server.URL, http.MethodGet, "/api/v1/not-a-route", nil, "")
	if unknownAPI.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown API route status = %d, want 404", unknownAPI.StatusCode)
	}
	unknownEnvelope := decodeAdminResponse(t, unknownAPI)
	closeAdminResponse(t, unknownAPI)
	if unknownEnvelope.Error == nil || unknownEnvelope.Error.Code != "not_found" {
		t.Fatalf("unknown API route returned non-envelope error: %#v", unknownEnvelope)
	}

	draftPath := "/api/v1/games/demo/item-drafts/" + created.DraftID
	initialValidation := sendAdminJSON(t, authorClient, server.URL, http.MethodPost, draftPath+"/validate", nil, authorCSRF)
	if initialValidation.StatusCode != http.StatusOK {
		body := readAdminBody(t, initialValidation)
		t.Fatalf("valid draft validation status = %d, want 200: %s", initialValidation.StatusCode, body)
	}
	initialValidationEnvelope := decodeAdminResponse(t, initialValidation)
	closeAdminResponse(t, initialValidation)
	var initialValidationResult struct {
		Valid bool `json:"valid"`
	}
	if err := json.Unmarshal(initialValidationEnvelope.Data, &initialValidationResult); err != nil || !initialValidationResult.Valid {
		t.Fatalf("initial item validation result = %#v (%v)", initialValidationResult, err)
	}
	invalidDefinition := itemDefinitionWithIcon(t, created.Definition, "demo:icon/unlicensed")
	if _, err := superDB.ExecContext(ctx, `UPDATE content_drafts SET definition = $3::jsonb WHERE game_id = $1 AND draft_id = $2::uuid`, gameID, created.DraftID, invalidDefinition); err != nil {
		t.Fatalf("simulate stale unlicensed asset reference: %v", err)
	}
	invalidValidation := sendAdminJSON(t, authorClient, server.URL, http.MethodPost, draftPath+"/validate", nil, authorCSRF)
	if invalidValidation.StatusCode != http.StatusOK {
		t.Fatalf("invalid icon validation report status = %d, want 200", invalidValidation.StatusCode)
	}
	invalidValidationEnvelope := decodeAdminResponse(t, invalidValidation)
	closeAdminResponse(t, invalidValidation)
	var invalidValidationResult struct {
		Valid  bool `json:"valid"`
		Errors []struct {
			Pointer string `json:"pointer"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(invalidValidationEnvelope.Data, &invalidValidationResult); err != nil || invalidValidationResult.Valid || len(invalidValidationResult.Errors) == 0 || invalidValidationResult.Errors[0].Pointer != "/public/icon_id" {
		t.Fatalf("unlicensed icon validation result = %#v (%v)", invalidValidationResult, err)
	}
	invalidSubmit := sendAdminJSON(t, authorClient, server.URL, http.MethodPost, draftPath+"/submit", map[string]any{"expected_draft_version": "1"}, authorCSRF)
	if invalidSubmit.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unlicensed icon submit status = %d, want 422", invalidSubmit.StatusCode)
	}
	closeAdminResponse(t, invalidSubmit)
	if _, err := superDB.ExecContext(ctx, `UPDATE content_drafts SET definition = $3::jsonb WHERE game_id = $1 AND draft_id = $2::uuid`, gameID, created.DraftID, created.Definition); err != nil {
		t.Fatalf("restore licensed draft definition: %v", err)
	}
	const largeDraftVersion = "9007199254740993"
	if _, err := superDB.ExecContext(ctx, `UPDATE content_drafts SET draft_version = $3::bigint WHERE game_id = $1 AND draft_id = $2::uuid`, gameID, created.DraftID, largeDraftVersion); err != nil {
		t.Fatalf("seed draft_version above JavaScript safe integer: %v", err)
	}
	readA := sendAdminJSON(t, authorClient, server.URL, http.MethodGet, draftPath, nil, "")
	readB := sendAdminJSON(t, authorClientB, server.URL, http.MethodGet, draftPath, nil, "")
	if readA.StatusCode != http.StatusOK || readB.StatusCode != http.StatusOK {
		closeAdminResponse(t, readA)
		closeAdminResponse(t, readB)
		t.Fatalf("two draft reads statuses = %d/%d, want 200/200", readA.StatusCode, readB.StatusCode)
	}
	readAEnvelope := decodeAdminResponse(t, readA)
	readBEnvelope := decodeAdminResponse(t, readB)
	closeAdminResponse(t, readA)
	closeAdminResponse(t, readB)
	var readVersions [2]string
	for index, envelope := range []adminAPIEnvelope{readAEnvelope, readBEnvelope} {
		var item struct {
			DraftVersion string `json:"draft_version"`
		}
		if err := json.Unmarshal(envelope.Data, &item); err != nil {
			t.Fatalf("decode browser %d draft version: %v", index, err)
		}
		readVersions[index] = item.DraftVersion
	}
	if readVersions[0] != largeDraftVersion || readVersions[1] != largeDraftVersion {
		t.Fatalf("independent browser draft versions = %#v, want exact decimal %s", readVersions, largeDraftVersion)
	}

	const versionAfterSave = "9007199254740994"
	updatedDefinition := cloneItemDefinition(t, definition, "测试版本")
	updateA := sendAdminJSON(t, authorClient, server.URL, http.MethodPut, draftPath, map[string]any{"expected_draft_version": largeDraftVersion, "definition": updatedDefinition}, authorCSRF)
	if updateA.StatusCode != http.StatusOK {
		body := readAdminBody(t, updateA)
		t.Fatalf("first CAS update status = %d, want 200: %s", updateA.StatusCode, body)
	}
	updatedEnvelope := decodeAdminResponse(t, updateA)
	var updated struct {
		DraftVersion string `json:"draft_version"`
	}
	if err := json.Unmarshal(updatedEnvelope.Data, &updated); err != nil || updated.DraftVersion != versionAfterSave {
		t.Fatalf("first CAS update version = %q, want %s: %v", updated.DraftVersion, versionAfterSave, err)
	}

	staleDefinition := cloneItemDefinition(t, definition, "不应覆盖服务器值")
	stale := sendAdminJSON(t, authorClientB, server.URL, http.MethodPut, draftPath, map[string]any{"expected_draft_version": largeDraftVersion, "definition": staleDefinition}, authorCSRFB)
	if stale.StatusCode != http.StatusConflict {
		body := readAdminBody(t, stale)
		t.Fatalf("stale CAS update status = %d, want 409: %s", stale.StatusCode, body)
	}
	staleEnvelope := decodeAdminResponse(t, stale)
	var staleDraft struct {
		DraftVersion string `json:"draft_version"`
	}
	if err := json.Unmarshal(staleEnvelope.Data, &staleDraft); err != nil || staleDraft.DraftVersion != versionAfterSave {
		t.Fatalf("CAS conflict did not return current version %s: %q (%v)", versionAfterSave, staleDraft.DraftVersion, err)
	}

	readerWrite := sendAdminJSON(t, readerClient, server.URL, http.MethodPut, draftPath, map[string]any{"expected_draft_version": versionAfterSave, "definition": definition}, readerCSRF)
	if readerWrite.StatusCode != http.StatusForbidden {
		body := readAdminBody(t, readerWrite)
		t.Fatalf("Reader direct write status = %d, want 403: %s", readerWrite.StatusCode, body)
	}
	closeAdminResponse(t, readerWrite)
	crossGameRead := sendAdminJSON(t, authorClient, server.URL, http.MethodGet, "/api/v1/games/other/item-drafts/"+created.DraftID, nil, "")
	if crossGameRead.StatusCode != http.StatusForbidden {
		body := readAdminBody(t, crossGameRead)
		t.Fatalf("cross-Game read status = %d, want 403: %s", crossGameRead.StatusCode, body)
	}
	closeAdminResponse(t, crossGameRead)

	invalidIconDefinition := cloneItemDefinition(t, definition, "缺少许可的图标")
	invalidIconDefinition["content_id"] = "demo:item/unlicensed"
	invalidIconDefinition["public"].(map[string]any)["content_id"] = "demo:item/unlicensed"
	invalidIconDefinition["public"].(map[string]any)["icon_id"] = "demo:icon/unlicensed"
	invalidCreate := sendAdminJSON(t, authorClient, server.URL, http.MethodPost, "/api/v1/games/demo/item-drafts", map[string]any{
		"content_id": "demo:item/unlicensed", "expected_draft_version": "0", "definition": invalidIconDefinition,
	}, authorCSRF)
	if invalidCreate.StatusCode != http.StatusUnprocessableEntity {
		body := readAdminBody(t, invalidCreate)
		t.Fatalf("unlicensed icon create status = %d, want 422: %s", invalidCreate.StatusCode, body)
	}
	invalidEnvelope := decodeAdminResponse(t, invalidCreate)
	if invalidEnvelope.Error == nil || len(invalidEnvelope.Error.Fields) != 1 || invalidEnvelope.Error.Fields[0].Pointer != "/public/icon_id" {
		t.Fatalf("unlicensed icon was not located at /public/icon_id: %#v", invalidEnvelope.Error)
	}

	invalidCSRFRequest := sendAdminJSONWithoutCSRF(t, authorClient, server.URL, http.MethodPut, draftPath, map[string]any{"expected_draft_version": versionAfterSave, "definition": definition})
	if invalidCSRFRequest.StatusCode != http.StatusForbidden {
		body := readAdminBody(t, invalidCSRFRequest)
		t.Fatalf("write without CSRF status = %d, want 403: %s", invalidCSRFRequest.StatusCode, body)
	}
	closeAdminResponse(t, invalidCSRFRequest)

	// A failed success-audit insert must roll back the draft write in the same transaction.
	if _, err := superDB.ExecContext(ctx, `
CREATE FUNCTION public.fail_admin_draft_success_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.action = 'item_draft.create' AND NEW.outcome = 'success' THEN
    RAISE EXCEPTION 'injected audit failure' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$`); err != nil {
		t.Fatalf("create transactional audit failure function: %v", err)
	}
	if _, err := superDB.ExecContext(ctx, `CREATE TRIGGER fail_admin_draft_success_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION public.fail_admin_draft_success_audit()`); err != nil {
		t.Fatalf("install transactional audit failure trigger: %v", err)
	}
	auditRollbackDefinition := cloneItemDefinition(t, definition, "事务回滚测试")
	auditRollbackDefinition["content_id"] = "demo:item/audit-rollback"
	auditRollbackDefinition["public"].(map[string]any)["content_id"] = "demo:item/audit-rollback"
	rollbackCreate := sendAdminJSON(t, authorClient, server.URL, http.MethodPost, "/api/v1/games/demo/item-drafts", map[string]any{
		"content_id": "demo:item/audit-rollback", "expected_draft_version": "0", "definition": auditRollbackDefinition,
	}, authorCSRF)
	if rollbackCreate.StatusCode != http.StatusInternalServerError {
		body := readAdminBody(t, rollbackCreate)
		t.Fatalf("audit-injected draft create status = %d, want 500: %s", rollbackCreate.StatusCode, body)
	}
	closeAdminResponse(t, rollbackCreate)
	var rollbackCount int
	if err := superDB.QueryRowContext(ctx, `SELECT count(*) FROM content_drafts WHERE game_id = 'demo' AND content_id = 'demo:item/audit-rollback'`).Scan(&rollbackCount); err != nil {
		t.Fatalf("query draft after audit rollback: %v", err)
	}
	if rollbackCount != 0 {
		t.Fatalf("draft count after failed transactional audit = %d, want 0", rollbackCount)
	}
	if _, err := superDB.ExecContext(ctx, `DROP TRIGGER fail_admin_draft_success_audit ON audit_logs`); err != nil {
		t.Fatalf("remove audit failure trigger: %v", err)
	}
	if _, err := superDB.ExecContext(ctx, `DROP FUNCTION public.fail_admin_draft_success_audit()`); err != nil {
		t.Fatalf("remove audit failure function: %v", err)
	}

	submit := sendAdminJSON(t, authorClient, server.URL, http.MethodPost, draftPath+"/submit", map[string]any{"expected_draft_version": versionAfterSave}, authorCSRF)
	if submit.StatusCode != http.StatusCreated {
		body := readAdminBody(t, submit)
		t.Fatalf("submit immutable revision status = %d, want 201: %s", submit.StatusCode, body)
	}
	submittedEnvelope := decodeAdminResponse(t, submit)
	var revision struct {
		Revision   int32           `json:"revision"`
		BodyHash   string          `json:"body_hash"`
		Definition json.RawMessage `json:"definition"`
	}
	if err := json.Unmarshal(submittedEnvelope.Data, &revision); err != nil || revision.Revision != 1 || len(revision.BodyHash) != 64 {
		t.Fatalf("submitted revision/hash = %d/%q, decode error %v", revision.Revision, revision.BodyHash, err)
	}
	closeAdminResponse(t, submit)
	duplicateSubmit := sendAdminJSON(t, authorClient, server.URL, http.MethodPost, draftPath+"/submit", map[string]any{"expected_draft_version": versionAfterSave}, authorCSRF)
	if duplicateSubmit.StatusCode != http.StatusConflict {
		body := readAdminBody(t, duplicateSubmit)
		t.Fatalf("repeated submission of the same draft snapshot status = %d, want 409: %s", duplicateSubmit.StatusCode, body)
	}
	closeAdminResponse(t, duplicateSubmit)

	queue := sendAdminJSON(t, reviewerClient, server.URL, http.MethodGet, "/api/v1/games/demo/review-queue", nil, "")
	if queue.StatusCode != http.StatusOK {
		body := readAdminBody(t, queue)
		t.Fatalf("Reviewer queue status = %d, want 200: %s", queue.StatusCode, body)
	}
	queueEnvelope := decodeAdminResponse(t, queue)
	closeAdminResponse(t, queue)
	var pending []struct {
		ContentID string `json:"content_id"`
		Revision  int32  `json:"revision"`
		BodyHash  string `json:"body_hash"`
	}
	if err := json.Unmarshal(queueEnvelope.Data, &pending); err != nil || len(pending) != 1 || pending[0].ContentID != contentID || pending[0].Revision != 1 || pending[0].BodyHash != revision.BodyHash {
		t.Fatalf("Reviewer queue did not bind exact submitted hash/revision: %#v (%v)", pending, err)
	}
	mismatchedHashReview := sendAdminJSON(t, reviewerClient, server.URL, http.MethodPost, "/api/v1/games/demo/revision-approvals", map[string]any{
		"content_id": contentID, "revision": 1, "body_hash": strings.Repeat("0", 64), "decision": "approved",
	}, reviewerCSRF)
	if mismatchedHashReview.StatusCode != http.StatusConflict {
		t.Fatalf("mismatched revision hash status = %d, want 409", mismatchedHashReview.StatusCode)
	}
	closeAdminResponse(t, mismatchedHashReview)
	missingRevision := sendAdminJSON(t, reviewerClient, server.URL, http.MethodPost, "/api/v1/games/demo/revision-approvals", map[string]any{
		"content_id": contentID, "revision": 2147483647, "body_hash": revision.BodyHash, "decision": "approved",
	}, reviewerCSRF)
	if missingRevision.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown exact revision review status = %d, want 404", missingRevision.StatusCode)
	}
	closeAdminResponse(t, missingRevision)

	nonItemID := "demo:map/not-an-item"
	nonItemDefinition := cloneItemDefinition(t, definition, "非物品内容")
	nonItemDefinition["content_id"] = nonItemID
	nonItemDefinition["public"].(map[string]any)["content_id"] = nonItemID
	nonItemBody, err := json.Marshal(nonItemDefinition)
	if err != nil {
		t.Fatalf("encode non-item revision fixture: %v", err)
	}
	if _, err := superDB.ExecContext(ctx, `
INSERT INTO content_revisions (game_id, content_id, revision, schema_version, content_type, body)
VALUES ($1, $2, 1, 1, 'map', $3::jsonb)`, gameID, nonItemID, nonItemBody); err != nil {
		t.Fatalf("seed non-item content revision: %v", err)
	}
	var storedNonItemBody []byte
	if err := superDB.QueryRowContext(ctx, `SELECT body FROM content_revisions WHERE game_id = $1 AND content_id = $2 AND revision = 1`, gameID, nonItemID).Scan(&storedNonItemBody); err != nil {
		t.Fatalf("read non-item revision for hash: %v", err)
	}
	canonicalNonItemBody, err := canonicalJSON(storedNonItemBody)
	if err != nil {
		t.Fatalf("canonicalize non-item revision body: %v", err)
	}
	nonItemHash := sha256.Sum256(canonicalNonItemBody)
	nonItemReview := sendAdminJSON(t, reviewerClient, server.URL, http.MethodPost, "/api/v1/games/demo/revision-approvals", map[string]any{
		"content_id": nonItemID, "revision": 1, "body_hash": hex.EncodeToString(nonItemHash[:]), "decision": "approved",
	}, reviewerCSRF)
	if nonItemReview.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("Reviewer API approved a revision whose body does not match its content family: status %d", nonItemReview.StatusCode)
	}
	closeAdminResponse(t, nonItemReview)
	var nonItemApprovalCount int
	if err := superDB.QueryRowContext(ctx, `SELECT count(*) FROM revision_approvals WHERE game_id = $1 AND content_id = $2`, gameID, nonItemID).Scan(&nonItemApprovalCount); err != nil {
		t.Fatalf("check non-item approval side effects: %v", err)
	}
	if nonItemApprovalCount != 0 {
		t.Fatalf("non-item approval rows = %d, want 0", nonItemApprovalCount)
	}

	publisherReview := sendAdminJSON(t, publisherClient, server.URL, http.MethodPost, "/api/v1/games/demo/revision-approvals", map[string]any{
		"content_id": contentID, "revision": 1, "body_hash": revision.BodyHash, "decision": "approved",
	}, publisherCSRF)
	if publisherReview.StatusCode != http.StatusForbidden {
		body := readAdminBody(t, publisherReview)
		t.Fatalf("Publisher-only approval status = %d, want 403: %s", publisherReview.StatusCode, body)
	}
	closeAdminResponse(t, publisherReview)

	var publisherID string
	if err := superDB.QueryRowContext(ctx, `SELECT admin_id::text FROM admin_users WHERE username = $1`, accounts.publisher.username).Scan(&publisherID); err != nil {
		t.Fatalf("load Publisher identity for repository authorization regression: %v", err)
	}
	revisionHash, err := hex.DecodeString(revision.BodyHash)
	if err != nil {
		t.Fatalf("decode exact item revision hash: %v", err)
	}
	if _, err := repository.ReviewRevision(ctx, gameID, contentID, 1, revisionHash, "approved", "", publisherID); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("repository accepted Publisher without Reviewer role: %v", err)
	}

	approval := sendAdminJSON(t, reviewerClient, server.URL, http.MethodPost, "/api/v1/games/demo/revision-approvals", map[string]any{
		"content_id": contentID, "revision": 1, "body_hash": revision.BodyHash, "decision": "approved", "comment": "reviewed exact revision",
	}, reviewerCSRF)
	if approval.StatusCode != http.StatusOK {
		body := readAdminBody(t, approval)
		t.Fatalf("Reviewer exact-revision approval status = %d, want 200: %s", approval.StatusCode, body)
	}
	closeAdminResponse(t, approval)

	duplicateApproval := sendAdminJSON(t, reviewerClient, server.URL, http.MethodPost, "/api/v1/games/demo/revision-approvals", map[string]any{
		"content_id": contentID, "revision": 1, "body_hash": revision.BodyHash, "decision": "approved",
	}, reviewerCSRF)
	if duplicateApproval.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate exact revision approval status = %d, want 409", duplicateApproval.StatusCode)
	}
	closeAdminResponse(t, duplicateApproval)

	postApprovalDefinition := cloneItemDefinition(t, definition, "审批后继续编辑")
	postApproval := sendAdminJSON(t, authorClient, server.URL, http.MethodPut, draftPath, map[string]any{
		"expected_draft_version": versionAfterSave, "definition": postApprovalDefinition,
	}, authorCSRF)
	if postApproval.StatusCode != http.StatusOK {
		body := readAdminBody(t, postApproval)
		t.Fatalf("post-approval draft edit status = %d, want 200: %s", postApproval.StatusCode, body)
	}
	closeAdminResponse(t, postApproval)
	var storedRevisionBody []byte
	var approvalHash []byte
	if err := superDB.QueryRowContext(ctx, `
SELECT c.body, a.body_hash
FROM content_revisions c JOIN revision_approvals a
USING (game_id, content_id, revision)
WHERE c.game_id = $1 AND c.content_id = $2 AND c.revision = 1`, gameID, contentID).Scan(&storedRevisionBody, &approvalHash); err != nil {
		t.Fatalf("read immutable approved revision: %v", err)
	}
	var storedDefinition map[string]any
	if err := json.Unmarshal(storedRevisionBody, &storedDefinition); err != nil {
		t.Fatalf("decode immutable approved revision: %v", err)
	}
	storedPublic := storedDefinition["public"].(map[string]any)
	if storedPublic["display_name"] != "测试版本" || hex.EncodeToString(approvalHash) != revision.BodyHash {
		t.Fatalf("draft edit changed approved body or approval hash: name=%v hash=%x", storedPublic["display_name"], approvalHash)
	}

	var deniedAuditCount int
	if err := superDB.QueryRowContext(ctx, `
SELECT count(*) FROM audit_logs
WHERE game_id = $1 AND outcome = 'denied' AND action IN ('role.rejected', 'admin.unauthenticated')`, gameID).Scan(&deniedAuditCount); err != nil {
		t.Fatalf("count independent denied-operation audits: %v", err)
	}
	if deniedAuditCount < 2 {
		t.Fatalf("denied audit count = %d, want at least 2", deniedAuditCount)
	}
	var submissionAuditCount, approvalAuditCount int
	if err := superDB.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs WHERE game_id = $1 AND action = 'revision.submit' AND outcome = 'success' AND resource_id = $2`, gameID, contentID+":1").Scan(&submissionAuditCount); err != nil {
		t.Fatalf("count successful revision submission audit: %v", err)
	}
	if err := superDB.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs WHERE game_id = $1 AND action = 'revision.review' AND outcome = 'success' AND resource_id = $2`, gameID, contentID+":1").Scan(&approvalAuditCount); err != nil {
		t.Fatalf("count successful revision approval audit: %v", err)
	}
	if submissionAuditCount != 1 || approvalAuditCount != 1 {
		t.Fatalf("success audit counts submit/review = %d/%d, want 1/1", submissionAuditCount, approvalAuditCount)
	}

	logout := sendAdminJSON(t, authorClient, server.URL, http.MethodPost, "/api/v1/logout", nil, authorCSRF)
	if logout.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d, want 200", logout.StatusCode)
	}
	clearedCookies := logout.Cookies()
	closeAdminResponse(t, logout)
	for _, name := range []string{"tbcg_admin_session", "tbcg_admin_csrf"} {
		cleared := false
		for _, cookie := range clearedCookies {
			if cookie.Name == name && cookie.MaxAge < 0 {
				cleared = true
			}
		}
		if !cleared {
			t.Errorf("logout did not clear %s cookie", name)
		}
	}
	newSessionResponse, err := authorClient.Get(server.URL + "/api/v1/session")
	if err != nil {
		t.Fatalf("read session after logout: %v", err)
	}
	loggedOutEnvelope := decodeAdminResponse(t, newSessionResponse)
	closeAdminResponse(t, newSessionResponse)
	var loggedOutSession struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.Unmarshal(loggedOutEnvelope.Data, &loggedOutSession); err != nil || loggedOutSession.Authenticated {
		t.Fatalf("session remained authenticated after logout: %#v (%v)", loggedOutSession, err)
	}
}
