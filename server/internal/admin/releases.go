package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"regexp"

	contentassets "github.com/taills/tbcg/server/internal/content/assets"
)

var realmIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// SnapshotPreview 是入队前将被冻结的已批准内容集合。
type SnapshotPreview struct {
	ProfileVersion int64             `json:"profile_version"`
	Entries        []json.RawMessage `json:"entries"`
	IconIDs        []string          `json:"icon_ids"`
}

// ReleaseStore 是完整 Game 快照构建与激活请求的窄端口。
type ReleaseStore interface {
	PreviewSnapshot(ctx context.Context, gameID, actorID string) (SnapshotPreview, error)
	EnqueueSnapshot(ctx context.Context, gameID, actorID, key string, fingerprint []byte, profileVersion int64, assets []contentassets.FrozenSnapshot, schemaFingerprint string) (json.RawMessage, error)
	GetSnapshotJob(ctx context.Context, gameID, actorID, jobID string) (json.RawMessage, error)
	ListReleases(ctx context.Context, gameID, actorID string) (json.RawMessage, error)
	RequestActivation(ctx context.Context, gameID, realmID, actorID, releaseID, key string) (json.RawMessage, error)
}

type releaseHandler struct {
	*buildHandler
	releases ReleaseStore
}

func registerReleaseRoutes(mux *http.ServeMux, api *handler) {
	releases, ok := api.store.(ReleaseStore)
	if !ok {
		return
	}
	routes := &releaseHandler{buildHandler: &buildHandler{handler: api}, releases: releases}
	mux.HandleFunc("POST /api/v1/games/{gameId}/snapshot-builds", routes.startSnapshot)
	mux.HandleFunc("GET /api/v1/games/{gameId}/snapshot-builds/{jobId}", routes.getSnapshot)
	mux.HandleFunc("GET /api/v1/games/{gameId}/releases", routes.listReleases)
	mux.HandleFunc("POST /api/v1/games/{gameId}/realms/{realmId}/activations", routes.requestActivation)
}

func (h *releaseHandler) startSnapshot(w http.ResponseWriter, r *http.Request) {
	if !h.exactOrigin(w, r) || !ensureEmptyBody(w, r) {
		return
	}
	current, ok := h.requestSession(w, r, true)
	if !ok {
		return
	}
	gameID := r.PathValue("gameId")
	if !h.requireRole(w, r, current, gameID, RolePublisher) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if err := validateIdempotencyKey(key); err != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key 必须是 16–128 个安全字符", nil)
		return
	}
	if h.registry == nil {
		writeFailure(w, http.StatusInternalServerError, "configuration_error", "内容校验器暂不可用", nil)
		return
	}
	actorID := current.value.AdminID
	preview, err := h.releases.PreviewSnapshot(r.Context(), gameID, actorID)
	if h.writeBuildStoreError(w, "preview snapshot", err) {
		return
	}
	assets := make([]contentassets.FrozenSnapshot, 0, len(preview.IconIDs))
	for _, iconID := range preview.IconIDs {
		snapshot, err := contentassets.ReadIcon(assetsForGame(h.assets, gameID), iconID)
		if errors.Is(err, contentassets.ErrInvalidAsset) || errors.Is(err, fs.ErrNotExist) {
			log.Printf("admin snapshot asset rejected error=%q", err.Error())
			writeFailure(w, http.StatusConflict, "asset_unavailable", "已批准内容引用的图标缺失、哈希不符或缺少许可记录", nil)
			return
		}
		if err != nil {
			h.internalError(w, "freeze snapshot asset", err)
			return
		}
		assets = append(assets, snapshot.Frozen())
	}
	fingerprint := sha256.Sum256([]byte("game_snapshot\x00" + gameID))
	job, err := h.releases.EnqueueSnapshot(r.Context(), gameID, actorID, key, fingerprint[:], preview.ProfileVersion, assets, h.registry.Fingerprint())
	if h.writeBuildStoreError(w, "enqueue snapshot build", err) {
		return
	}
	writeSuccess(w, http.StatusAccepted, job, nil)
}

func (h *releaseHandler) getSnapshot(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("gameId")
	current, ok := h.requireAuthenticated(w, r)
	if !ok || !h.requireRole(w, r, current, gameID, RolePublisher) {
		return
	}
	jobID := r.PathValue("jobId")
	if !buildJobIDPattern.MatchString(jobID) {
		writeFailure(w, http.StatusBadRequest, "invalid_job_id", "构建任务 ID 无效", nil)
		return
	}
	job, err := h.releases.GetSnapshotJob(r.Context(), gameID, current.value.AdminID, jobID)
	if h.writeBuildStoreError(w, "read snapshot job", err) {
		return
	}
	writeSuccess(w, http.StatusOK, job, nil)
}

func (h *releaseHandler) listReleases(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("gameId")
	current, ok := h.requireAuthenticated(w, r)
	if !ok || !h.requireRole(w, r, current, gameID, readableRoles...) {
		return
	}
	listing, err := h.releases.ListReleases(r.Context(), gameID, current.value.AdminID)
	if h.writeBuildStoreError(w, "list releases", err) {
		return
	}
	writeSuccess(w, http.StatusOK, listing, nil)
}

func (h *releaseHandler) requestActivation(w http.ResponseWriter, r *http.Request) {
	if !h.exactOrigin(w, r) {
		return
	}
	current, ok := h.requestSession(w, r, true)
	if !ok {
		return
	}
	gameID := r.PathValue("gameId")
	if !h.requireRole(w, r, current, gameID, RolePublisher) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if err := validateIdempotencyKey(key); err != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key 必须是 16–128 个安全字符", nil)
		return
	}
	realmID := r.PathValue("realmId")
	var input struct {
		ReleaseID string `json:"release_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !realmIDPattern.MatchString(realmID) || !buildGameIDPattern.MatchString(input.ReleaseID) {
		writeFailure(w, http.StatusBadRequest, "invalid_activation", "必须指定合法的 Realm 与 Release", nil)
		return
	}
	result, err := h.releases.RequestActivation(r.Context(), gameID, realmID, current.value.AdminID, input.ReleaseID, key)
	if errors.Is(err, ErrBuildAlreadyActive) {
		writeFailure(w, http.StatusConflict, "activation_pending", "该 Realm 已有等待 runtime 确认的激活请求", nil)
		return
	}
	if h.writeBuildStoreError(w, "request activation", err) {
		return
	}
	writeSuccess(w, http.StatusAccepted, result, nil)
}
