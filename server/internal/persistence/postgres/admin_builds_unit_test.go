package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"example.com/tbcg/server/internal/admin"
	"example.com/tbcg/server/internal/content/assets"
	"example.com/tbcg/server/internal/persistence"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestBuildStoreErrorMapsStableSQLStates(t *testing.T) {
	for code, want := range map[string]error{
		"42501": admin.ErrForbidden,
		"P0002": admin.ErrNotFound,
		"55000": admin.ErrBuildProfileNotConfigured,
		"22023": admin.ErrInvalidBuildRequest,
		"23514": admin.ErrInvalidBuildRequest,
		"23505": admin.ErrConflict,
		"40001": admin.ErrConflict,
		"55006": admin.ErrBuildAlreadyActive,
		"53400": admin.ErrBuildQueueFull,
	} {
		if err := buildStoreError("op", &pgconn.PgError{Code: code, SeverityUnlocalized: "ERROR"}); !errors.Is(err, want) {
			t.Errorf("SQLSTATE %s mapped to %v, want %v", code, err, want)
		}
	}
	plain := errors.New("network down")
	if err := buildStoreError("op", plain); !errors.Is(err, plain) || errors.Is(err, admin.ErrConflict) {
		t.Fatalf("unclassified error = %v", err)
	}
	if err := buildWorkerStoreError("op", &pgconn.PgError{Code: "42501", SeverityUnlocalized: "ERROR"}); !errors.Is(err, admin.ErrForbidden) || errors.Is(err, persistence.ErrUnknown) {
		t.Fatalf("worker permission error = %v", err)
	}
	if err := buildWorkerStoreError("op", plain); !errors.Is(err, plain) || !errors.Is(err, persistence.ErrUnknown) {
		t.Fatalf("worker transport error must be an unknown outcome: %v", err)
	}
}

func TestDecodeBuildJobRejectsOutOfScopeOrMalformedRows(t *testing.T) {
	hash := strings.Repeat("a", 64)
	valid := `{"job_id":"00000000-0000-4000-8000-000000000001","game_id":"demo","content_id":"demo:item/a","revision":1,"body_hash":"` + hash +
		`","release_id":"build-1","release_version":1,"status":"queued","created_at":"2026-10-06T00:00:00Z","updated_at":"2026-10-06T00:00:00Z","public_artifact_sha256":null,"error_code":null}`
	if _, err := decodeBuildJob([]byte(valid), "demo"); err != nil {
		t.Fatalf("decode valid job: %v", err)
	}
	for name, mutate := range map[string]func(string) string{
		"other Game":     func(s string) string { return strings.Replace(s, `"game_id":"demo"`, `"game_id":"other"`, 1) },
		"unknown status": func(s string) string { return strings.Replace(s, `"queued"`, `"activated"`, 1) },
		"uppercase hash": func(s string) string { return strings.Replace(s, hash, strings.ToUpper(hash), 1) },
		"bad public hash": func(s string) string {
			return strings.Replace(s, `"public_artifact_sha256":null`, `"public_artifact_sha256":"xyz"`, 1)
		},
		"not JSON": func(string) string { return "{" },
	} {
		if _, err := decodeBuildJob([]byte(mutate(valid)), "demo"); err == nil {
			t.Errorf("%s: decodeBuildJob accepted an invalid row", name)
		}
	}
	if _, err := decodeWorkerJobResult([]byte(valid), "00000000-0000-4000-8000-000000000001", admin.BuildStatusBuilt); err == nil {
		t.Fatal("worker result accepted a job in the wrong state")
	}
	if _, err := decodeWorkerJobResult([]byte("{"), "x", admin.BuildStatusBuilt); err == nil {
		t.Fatal("worker result accepted malformed JSON")
	}
}

func TestBuildRepositoriesRejectInvalidArgumentsBeforeQuerying(t *testing.T) {
	ctx := context.Background()
	repository := &AdminRepository{}
	if _, err := repository.ReadApprovedBuildInput(ctx, "demo", "actor", "demo:item/a", 0, make([]byte, 32)); !errors.Is(err, admin.ErrInvalidBuildRequest) {
		t.Fatalf("zero revision read = %v", err)
	}
	if _, _, err := repository.FindBuildJobByKey(ctx, "demo", "actor", "key", []byte("short")); !errors.Is(err, admin.ErrInvalidBuildRequest) {
		t.Fatalf("short fingerprint lookup = %v", err)
	}
	if _, err := repository.EnqueueBuild(ctx, "demo", "actor", admin.BuildRequest{BodyHash: "zz"}, "key", make([]byte, 32), 1, strings.Repeat("a", 64), assets.FrozenSnapshot{}, ""); err == nil {
		t.Fatal("enqueue accepted a malformed request before querying")
	}

	worker := &BuildWorkerRepository{}
	uuid := "00000000-0000-4000-8000-000000000001"
	if _, _, err := worker.Claim(ctx, "not-a-uuid", 30); err == nil {
		t.Fatal("claim accepted an invalid worker identity")
	}
	if _, _, err := worker.Claim(ctx, uuid, 301); err == nil {
		t.Fatal("claim accepted an unbounded lease")
	}
	if _, err := worker.RenewLease(ctx, uuid, uuid, "bad", 30); err == nil {
		t.Fatal("renew accepted an invalid claim token")
	}
	if _, err := worker.Finish(ctx, uuid, uuid, uuid, []byte("{"), make([]byte, 32), make([]byte, 32)); err == nil {
		t.Fatal("finish accepted a malformed manifest")
	}
	if _, err := worker.Finish(ctx, uuid, uuid, uuid, []byte("{}"), make([]byte, 31), make([]byte, 32)); err == nil {
		t.Fatal("finish accepted a short artifact hash")
	}
	if _, err := worker.Fail(ctx, uuid, uuid, uuid, "Bad Code", false, 0); err == nil {
		t.Fatal("fail accepted an unsafe error code")
	}
	if _, err := worker.Fail(ctx, uuid, uuid, uuid, "artifact_store_unavailable", true, 3601); err == nil {
		t.Fatal("fail accepted an unbounded backoff")
	}
}
