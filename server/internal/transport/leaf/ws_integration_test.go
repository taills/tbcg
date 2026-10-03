package leaf

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/taills/leaf/v2/network"
)

func TestLeafWSServerHelloPingRoundTrip(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}

	server := &network.WSServer{
		Addr:            addr,
		MaxConnNum:      4,
		PendingWriteNum: 4,
		MaxMsgLen:       MaxMessageBytes,
		HTTPTimeout:     time.Second * 5,
		CheckOrigin:     func(*http.Request) bool { return true },
		NewAgent: func(conn *network.WSConn) network.Agent {
			return NewAgent(conn, NewProcessor(), validAgentConfig(t))
		},
	}
	server.Start()
	t.Cleanup(server.Close)

	var conn *websocket.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, _, err = websocket.DefaultDialer.Dial("ws://"+addr, nil)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial Leaf WebSocket server: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.BinaryMessage, readFixture(t, "hello.request.json")); err != nil {
		t.Fatalf("write binary Hello: %v", err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, readFixture(t, "ping.request.json")); err != nil {
		t.Fatalf("write binary Ping: %v", err)
	}

	for index, expectedType := range []string{"system.hello_ack", "system.pong"} {
		frameType, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response %d: %v", index+1, err)
		}
		if frameType != websocket.BinaryMessage {
			t.Fatalf("response %d frame type = %d, want binary", index+1, frameType)
		}
		var response struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			ServerSeq string `json:"server_seq"`
		}
		if err := json.Unmarshal(payload, &response); err != nil {
			t.Fatalf("decode response %d: %v", index+1, err)
		}
		if response.Type != expectedType || response.ServerSeq != fmt.Sprint(index+1) {
			t.Fatalf("response %d = %#v, want type %s and sequence %d", index+1, response, expectedType, index+1)
		}
		if wantRequestID := []string{"req-hello-1", "req-ping-2"}[index]; response.RequestID != wantRequestID {
			t.Fatalf("response %d request_id = %q, want %q", index+1, response.RequestID, wantRequestID)
		}
	}
}
