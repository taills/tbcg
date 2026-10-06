package runtime

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

var releaseIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// HTTPAPI 提供玩家账号、Realm 信息与公开白名单包下载；私有包永不经 HTTP 暴露。
type HTTPAPI struct {
	Realm     *Realm
	Auth      *Auth
	Loader    *ReleaseLoader
	DevBundle []byte
	limiter   sync.Map
	entries   atomic.Uint64
}

type limitEntry struct {
	mu    sync.Mutex
	start time.Time
	count int
}

func (api *HTTPAPI) allow(r *http.Request, bucket string, max int) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if api.entries.Add(1)%1024 == 0 {
		api.limiter.Range(func(key, value any) bool {
			entry := value.(*limitEntry)
			entry.mu.Lock()
			stale := time.Since(entry.start) > 2*time.Minute
			entry.mu.Unlock()
			if stale {
				api.limiter.Delete(key)
			}
			return true
		})
	}
	value, _ := api.limiter.LoadOrStore(bucket+"|"+host, &limitEntry{start: time.Now()})
	entry := value.(*limitEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if time.Since(entry.start) > time.Minute {
		entry.start, entry.count = time.Now(), 0
	}
	entry.count++
	return entry.count <= max
}

// Handler 返回 HTTP 路由。
func (api *HTTPAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/accounts", api.register)
	mux.HandleFunc("POST /api/v1/sessions", api.login)
	mux.HandleFunc("GET /api/v1/realm", api.realm)
	mux.HandleFunc("GET /api/v1/releases/{releaseId}/public", api.publicBundle)
	return http.MaxBytesHandler(mux, 16<<10)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func failJSON(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"success": false, "data": nil, "error": map[string]string{"code": code, "message": message}})
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func decodeCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var input credentials
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF {
		failJSON(w, http.StatusBadRequest, "invalid_request", "请求体必须是包含 username/password 的 JSON")
		return credentials{}, false
	}
	return input, true
}

func (api *HTTPAPI) register(w http.ResponseWriter, r *http.Request) {
	if !api.allow(r, "register", 10) {
		failJSON(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁")
		return
	}
	input, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	accountID, err := api.Auth.Register(r.Context(), input.Username, input.Password)
	var protocolErr *ProtocolError
	switch {
	case errors.As(err, &protocolErr):
		failJSON(w, http.StatusBadRequest, protocolErr.Code, protocolErr.Message)
	case errors.Is(err, ErrAccountExists):
		failJSON(w, http.StatusConflict, "account_exists", "用户名已被占用")
	case err != nil:
		log.Printf("register player: %v", err)
		failJSON(w, http.StatusInternalServerError, "internal_error", "注册失败")
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"success": true, "data": map[string]string{"account_id": accountID}, "error": nil})
	}
}

func (api *HTTPAPI) login(w http.ResponseWriter, r *http.Request) {
	if !api.allow(r, "login", 20) {
		failJSON(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁")
		return
	}
	input, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	token, expires, err := api.Auth.Login(r.Context(), input.Username, input.Password)
	if errors.Is(err, ErrUnauthenticated) {
		failJSON(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	if err != nil {
		log.Printf("player login: %v", err)
		failJSON(w, http.StatusInternalServerError, "internal_error", "登录失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": map[string]any{"session_token": token, "expires_at": expires.UTC().Format(time.RFC3339)}, "error": nil})
}

func (api *HTTPAPI) realm(w http.ResponseWriter, _ *http.Request) {
	release := api.Realm.Release()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "error": nil, "data": map[string]any{
		"game_id": api.Realm.GameID, "realm_id": api.Realm.RealmID, "protocol_version": ProtocolVersion,
		"release_id": release.ReleaseID, "release_version": release.ReleaseVersion, "public_artifact_sha256": release.PublicSHA256,
		"dev_local": release.DevLocal,
	}})
}

// publicBundle 只返回当前服务中 Release 的公开白名单包（字节与 manifest 中的哈希一致）。
func (api *HTTPAPI) publicBundle(w http.ResponseWriter, r *http.Request) {
	if !api.allow(r, "bundle", 120) {
		failJSON(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁")
		return
	}
	releaseID := r.PathValue("releaseId")
	current := api.Realm.Release()
	if !releaseIDPattern.MatchString(releaseID) || releaseID != current.ReleaseID {
		failJSON(w, http.StatusNotFound, "not_found", "该 Release 不是当前服务中的公开包")
		return
	}
	var data []byte
	if current.DevLocal {
		data = api.DevBundle
	} else {
		var err error
		data, err = api.Loader.Store.Read(api.Realm.GameID, releaseID, "public")
		if err != nil || sha256Hex(data) != current.PublicSHA256 {
			log.Printf("public bundle unavailable for the serving release")
			failJSON(w, http.StatusServiceUnavailable, "bundle_unavailable", "公开包暂不可用")
			return
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-TBCG-Bundle-SHA256", current.PublicSHA256)
	w.Header().Set("Cache-Control", "public, max-age=300, immutable")
	_, _ = w.Write(data) // #nosec G705 -- 以 application/json + nosniff 返回哈希校验过的公开包字节。
}
