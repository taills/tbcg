package main

import (
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestLoadProbeConfigRequiresSessionToken(t *testing.T) {
	_, err := loadProbeConfig(func(key string) string {
		if key == "TBCG_PROTOCOL_PROBE_PORT" {
			return "8082"
		}
		return ""
	})
	if err == nil {
		t.Fatal("loadProbeConfig accepted an empty test token")
	}
}

func TestLoadProbeConfigRejectsShortSessionTokenAndInvalidPort(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]string
	}{
		{name: "short token", values: map[string]string{"TBCG_PROTOCOL_PROBE_TOKEN": "short"}},
		{name: "invalid port", values: map[string]string{"TBCG_PROTOCOL_PROBE_TOKEN": randomProbeToken(t), "TBCG_PROTOCOL_PROBE_PORT": "70000"}},
	}
	for _, test := range cases {
		if _, err := loadProbeConfig(func(key string) string { return test.values[key] }); err == nil {
			t.Errorf("loadProbeConfig accepted %s", test.name)
		}
	}
}

func TestLoadProbeConfigBuildsLoopbackServerAndValidator(t *testing.T) {
	token := randomProbeToken(t)
	config, err := loadProbeConfig(func(key string) string {
		values := map[string]string{
			"TBCG_PROTOCOL_PROBE_PORT":  "8082",
			"TBCG_PROTOCOL_PROBE_TOKEN": token,
		}
		return values[key]
	})
	if err != nil {
		t.Fatalf("loadProbeConfig: %v", err)
	}
	if config.address != "127.0.0.1:8082" {
		t.Fatalf("address = %q, want loopback address", config.address)
	}
	agentConfig := config.agentConfig()
	if agentConfig.ValidateSession == nil || !agentConfig.ValidateSession(token) || agentConfig.ValidateSession("other") {
		t.Fatal("probe session validator did not compare the configured token")
	}
	if agentConfig.ExpectedGameID != "demo" || agentConfig.ExpectedRealmID != "local-test" || agentConfig.ExpectedClientBuild != "0.0.1-test" {
		t.Fatalf("unexpected probe identity: %#v", agentConfig)
	}
}

func TestRunProbeRequiresTLSFilesAsPair(t *testing.T) {
	token := randomProbeToken(t)
	err := runProbe(func(key string) string {
		values := map[string]string{
			"TBCG_PROTOCOL_PROBE_TOKEN":    token,
			"TBCG_PROTOCOL_PROBE_TLS_CERT": "certificate.pem",
		}
		return values[key]
	}, make(chan os.Signal))
	if err == nil {
		t.Fatal("runProbe accepted only one TLS file")
	}
}

func TestRunProbeStartsAndStopsLoopbackServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("read reserved port: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}
	values := map[string]string{
		"TBCG_PROTOCOL_PROBE_PORT":  port,
		"TBCG_PROTOCOL_PROBE_TOKEN": randomProbeToken(t),
	}
	stopped := make(chan os.Signal, 1)
	done := make(chan error, 1)
	finished := false
	go func() { done <- runProbe(func(key string) string { return values[key] }, stopped) }()
	defer func() {
		if finished {
			return
		}
		select {
		case stopped <- syscall.SIGTERM:
		default:
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}()

	var conn *websocket.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, _, err = websocket.DefaultDialer.Dial("ws://127.0.0.1:"+port, nil)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial probe server: %v", err)
	}
	_ = conn.Close()
	stopped <- syscall.SIGTERM
	select {
	case err := <-done:
		finished = true
		if err != nil {
			t.Fatalf("runProbe: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probe server did not stop after signal")
	}
}

func TestProbeCheckOriginOnlyAllowsLoopbackOrNoOrigin(t *testing.T) {
	checkOrigin := probeCheckOrigin
	tests := []struct {
		origin       string
		secondOrigin string
		present      bool
		allow        bool
	}{
		{origin: "", allow: true},
		{origin: "http://localhost:8082", allow: true},
		{origin: "https://127.0.0.1:8082", allow: true},
		{origin: "http://[::1]:8082", allow: true},
		{origin: "https://example.com", allow: false},
		{origin: "http://localhost.evil", allow: false},
		{origin: "file://localhost", allow: false},
		{origin: "http://192.0.2.1", allow: false},
		{origin: "", allow: false, present: true},
		{origin: "http://localhost", secondOrigin: "https://example.com", allow: false},
	}
	for _, test := range tests {
		t.Run(test.origin, func(t *testing.T) {
			request := httptest.NewRequest("GET", "http://example.com", nil)
			if test.present || test.origin != "" {
				request.Header.Add("Origin", test.origin)
			}
			if test.secondOrigin != "" {
				request.Header.Add("Origin", test.secondOrigin)
			}
			if got := checkOrigin(request); got != test.allow {
				t.Fatalf("probeCheckOrigin(%q) = %t, want %t", test.origin, got, test.allow)
			}
		})
	}
}

func randomProbeToken(t *testing.T) string {
	t.Helper()
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("generate probe token: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(data)
}
