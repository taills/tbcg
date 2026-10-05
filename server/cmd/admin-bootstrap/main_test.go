package main

import "testing"

func TestBootstrapRequiresExplicitLocalConfirmationAndGameRoles(t *testing.T) {
	valid := map[string]string{
		"TBCG_ADMIN_BOOTSTRAP_DATABASE_URL": "postgresql://bootstrap:local-test@127.0.0.1:55432/tbcg?sslmode=disable",
		"TBCG_ADMIN_BOOTSTRAP_CONFIRM":      bootstrapConfirmation,
		"TBCG_ADMIN_BOOTSTRAP_USERNAME":     "content-reviewer",
		"TBCG_ADMIN_BOOTSTRAP_PASSWORD":     "temporary-local-password",
		"TBCG_ADMIN_BOOTSTRAP_GAME_ID":      "demo",
		"TBCG_ADMIN_BOOTSTRAP_ROLES":        "Author,Reviewer",
	}
	config, err := parseBootstrapConfig(valid)
	if err != nil {
		t.Fatalf("parse confirmed local bootstrap config: %v", err)
	}
	if config.Username != "content-reviewer" || len(config.Roles) != 2 || config.Roles[0] != "Author" || config.Roles[1] != "Reviewer" {
		t.Fatalf("parsed bootstrap identity/roles = %#v", config)
	}
	delete(valid, "TBCG_ADMIN_BOOTSTRAP_CONFIRM")
	if _, err := parseBootstrapConfig(valid); err == nil {
		t.Fatal("bootstrap was allowed without explicit confirmation")
	}
}

func TestBootstrapRejectsRemoteDatabaseAndBcryptOverLimitPassword(t *testing.T) {
	base := map[string]string{
		"TBCG_ADMIN_BOOTSTRAP_DATABASE_URL": "postgresql://bootstrap:local-test@127.0.0.1:55432/tbcg?sslmode=disable",
		"TBCG_ADMIN_BOOTSTRAP_CONFIRM":      bootstrapConfirmation,
		"TBCG_ADMIN_BOOTSTRAP_USERNAME":     "author",
		"TBCG_ADMIN_BOOTSTRAP_PASSWORD":     "temporary-local-password",
		"TBCG_ADMIN_BOOTSTRAP_GAME_ID":      "demo",
		"TBCG_ADMIN_BOOTSTRAP_ROLES":        "Author",
	}
	remote := cloneValues(base)
	remote["TBCG_ADMIN_BOOTSTRAP_DATABASE_URL"] = "postgresql://bootstrap:local-test@192.0.2.10:55432/tbcg?sslmode=disable"
	if _, err := parseBootstrapConfig(remote); err == nil {
		t.Fatal("bootstrap accepted a non-loopback PostgreSQL endpoint")
	}
	for _, dsn := range []string{
		"host=127.0.0.1,192.0.2.10 port=55432,55432 user=bootstrap password=local-test dbname=tbcg sslmode=disable",
		"host=localhost,remote.invalid port=55432,55432 user=bootstrap password=local-test dbname=tbcg sslmode=prefer",
	} {
		fallback := cloneValues(base)
		fallback["TBCG_ADMIN_BOOTSTRAP_DATABASE_URL"] = dsn
		if _, err := parseBootstrapConfig(fallback); err == nil {
			t.Fatal("bootstrap 不得接受具有非 loopback 回退目标的连接配置")
		}
	}
	longPassword := cloneValues(base)
	longPassword["TBCG_ADMIN_BOOTSTRAP_PASSWORD"] = string(make([]byte, 73))
	if _, err := parseBootstrapConfig(longPassword); err == nil {
		t.Fatal("bootstrap accepted a password beyond bcrypt's byte limit")
	}
}

func cloneValues(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}
