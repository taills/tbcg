package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"

	contentbuild "github.com/taills/tbcg/server/internal/content/build"
	"github.com/taills/tbcg/server/internal/content/catalog"
	"github.com/taills/tbcg/server/internal/content/mapdata"
	"github.com/taills/tbcg/server/internal/content/schema"
)

// ReleaseLoader 只读取私有产物存储中的已构建快照，并用 runtime 自身的 Schema 再次校验。
type ReleaseLoader struct {
	Store    *contentbuild.ArtifactStore
	Registry *schema.Registry
	Geometry *mapdata.Validator
	DevDir   fs.FS
}

type releaseRow struct {
	ReleaseID      string
	ReleaseVersion int
	Status         string
	Ruleset        int
	Protocol       int
	Instance       int
	ContentSchema  int
	Manifest       contentbuild.SnapshotManifest
}

// Load 校验并加载一个快照 Release；失败不影响当前服务内容。
func (l *ReleaseLoader) Load(ctx context.Context, db *sql.DB, gameID, releaseID string) (*catalog.Catalog, ReleaseInfo, error) {
	var row releaseRow
	var manifest []byte
	err := db.QueryRowContext(ctx, `SELECT release_id, release_version, status, ruleset_version, protocol_version, instance_schema_version, content_schema_version, manifest
FROM releases WHERE game_id=$1 AND release_id=$2`, gameID, releaseID).Scan(&row.ReleaseID, &row.ReleaseVersion, &row.Status, &row.Ruleset, &row.Protocol, &row.Instance, &row.ContentSchema, &manifest)
	if err != nil {
		return nil, ReleaseInfo{}, fmt.Errorf("read release row: %w", err)
	}
	if err := json.Unmarshal(manifest, &row.Manifest); err != nil {
		return nil, ReleaseInfo{}, fmt.Errorf("decode release manifest: %w", err)
	}
	if row.Status != "built" && row.Status != "activated" && row.Status != "deprecated" {
		return nil, ReleaseInfo{}, errors.New("release_not_snapshot: unexpected status")
	}
	if row.Manifest.Scope != contentbuild.SnapshotScope {
		return nil, ReleaseInfo{}, errors.New("release_not_snapshot")
	}
	if row.Ruleset != RulesetVersion || row.Protocol != ProtocolVersion || row.Instance != InstanceSchemaVersion || row.ContentSchema != ContentSchemaVersion {
		return nil, ReleaseInfo{}, errors.New("release_incompatible")
	}
	if l.Store == nil {
		return nil, ReleaseInfo{}, errors.New("artifact_store_unavailable")
	}
	private, err := l.Store.Read(gameID, releaseID, "private")
	if err != nil {
		return nil, ReleaseInfo{}, fmt.Errorf("artifact_missing: %w", err)
	}
	entries, err := contentbuild.DecodePrivateSnapshot(private, row.Manifest.PrivateArtifactSHA256, gameID, releaseID)
	if err != nil {
		return nil, ReleaseInfo{}, fmt.Errorf("artifact_invalid: %w", err)
	}
	content, issues := catalog.Load(gameID, entries, l.Registry, l.Geometry)
	if len(issues) != 0 {
		return nil, ReleaseInfo{}, fmt.Errorf("content_invalid: %s", issues[0].Error())
	}
	if len(content.Archetypes) == 0 || len(content.Maps) == 0 {
		return nil, ReleaseInfo{}, errors.New("content_not_playable")
	}
	content.ReleaseID, content.ReleaseVersion = releaseID, row.ReleaseVersion
	return content, ReleaseInfo{ReleaseID: releaseID, ReleaseVersion: row.ReleaseVersion, PublicSHA256: row.Manifest.PublicArtifactSHA256}, nil
}

// LoadDev 从本地内容目录加载开发 Release（明确标记 dev_local，不写数据库指针）。
func (l *ReleaseLoader) LoadDev(gameID string) (*catalog.Catalog, ReleaseInfo, []byte, error) {
	entries, err := catalog.ReadDir(l.DevDir)
	if err != nil {
		return nil, ReleaseInfo{}, nil, err
	}
	content, issues := catalog.Load(gameID, entries, l.Registry, l.Geometry)
	if len(issues) != 0 {
		return nil, ReleaseInfo{}, nil, fmt.Errorf("dev content invalid: %s", issues[0].Error())
	}
	public, err := catalog.PublicProjection(l.Registry, entries)
	if err != nil {
		return nil, ReleaseInfo{}, nil, err
	}
	bundle, err := json.Marshal(contentbuild.PublicSnapshot{FormatVersion: 1, Scope: "public", GameID: gameID, ReleaseID: "dev-local", ReleaseVersion: 0, Entries: public, Assets: nil})
	if err != nil {
		return nil, ReleaseInfo{}, nil, err
	}
	content.ReleaseID = "dev-local"
	return content, ReleaseInfo{ReleaseID: "dev-local", DevLocal: true, PublicSHA256: sha256Hex(bundle)}, bundle, nil
}

// LoadActive 在启动时加载 Realm 当前活动指针。
func (r *Realm) LoadActive(ctx context.Context) error {
	var active sql.NullString
	if err := r.DB.QueryRowContext(ctx, `SELECT active_release_id FROM realms WHERE game_id=$1 AND realm_id=$2`, r.GameID, r.RealmID).Scan(&active); err != nil {
		return fmt.Errorf("read active release pointer: %w", err)
	}
	if !active.Valid {
		return nil
	}
	content, info, err := r.loader.Load(ctx, r.DB, r.GameID, active.String)
	if err != nil {
		return fmt.Errorf("load active release %s: %w", active.String, err)
	}
	r.setContent(content, info)
	return nil
}

// UseDevContent 设置开发内容（仅当没有活动 Release 时）。
func (r *Realm) UseDevContent(content *catalog.Catalog, info ReleaseInfo) {
	if r.content == nil {
		r.setContent(content, info)
	}
}

// processActivation 处理一个待确认激活请求：校验成功后在 owner 守卫下切换指针，失败保持旧指针。
func (r *Realm) processActivation(ctx context.Context) {
	var requestID, releaseID string
	err := r.DB.QueryRowContext(ctx, `SELECT request_id::text, release_id FROM release_activation_requests
WHERE game_id=$1 AND realm_id=$2 AND status='pending' ORDER BY created_at LIMIT 1`, r.GameID, r.RealmID).Scan(&requestID, &releaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		log.Printf("read activation request: %v", err)
		return
	}
	content, info, loadErr := r.loader.Load(ctx, r.DB, r.GameID, releaseID)
	if loadErr != nil {
		code := classifyActivationError(loadErr)
		if transientActivationError(code) {
			// 暂态故障（如私有产物存储不可用）不落成拒绝，下一轮重试。
			log.Printf("activation deferred request=%s code=%s", requestID, code)
			return
		}
		log.Printf("activation rejected request=%s release=%s code=%s", requestID, releaseID, code)
		err := r.Tx(ctx, func(tx *Tx) error {
			if pending, err := lockPendingActivation(tx, requestID); err != nil || !pending {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE release_activation_requests SET status='rejected', error_code=$2, decided_at=now(), owner_epoch=$3
WHERE request_id=$1::uuid AND status='pending'`, requestID, code, r.Epoch)
			return err
		})
		if err != nil {
			log.Printf("record activation rejection: %v", err)
		}
		return
	}
	previous := r.Release().ReleaseID
	err = r.Tx(ctx, func(tx *Tx) error {
		pending, err := lockPendingActivation(tx, requestID)
		if err != nil || !pending {
			if err == nil {
				err = errors.New("activation request is no longer pending")
			}
			return err
		}
		var current sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT active_release_id FROM realms WHERE game_id=$1 AND realm_id=$2 FOR NO KEY UPDATE`, r.GameID, r.RealmID).Scan(&current); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE realms SET active_release_id=$3 WHERE game_id=$1 AND realm_id=$2`, r.GameID, r.RealmID, releaseID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE releases SET status='activated', activated_at=COALESCE(activated_at, now()) WHERE game_id=$1 AND release_id=$2`, r.GameID, releaseID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE release_activation_requests SET status='activated', decided_at=now(), previous_release_id=$2, owner_epoch=$3
WHERE request_id=$1::uuid AND status='pending'`, requestID, nullable(current.String), r.Epoch)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows != 1 {
			return errors.New("activation request changed concurrently")
		}
		return nil
	})
	if err != nil {
		// 提交失败或结果未知：内存仍服务旧 Release；下一轮重新读取请求状态再决定。
		log.Printf("activation commit failed request=%s: %v", requestID, err)
		return
	}
	r.setContent(content, info)
	log.Printf("release activated realm=%s release=%s previous=%s", r.RealmID, releaseID, previous)
	r.BroadcastAll("system.release_changed", map[string]any{"release_id": info.ReleaseID, "release_version": info.ReleaseVersion, "public_artifact_sha256": info.PublicSHA256})
}

// lockPendingActivation 在 owner 守卫事务内锁定请求行；返回 false 表示已被处理。
func lockPendingActivation(tx *Tx, requestID string) (bool, error) {
	var status string
	err := tx.QueryRowContext(tx.Ctx, `SELECT status FROM release_activation_requests WHERE request_id=$1::uuid FOR UPDATE`, requestID).Scan(&status)
	if err != nil {
		return false, err
	}
	return status == "pending", nil
}

func transientActivationError(code string) bool {
	return code == "artifact_store_unavailable" || code == "release_unreadable"
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func classifyActivationError(err error) string {
	message := err.Error()
	for _, code := range []string{"release_not_snapshot", "release_incompatible", "artifact_store_unavailable", "artifact_missing", "artifact_invalid", "content_invalid", "content_not_playable"} {
		if len(message) >= len(code) && message[:len(code)] == code {
			return code
		}
	}
	return "release_unreadable"
}

// CatalogFor 返回指定 Release 的内容（实例固定其创建时的 Release；旧版本按需从私有产物加载并缓存）。
func (r *Realm) CatalogFor(ctx context.Context, releaseID string) (*catalog.Catalog, error) {
	if r.content != nil && r.content.ReleaseID == releaseID {
		return r.content, nil
	}
	cache := r.Module("release_cache", func() any { return map[string]*catalog.Catalog{} }).(map[string]*catalog.Catalog)
	if cached := cache[releaseID]; cached != nil {
		return cached, nil
	}
	if releaseID == "dev-local" {
		return nil, errors.New("dev-local release is no longer loaded")
	}
	content, _, err := r.loader.Load(ctx, r.DB, r.GameID, releaseID)
	if err != nil {
		return nil, err
	}
	cache[releaseID] = content
	return content, nil
}

// Loader 返回 Realm 使用的 Release 加载器（只读）。
func (r *Realm) Loader() *ReleaseLoader { return r.loader }
