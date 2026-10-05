package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestOpenAdminRepositoryRejectsPlaintextWithoutConnecting(t *testing.T) {
	_, err := OpenAdminRepository(t.Context(), "postgresql://user:pass@127.0.0.1:55432/test_db?sslmode=disable")
	if err == nil {
		t.Fatal("production repository constructor accepted disabled PostgreSQL TLS")
	}
}

func TestAdminPostgresTLSRejectsMissingConfig(t *testing.T) {
	if err := validateAdminPostgresTLS(nil, false); err == nil {
		t.Fatal("TLS validation accepted a missing PostgreSQL config")
	}
}

func TestAdminPostgresTLSRejectsInsecureFallbackEvenWithVerifiedPrimary(t *testing.T) {
	config, err := pgx.ParseConfig("postgresql://user:pass@db-a.example.invalid:5432,db-b.example.invalid:5432/admin?sslmode=verify-full")
	if err != nil {
		t.Fatalf("parse multi-host verified test config: %v", err)
	}
	if len(config.Fallbacks) != 1 || config.Fallbacks[0].TLSConfig == nil {
		t.Fatalf("verified multi-host PostgreSQL config fallbacks = %#v, want one TLS fallback", config.Fallbacks)
	}
	config.Fallbacks[0].TLSConfig.InsecureSkipVerify = true
	if err := validateAdminPostgresTLS(config, false); err == nil {
		t.Fatal("admin PostgreSQL TLS policy accepted an insecure fallback")
	}
}

func TestAdminPostgresTLSRequiresVerifiedRemoteAndAllowsOnlyOwnedLoopbackPort(t *testing.T) {
	for _, test := range []struct {
		name               string
		dsn                string
		allowOwnedLoopback bool
		wantErr            bool
	}{
		{name: "remote verify-full", dsn: "postgresql://user:pass@db.example.invalid:5432/admin?sslmode=verify-full"},
		{name: "remote verify-full fallbacks", dsn: "postgresql://user:pass@db-a.example.invalid:5432,db-b.example.invalid:5432/admin?sslmode=verify-full"},
		{name: "remote prefer downgrade", dsn: "postgresql://user:pass@db.example.invalid:5432/admin?sslmode=prefer", wantErr: true},
		{name: "remote require without hostname verification", dsn: "postgresql://user:pass@db.example.invalid:5432/admin?sslmode=require", wantErr: true},
		{name: "remote verify-ca without hostname verification", dsn: "postgresql://user:pass@db.example.invalid:5432/admin?sslmode=verify-ca", wantErr: true},
		{name: "remote plaintext", dsn: "postgresql://user:pass@db.example.invalid:5432/admin?sslmode=disable", wantErr: true},
		{name: "production path rejects local plaintext", dsn: "postgresql://user:pass@127.0.0.1:55432/test_db?sslmode=disable", wantErr: true},
		{name: "owned loopback disposable server", dsn: "postgresql://user:pass@127.0.0.1:55432/test_db?sslmode=disable", allowOwnedLoopback: true},
		{name: "loopback default port is not owned test server", dsn: "postgresql://user:pass@127.0.0.1:5432/admin?sslmode=disable", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, err := pgx.ParseConfig(test.dsn)
			if err != nil {
				t.Fatalf("parse test PostgreSQL config: %v", err)
			}
			err = validateAdminPostgresTLS(config, test.allowOwnedLoopback)
			if (err != nil) != test.wantErr {
				t.Fatalf("validate effective database TLS config error = %v, want error %t", err, test.wantErr)
			}
		})
	}
}
