package main

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	leaftransport "example.com/tbcg/server/internal/transport/leaf"
)

const minimumProbeTokenBytes = 32

type probeConfig struct {
	address         string
	releaseID       string
	validateSession func(string) bool
}

func loadProbeConfig(getenv func(string) string) (probeConfig, error) {
	portText := getenv("TBCG_PROTOCOL_PROBE_PORT")
	if portText == "" {
		portText = "8081"
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return probeConfig{}, fmt.Errorf("TBCG_PROTOCOL_PROBE_PORT must be between 1 and 65535")
	}
	token := getenv("TBCG_PROTOCOL_PROBE_TOKEN")
	if len(token) < minimumProbeTokenBytes {
		return probeConfig{}, fmt.Errorf("TBCG_PROTOCOL_PROBE_TOKEN must contain at least %d bytes", minimumProbeTokenBytes)
	}
	releaseID := getenv("TBCG_PROTOCOL_PROBE_RELEASE_ID")
	if releaseID == "" {
		releaseID = "demo-release-1"
	}
	return probeConfig{
		address:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		releaseID: releaseID,
		validateSession: func(candidate string) bool {
			return subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1
		},
	}, nil
}

// agentConfig uses only local probe-token matching, not production session validation.
func (config probeConfig) agentConfig() leaftransport.AgentConfig {
	return leaftransport.AgentConfig{
		ExpectedGameID:      "demo",
		ExpectedRealmID:     "local-test",
		ExpectedClientBuild: "0.0.1-test",
		ReleaseID:           config.releaseID,
		Capabilities:        []string{"protocol.v1"},
		ValidateSession:     config.validateSession,
	}
}

func probeCheckOrigin(request *http.Request) bool {
	origins := request.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 || strings.TrimSpace(origins[0]) == "" {
		return false
	}
	parsed, err := url.Parse(origins[0])
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
