package leaf

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type memoryConn struct {
	reads  [][]byte
	writes [][]byte
	closed bool
}

func (conn *memoryConn) ReadMsg() ([]byte, error) {
	if len(conn.reads) == 0 {
		return nil, io.EOF
	}
	data := conn.reads[0]
	conn.reads = conn.reads[1:]
	return data, nil
}

func (conn *memoryConn) WriteMsg(parts ...[]byte) error {
	joined := []byte{}
	for _, part := range parts {
		joined = append(joined, part...)
	}
	conn.writes = append(conn.writes, joined)
	return nil
}

func (conn *memoryConn) LocalAddr() net.Addr  { return memoryAddr("local") }
func (conn *memoryConn) RemoteAddr() net.Addr { return memoryAddr("remote") }
func (conn *memoryConn) Close()               { conn.closed = true }
func (conn *memoryConn) Destroy()             { conn.closed = true }

type memoryAddr string

func (addr memoryAddr) Network() string { return "memory" }
func (addr memoryAddr) String() string  { return string(addr) }

func TestAgentRejectsBusinessMessageBeforeHello(t *testing.T) {
	conn := &memoryConn{reads: [][]byte{readFixture(t, "ping.request.json")}}
	agent := NewAgent(conn, NewProcessor(), validAgentConfig(t))
	agent.Run()
	if !conn.closed {
		t.Fatal("connection remains open after pre-ready business message")
	}
	if len(conn.writes) != 0 {
		t.Fatalf("pre-ready request generated %d responses", len(conn.writes))
	}
}

func TestAgentHelloThenPingPong(t *testing.T) {
	conn := &memoryConn{reads: [][]byte{
		readFixture(t, "hello.request.json"),
		readFixture(t, "ping.request.json"),
	}}
	agent := NewAgent(conn, NewProcessor(), validAgentConfig(t))
	agent.Run()
	if len(conn.writes) != 2 {
		t.Fatalf("got %d responses, want HelloAck and Pong", len(conn.writes))
	}
	var helloAck, pong struct {
		Type      string         `json:"type"`
		RequestID string         `json:"request_id"`
		ServerSeq string         `json:"server_seq"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal(conn.writes[0], &helloAck); err != nil {
		t.Fatalf("decode HelloAck: %v", err)
	}
	if err := json.Unmarshal(conn.writes[1], &pong); err != nil {
		t.Fatalf("decode Pong: %v", err)
	}
	if helloAck.Type != "system.hello_ack" || helloAck.RequestID != "req-hello-1" || helloAck.ServerSeq != "1" {
		t.Fatalf("unexpected HelloAck: %#v", helloAck)
	}
	if pong.Type != "system.pong" || pong.RequestID != "req-ping-2" || pong.ServerSeq != "2" {
		t.Fatalf("unexpected Pong: %#v", pong)
	}
	if pong.Data["nonce"] != "ping-nonce-2" {
		t.Fatalf("Pong data does not echo nonce: %#v", pong.Data)
	}
}

func TestAgentRejectsRepeatedHello(t *testing.T) {
	conn := &memoryConn{reads: [][]byte{
		readFixture(t, "hello.request.json"),
		readFixture(t, "hello.request.json"),
	}}
	NewAgent(conn, NewProcessor(), validAgentConfig(t)).Run()
	if !conn.closed {
		t.Fatal("connection remains open after repeated Hello")
	}
	if len(conn.writes) != 1 {
		t.Fatalf("got %d responses, want only first HelloAck", len(conn.writes))
	}
}

func TestAgentRejectsEmptyHelloToken(t *testing.T) {
	hello := []byte(`{"protocol_version":"1","request_id":"r","type":"system.hello","payload":{"game_id":"g","realm_id":"r","client_build":"b","release_id":"demo-release-1","session_token":"","capabilities":["protocol.v1"]}}`)
	conn := &memoryConn{reads: [][]byte{hello}}
	NewAgent(conn, NewProcessor(), validAgentConfig(t)).Run()
	if len(conn.writes) != 0 {
		t.Fatal("empty-token Hello received a success response")
	}
}

func TestAgentWriteFailureClosesConnection(t *testing.T) {
	conn := &failingWriteConn{memoryConn: &memoryConn{reads: [][]byte{readFixture(t, "hello.request.json")}}}
	NewAgent(conn, NewProcessor(), validAgentConfig(t)).Run()
	if !conn.closed {
		t.Fatal("connection remains open after response write failure")
	}
}

type failingWriteConn struct{ *memoryConn }

func (conn *failingWriteConn) WriteMsg(...[]byte) error { return errors.New("write failed") }
func TestAgentRejectsMissingSessionValidator(t *testing.T) {
	config := validAgentConfig(t)
	config.ValidateSession = nil
	conn := &memoryConn{reads: [][]byte{readFixture(t, "hello.request.json")}}
	NewAgent(conn, NewProcessor(), config).Run()
	if len(conn.writes) != 0 {
		t.Fatal("Hello succeeded without a session validator")
	}
}

func TestAgentRejectsUnexpectedHelloIdentity(t *testing.T) {
	cases := []struct {
		name string
		edit func(*AgentConfig)
	}{
		{name: "game", edit: func(config *AgentConfig) { config.ExpectedGameID = "other" }},
		{name: "realm", edit: func(config *AgentConfig) { config.ExpectedRealmID = "other" }},
		{name: "client build", edit: func(config *AgentConfig) { config.ExpectedClientBuild = "other" }},
		{name: "release", edit: func(config *AgentConfig) { config.ReleaseID = "other" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			config := validAgentConfig(t)
			test.edit(&config)
			conn := &memoryConn{reads: [][]byte{readFixture(t, "hello.request.json")}}
			NewAgent(conn, NewProcessor(), config).Run()
			if len(conn.writes) != 0 {
				t.Fatal("Hello succeeded with unexpected identity")
			}
		})
	}
}

func TestAgentRejectsDuplicateRequestID(t *testing.T) {
	duplicatePing := bytes.Replace(readFixture(t, "ping.request.json"), []byte("req-ping-2"), []byte("req-hello-1"), 1)
	conn := &memoryConn{reads: [][]byte{readFixture(t, "hello.request.json"), duplicatePing}}
	NewAgent(conn, NewProcessor(), validAgentConfig(t)).Run()
	if len(conn.writes) != 1 {
		t.Fatalf("got %d responses after duplicate request_id, want only HelloAck", len(conn.writes))
	}
}

func TestAgentLimitsRequestsPerConnection(t *testing.T) {
	config := validAgentConfig(t)
	config.MaxRequests = 2
	thirdPing := bytes.Replace(readFixture(t, "ping.request.json"), []byte("req-ping-2"), []byte("req-ping-3"), 1)
	conn := &memoryConn{reads: [][]byte{
		readFixture(t, "hello.request.json"),
		readFixture(t, "ping.request.json"),
		thirdPing,
	}}
	NewAgent(conn, NewProcessor(), config).Run()
	if len(conn.writes) != 2 {
		t.Fatalf("got %d responses at request limit, want 2", len(conn.writes))
	}
}

func TestAgentHasBoundedReadLoop(t *testing.T) {
	conn := &memoryConn{}
	agent := NewAgent(conn, NewProcessor(), validAgentConfig(t))
	done := make(chan struct{})
	go func() { agent.Run(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("agent did not stop when connection closed")
	}
}

func validAgentConfig(t *testing.T) AgentConfig {
	t.Helper()
	var hello struct {
		Payload struct {
			SessionToken string `json:"session_token"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(readFixture(t, "hello.request.json"), &hello); err != nil {
		t.Fatalf("decode hello fixture for session validator: %v", err)
	}
	fixtureToken := hello.Payload.SessionToken
	return AgentConfig{
		ExpectedGameID:      "demo",
		ExpectedRealmID:     "local-test",
		ExpectedClientBuild: "0.0.1-test",
		ReleaseID:           "demo-release-1",
		Capabilities:        []string{"protocol.v1"},
		ValidateSession:     func(token string) bool { return token == fixtureToken },
	}
}
