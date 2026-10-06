package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/taills/tbcg/server/internal/content/catalog"
)

// 运行面固定的规则/协议/实例版本；激活的 Release 必须声明相同版本。
const (
	RulesetVersion        = 1
	ProtocolVersion       = 1
	InstanceSchemaVersion = 1
	ContentSchemaVersion  = 1
	CurrencyCode          = "coin"
	commandTimeout        = 10 * time.Second
)

// ErrOutcomeUnknown 表示提交结果未知；客户端必须用同一幂等键重试核验。
var ErrOutcomeUnknown = errors.New("runtime: commit outcome unknown")

// Handler 处理一个已就绪会话的请求，始终在 Realm 单写者循环中运行。
type Handler func(c *Ctx) (any, *ProtocolError)

type handlerSpec struct {
	fn             Handler
	needsCharacter bool
}

var (
	registryMu sync.Mutex
	handlers   = map[string]handlerSpec{}
	tickers    []func(*Realm, time.Time)
	hooks      = map[string][]func(*Tx, any) error{}
	disconnect []func(*Realm, *Player)
	enterHooks []func(*Realm, *Player)
)

// OnEnter 注册角色进入（含断线重连、顶号）后的运行态恢复逻辑。
func OnEnter(fn func(*Realm, *Player)) {
	registryMu.Lock()
	defer registryMu.Unlock()
	enterHooks = append(enterHooks, fn)
}

// RunEnterHooks 由角色进入流程调用。
func (r *Realm) RunEnterHooks(player *Player) {
	for _, fn := range enterHooks {
		r.guard("enter hook", func() { fn(r, player) })
	}
}

// guard 在单写者循环中执行函数并拦截 panic，避免整个进程退出。
func (r *Realm) guard(label string, fn func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("realm %s panic: %v", label, recovered)
		}
	}()
	fn()
}

// DoEventually 保证投递（队列满时在后台阻塞等待），用于不可丢弃的清理。
func (r *Realm) DoEventually(fn func()) {
	if !r.Do(fn) {
		go func() { r.inbox <- fn }()
	}
}

// Register 注册消息处理器；needsCharacter 要求会话已进入角色。
func Register(messageType string, needsCharacter bool, fn Handler) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := handlers[messageType]; exists {
		panic("runtime: duplicate handler " + messageType)
	}
	handlers[messageType] = handlerSpec{fn: fn, needsCharacter: needsCharacter}
}

// OnTick 注册周期任务（在 Realm 循环中运行）。
func OnTick(fn func(*Realm, time.Time)) {
	registryMu.Lock()
	defer registryMu.Unlock()
	tickers = append(tickers, fn)
}

// OnFact 注册在同一事务内消费领域事实（例如战斗胜利）的钩子。
func OnFact(name string, fn func(*Tx, any) error) {
	registryMu.Lock()
	defer registryMu.Unlock()
	hooks[name] = append(hooks[name], fn)
}

// OnDisconnect 注册玩家离线时的清理逻辑。
func OnDisconnect(fn func(*Realm, *Player)) {
	registryMu.Lock()
	defer registryMu.Unlock()
	disconnect = append(disconnect, fn)
}

// ReleaseInfo 描述当前服务中的 Release。
type ReleaseInfo struct {
	ReleaseID      string `json:"release_id"`
	ReleaseVersion int    `json:"release_version"`
	PublicSHA256   string `json:"public_artifact_sha256"`
	DevLocal       bool   `json:"dev_local"`
}

// Realm 是单写者：所有内存状态只在 loop goroutine 中读写。
type Realm struct {
	GameID   string
	RealmID  string
	DB       *sql.DB
	Protocol *Catalog
	Epoch    int64

	content *catalog.Catalog
	release ReleaseInfo
	mu      sync.RWMutex // 仅保护 content/release 供 HTTP 只读访问

	players  map[string]*Player
	sessions map[*Session]bool
	modules  map[string]any
	inbox    chan func()
	now      func() time.Time
	loader   *ReleaseLoader
}

// NewRealm 创建 Realm；调用方随后调用 Run。
func NewRealm(gameID, realmID string, db *sql.DB, protocol *Catalog, loader *ReleaseLoader) *Realm {
	return &Realm{GameID: gameID, RealmID: realmID, DB: db, Protocol: protocol, players: map[string]*Player{},
		sessions: map[*Session]bool{}, modules: map[string]any{}, inbox: make(chan func(), 4096), now: time.Now, loader: loader}
}

// Content 返回当前 Release 内容（只在循环中调用，或经 Snapshot 读取）。
func (r *Realm) Content() *catalog.Catalog { return r.content }

// Release 返回当前 Release 信息（线程安全）。
func (r *Realm) Release() ReleaseInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.release
}

func (r *Realm) setContent(content *catalog.Catalog, info ReleaseInfo) {
	r.mu.Lock()
	r.content, r.release = content, info
	r.mu.Unlock()
}

// Now 返回可信服务器时钟。
func (r *Realm) Now() time.Time { return r.now() }

// Module 返回（必要时创建）领域模块的内存状态。
func (r *Realm) Module(name string, create func() any) any {
	value, ok := r.modules[name]
	if !ok {
		value = create()
		r.modules[name] = value
	}
	return value
}

// Do 把函数投递到单写者循环。
func (r *Realm) Do(fn func()) bool {
	select {
	case r.inbox <- fn:
		return true
	default:
		return false
	}
}

// Run 运行单写者循环直至 ctx 结束。
func (r *Realm) Run(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	activation := time.NewTicker(2 * time.Second)
	defer activation.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case fn := <-r.inbox:
			r.guard("task", fn)
		case now := <-ticker.C:
			for _, tick := range tickers {
				r.guard("tick", func() { tick(r, now) })
			}
		case <-activation.C:
			r.guard("activation", func() { r.processActivation(ctx) })
		}
	}
}

// Player 按角色 ID 返回在线玩家。
func (r *Realm) Player(characterID string) *Player { return r.players[characterID] }

// PlayerByName 按角色名返回在线玩家。
func (r *Realm) PlayerByName(name string) *Player {
	for _, player := range r.players {
		if player.State.Name == name {
			return player
		}
	}
	return nil
}

// PlayersOnMap 返回同地图在线玩家（按 ID 排序）。
func (r *Realm) PlayersOnMap(mapID string) []*Player {
	var out []*Player
	for _, player := range r.players {
		if player.State.MapID == mapID {
			out = append(out, player)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CharacterID < out[j].CharacterID })
	return out
}

// Push 向在线角色推送服务端事件。
func (r *Realm) Push(characterID, messageType string, data any) {
	if player := r.players[characterID]; player != nil && player.Session != nil {
		player.Session.Push(messageType, data)
	}
}

// Broadcast 向地图上的所有玩家推送（可排除一个角色）。
func (r *Realm) Broadcast(mapID, except, messageType string, data any) {
	for _, player := range r.PlayersOnMap(mapID) {
		if player.CharacterID != except {
			player.Session.Push(messageType, data)
		}
	}
}

// BroadcastAll 向全部会话推送。
func (r *Realm) BroadcastAll(messageType string, data any) {
	for session := range r.sessions {
		session.Push(messageType, data)
	}
}

// Ctx 是一次请求处理上下文。
type Ctx struct {
	Realm        *Realm
	Session      *Session
	Player       *Player
	Request      Request
	Context      context.Context
	StateVersion int64
}

// Decode 严格解码请求载荷。
func (c *Ctx) Decode(target any) *ProtocolError { return DecodeStrict(c.Request.Payload, target) }

// Content 返回当前 Release 内容。
func (c *Ctx) Content() *catalog.Catalog { return c.Realm.content }

func (r *Realm) dispatch(session *Session, request Request) {
	spec, ok := handlers[request.Type]
	responseType := r.Protocol.ResponseType(request.Type)
	if !ok {
		session.respond(request.RequestID, responseType, 0, nil, Fail("unsupported", "该消息尚未在服务端实现"))
		return
	}
	if r.content == nil && request.Type != "character.list" {
		session.respond(request.RequestID, responseType, 0, nil, Fail("no_release", "Realm 尚未激活任何 Release"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	c := &Ctx{Realm: r, Session: session, Request: request, Context: ctx}
	if session.CharacterID != "" {
		c.Player = r.players[session.CharacterID]
	}
	if spec.needsCharacter && c.Player == nil {
		session.respond(request.RequestID, responseType, 0, nil, Fail("no_character", "请先进入角色"))
		return
	}
	data, failure := r.safeCall(spec.fn, c)
	if c.Player != nil && c.StateVersion == 0 {
		c.StateVersion = c.Player.Version
	}
	session.respond(request.RequestID, responseType, c.StateVersion, data, failure)
}

func (r *Realm) safeCall(fn Handler, c *Ctx) (data any, failure *ProtocolError) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("game handler panic type=%s error=%v", c.Request.Type, recovered)
			data, failure = nil, Fail("internal_error", "服务器处理失败")
		}
	}()
	return fn(c)
}

// FailFromError 把持久化错误映射为客户端错误。
func FailFromError(err error) *ProtocolError {
	var protocolErr *ProtocolError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &protocolErr):
		return protocolErr
	case errors.Is(err, ErrOutcomeUnknown):
		return Fail("outcome_unknown", "提交结果未确认，请使用同一幂等键重试")
	case errors.Is(err, ErrIdempotencyConflict):
		return Fail("idempotency_conflict", "幂等键已用于不同请求")
	case errors.Is(err, ErrOwnerLost):
		return Fail("owner_lost", "Realm 所有权已转移，请重新连接")
	case errors.Is(err, ErrVersionConflict):
		return Fail("state_conflict", "角色状态已变化，请刷新后重试")
	default:
		log.Printf("game persistence error: %v", err)
		return Fail("internal_error", "服务器处理失败")
	}
}

// RunFacts 在当前事务内调用指定事实的全部钩子。
func (tx *Tx) RunFacts(name string, payload any) error {
	for _, hook := range hooks[name] {
		if err := hook(tx, payload); err != nil {
			return fmt.Errorf("fact %s: %w", name, err)
		}
	}
	return nil
}

// MustJSON 把值编码为 JSON（失败时 panic，仅用于受控结构）。
func MustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
