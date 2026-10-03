package leaf

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/taills/leaf/v2/network"
)

const DefaultMaxRequests = 1024

type AgentConfig struct {
	ExpectedGameID      string
	ExpectedRealmID     string
	ExpectedClientBuild string
	ReleaseID           string
	Capabilities        []string
	MaxRequests         int
	ValidateSession     func(string) bool
	Now                 func() time.Time
}

type Agent struct {
	conn           network.Conn
	processor      *Processor
	config         AgentConfig
	ready          bool
	serverSeq      uint64
	seenRequestIDs map[string]struct{}
}

var _ network.Agent = (*Agent)(nil)

func NewAgent(conn network.Conn, processor *Processor, config AgentConfig) *Agent {
	if processor == nil {
		processor = NewProcessor()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.MaxRequests <= 0 {
		config.MaxRequests = DefaultMaxRequests
	}
	return &Agent{
		conn:           conn,
		processor:      processor,
		config:         config,
		seenRequestIDs: make(map[string]struct{}),
	}
}

func (agent *Agent) Run() {
	defer agent.conn.Close()
	for {
		data, err := agent.conn.ReadMsg()
		if err != nil {
			return
		}
		message, err := agent.processor.Unmarshal(data)
		if err != nil {
			agent.conn.Destroy()
			return
		}
		request, ok := message.(Request)
		if !ok || agent.acceptRequest(request) != nil {
			agent.conn.Destroy()
			return
		}
		if err := agent.processor.Route(request, agent); err != nil {
			agent.conn.Destroy()
			return
		}
	}
}

func (*Agent) OnClose() {}

func (agent *Agent) acceptRequest(request Request) error {
	if _, exists := agent.seenRequestIDs[request.RequestID]; exists {
		return errors.New("duplicate request_id")
	}
	if len(agent.seenRequestIDs) >= agent.config.MaxRequests {
		return errors.New("connection request limit exceeded")
	}
	agent.seenRequestIDs[request.RequestID] = struct{}{}
	return nil
}

func (agent *Agent) handle(request Request) error {
	if !agent.ready {
		if request.ID != MessageHello {
			return errors.New("business message received before Hello")
		}
		return agent.hello(request)
	}
	if request.ID != MessagePing {
		return errors.New("only Ping is supported after Hello")
	}
	return agent.ping(request)
}

func (agent *Agent) hello(request Request) error {
	if agent.ready {
		return errors.New("duplicate Hello")
	}
	if request.Payload["game_id"] != agent.config.ExpectedGameID ||
		request.Payload["realm_id"] != agent.config.ExpectedRealmID ||
		request.Payload["client_build"] != agent.config.ExpectedClientBuild ||
		request.Payload["release_id"] != agent.config.ReleaseID {
		return errors.New("incompatible Hello identity")
	}
	token, ok := request.Payload["session_token"].(string)
	if !ok || agent.config.ValidateSession == nil || !agent.config.ValidateSession(token) {
		return errors.New("session validation failed")
	}
	capabilities, ok := request.Payload["capabilities"].([]any)
	if !ok || !containsString(capabilities, "protocol.v1") {
		return errors.New("client does not support protocol.v1")
	}
	releaseID := agent.config.ReleaseID
	serverCapabilities := agent.config.Capabilities
	if len(serverCapabilities) == 0 {
		serverCapabilities = []string{"protocol.v1"}
	}
	response := Response{
		ID:        MessageHelloAck,
		RequestID: request.RequestID,
		Success:   true,
		ServerSeq: strconv.FormatUint(agent.serverSeq+1, 10),
		Data:      map[string]any{"release_id": releaseID, "capabilities": serverCapabilities},
	}
	if err := agent.write(response); err != nil {
		return err
	}
	agent.ready = true
	return nil
}

func (agent *Agent) ping(request Request) error {
	response := Response{
		ID:        MessagePong,
		RequestID: request.RequestID,
		Success:   true,
		ServerSeq: strconv.FormatUint(agent.serverSeq+1, 10),
		Data: map[string]any{
			"nonce":       request.Payload["nonce"],
			"server_time": agent.config.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	return agent.write(response)
}

func (agent *Agent) write(response Response) error {
	frames, err := agent.processor.Marshal(response)
	if err != nil {
		return fmt.Errorf("marshal protocol response: %w", err)
	}
	if err := agent.conn.WriteMsg(frames...); err != nil {
		return fmt.Errorf("write protocol response: %w", err)
	}
	agent.serverSeq++
	return nil
}

func containsString(values []any, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
