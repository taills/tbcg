package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/taills/tbcg/server/internal/admin"
	"golang.org/x/crypto/bcrypt"
)

const (
	bootstrapConfirmation = "I_UNDERSTAND_LOCAL_ADMIN_BOOTSTRAP"
	bootstrapBcryptCost   = 12
)

var gameIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type bootstrapConfig struct {
	DatabaseURL string
	Username    string
	Password    string
	GameID      string
	Roles       []string
}

func main() {
	if err := runBootstrap(context.Background(), os.LookupEnv); err != nil {
		log.Fatal(err)
	}
	fmt.Println("管理员 bootstrap 完成。")
}

func parseBootstrapConfig(lookup map[string]string) (bootstrapConfig, error) {
	if lookup["TBCG_ADMIN_BOOTSTRAP_CONFIRM"] != bootstrapConfirmation {
		return bootstrapConfig{}, errors.New("bootstrap requires the explicit local confirmation variable")
	}
	config := bootstrapConfig{
		DatabaseURL: strings.TrimSpace(lookup["TBCG_ADMIN_BOOTSTRAP_DATABASE_URL"]),
		Username:    strings.TrimSpace(lookup["TBCG_ADMIN_BOOTSTRAP_USERNAME"]),
		Password:    lookup["TBCG_ADMIN_BOOTSTRAP_PASSWORD"],
		GameID:      strings.TrimSpace(lookup["TBCG_ADMIN_BOOTSTRAP_GAME_ID"]),
	}
	if config.DatabaseURL == "" || config.Username == "" || len(config.Username) > 128 || len(config.Password) == 0 || len([]byte(config.Password)) > 72 || !gameIDPattern.MatchString(config.GameID) {
		return bootstrapConfig{}, errors.New("bootstrap requires a valid DSN, username, bcrypt-sized password, and Game ID")
	}
	parsed, err := pgx.ParseConfig(config.DatabaseURL)
	if err != nil {
		return bootstrapConfig{}, errors.New("bootstrap PostgreSQL DSN is invalid")
	}
	if !isLoopbackPostgresHost(parsed.Host) {
		return bootstrapConfig{}, errors.New("bootstrap database must be reachable only through a loopback host")
	}
	for _, fallback := range parsed.Fallbacks {
		if fallback == nil || !isLoopbackPostgresHost(fallback.Host) {
			return bootstrapConfig{}, errors.New("bootstrap database fallbacks must remain on loopback hosts")
		}
	}
	roles := strings.Split(lookup["TBCG_ADMIN_BOOTSTRAP_ROLES"], ",")
	if len(roles) == 0 {
		return bootstrapConfig{}, errors.New("bootstrap must bind at least one Game role")
	}
	seen := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if role != string(admin.RoleReader) && role != string(admin.RoleAuthor) && role != string(admin.RoleReviewer) && role != string(admin.RolePublisher) {
			return bootstrapConfig{}, errors.New("bootstrap roles must be Reader, Author, Reviewer, or Publisher")
		}
		if _, exists := seen[role]; exists {
			return bootstrapConfig{}, errors.New("bootstrap role list contains duplicates")
		}
		seen[role] = struct{}{}
		config.Roles = append(config.Roles, role)
	}
	return config, nil
}

func isLoopbackPostgresHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func runBootstrap(parent context.Context, lookupEnv func(string) (string, bool)) error {
	values := make(map[string]string)
	for _, key := range []string{
		"TBCG_ADMIN_BOOTSTRAP_DATABASE_URL", "TBCG_ADMIN_BOOTSTRAP_CONFIRM", "TBCG_ADMIN_BOOTSTRAP_USERNAME",
		"TBCG_ADMIN_BOOTSTRAP_PASSWORD", "TBCG_ADMIN_BOOTSTRAP_GAME_ID", "TBCG_ADMIN_BOOTSTRAP_ROLES",
	} {
		if value, ok := lookupEnv(key); ok {
			values[key] = value
		}
	}
	config, err := parseBootstrapConfig(values)
	if err != nil {
		return err
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(config.Password), bootstrapBcryptCost)
	if err != nil {
		return errors.New("hash bootstrap password")
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	parsed, err := pgx.ParseConfig(config.DatabaseURL)
	if err != nil {
		return errors.New("bootstrap PostgreSQL DSN is invalid")
	}
	db := stdlib.OpenDB(*parsed)
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("bootstrap PostgreSQL close failed: %v", err)
		}
	}()
	if err := db.PingContext(ctx); err != nil {
		return errors.New("cannot connect to the configured local bootstrap database")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot start local admin bootstrap transaction")
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			log.Printf("bootstrap transaction rollback failed: %v", err)
		}
	}()
	var adminID string
	if err := tx.QueryRowContext(ctx, `
INSERT INTO admin_users (username, password_hash)
VALUES ($1, $2)
RETURNING admin_id::text`, config.Username, string(passwordHash)).Scan(&adminID); err != nil {
		return errors.New("cannot create bootstrap admin; username may already exist")
	}
	for _, role := range config.Roles {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO admin_role_bindings (game_id, admin_id, role)
VALUES ($1, $2, $3)`, config.GameID, adminID, role); err != nil {
			return errors.New("cannot bind bootstrap role; check the Game ID and migration state")
		}
	}
	details, err := json.Marshal(map[string]any{"roles": config.Roles})
	if err != nil {
		return errors.New("cannot encode bootstrap audit details")
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO audit_logs (game_id, actor_id, action, resource_type, resource_id, outcome, details)
VALUES ($1, $2::uuid, 'admin.bootstrap', 'admin_user', $2::text, 'success', $3::jsonb)`, config.GameID, adminID, string(details)); err != nil {
		return errors.New("cannot audit bootstrap account creation")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit admin bootstrap transaction")
	}
	return nil
}
