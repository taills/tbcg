package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"example.com/tbcg/server/internal/admin"
	contentassets "example.com/tbcg/server/internal/content/assets"
	contentbuild "example.com/tbcg/server/internal/content/build"
	"example.com/tbcg/server/internal/content/buildworker"
	"example.com/tbcg/server/internal/content/catalog"
	"example.com/tbcg/server/internal/content/mapdata"
	"example.com/tbcg/server/internal/content/schema"
	rt "example.com/tbcg/server/modules/runtime"
)

// seedApprovedDemoContent 以受控测试数据写入全部示例内容的已批准 revision。
func seedApprovedDemoContent(t *testing.T, ctx context.Context, db *sql.DB, root, reviewer string) {
	t.Helper()
	entries, err := catalog.ReadDir(os.DirFS(filepath.Join(root, "games/demo/content")))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		var stored []byte
		if err := db.QueryRowContext(ctx, `INSERT INTO content_revisions (game_id, content_id, revision, schema_version, content_type, body)
VALUES ('demo',$1,1,1,$2,$3::jsonb) RETURNING body`, entry.ContentID, entry.ContentType, []byte(entry.Definition)).Scan(&stored); err != nil {
			t.Fatalf("insert %s: %v", entry.ContentID, err)
		}
		canonical, err := canonicalJSON(stored)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(canonical)
		if _, err := db.ExecContext(ctx, `INSERT INTO revision_approvals (game_id, content_id, revision, body_hash, decision, reviewer_id) VALUES ('demo',$1,1,$2,'approved',$3::uuid)`,
			entry.ContentID, hash[:], reviewer); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotBuildAndRuntimeActivation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db := newGameTestDatabase(t, ctx)
	root := filepath.Dir(findServerRoot(t))
	var actor string
	if _, err := db.ExecContext(ctx, `INSERT INTO games (game_id, display_name) VALUES ('demo','Demo')`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO admin_users (username, password_hash) VALUES ('publisher', 'x') RETURNING admin_id::text`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"Publisher", "Reviewer"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO admin_role_bindings (game_id, admin_id, role) VALUES ('demo',$1::uuid,$2)`, actor, role); err != nil {
			t.Fatal(err)
		}
	}
	manifestBytes, err := os.ReadFile(filepath.Join(root, "games/demo/assets/manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifestHash := sha256.Sum256(manifestBytes)
	if _, err := db.ExecContext(ctx, `INSERT INTO game_build_profiles (game_id, content_schema_version, instance_schema_version, ruleset_version, protocol_version, client_min_build, asset_manifest_sha256)
VALUES ('demo',1,1,1,1,'0.1.0',$1)`, manifestHash[:]); err != nil {
		t.Fatal(err)
	}
	seedApprovedDemoContent(t, ctx, db, root, actor)

	contracts := os.DirFS(filepath.Join(root, "contracts"))
	registry, err := schema.NewRegistry(contracts)
	if err != nil {
		t.Fatal(err)
	}
	geometry, err := mapdata.NewValidator(contracts)
	if err != nil {
		t.Fatal(err)
	}
	repository := newAdminRepository(db)
	preview, err := repository.PreviewSnapshot(ctx, "demo", actor)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	var frozen []contentassets.FrozenSnapshot
	for _, iconID := range preview.IconIDs {
		snapshot, err := contentassets.ReadIcon(os.DirFS(filepath.Join(root, "games/demo/assets")), iconID)
		if err != nil {
			t.Fatal(err)
		}
		frozen = append(frozen, snapshot.Frozen())
	}
	fingerprint := sha256.Sum256([]byte("game_snapshot\x00demo"))
	job, err := repository.EnqueueSnapshot(ctx, "demo", actor, "snapshot-key-0001", fingerprint[:], preview.ProfileVersion, frozen, registry.Fingerprint())
	if err != nil {
		t.Fatalf("enqueue snapshot: %v", err)
	}
	replay, err := repository.EnqueueSnapshot(ctx, "demo", actor, "snapshot-key-0001", fingerprint[:], preview.ProfileVersion, frozen, registry.Fingerprint())
	if err != nil || string(replay) != string(job) {
		t.Fatalf("idempotent snapshot replay differs: %v", err)
	}

	artifactDir := t.TempDir()
	if err := os.Chmod(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := contentbuild.OpenArtifactStore(artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workerRepository := &BuildWorkerRepository{db: db}
	worker, err := buildworker.New("7f1c7a52-4a11-4a51-9b7c-0c4f2d6a1e90", 30, workerRepository.WorkerQueue(), store, registry.Item())
	if err != nil {
		t.Fatal(err)
	}
	worker, err = worker.WithSnapshots(workerRepository.SnapshotQueue(), registry, geometry)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := worker.RunOnce(ctx)
	if err != nil || !claimed {
		t.Fatalf("worker RunOnce claimed=%v err=%v", claimed, err)
	}
	var releaseID, status string
	var entries int
	if err := db.QueryRowContext(ctx, `SELECT release_id, status FROM admin_snapshot_jobs`).Scan(&releaseID, &status); err != nil || status != "built" {
		t.Fatalf("snapshot job status=%s err=%v", status, err)
	}
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM release_entries WHERE release_id=$1`, releaseID).Scan(&entries)
	if entries < 30 {
		t.Fatalf("release entries = %d", entries)
	}
	public, err := store.Read("demo", releaseID, "public")
	if err != nil {
		t.Fatal(err)
	}
	if containsBytes(public, "drop_weight_basis_points") || containsBytes(public, "\"loot\"") || containsBytes(public, "reward_value_minor") {
		t.Fatal("public snapshot leaked private fields")
	}

	ownerConn, epoch, err := rt.AcquireOwnership(ctx, db, "demo", "main", true)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerConn.Close()
	protocol, err := rt.LoadCatalog(filepath.Join(root, "contracts/protocol/message_catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	realm := rt.NewRealm("demo", "main", db, protocol, &rt.ReleaseLoader{Store: store, Registry: registry, Geometry: geometry})
	realm.Epoch = epoch
	if err := realm.LoadActive(ctx); err != nil {
		t.Fatal(err)
	}
	if realm.Release().ReleaseID != "" {
		t.Fatal("realm should start without an active release")
	}
	if _, err := repository.RequestActivation(ctx, "demo", "main", actor, releaseID, "activation-key-0001"); err != nil {
		t.Fatalf("request activation: %v", err)
	}
	if _, err := repository.RequestActivation(ctx, "demo", "main", actor, releaseID, "activation-key-0002"); !errors.Is(err, admin.ErrBuildAlreadyActive) {
		t.Fatalf("second pending activation error = %v", err)
	}
	runCtx, stopRealm := context.WithCancel(ctx)
	defer stopRealm()
	go realm.Run(runCtx)
	deadline := time.Now().Add(15 * time.Second)
	for realm.Release().ReleaseID != releaseID {
		if time.Now().After(deadline) {
			t.Fatal("runtime owner did not confirm activation")
		}
		time.Sleep(200 * time.Millisecond)
	}
	var active, requestStatus string
	_ = db.QueryRowContext(ctx, `SELECT active_release_id FROM realms WHERE game_id='demo' AND realm_id='main'`).Scan(&active)
	_ = db.QueryRowContext(ctx, `SELECT status FROM release_activation_requests WHERE idempotency_key='activation-key-0001'`).Scan(&requestStatus)
	if active != releaseID || requestStatus != "activated" {
		t.Fatalf("active=%s request=%s", active, requestStatus)
	}

	// 真实 Godot 客户端读取已激活快照的公开白名单包（若本机安装了 Godot）。
	if godot := godotBinary(); godot != "" {
		runGodotProbe(t, ctx, godot, root, db, realm, realm.Loader(), nil)
	}

	// 产物缺失的 Release 激活失败时，旧指针保持不变。
	if _, err := db.ExecContext(ctx, `INSERT INTO releases (game_id, release_id, release_version, content_schema_version, instance_schema_version, ruleset_version, protocol_version, client_min_build, status, manifest)
VALUES ('demo','broken-release',999,1,1,1,1,'0.1.0','built','{"scope":"game_snapshot","private_artifact_sha256":"00"}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RequestActivation(ctx, "demo", "main", actor, "broken-release", "activation-key-0003"); err != nil {
		t.Fatalf("request broken activation: %v", err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		_ = db.QueryRowContext(ctx, `SELECT status FROM release_activation_requests WHERE idempotency_key='activation-key-0003'`).Scan(&requestStatus)
		if requestStatus == "rejected" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("broken activation was not rejected")
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = db.QueryRowContext(ctx, `SELECT active_release_id FROM realms WHERE game_id='demo' AND realm_id='main'`).Scan(&active)
	if active != releaseID || realm.Release().ReleaseID != releaseID {
		t.Fatalf("failed activation changed pointer to %s", active)
	}
}

func containsBytes(data []byte, needle string) bool {
	return len(needle) > 0 && len(data) >= len(needle) && string(data) != "" && indexOf(string(data), needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func godotBinary() string {
	godot := os.Getenv("TBCG_GODOT_BIN")
	if godot == "" {
		godot = "/Applications/Godot.app/Contents/MacOS/Godot"
	}
	if _, err := os.Stat(godot); err != nil {
		return ""
	}
	return godot
}
