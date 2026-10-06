package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	release "github.com/taills/tbcg/server/internal/content/release"
)

func TestReleaseValidationRejectsInvalidDefinitionsAndMissingDependencies(t *testing.T) {
	publisher := release.NewPublisher(nil, t.TempDir())
	valid := releaseDefinition("game", "realm", "release-valid", 1)
	build := func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("public"), Private: []byte("private")}, nil
	}
	validate := func(context.Context, release.Definition) error { return nil }
	invalidID := valid
	invalidID.ReleaseID = "../private"
	if _, err := publisher.Publish(context.Background(), invalidID, build, validate); !errors.Is(err, release.ErrReleaseIDInvalid) {
		t.Fatalf("path-traversing release ID error = %v, want ErrReleaseIDInvalid", err)
	}
	if _, err := publisher.Publish(context.Background(), valid, nil, validate); err == nil {
		t.Fatal("publish accepted a nil artifact builder")
	}
	if _, err := publisher.Publish(context.Background(), valid, build, nil); err == nil {
		t.Fatal("publish accepted a nil activation validator")
	}
	invalidManifest := valid
	invalidManifest.Manifest = json.RawMessage(`[]`)
	if _, err := publisher.Publish(context.Background(), invalidManifest, build, validate); err == nil {
		t.Fatal("publish accepted a non-object release manifest")
	}
}

func TestReleaseRequiresExistingRealmAndNonEmptyArtifacts(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	publisher := release.NewPublisher(store.db, filepath.Join(t.TempDir(), "artifacts"))
	called := false
	definition := releaseDefinition("missing-game", "missing-realm", "release-no-realm", 1)
	if _, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
		called = true
		return release.Artifacts{Public: []byte("public"), Private: []byte("private")}, nil
	}, func(context.Context, release.Definition) error { return nil }); err == nil {
		t.Fatal("publish succeeded for a missing realm")
	}
	if called {
		t.Fatal("release builder ran before target realm validation")
	}

	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	definition = releaseDefinition(gameID, realmID, "release-empty-artifacts", 2)
	if _, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{}, nil
	}, func(context.Context, release.Definition) error { return nil }); err == nil {
		t.Fatal("publish accepted empty artifact bundles")
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, oldRelease)
	assertReleaseStatusMissing(t, ctx, store, gameID, definition.ReleaseID)
}

func TestReleaseBuildFailureKeepsCurrentRelease(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	publisher := release.NewPublisher(store.db, filepath.Join(t.TempDir(), "artifacts"))
	definition := releaseDefinition(gameID, realmID, "release-build-fail", 2)

	_, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{}, errors.New("injected release build failure")
	}, func(context.Context, release.Definition) error { return nil })
	if err == nil {
		t.Fatal("release build failure was not reported")
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, oldRelease)
	assertReleaseStatusMissing(t, ctx, store, gameID, definition.ReleaseID)
}

func TestReleaseArtifactStorageFailureKeepsCurrentRelease(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	artifactRoot := filepath.Join(t.TempDir(), "file-not-directory")
	if err := os.WriteFile(artifactRoot, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("create artifact path obstruction: %v", err)
	}
	publisher := release.NewPublisher(store.db, artifactRoot)
	definition := releaseDefinition(gameID, realmID, "release-storage-fail", 2)
	_, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("public"), Private: []byte("private")}, nil
	}, func(context.Context, release.Definition) error { return nil })
	if err == nil {
		t.Fatal("release publish succeeded with an invalid artifact directory")
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, oldRelease)
	assertReleaseStatusMissing(t, ctx, store, gameID, definition.ReleaseID)
}

func TestReleaseRefusesInsecureExistingArtifactDirectoryWithoutChangingIt(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	artifactRoot := filepath.Join(t.TempDir(), "insecure-artifact-root")
	if err := os.Mkdir(artifactRoot, 0o755); err != nil {
		t.Fatalf("create intentionally non-private artifact directory: %v", err)
	}
	definition := releaseDefinition(gameID, realmID, "release-insecure-root", 2)
	publisher := release.NewPublisher(store.db, artifactRoot)
	if _, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("public"), Private: []byte("private")}, nil
	}, func(context.Context, release.Definition) error { return nil }); err == nil {
		t.Fatal("publish accepted a shared artifact directory")
	}
	info, err := os.Stat(artifactRoot)
	if err != nil {
		t.Fatalf("stat artifact root after rejected publish: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("publisher changed caller directory permissions to %04o", info.Mode().Perm())
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, oldRelease)
}

func TestReleasePrivateArtifactConflictKeepsCurrentRelease(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	artifactRoot := filepath.Join(t.TempDir(), "artifacts")
	if err := os.Mkdir(artifactRoot, 0o700); err != nil {
		t.Fatalf("create private artifact root: %v", err)
	}
	if err := os.Mkdir(filepath.Join(artifactRoot, gameID), 0o700); err != nil {
		t.Fatalf("create private game artifact directory: %v", err)
	}
	definition := releaseDefinition(gameID, realmID, "release-private-conflict", 2)
	privatePath := filepath.Join(artifactRoot, gameID, definition.ReleaseID+".private")
	if err := os.WriteFile(privatePath, []byte("existing-private"), 0o600); err != nil {
		t.Fatalf("create existing private artifact: %v", err)
	}
	publisher := release.NewPublisher(store.db, artifactRoot)
	if _, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("public"), Private: []byte("replacement-private")}, nil
	}, func(context.Context, release.Definition) error { return nil }); err == nil {
		t.Fatal("publish overwrote an existing private artifact")
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, oldRelease)
	assertReleaseStatusMissing(t, ctx, store, gameID, definition.ReleaseID)
	actualPrivate, err := os.ReadFile(privatePath)
	if err != nil || string(actualPrivate) != "existing-private" {
		t.Fatalf("existing private artifact changed to %q, error %v", actualPrivate, err)
	}
}

func TestReleaseRegistrationConflictKeepsCurrentRelease(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	definition := releaseDefinition(gameID, realmID, "release-registration-conflict", 2)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO releases (game_id,release_id,release_version,content_schema_version,instance_schema_version,ruleset_version,protocol_version,client_min_build,status,manifest)
VALUES ($1,$2,2,1,1,1,1,'1','built','{}'::jsonb)`, gameID, definition.ReleaseID); err != nil {
		t.Fatalf("create conflicting release record: %v", err)
	}
	publisher := release.NewPublisher(store.db, filepath.Join(t.TempDir(), "artifacts"))
	if _, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("public"), Private: []byte("private")}, nil
	}, func(context.Context, release.Definition) error { return nil }); err == nil {
		t.Fatal("publish succeeded despite release registration conflict")
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, oldRelease)
}

func TestReleaseActivationFailureKeepsOldPointerAndPrivateArtifactPrivate(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	artifactRoot := filepath.Join(t.TempDir(), "artifacts")
	publisher := release.NewPublisher(store.db, artifactRoot)
	definition := releaseDefinition(gameID, realmID, "release-activation-fail", 2)

	_, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte(`{"client":"visible"}`), Private: []byte(`{"loot":"secret"}`)}, nil
	}, func(context.Context, release.Definition) error {
		return errors.New("injected compatibility activation failure")
	})
	if err == nil {
		t.Fatal("activation validation failure was not reported")
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, oldRelease)
	assertReleaseStatus(t, ctx, store, gameID, definition.ReleaseID, "built")
	privatePath := filepath.Join(artifactRoot, gameID, definition.ReleaseID+".private")
	privateInfo, err := os.Stat(privatePath)
	if err != nil {
		t.Fatalf("private artifact was not staged: %v", err)
	}
	if privateInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("private artifact permissions = %04o, must not be accessible to group/other", privateInfo.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(artifactRoot, gameID, "public", definition.ReleaseID+".private")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private artifact appeared under public artifact path: %v", err)
	}
	var storedManifest []byte
	if err := store.db.QueryRowContext(ctx, `SELECT manifest FROM releases WHERE game_id=$1 AND release_id=$2`, gameID, definition.ReleaseID).Scan(&storedManifest); err != nil {
		t.Fatalf("read staged release manifest: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(storedManifest, &manifest); err != nil {
		t.Fatalf("decode staged manifest: %v", err)
	}
	if manifest["public_artifact_sha256"] == nil || manifest["private_artifact_sha256"] == nil {
		t.Fatalf("manifest omitted artifact hashes: %s", storedManifest)
	}
}

func TestReleaseActivationCompareAndSwapDoesNotClobberConcurrentPointer(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	assertActiveRelease(t, ctx, store, gameID, realmID, oldRelease)
	alternateRelease := "alternate-" + randomTestIdentifier(t, "")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO releases (game_id,release_id,release_version,content_schema_version,instance_schema_version,ruleset_version,protocol_version,client_min_build,status,manifest)
VALUES ($1,$2,2,1,1,1,1,'1','activated','{}'::jsonb)`, gameID, alternateRelease); err != nil {
		t.Fatalf("create concurrent active release: %v", err)
	}
	publisher := release.NewPublisher(store.db, filepath.Join(t.TempDir(), "artifacts"))
	definition := releaseDefinition(gameID, realmID, "release-stale-pointer", 3)
	_, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("public"), Private: []byte("private")}, nil
	}, func(context.Context, release.Definition) error {
		_, err := store.db.ExecContext(ctx, `UPDATE realms SET active_release_id=$3 WHERE game_id=$1 AND realm_id=$2`, gameID, realmID, alternateRelease)
		return err
	})
	if !errors.Is(err, release.ErrActivationConflict) {
		t.Fatalf("stale release pointer activation error = %v, want ErrActivationConflict", err)
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, alternateRelease)
}

func TestReleaseStatusRaceRollsBackPointerSwitch(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	publisher := release.NewPublisher(store.db, filepath.Join(t.TempDir(), "artifacts"))
	definition := releaseDefinition(gameID, realmID, "release-status-race", 2)
	_, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("public"), Private: []byte("private")}, nil
	}, func(context.Context, release.Definition) error {
		_, err := store.db.ExecContext(ctx, `UPDATE releases SET status='deprecated' WHERE game_id=$1 AND release_id=$2`, gameID, definition.ReleaseID)
		return err
	})
	if !errors.Is(err, release.ErrActivationConflict) {
		t.Fatalf("release status race error = %v, want ErrActivationConflict", err)
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, oldRelease)
}

func TestReleaseCommitRejectionKeepsOldPointer(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	if _, err := store.db.ExecContext(ctx, `
CREATE FUNCTION reject_release_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected release activation commit rejection'; END $$;
CREATE CONSTRAINT TRIGGER reject_release_commit_trigger AFTER UPDATE OF active_release_id ON realms
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_release_commit()`); err != nil {
		t.Fatalf("install release commit rejection: %v", err)
	}
	t.Cleanup(func() {
		_, err := store.db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS reject_release_commit_trigger ON realms; DROP FUNCTION IF EXISTS reject_release_commit()`)
		if err != nil {
			t.Errorf("remove release commit rejection: %v", err)
		}
	})
	definition := releaseDefinition(gameID, realmID, "release-commit-rejected", 2)
	publisher := release.NewPublisher(store.db, filepath.Join(t.TempDir(), "artifacts"))
	if _, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("public"), Private: []byte("private")}, nil
	}, func(context.Context, release.Definition) error { return nil }); err == nil {
		t.Fatal("publish succeeded despite PostgreSQL activation commit rejection")
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, oldRelease)
	assertReleaseStatus(t, ctx, store, gameID, definition.ReleaseID, "built")
}

func TestReleaseActivationSwitchesPointerAtomically(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	publisher := release.NewPublisher(store.db, filepath.Join(t.TempDir(), "artifacts"))
	definition := releaseDefinition(gameID, realmID, "release-activated", 2)
	published, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("public"), Private: []byte("private")}, nil
	}, func(context.Context, release.Definition) error { return nil })
	if err != nil {
		t.Fatalf("publish release: %v", err)
	}
	if published.PreviousReleaseID != oldRelease || published.ReleaseID != definition.ReleaseID || published.PublicHash == "" || published.PrivateHash == "" {
		t.Fatalf("published release result = %+v", published)
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, definition.ReleaseID)
	assertReleaseStatus(t, ctx, store, gameID, definition.ReleaseID, "activated")
	assertReleaseStatus(t, ctx, store, gameID, oldRelease, "activated")
}

func TestReleaseArtifactsAreIsolatedByGameID(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	artifactRoot := filepath.Join(t.TempDir(), "artifacts")
	publisher := release.NewPublisher(store.db, artifactRoot)
	gameOne, realmOne, _ := newReleaseFixture(t, ctx, store)
	gameTwo, realmTwo, _ := newReleaseFixture(t, ctx, store)
	const sharedReleaseID = "release-shared"
	build := func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("public"), Private: []byte("private")}, nil
	}
	for _, target := range [][2]string{{gameOne, realmOne}, {gameTwo, realmTwo}} {
		definition := releaseDefinition(target[0], target[1], sharedReleaseID, 2)
		if _, err := publisher.Publish(ctx, definition, build, func(context.Context, release.Definition) error { return nil }); err != nil {
			t.Fatalf("publish same release ID for game %q: %v", target[0], err)
		}
		for _, suffix := range []string{"public", "private"} {
			path := filepath.Join(artifactRoot, target[0], sharedReleaseID+"."+suffix)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("find %s artifact for game %q at %q: %v", suffix, target[0], path, err)
			}
		}
	}
}

func TestReleaseActivationRejectsStaleOwnerEpoch(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	definition := releaseDefinition(gameID, realmID, "release-stale-owner", 2)
	definition.OwnerEpoch = 0
	if _, err := store.db.ExecContext(ctx, `UPDATE owner_guards SET owner_epoch=owner_epoch+1 WHERE game_id=$1 AND realm_id=$2`, gameID, realmID); err != nil {
		t.Fatalf("take over release owner: %v", err)
	}
	publisher := release.NewPublisher(store.db, filepath.Join(t.TempDir(), "artifacts"))
	if _, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("public"), Private: []byte("private")}, nil
	}, func(context.Context, release.Definition) error { return nil }); !errors.Is(err, release.ErrOwnerEpochMismatch) {
		t.Fatalf("stale owner activation error = %v, want ErrOwnerEpochMismatch", err)
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, oldRelease)
}

func TestReleaseActivationGuardBlocksConcurrentOwnerTakeover(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	gameID, realmID, oldRelease := newReleaseFixture(t, ctx, store)
	const activationBarrier int64 = 71826009
	if _, err := store.db.ExecContext(ctx, `
CREATE FUNCTION wait_for_release_activation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM pg_advisory_xact_lock(71826009); RETURN NEW; END $$;
CREATE TRIGGER wait_for_release_activation_trigger BEFORE UPDATE OF active_release_id ON realms
FOR EACH ROW EXECUTE FUNCTION wait_for_release_activation()`); err != nil {
		t.Fatalf("install activation barrier: %v", err)
	}
	t.Cleanup(func() {
		_, err := store.db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS wait_for_release_activation_trigger ON realms; DROP FUNCTION IF EXISTS wait_for_release_activation()`)
		if err != nil {
			t.Errorf("remove activation barrier: %v", err)
		}
	})
	locker, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve activation barrier connection: %v", err)
	}
	defer locker.Close()
	if _, err := locker.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, activationBarrier); err != nil {
		t.Fatalf("hold activation barrier: %v", err)
	}
	defer locker.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, activationBarrier)

	definition := releaseDefinition(gameID, realmID, "release-owner-race", 2)
	definition.OwnerEpoch = 0
	publisher := release.NewPublisher(store.db, filepath.Join(t.TempDir(), "artifacts"))
	activationDone := make(chan error, 1)
	go func() {
		_, err := publisher.Publish(ctx, definition, func(context.Context, release.Definition) (release.Artifacts, error) {
			return release.Artifacts{Public: []byte("public"), Private: []byte("private")}, nil
		}, func(context.Context, release.Definition) error { return nil })
		activationDone <- err
	}()
	awaitLockWaitingQuery(t, waitCtx, store, `UPDATE realms SET active_release_id%`)

	takeoverDone := make(chan error, 1)
	go func() {
		_, err := store.db.ExecContext(ctx, `UPDATE owner_guards SET owner_epoch=owner_epoch+1 WHERE game_id=$1 AND realm_id=$2`, gameID, realmID)
		takeoverDone <- err
	}()
	awaitLockWaitingQuery(t, waitCtx, store, `UPDATE owner_guards SET owner_epoch%`)
	if _, err := locker.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, activationBarrier); err != nil {
		t.Fatalf("release activation barrier: %v", err)
	}
	select {
	case err := <-activationDone:
		if err != nil {
			t.Fatalf("activate release while owner takeover waits: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("release activation did not finish after releasing its barrier")
	}
	select {
	case err := <-takeoverDone:
		if err != nil {
			t.Fatalf("advance owner epoch after release activation: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("owner takeover did not finish after activation committed")
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, definition.ReleaseID)
	assertReleaseStatus(t, ctx, store, gameID, definition.ReleaseID, "activated")
	assertReleaseStatus(t, ctx, store, gameID, oldRelease, "activated")
}

func TestReleaseRetryIsIdempotentForSameHashesAndRejectsChangedHashes(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, realmID, _ := newReleaseFixture(t, ctx, store)
	artifactRoot := filepath.Join(t.TempDir(), "artifacts")
	publisher := release.NewPublisher(store.db, artifactRoot)
	definition := releaseDefinition(gameID, realmID, "release-retry", 2)
	build := func(_ context.Context, _ release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("public-v1"), Private: []byte("private-v1")}, nil
	}
	validate := func(context.Context, release.Definition) error { return nil }
	first, err := publisher.Publish(ctx, definition, build, validate)
	if err != nil {
		t.Fatalf("publish initial release: %v", err)
	}
	second, err := publisher.Publish(ctx, definition, build, validate)
	if err != nil {
		t.Fatalf("retry identical release: %v", err)
	}
	if second.ReleaseID != first.ReleaseID || second.PublicHash != first.PublicHash || second.PrivateHash != first.PrivateHash {
		t.Fatalf("idempotent retry result = %+v, want release/hash identity from %+v", second, first)
	}

	changedBuild := func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("public-v2"), Private: []byte("private-v2")}, nil
	}
	if _, err := publisher.Publish(ctx, definition, changedBuild, validate); !errors.Is(err, release.ErrReleaseConflict) {
		t.Fatalf("same release ID with changed artifact hashes error = %v, want ErrReleaseConflict", err)
	}
	for suffix, expected := range map[string]string{"public": "public-v1", "private": "private-v1"} {
		path := filepath.Join(artifactRoot, gameID, definition.ReleaseID+"."+suffix)
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != expected {
			t.Errorf("existing %s artifact = %q, error %v; want unchanged %q", suffix, actual, err, expected)
		}
	}
	assertActiveRelease(t, ctx, store, gameID, realmID, definition.ReleaseID)
}

func TestReleaseCanBeReusedAcrossRealmsButNotWithChangedMetadata(t *testing.T) {
	ctx, store := openUnitOfWorkTestStore(t)
	gameID, firstRealmID, oldRelease := newReleaseFixture(t, ctx, store)
	secondRealmID := randomTestIdentifier(t, "release-second-realm-")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO realms (game_id,realm_id,display_name,active_release_id) VALUES ($1,$2,'Second release realm',$3)`, gameID, secondRealmID, oldRelease); err != nil {
		t.Fatalf("create second release realm: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO owner_guards (game_id,realm_id) VALUES ($1,$2)`, gameID, secondRealmID); err != nil {
		t.Fatalf("create second realm owner guard: %v", err)
	}

	artifactRoot := filepath.Join(t.TempDir(), "artifacts")
	publisher := release.NewPublisher(store.db, artifactRoot)
	definition := releaseDefinition(gameID, firstRealmID, "release-cross-realm", 2)
	definition.OwnerEpoch = 0
	build := func(context.Context, release.Definition) (release.Artifacts, error) {
		return release.Artifacts{Public: []byte("shared-public"), Private: []byte("shared-private")}, nil
	}
	validate := func(context.Context, release.Definition) error { return nil }
	first, err := publisher.Publish(ctx, definition, build, validate)
	if err != nil {
		t.Fatalf("publish release to first realm: %v", err)
	}

	secondDefinition := definition
	secondDefinition.RealmID = secondRealmID
	second, err := publisher.Publish(ctx, secondDefinition, build, validate)
	if err != nil {
		t.Fatalf("reuse identical game-scoped release in second realm: %v", err)
	}
	if second.ReleaseID != first.ReleaseID || second.PublicHash != first.PublicHash || second.PrivateHash != first.PrivateHash {
		t.Fatalf("cross-realm reuse result = %+v, want matching hashes from %+v", second, first)
	}
	assertActiveRelease(t, ctx, store, gameID, firstRealmID, definition.ReleaseID)
	assertActiveRelease(t, ctx, store, gameID, secondRealmID, definition.ReleaseID)

	changedMetadata := secondDefinition
	changedMetadata.ReleaseVersion++
	if _, err := publisher.Publish(ctx, changedMetadata, build, validate); !errors.Is(err, release.ErrReleaseConflict) {
		t.Fatalf("same release ID with changed version error = %v, want ErrReleaseConflict", err)
	}
	for suffix, expected := range map[string]string{"public": "shared-public", "private": "shared-private"} {
		path := filepath.Join(artifactRoot, gameID, definition.ReleaseID+"."+suffix)
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != expected {
			t.Errorf("cross-realm %s artifact = %q, error %v; want unchanged %q", suffix, actual, err, expected)
		}
	}
}

func awaitLockWaitingQuery(t *testing.T, ctx context.Context, store *Store, queryPattern string) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int
		err := store.db.QueryRowContext(ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND state='active' AND wait_event_type='Lock' AND pid<>pg_backend_pid() AND query LIKE $1 ORDER BY query_start LIMIT 1`, queryPattern).Scan(&pid)
		if err == nil {
			var waiting bool
			if err := store.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid=$1 AND NOT granted)`, pid).Scan(&waiting); err != nil {
				t.Fatalf("inspect PostgreSQL lock wait for backend %d: %v", pid, err)
			}
			if !waiting {
				t.Fatalf("backend %d was not waiting on a PostgreSQL lock", pid)
			}
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("find backend waiting on query %q: %v", queryPattern, err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("backend did not reach lock wait for query %q", queryPattern)
		case <-ticker.C:
		}
	}
}

func newReleaseFixture(t *testing.T, ctx context.Context, store *Store) (string, string, string) {
	t.Helper()
	gameID := randomTestIdentifier(t, "release-game-")
	realmID := randomTestIdentifier(t, "release-realm-")
	oldRelease := "release-old-" + randomTestIdentifier(t, "")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO games (game_id,display_name) VALUES ($1,'Release test')`, gameID); err != nil {
		t.Fatalf("create release fixture game: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO releases (game_id,release_id,release_version,content_schema_version,instance_schema_version,ruleset_version,protocol_version,client_min_build,status,manifest)
VALUES ($1,$2,1,1,1,1,1,'1','activated','{}'::jsonb)`, gameID, oldRelease); err != nil {
		t.Fatalf("create initial release: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO realms (game_id,realm_id,display_name,active_release_id) VALUES ($1,$2,'Release test realm',$3)`, gameID, realmID, oldRelease); err != nil {
		t.Fatalf("create release fixture realm: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO owner_guards (game_id,realm_id) VALUES ($1,$2)`, gameID, realmID); err != nil {
		t.Fatalf("create release fixture owner guard: %v", err)
	}
	return gameID, realmID, oldRelease
}

func releaseDefinition(gameID, realmID, releaseID string, version int) release.Definition {
	return release.Definition{
		GameID: gameID, RealmID: realmID, ReleaseID: releaseID, ReleaseVersion: version,
		ContentSchemaVersion: 1, InstanceSchemaVersion: 1, RulesetVersion: 1, ProtocolVersion: 1,
		ClientMinBuild: "1.0.0", Manifest: json.RawMessage(`{"capabilities":[]}`),
	}
}

func assertActiveRelease(t *testing.T, ctx context.Context, store *Store, gameID, realmID, want string) {
	t.Helper()
	var actual string
	if err := store.db.QueryRowContext(ctx, `SELECT active_release_id FROM realms WHERE game_id=$1 AND realm_id=$2`, gameID, realmID).Scan(&actual); err != nil {
		t.Fatalf("read active release pointer: %v", err)
	}
	if actual != want {
		t.Fatalf("active release = %q, want %q", actual, want)
	}
}

func assertReleaseStatus(t *testing.T, ctx context.Context, store *Store, gameID, releaseID, want string) {
	t.Helper()
	var actual string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM releases WHERE game_id=$1 AND release_id=$2`, gameID, releaseID).Scan(&actual); err != nil {
		t.Fatalf("read release status: %v", err)
	}
	if actual != want {
		t.Fatalf("release status = %q, want %q", actual, want)
	}
}

func assertReleaseStatusMissing(t *testing.T, ctx context.Context, store *Store, gameID, releaseID string) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM releases WHERE game_id=$1 AND release_id=$2`, gameID, releaseID).Scan(&count); err != nil {
		t.Fatalf("check failed release record: %v", err)
	}
	if count != 0 {
		t.Fatalf("release build failure left %d persisted release records", count)
	}
}
