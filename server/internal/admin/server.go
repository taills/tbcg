package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/taills/tbcg/server/internal/content/mapdata"
	"github.com/taills/tbcg/server/internal/content/schema"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookieName = "tbcg_admin_session"
	csrfCookieName    = "tbcg_admin_csrf"
	defaultBodyLimit  = 1 << 20
	minimumBcryptCost = 12
)

type Config struct {
	ExpectedOrigin         string
	SessionTTL             time.Duration
	IdleTTL                time.Duration
	MaxBodyBytes           int64
	RequestsPerIPPerMinute int
	LoginsPerIPPerMinute   int
	LoginsPerAccountMinute int
}

type handler struct {
	store     Store
	config    Config
	validator *schema.Validator
	registry  *schema.Registry
	extras    Extensions
	assets    fs.FS
	limiter   *requestLimiter
	dummyHash []byte
}

type sessionData struct {
	Authenticated bool         `json:"authenticated"`
	CSRFToken     string       `json:"csrf_token"`
	User          *sessionUser `json:"user"`
	GameRoles     []GameRole   `json:"game_roles"`
}

type sessionUser struct {
	AdminID  string `json:"admin_id"`
	Username string `json:"username"`
}

type apiError struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Fields  []fieldIssue `json:"fields"`
}

type fieldIssue struct {
	Pointer string `json:"pointer"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	Success bool           `json:"success"`
	Data    any            `json:"data"`
	Error   *apiError      `json:"error"`
	Meta    map[string]any `json:"meta"`
}

type requestSession struct {
	value     Session
	tokenHash []byte
	csrfToken string
}

const (
	maxLimiterKeys    = 10000
	limiterPruneBatch = 256
)

type requestLimiter struct {
	mu         sync.Mutex
	windows    map[string]limitWindow
	maxKeys    int
	pruneBatch int
}

type limitWindow struct {
	start time.Time
	count int
}

// NewHandler creates the management API. The server process must supply a restricted PostgreSQL role.
func NewHandler(store Store, config Config, validator *schema.Validator, assets fs.FS) http.Handler {
	return newHandler(store, config, validator, nil, assets, Extensions{})
}

// Extensions 为全内容类型编辑、快照构建与预览提供可选依赖。
type Extensions struct {
	// Preview 使用与 runtime 相同的 Go 规则模拟内容，nil 时预览路由返回 503。
	Preview PreviewRunner
	// Geometry 用于全量交叉引用校验中的地图几何解码。
	Geometry *mapdata.Validator
}

// NewContentHandler 创建支持全部内容类型的管理 API。
func NewContentHandler(store Store, config Config, registry *schema.Registry, assets fs.FS, extras Extensions) http.Handler {
	if registry == nil {
		return newHandler(store, config, nil, nil, assets, extras)
	}
	return newHandler(store, config, registry.Item(), registry, assets, extras)
}

func newHandler(store Store, config Config, validator *schema.Validator, registry *schema.Registry, assets fs.FS, extras Extensions) http.Handler {
	config = normalizeConfig(config)
	_, err := parseExpectedOrigin(config)
	if err != nil || store == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeFailure(w, http.StatusInternalServerError, "configuration_error", "管理服务配置无效", nil)
		})
	}
	dummyHash, err := bcrypt.GenerateFromPassword([]byte("tbcg-authentication-dummy"), minimumBcryptCost)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeFailure(w, http.StatusInternalServerError, "configuration_error", "管理服务配置无效", nil)
		})
	}
	api := &handler{
		store:     store,
		config:    config,
		validator: validator,
		registry:  registry,
		extras:    extras,
		assets:    assets,
		limiter:   &requestLimiter{windows: make(map[string]limitWindow)},
		dummyHash: dummyHash,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/session", api.getSession)
	mux.HandleFunc("POST /api/v1/login", api.login)
	mux.HandleFunc("POST /api/v1/logout", api.logout)
	mux.HandleFunc("POST /api/v1/games/{gameId}/item-drafts", api.createDraft)
	mux.HandleFunc("GET /api/v1/games/{gameId}/item-drafts", api.listDrafts)
	mux.HandleFunc("GET /api/v1/games/{gameId}/item-drafts/{draftId}", api.getDraft)
	mux.HandleFunc("PUT /api/v1/games/{gameId}/item-drafts/{draftId}", api.updateDraft)
	mux.HandleFunc("POST /api/v1/games/{gameId}/item-drafts/{draftId}/validate", api.validateDraft)
	mux.HandleFunc("POST /api/v1/games/{gameId}/item-drafts/{draftId}/submit", api.submitDraft)
	mux.HandleFunc("POST /api/v1/games/{gameId}/content-drafts", api.createDraft)
	mux.HandleFunc("GET /api/v1/games/{gameId}/content-drafts", api.listDrafts)
	mux.HandleFunc("GET /api/v1/games/{gameId}/content-drafts/{draftId}", api.getDraft)
	mux.HandleFunc("PUT /api/v1/games/{gameId}/content-drafts/{draftId}", api.updateDraft)
	mux.HandleFunc("POST /api/v1/games/{gameId}/content-drafts/{draftId}/validate", api.validateDraft)
	mux.HandleFunc("POST /api/v1/games/{gameId}/content-drafts/{draftId}/submit", api.submitDraft)
	mux.HandleFunc("GET /api/v1/games/{gameId}/review-queue", api.reviewQueue)
	mux.HandleFunc("POST /api/v1/games/{gameId}/revision-approvals", api.reviewRevision)
	registerBuildRoutes(mux, api)
	registerReleaseRoutes(mux, api)
	registerContentRoutes(mux, api)
	mux.HandleFunc("/", api.notFound)
	return api.middleware(mux)
}

func normalizeConfig(config Config) Config {
	if config.SessionTTL <= 0 {
		config.SessionTTL = 12 * time.Hour
	}
	if config.IdleTTL <= 0 {
		config.IdleTTL = 30 * time.Minute
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = defaultBodyLimit
	}
	if config.RequestsPerIPPerMinute <= 0 {
		config.RequestsPerIPPerMinute = 120
	}
	if config.LoginsPerIPPerMinute <= 0 {
		config.LoginsPerIPPerMinute = 10
	}
	if config.LoginsPerAccountMinute <= 0 {
		config.LoginsPerAccountMinute = 5
	}
	return config
}

func parseExpectedOrigin(config Config) (*url.URL, error) {
	origin, err := url.Parse(config.ExpectedOrigin)
	if err != nil || origin == nil || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("expected origin must be an exact HTTP origin")
	}
	if origin.Scheme != "https" {
		return nil, errors.New("management origin must use TLS")
	}
	if config.IdleTTL > config.SessionTTL {
		return nil, errors.New("idle session expiry cannot exceed absolute session expiry")
	}
	return origin, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (h *handler) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !h.limiter.Allow("ip:"+clientIP(r), h.config.RequestsPerIPPerMinute, time.Minute, time.Now()) {
			writeFailure(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后重试", nil)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, h.config.MaxBodyBytes)
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (l *requestLimiter) Allow(key string, max int, window time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windows == nil {
		l.windows = make(map[string]limitWindow)
	}
	capacity := l.maxKeys
	if capacity <= 0 {
		capacity = maxLimiterKeys
	}
	pruneBatch := l.pruneBatch
	if pruneBatch <= 0 {
		pruneBatch = limiterPruneBatch
	}
	current, exists := l.windows[key]
	if exists && now.Sub(current.start) < window && !now.Before(current.start) {
		if current.count >= max {
			return false
		}
		current.count++
		l.windows[key] = current
		return true
	}
	if !exists && len(l.windows) >= capacity {
		removed := 0
		for candidate, value := range l.windows {
			if now.Sub(value.start) >= window || now.Before(value.start) {
				delete(l.windows, candidate)
				removed++
				if removed >= pruneBatch {
					break
				}
			}
		}
		if len(l.windows) >= capacity {
			return false
		}
	}
	l.windows[key] = limitWindow{start: now, count: 1}
	return true
}

func loginAccountLimitKey(ip, username string) string {
	return "login-account:" + strconv.Itoa(len(ip)) + ":" + ip + ":" + strings.ToLower(username)
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (h *handler) getSession(w http.ResponseWriter, r *http.Request) {
	current, err := h.loadRequestSession(r)
	if errors.Is(err, ErrNotFound) {
		current, err = h.createAnonymousSession(w, r.Context())
	}
	if err != nil {
		h.internalError(w, "load admin session", err)
		return
	}
	csrfToken := current.csrfToken
	if csrfToken == "" {
		csrfToken, err = h.sessionCSRF(w, r, current)
		if err != nil {
			h.internalError(w, "refresh admin CSRF", err)
			return
		}
	}
	if err := h.touchSession(r.Context(), current); err != nil {
		h.internalError(w, "touch admin session", err)
		return
	}
	writeSuccess(w, http.StatusOK, h.sessionData(current.value, csrfToken), nil)
}

func (h *handler) createAnonymousSession(w http.ResponseWriter, ctx context.Context) (requestSession, error) {
	token, tokenHash, err := randomToken()
	if err != nil {
		return requestSession{}, fmt.Errorf("generate anonymous session token: %w", err)
	}
	csrfToken, csrfHash, err := randomToken()
	if err != nil {
		return requestSession{}, fmt.Errorf("generate anonymous CSRF token: %w", err)
	}
	now := time.Now().UTC()
	expiresAt := now.Add(h.config.SessionTTL)
	idleExpiresAt := minTime(now.Add(h.config.IdleTTL), expiresAt)
	if err := h.store.CreateSession(ctx, tokenHash, csrfHash, expiresAt, idleExpiresAt); err != nil {
		return requestSession{}, fmt.Errorf("persist anonymous session: %w", err)
	}
	value := Session{CSRFHash: csrfHash, ExpiresAt: expiresAt, IdleExpiresAt: idleExpiresAt}
	h.setCookies(w, token, csrfToken, expiresAt, idleExpiresAt)
	return requestSession{value: value, tokenHash: tokenHash, csrfToken: csrfToken}, nil
}

func (h *handler) sessionData(session Session, csrfToken string) sessionData {
	roles := make([]GameRole, len(session.Roles))
	for i, role := range session.Roles {
		roles[i] = GameRole{GameID: role.GameID, Roles: append([]string{}, role.Roles...)}
	}
	var user *sessionUser
	if session.AdminID != "" {
		user = &sessionUser{AdminID: session.AdminID, Username: session.Username}
	}
	return sessionData{
		Authenticated: user != nil,
		CSRFToken:     csrfToken,
		User:          user,
		GameRoles:     roles,
	}
}

func (h *handler) sessionCSRF(w http.ResponseWriter, r *http.Request, current requestSession) (string, error) {
	cookie, err := r.Cookie(csrfCookieName)
	if err == nil {
		token, decodeErr := base64.RawURLEncoding.DecodeString(cookie.Value)
		if decodeErr == nil && len(token) == sha256.Size {
			hash := sha256.Sum256(token)
			if subtle.ConstantTimeCompare(hash[:], current.value.CSRFHash) == 1 {
				h.setCSRFCookie(w, cookie.Value, minTime(time.Now().Add(h.config.IdleTTL), current.value.ExpiresAt))
				return cookie.Value, nil
			}
		}
	}
	csrfToken, csrfHash, err := randomToken()
	if err != nil {
		return "", fmt.Errorf("generate replacement CSRF token: %w", err)
	}
	if err := h.store.UpdateCSRF(r.Context(), current.tokenHash, csrfHash); err != nil {
		return "", fmt.Errorf("persist replacement CSRF hash: %w", err)
	}
	current.value.CSRFHash = csrfHash
	h.setCSRFCookie(w, csrfToken, minTime(time.Now().Add(h.config.IdleTTL), current.value.ExpiresAt))
	return csrfToken, nil
}

func (h *handler) touchSession(ctx context.Context, current requestSession) error {
	idleExpiresAt := minTime(time.Now().UTC().Add(h.config.IdleTTL), current.value.ExpiresAt)
	if err := h.store.TouchSession(ctx, current.tokenHash, idleExpiresAt); err != nil {
		return fmt.Errorf("update admin idle expiry: %w", err)
	}
	return nil
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.exactOrigin(w, r) {
		return
	}
	current, ok := h.requestAnySession(w, r, true)
	if !ok {
		return
	}
	if current.value.AdminID != "" {
		writeFailure(w, http.StatusConflict, "already_authenticated", "当前会话已登录", nil)
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	loginIPKey := "login-ip:" + clientIP(r)
	if !h.limiter.Allow(loginIPKey, h.config.LoginsPerIPPerMinute, time.Minute, time.Now()) {
		h.rateLimitedLogin(w, r)
		return
	}
	username := strings.TrimSpace(input.Username)
	if len(username) == 0 || len(username) > 128 || strings.IndexFunc(username, unicode.IsControl) >= 0 {
		h.failedLogin(w, r, "")
		return
	}
	accountKey := loginAccountLimitKey(clientIP(r), username)
	if !h.limiter.Allow(accountKey, h.config.LoginsPerAccountMinute, time.Minute, time.Now()) {
		h.rateLimitedLogin(w, r)
		return
	}
	user, userErr := h.store.Authenticate(r.Context(), username)
	if userErr != nil && !errors.Is(userErr, ErrNotFound) {
		h.internalError(w, "authenticate admin account", userErr)
		return
	}
	if !verifyPasswordCandidate(userErr == nil, []byte(user.Hash), []byte(input.Password), h.dummyHash, bcrypt.CompareHashAndPassword) {
		h.failedLogin(w, r, "")
		return
	}
	newSessionToken, newSessionHash, err := randomToken()
	if err != nil {
		h.internalError(w, "rotate admin session", err)
		return
	}
	csrfToken, csrfHash, err := randomToken()
	if err != nil {
		h.internalError(w, "rotate admin csrf", err)
		return
	}
	now := time.Now().UTC()
	expiresAt := now.Add(h.config.SessionTTL)
	idleExpiresAt := minTime(now.Add(h.config.IdleTTL), expiresAt)
	if err := h.store.RotateSession(r.Context(), current.tokenHash, newSessionHash, csrfHash, expiresAt, idleExpiresAt, user.ID); err != nil {
		h.internalError(w, "rotate admin session in database", err)
		return
	}
	authenticatedSession, err := h.store.LoadSession(r.Context(), newSessionHash)
	if err != nil {
		h.internalError(w, "load rotated admin session roles", err)
		return
	}
	current.value = authenticatedSession
	current.tokenHash = newSessionHash
	h.setCookies(w, newSessionToken, csrfToken, expiresAt, idleExpiresAt)
	writeSuccess(w, http.StatusOK, h.sessionData(current.value, csrfToken), nil)
}

func verifyPasswordCandidate(userFound bool, storedHash, password, dummyHash []byte, compare func([]byte, []byte) error) bool {
	passwordValid := validatePasswordBytes(string(password)) == nil
	comparisonPassword := password
	if !passwordValid {
		comparisonPassword = []byte("invalid-password")
	}

	if userFound && passwordValid {
		cost, err := bcrypt.Cost(storedHash)
		if err == nil && cost == minimumBcryptCost {
			return compare(storedHash, comparisonPassword) == nil
		}
	}
	_ = compare(dummyHash, comparisonPassword)
	return false
}

func validatePasswordBytes(password string) error {
	if len(password) == 0 {
		return errors.New("password is required")
	}
	if len([]byte(password)) > 72 {
		return errors.New("password exceeds bcrypt's 72-byte limit")
	}
	return nil
}

func (h *handler) rateLimitedLogin(w http.ResponseWriter, r *http.Request) {
	if err := h.store.WriteAudit(r.Context(), AuditEvent{Action: "admin.login", Resource: "session", ResourceID: "login", Outcome: "denied", DetailsJSON: []byte(`{"reason":"rate_limited"}`)}); err != nil {
		h.internalError(w, "audit rate-limited login", err)
		return
	}
	writeFailure(w, http.StatusTooManyRequests, "rate_limited", "登录尝试过于频繁，请稍后重试", nil)
}

func (h *handler) failedLogin(w http.ResponseWriter, r *http.Request, gameID string) {
	if err := h.store.WriteAudit(r.Context(), AuditEvent{
		GameID: gameID, Action: "admin.login", Resource: "session", ResourceID: "login",
		Outcome: "denied", DetailsJSON: []byte(`{"reason":"credentials_rejected"}`),
	}); err != nil {
		log.Printf("admin audit write failed action=admin.login error=%v", err)
		writeFailure(w, http.StatusInternalServerError, "internal_error", "服务暂时不可用", nil)
		return
	}
	writeFailure(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误", nil)
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if !h.exactOrigin(w, r) || !ensureEmptyBody(w, r) {
		return
	}
	current, ok := h.requestSession(w, r, true)
	if !ok {
		return
	}
	if err := h.store.RevokeSession(r.Context(), current.tokenHash); err != nil {
		h.internalError(w, "revoke admin session", err)
		return
	}
	h.clearCookies(w)
	writeSuccess(w, http.StatusOK, map[string]any{}, nil)
}

func (h *handler) exactOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || origin != h.config.ExpectedOrigin {
		if err := h.store.WriteAudit(r.Context(), AuditEvent{
			Action: "origin.rejected", Resource: "admin_request", ResourceID: "request",
			Outcome: "denied", DetailsJSON: []byte(`{}`),
		}); err != nil {
			h.internalError(w, "audit rejected Origin", err)
			return false
		}
		writeFailure(w, http.StatusForbidden, "invalid_origin", "请求来源不被允许", nil)
		return false
	}
	return true
}

func (h *handler) requestSession(w http.ResponseWriter, r *http.Request, requireCSRF bool) (requestSession, bool) {
	current, ok := h.requestAnySession(w, r, requireCSRF)
	if !ok {
		return requestSession{}, false
	}
	if current.value.AdminID == "" {
		if err := h.store.WriteAudit(r.Context(), AuditEvent{Action: "admin.unauthenticated", Resource: "session", ResourceID: "request", Outcome: "denied", DetailsJSON: []byte(`{}`)}); err != nil {
			h.internalError(w, "audit unauthenticated admin request", err)
			return requestSession{}, false
		}
		writeFailure(w, http.StatusUnauthorized, "unauthorized", "需要登录管理账号", nil)
		return requestSession{}, false
	}
	return current, true
}

func (h *handler) requestAnySession(w http.ResponseWriter, r *http.Request, requireCSRF bool) (requestSession, bool) {
	current, err := h.loadRequestSession(r)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			h.internalError(w, "load admin session", err)
			return requestSession{}, false
		}
		if auditErr := h.store.WriteAudit(r.Context(), AuditEvent{Action: "admin.unauthenticated", Resource: "session", ResourceID: "request", Outcome: "denied", DetailsJSON: []byte(`{}`)}); auditErr != nil {
			h.internalError(w, "audit unauthenticated admin request", auditErr)
			return requestSession{}, false
		}
		writeFailure(w, http.StatusUnauthorized, "unauthorized", "需要建立安全会话后重试", nil)
		return requestSession{}, false
	}
	if requireCSRF && !h.validCSRF(r, current.value.CSRFHash) {
		if err := h.auditDenied(r.Context(), current.value.AdminID, "", "csrf.rejected"); err != nil {
			h.internalError(w, "audit csrf rejection", err)
			return requestSession{}, false
		}
		writeFailure(w, http.StatusForbidden, "csrf_rejected", "请求校验失败，请刷新页面后重试", nil)
		return requestSession{}, false
	}
	if err := h.touchSession(r.Context(), current); err != nil {
		h.internalError(w, "touch admin session", err)
		return requestSession{}, false
	}
	h.refreshSessionCookies(w, r, current)
	return current, true
}

func (h *handler) refreshSessionCookies(w http.ResponseWriter, r *http.Request, current requestSession) {
	sessionCookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return
	}
	expiresAt := minTime(time.Now().UTC().Add(h.config.IdleTTL), current.value.ExpiresAt)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: sessionCookie.Value, Path: "/", Expires: expiresAt,
		MaxAge: int(time.Until(expiresAt).Seconds()), HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode,
	})
	if csrfCookie, csrfErr := r.Cookie(csrfCookieName); csrfErr == nil {
		h.setCSRFCookie(w, csrfCookie.Value, expiresAt)
	}
}

func (h *handler) loadRequestSession(r *http.Request) (requestSession, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return requestSession{}, ErrNotFound
	}
	token, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(token) != sha256.Size {
		return requestSession{}, ErrNotFound
	}
	tokenHash := sha256.Sum256(token)
	value, err := h.store.LoadSession(r.Context(), tokenHash[:])
	if err != nil {
		return requestSession{}, err
	}
	return requestSession{value: value, tokenHash: tokenHash[:]}, nil
}

func (h *handler) validCSRF(r *http.Request, storedHash []byte) bool {
	headerToken := r.Header.Get("X-CSRF-Token")
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || headerToken == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(headerToken)) != 1 {
		return false
	}
	token, err := base64.RawURLEncoding.DecodeString(headerToken)
	if err != nil || len(token) != sha256.Size {
		return false
	}
	hash := sha256.Sum256(token)
	return subtle.ConstantTimeCompare(hash[:], storedHash) == 1
}

func (h *handler) requireRole(w http.ResponseWriter, r *http.Request, current requestSession, gameID string, roles ...Role) bool {
	allowed, err := h.store.HasRole(r.Context(), gameID, current.value.AdminID, roles...)
	if err != nil {
		h.internalError(w, "check Game-bound admin role", err)
		return false
	}
	if allowed {
		return true
	}
	if err := h.auditDenied(r.Context(), current.value.AdminID, gameID, "role.rejected"); err != nil {
		h.internalError(w, "audit role rejection", err)
		return false
	}
	writeFailure(w, http.StatusForbidden, "forbidden", "当前账号没有此游戏的相应权限", nil)
	return false
}

func (h *handler) auditDenied(ctx context.Context, actorID, gameID, action string) error {
	resourceID := gameID
	if resourceID == "" {
		resourceID = "request"
	}
	if err := h.store.WriteAudit(ctx, AuditEvent{
		GameID: gameID, ActorID: actorID, Action: action, Resource: "admin_operation",
		ResourceID: resourceID, Outcome: "denied", DetailsJSON: []byte(`{}`),
	}); err != nil {
		return fmt.Errorf("persist denied-operation audit: %w", err)
	}
	return nil
}

func (h *handler) setCookies(w http.ResponseWriter, sessionToken, csrfToken string, expiresAt, idleExpiresAt time.Time) {
	if w == nil {
		return
	}
	maxAge := int(time.Until(minTime(expiresAt, idleExpiresAt)).Seconds())
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: sessionToken, Path: "/", Expires: minTime(expiresAt, idleExpiresAt),
		MaxAge: maxAge, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	h.setCSRFCookie(w, csrfToken, minTime(expiresAt, idleExpiresAt))
}

func (h *handler) setCSRFCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	// #nosec G124 -- JavaScript must read this double-submit token; every write also verifies its hash against the server-side session.
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookieName, Value: token, Path: "/", Expires: expiresAt,
		MaxAge: int(time.Until(expiresAt).Seconds()), HttpOnly: false,
		Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

func (h *handler) clearCookies(w http.ResponseWriter) {
	baseExpiry := time.Unix(1, 0)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, Expires: baseExpiry,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	// #nosec G124 -- JavaScript-readable double-submit cookie is cleared with the same Secure/SameSite scope used on issuance.
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookieName, Value: "", Path: "/", MaxAge: -1, Expires: baseExpiry,
		HttpOnly: false, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

func randomToken() (string, []byte, error) {
	raw := make([]byte, sha256.Size)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate opaque token: %w", err)
	}
	hash := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), hash[:], nil
}

func minTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

func ensureEmptyBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeFailure(w, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容超出限制", nil)
			return false
		}
		writeFailure(w, http.StatusBadRequest, "invalid_body", "此操作不接受请求内容", nil)
		return false
	}
	if len(body) != 0 {
		writeFailure(w, http.StatusBadRequest, "unexpected_body", "此操作不接受请求内容", nil)
		return false
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	mediaType := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	if mediaType != "application/json" {
		writeFailure(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "请求必须使用 JSON 格式", nil)
		return false
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeFailure(w, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容超出限制", nil)
			return false
		}
		writeFailure(w, http.StatusBadRequest, "invalid_json", "请求格式无效", nil)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeFailure(w, http.StatusBadRequest, "invalid_json", "请求只能包含一个 JSON 对象", nil)
		return false
	}
	return true
}

func writeSuccess(w http.ResponseWriter, status int, data any, meta map[string]any) {
	writeJSON(w, status, envelope{Success: true, Data: data, Error: nil, Meta: nonNilMeta(meta)})
}

func writeFailure(w http.ResponseWriter, status int, code, message string, fields []fieldIssue) {
	writeJSON(w, status, envelope{
		Success: false,
		Data:    nil,
		Error:   &apiError{Code: code, Message: message, Fields: nonNilFields(fields)},
		Meta:    map[string]any{},
	})
}

func writeJSON(w http.ResponseWriter, status int, response envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("admin response encode failed status=%d error=%v", status, err)
	}
}

func nonNilMeta(meta map[string]any) map[string]any {
	if meta == nil {
		return map[string]any{}
	}
	return meta
}

func nonNilFields(fields []fieldIssue) []fieldIssue {
	if fields == nil {
		return []fieldIssue{}
	}
	return fields
}

func (h *handler) internalError(w http.ResponseWriter, action string, err error) {
	log.Printf("admin request failed action=%q error=%v", action, err)
	writeFailure(w, http.StatusInternalServerError, "internal_error", "服务暂时不可用", nil)
}

func (h *handler) notFound(w http.ResponseWriter, _ *http.Request) {
	writeFailure(w, http.StatusNotFound, "not_found", "找不到管理 API 资源", nil)
}

func parseDraftVersion(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, errors.New("draft version must be a nonnegative decimal string")
	}
	return parsed, nil
}
