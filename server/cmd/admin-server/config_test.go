package main

import (
	"os"
	"testing"
)

func TestDiscoverProjectRootRejectsUnmarkedTree(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("read original working directory: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("change to empty project tree: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore original working directory: %v", err)
		}
	})
	if _, err := discoverProjectRoot(); err == nil {
		t.Fatal("project root discovery accepted a tree without trusted schemas")
	}
}

func TestServerConfigRequiresDatabaseAndDefaultsToLoopbackTLS(t *testing.T) {
	if _, err := parseServerConfig(map[string]string{}); err == nil {
		t.Fatal("server config accepted a missing PostgreSQL DSN")
	}
	config, err := parseServerConfig(map[string]string{
		"TBCG_ADMIN_DATABASE_URL": "postgresql://admin:local@127.0.0.1:55432/tbcg?sslmode=verify-full",
		"TBCG_ADMIN_TLS_CERT":     "/tmp/admin-test.crt",
		"TBCG_ADMIN_TLS_KEY":      "/tmp/admin-test.key",
	})
	if err != nil {
		t.Fatalf("parse explicit TLS config: %v", err)
	}
	if config.BindAddress != "127.0.0.1:8443" || config.Origin != "https://127.0.0.1:8443" || !config.CookieSecure {
		t.Fatalf("default admin binding is unsafe or inconsistent: %#v", config)
	}
}

func TestServerConfigRejectsInvalidTLSAndBounds(t *testing.T) {
	base := map[string]string{
		"TBCG_ADMIN_DATABASE_URL": "postgresql://admin:local@127.0.0.1:55432/tbcg?sslmode=verify-full",
		"TBCG_ADMIN_TLS_CERT":     "/tmp/admin-test.crt",
		"TBCG_ADMIN_TLS_KEY":      "/tmp/admin-test.key",
		"TBCG_PROJECT_ROOT":       "/tmp/tbcg-admin-config-test",
	}
	for _, test := range []struct {
		name   string
		change func(map[string]string)
	}{
		{name: "plaintext origin", change: func(values map[string]string) { values["TBCG_ADMIN_ORIGIN"] = "http://localhost:8443" }},
		{name: "invalid bind address", change: func(values map[string]string) { values["TBCG_ADMIN_BIND"] = "missing-port" }},
		{name: "invalid session ttl", change: func(values map[string]string) { values["TBCG_ADMIN_SESSION_TTL"] = "not-a-duration" }},
		{name: "invalid idle ttl", change: func(values map[string]string) { values["TBCG_ADMIN_IDLE_TTL"] = "not-a-duration" }},
		{name: "idle ttl exceeds session ttl", change: func(values map[string]string) {
			values["TBCG_ADMIN_SESSION_TTL"] = "1m"
			values["TBCG_ADMIN_IDLE_TTL"] = "2m"
		}},
		{name: "body limit too small", change: func(values map[string]string) { values["TBCG_ADMIN_MAX_BODY_BYTES"] = "100" }},
		{name: "incomplete tls key pair", change: func(values map[string]string) { delete(values, "TBCG_ADMIN_TLS_KEY") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := make(map[string]string, len(base))
			for key, value := range base {
				values[key] = value
			}
			test.change(values)
			if _, err := parseServerConfig(values); err == nil {
				t.Fatal("server config accepted invalid TLS or bounds")
			}
		})
	}
}

func TestServerConfigRejectsPlaintextListeners(t *testing.T) {
	base := map[string]string{
		"TBCG_ADMIN_DATABASE_URL": "postgresql://admin:local@127.0.0.1:55432/tbcg?sslmode=verify-full",
	}
	if _, err := parseServerConfig(base); err == nil {
		t.Fatal("server config accepted plaintext without explicit loopback opt-in")
	}
	local := map[string]string{
		"TBCG_ADMIN_DATABASE_URL":        base["TBCG_ADMIN_DATABASE_URL"],
		"TBCG_ADMIN_ORIGIN":              "http://127.0.0.1:8443",
		"TBCG_ADMIN_ALLOW_HTTP_LOOPBACK": "1",
	}
	if _, err := parseServerConfig(local); err == nil {
		t.Fatal("server config accepted the removed loopback HTTP exception")
	}
}
