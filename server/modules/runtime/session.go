package runtime

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/taills/leaf/v2/network"
)

const maxRequestsPerConnection = 100_000

// ServerCapabilities 是本运行进程支持的能力集合。
var ServerCapabilities = []string{"protocol.v1", "world.grid_move", "npc.dialog", "quest", "inventory", "battle.turn",
	"party", "dungeon", "pet", "partner", "mount", "crafting", "shop", "friends", "guild", "trade", "events"}

// Session 是一个 WSS 连接的 Leaf Agent；读循环在独立 goroutine，业务处理在 Realm 循环。
type Session struct {
	conn        network.Conn
	realm       *Realm
	auth        *Auth
	seq         atomic.Uint64
	writeMu     sync.Mutex
	closed      atomic.Bool
	ready       bool
	seen        map[string]struct{}
	clientMin   string
	windowStart time.Time
	windowCount int

	// 以下字段只在 Realm 循环中读写。
	AccountID   string
	CharacterID string
}

// NewSession 创建连接会话。
func NewSession(conn network.Conn, realm *Realm, auth *Auth, clientMinBuild string) *Session {
	return &Session{conn: conn, realm: realm, auth: auth, seen: map[string]struct{}{}, clientMin: clientMinBuild}
}

// Run 实现 network.Agent。
func (s *Session) Run() {
	defer s.conn.Close()
	for {
		data, err := s.conn.ReadMsg()
		if err != nil {
			return
		}
		request, err := s.realm.Protocol.Decode(data)
		if err != nil {
			s.conn.Destroy()
			return
		}
		if _, dup := s.seen[request.RequestID]; dup || len(s.seen) >= maxRequestsPerConnection {
			s.conn.Destroy()
			return
		}
		s.seen[request.RequestID] = struct{}{}
		if !s.ready {
			if request.Type != "system.hello" || !s.hello(request) {
				s.conn.Destroy()
				return
			}
			continue
		}
		if request.Type == "system.hello" {
			s.conn.Destroy()
			return
		}
		if request.Type == "system.ping" {
			var payload struct {
				Nonce string `json:"nonce"`
			}
			if DecodeStrict(request.Payload, &payload) != nil || payload.Nonce == "" {
				s.conn.Destroy()
				return
			}
			s.respond(request.RequestID, "system.pong", 0, map[string]any{"nonce": payload.Nonce, "server_time": time.Now().UTC().Format(time.RFC3339Nano)}, nil)
			continue
		}
		if !s.allowRequest(time.Now()) {
			s.respond(request.RequestID, s.realm.Protocol.ResponseType(request.Type), 0, nil, Fail("rate_limited", "请求过于频繁"))
			continue
		}
		if !s.realm.Do(func() { s.realm.dispatch(s, request) }) {
			s.respond(request.RequestID, s.realm.Protocol.ResponseType(request.Type), 0, nil, Fail("server_busy", "服务器繁忙，请稍后重试"))
		}
	}
}

const maxRequestsPerSecond = 100

// allowRequest 是每连接固定窗口限流，防止单个客户端占满 Realm 单写者队列。
func (s *Session) allowRequest(now time.Time) bool {
	if now.Sub(s.windowStart) >= time.Second {
		s.windowStart, s.windowCount = now, 0
	}
	s.windowCount++
	return s.windowCount <= maxRequestsPerSecond
}

type helloPayload struct {
	GameID       string   `json:"game_id"`
	RealmID      string   `json:"realm_id"`
	ClientBuild  string   `json:"client_build"`
	ReleaseID    string   `json:"release_id"`
	SessionToken string   `json:"session_token"`
	Capabilities []string `json:"capabilities"`
}

func (s *Session) hello(request Request) bool {
	var payload helloPayload
	if DecodeStrict(request.Payload, &payload) != nil || payload.GameID != s.realm.GameID || payload.RealmID != s.realm.RealmID {
		return false
	}
	if !contains(payload.Capabilities, "protocol.v1") || !buildAtLeast(payload.ClientBuild, s.clientMin) {
		s.respond(request.RequestID, "system.hello_ack", 0, nil, Fail("client_incompatible", "客户端版本过旧或不支持 protocol.v1"))
		return false
	}
	accountID, err := s.auth.Validate(payload.SessionToken)
	if err != nil {
		s.respond(request.RequestID, "system.hello_ack", 0, nil, Fail("unauthenticated", "会话无效或已过期"))
		return false
	}
	release := s.realm.Release()
	if payload.ReleaseID != release.ReleaseID {
		s.respond(request.RequestID, "system.hello_ack", 0, map[string]any{"release_id": release.ReleaseID}, Fail("release_mismatch", "客户端内容版本与服务器不一致，请下载最新公开包"))
		return false
	}
	done := make(chan struct{})
	if !s.realm.Do(func() {
		s.AccountID = accountID
		s.realm.sessions[s] = true
		close(done)
	}) {
		return false
	}
	<-done
	s.ready = true
	s.respond(request.RequestID, "system.hello_ack", 0, map[string]any{"release_id": release.ReleaseID, "release_version": release.ReleaseVersion,
		"capabilities": ServerCapabilities, "account_id": accountID, "server_time": time.Now().UTC().Format(time.RFC3339Nano)}, nil)
	return true
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// buildAtLeast 比较点分数字版本号。
func buildAtLeast(build, minimum string) bool {
	if minimum == "" {
		return build != ""
	}
	a, b := strings.Split(build, "."), strings.Split(minimum, ".")
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x, _ = strconv.Atoi(a[i])
		}
		if i < len(b) {
			y, _ = strconv.Atoi(b[i])
		}
		if x != y {
			return x > y
		}
	}
	return true
}

// OnClose 实现 network.Agent；离线清理投递到 Realm 循环。
func (s *Session) OnClose() {
	s.closed.Store(true)
	s.realm.DoEventually(func() { s.realm.disconnect(s) })
}

func (r *Realm) disconnect(session *Session) {
	delete(r.sessions, session)
	if session.CharacterID == "" {
		return
	}
	player := r.players[session.CharacterID]
	if player == nil || player.Session != session {
		return
	}
	for _, fn := range disconnect {
		fn(r, player)
	}
	delete(r.players, session.CharacterID)
	r.Broadcast(player.State.MapID, player.CharacterID, "world.entity_left", map[string]any{"character_id": player.CharacterID})
}

// Push 发送服务端推送；request_id 使用 push- 前缀。
func (s *Session) Push(messageType string, data any) {
	if s == nil || s.closed.Load() {
		return
	}
	s.respond("push-"+strconv.FormatUint(s.seq.Load()+1, 10), messageType, 0, data, nil)
}

func (s *Session) respond(requestID, messageType string, stateVersion int64, data any, failure *ProtocolError) {
	if s.closed.Load() {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	seq := s.seq.Add(1)
	encoded, err := encodeResponse(s.realm.Protocol.ProtocolVersion, requestID, messageType, seq, stateVersion, data, failure)
	if err != nil {
		log.Printf("encode response type=%s error=%v", messageType, err)
		encoded, _ = encodeResponse(s.realm.Protocol.ProtocolVersion, requestID, messageType, seq, 0, nil, Fail("response_too_large", "响应过大"))
	}
	if err := s.conn.WriteMsg(encoded); err != nil {
		s.closed.Store(true)
	}
}

// RawJSON 是已编码 JSON 的便捷别名。
type RawJSON = json.RawMessage
