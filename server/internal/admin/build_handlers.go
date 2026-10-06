package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"strconv"

	contentassets "example.com/tbcg/server/internal/content/assets"
)

var buildJobIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// buildHandler serves Publisher build routes only when the store provides durable build jobs.
type buildHandler struct {
	*handler
	builds BuildStore
}

func registerBuildRoutes(mux *http.ServeMux, api *handler) {
	builds, ok := api.store.(BuildStore)
	if !ok {
		return
	}
	routes := &buildHandler{handler: api, builds: builds}
	mux.HandleFunc("GET /api/v1/games/{gameId}/build-candidates", routes.listCandidates)
	mux.HandleFunc("POST /api/v1/games/{gameId}/builds", routes.startBuild)
	mux.HandleFunc("GET /api/v1/games/{gameId}/builds/{jobId}", routes.getBuildJob)
}

func (h *buildHandler) listCandidates(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("gameId")
	current, ok := h.requireAuthenticated(w, r)
	if !ok || !h.requireRole(w, r, current, gameID, RolePublisher) {
		return
	}
	limit, offset, ok := parsePage(w, r)
	if !ok {
		return
	}
	candidates, total, err := h.builds.ListBuildCandidates(r.Context(), gameID, current.value.AdminID, limit, offset)
	if h.writeBuildStoreError(w, "list build candidates", err) {
		return
	}
	writeSuccess(w, http.StatusOK, candidates, map[string]any{"total": total, "limit": limit, "offset": offset})
}

func (h *buildHandler) startBuild(w http.ResponseWriter, r *http.Request) {
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
	if h.validator == nil {
		writeFailure(w, http.StatusInternalServerError, "configuration_error", "内容校验器暂不可用", nil)
		return
	}
	var input BuildRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	fingerprint, err := buildRequestFingerprint(gameID, input)
	if err != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_build_request", "必须指定已批准的 content_id、整数 revision 和小写 body_hash", nil)
		return
	}
	actorID := current.value.AdminID
	existing, found, err := h.builds.FindBuildJobByKey(r.Context(), gameID, actorID, key, fingerprint)
	if h.writeBuildStoreError(w, "look up build idempotency key", err) {
		return
	}
	if found {
		writeSuccess(w, http.StatusAccepted, existing, nil)
		return
	}
	bodyHash, _ := hex.DecodeString(input.BodyHash)
	preparation, err := h.builds.ReadApprovedBuildInput(r.Context(), gameID, actorID, input.ContentID, input.Revision, bodyHash)
	if h.writeBuildStoreError(w, "read approved build input", err) {
		return
	}
	asset, ok := h.freezeBuildAsset(w, gameID, preparation)
	if !ok {
		return
	}
	job, err := h.builds.EnqueueBuild(r.Context(), gameID, actorID, input, key, fingerprint,
		preparation.Profile.ProfileVersion, preparation.Profile.AssetManifestSHA256, asset, h.validator.Fingerprint())
	if h.writeBuildStoreError(w, "enqueue approved build", err) {
		return
	}
	writeSuccess(w, http.StatusAccepted, job, nil)
}

// freezeBuildAsset reads the approved icon from the trusted Game asset root and pins it to the profile hash.
func (h *buildHandler) freezeBuildAsset(w http.ResponseWriter, gameID string, preparation BuildPreparation) (contentassets.FrozenSnapshot, bool) {
	var definition struct {
		Public struct {
			IconID string `json:"icon_id"`
		} `json:"public"`
	}
	if err := json.Unmarshal(preparation.Definition, &definition); err != nil {
		h.internalError(w, "decode approved build Definition", err)
		return contentassets.FrozenSnapshot{}, false
	}
	snapshot, err := contentassets.ReadIcon(assetsForGame(h.assets, gameID), definition.Public.IconID)
	if errors.Is(err, contentassets.ErrInvalidAsset) || errors.Is(err, fs.ErrNotExist) {
		log.Printf("admin build asset rejected error=%q", err.Error())
		writeFailure(w, http.StatusConflict, "asset_unavailable", "批准内容引用的图标缺失、哈希不符或缺少许可记录", nil)
		return contentassets.FrozenSnapshot{}, false
	}
	if err != nil {
		h.internalError(w, "freeze build asset", err)
		return contentassets.FrozenSnapshot{}, false
	}
	frozen := snapshot.Frozen()
	manifestHash := sha256.Sum256(frozen.Manifest)
	if hex.EncodeToString(manifestHash[:]) != preparation.Profile.AssetManifestSHA256 {
		writeFailure(w, http.StatusConflict, "asset_manifest_mismatch", "素材清单与该游戏受控构建配置不一致", nil)
		return contentassets.FrozenSnapshot{}, false
	}
	return frozen, true
}

func (h *buildHandler) getBuildJob(w http.ResponseWriter, r *http.Request) {
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
	job, err := h.builds.GetBuildJob(r.Context(), gameID, current.value.AdminID, jobID)
	if h.writeBuildStoreError(w, "read build job", err) {
		return
	}
	writeSuccess(w, http.StatusOK, job, nil)
}

// writeBuildStoreError maps store errors to stable codes; it returns true when a response was written.
func (h *buildHandler) writeBuildStoreError(w http.ResponseWriter, action string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrBuildOutcomeUnknown):
		writeFailure(w, http.StatusServiceUnavailable, "build_outcome_unknown", "构建请求结果未确认，请使用同一 Idempotency-Key 重试", nil)
	case errors.Is(err, ErrForbidden):
		writeFailure(w, http.StatusForbidden, "forbidden", "当前账号没有此游戏的 Publisher 权限", nil)
	case errors.Is(err, ErrNotFound):
		writeFailure(w, http.StatusNotFound, "not_found", "找不到已批准的确切 revision 或构建任务", nil)
	case errors.Is(err, ErrBuildAlreadyActive):
		writeFailure(w, http.StatusConflict, "build_already_active", "该 revision 已有排队或进行中的构建任务", nil)
	case errors.Is(err, ErrBuildQueueFull):
		writeFailure(w, http.StatusTooManyRequests, "build_queue_full", "该游戏的构建队列已满，请稍后重试", nil)
	case errors.Is(err, ErrBuildProfileNotConfigured):
		writeFailure(w, http.StatusConflict, "build_profile_unavailable", "该游戏缺少受控构建配置或 Schema 版本不兼容", nil)
	case errors.Is(err, ErrInvalidBuildRequest):
		writeFailure(w, http.StatusConflict, "build_input_rejected", "冻结的构建输入未通过服务端校验", nil)
	case errors.Is(err, ErrConflict) && action == "look up build idempotency key":
		writeFailure(w, http.StatusConflict, "idempotency_conflict", "该 Idempotency-Key 已绑定其他构建请求", nil)
	case errors.Is(err, ErrConflict):
		writeFailure(w, http.StatusConflict, "build_conflict", "构建配置或幂等键在提交期间发生变化，请重新读取后重试", nil)
	default:
		h.internalError(w, action, err)
	}
	return true
}

func parsePage(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, offset := 50, 0
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeFailure(w, http.StatusBadRequest, "invalid_limit", "分页大小必须介于 1 和 100 之间", nil)
			return 0, 0, false
		}
		limit = parsed
	}
	if value := r.URL.Query().Get("offset"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > 1000000 {
			writeFailure(w, http.StatusBadRequest, "invalid_offset", "分页偏移必须介于 0 和 1000000 之间", nil)
			return 0, 0, false
		}
		offset = parsed
	}
	return limit, offset, true
}
