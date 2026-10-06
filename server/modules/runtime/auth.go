package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	playerSessionTTL = 12 * time.Hour
	playerBcryptCost = 12
)

var (
	ErrUnauthenticated = errors.New("runtime: invalid player credentials or session")
	ErrAccountExists   = errors.New("runtime: account already exists")
	usernamePattern    = regexp.MustCompile(`^[A-Za-z0-9_]{3,24}$`)
)

// Auth 管理玩家账号与不透明会话令牌（只存 SHA-256）。
type Auth struct {
	db     *sql.DB
	gameID string
	dummy  []byte
}

// NewAuth 创建账号服务。
func NewAuth(db *sql.DB, gameID string) (*Auth, error) {
	dummy, err := bcrypt.GenerateFromPassword([]byte("tbcg-player-dummy"), playerBcryptCost)
	if err != nil {
		return nil, err
	}
	return &Auth{db: db, gameID: gameID, dummy: dummy}, nil
}

func validPassword(password string) bool { return len(password) >= 8 && len(password) <= 72 }

// Register 创建玩家账号。
func (a *Auth) Register(ctx context.Context, username, password string) (string, error) {
	if !usernamePattern.MatchString(username) || !validPassword(password) {
		return "", Fail("invalid_credentials_format", "用户名需 3–24 位字母数字下划线，密码 8–72 字节")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), playerBcryptCost)
	if err != nil {
		return "", err
	}
	var accountID string
	err = a.db.QueryRowContext(ctx, `INSERT INTO player_accounts (game_id, username, password_hash) VALUES ($1,$2,$3)
ON CONFLICT (game_id, username) DO NOTHING RETURNING account_id::text`, a.gameID, username, string(hash)).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAccountExists
	}
	if err != nil {
		return "", fmt.Errorf("create player account: %w", err)
	}
	return accountID, nil
}

// Login 校验密码并签发会话令牌。
func (a *Auth) Login(ctx context.Context, username, password string) (string, time.Time, error) {
	var accountID, hash string
	err := a.db.QueryRowContext(ctx, `SELECT account_id::text, password_hash FROM player_accounts WHERE game_id=$1 AND username=$2`, a.gameID, username).Scan(&accountID, &hash)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, fmt.Errorf("read player account: %w", err)
	}
	candidate := a.dummy
	if found {
		candidate = []byte(hash)
	}
	if bcrypt.CompareHashAndPassword(candidate, []byte(password)) != nil || !found {
		return "", time.Time{}, ErrUnauthenticated
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	digest := sha256.Sum256([]byte(token))
	expires := time.Now().Add(playerSessionTTL)
	if _, err := a.db.ExecContext(ctx, `INSERT INTO player_sessions (token_hash, account_id, expires_at) VALUES ($1,$2::uuid,$3)`, digest[:], accountID, expires); err != nil {
		return "", time.Time{}, fmt.Errorf("create player session: %w", err)
	}
	_, _ = a.db.ExecContext(ctx, `DELETE FROM player_sessions WHERE expires_at < now() - interval '1 day'`)
	return token, expires, nil
}

// Validate 返回令牌对应的账号 ID。
func (a *Auth) Validate(token string) (string, error) {
	if len(token) < 32 || len(token) > 128 {
		return "", ErrUnauthenticated
	}
	digest := sha256.Sum256([]byte(token))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var accountID string
	err := a.db.QueryRowContext(ctx, `SELECT s.account_id::text FROM player_sessions s JOIN player_accounts a ON a.account_id = s.account_id
WHERE s.token_hash=$1 AND s.expires_at > now() AND a.game_id=$2`, digest[:], a.gameID).Scan(&accountID)
	if err != nil {
		return "", ErrUnauthenticated
	}
	return accountID, nil
}
