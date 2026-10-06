// Command game-server 是模块化单体游戏运行进程：每个 Realm 一个单写者 owner，提供 WSS 协议、
// 玩家账号 HTTP 接口与公开白名单包下载，并在 owner 守卫下确认 Release 激活。
package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	contentbuild "example.com/tbcg/server/internal/content/build"
	"example.com/tbcg/server/internal/content/mapdata"
	"example.com/tbcg/server/internal/content/schema"
	rt "example.com/tbcg/server/modules/runtime"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/taills/leaf/v2/network"

	_ "example.com/tbcg/server/modules/combat"
	_ "example.com/tbcg/server/modules/companion"
	_ "example.com/tbcg/server/modules/crafting"
	_ "example.com/tbcg/server/modules/dungeon"
	_ "example.com/tbcg/server/modules/event"
	_ "example.com/tbcg/server/modules/guild"
	_ "example.com/tbcg/server/modules/inventory"
	_ "example.com/tbcg/server/modules/party"
	_ "example.com/tbcg/server/modules/quest"
	_ "example.com/tbcg/server/modules/shop"
	_ "example.com/tbcg/server/modules/social"
	_ "example.com/tbcg/server/modules/trade"
	_ "example.com/tbcg/server/modules/world"
)

type config struct {
	DatabaseURL    string
	GameID         string
	RealmID        string
	WSBind         string
	HTTPBind       string
	TLSCert        string
	TLSKey         string
	ProjectRoot    string
	ArtifactDir    string
	DevContent     bool
	Bootstrap      bool
	ClientMinBuild string
	AllowedOrigins []string
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func loadConfig() (config, error) {
	cfg := config{
		DatabaseURL: os.Getenv("TBCG_GAME_DATABASE_URL"), GameID: env("TBCG_GAME_ID", "demo"), RealmID: env("TBCG_REALM_ID", "main"),
		WSBind: env("TBCG_GAME_WS_BIND", "127.0.0.1:7443"), HTTPBind: env("TBCG_GAME_HTTP_BIND", "127.0.0.1:7080"),
		TLSCert: os.Getenv("TBCG_GAME_TLS_CERT"), TLSKey: os.Getenv("TBCG_GAME_TLS_KEY"), ProjectRoot: env("TBCG_PROJECT_ROOT", ".."),
		ArtifactDir: os.Getenv("TBCG_BUILD_ARTIFACT_DIR"), DevContent: os.Getenv("TBCG_GAME_DEV_CONTENT") == "1",
		Bootstrap: os.Getenv("TBCG_GAME_BOOTSTRAP_REALM") == "1", ClientMinBuild: env("TBCG_GAME_CLIENT_MIN_BUILD", "0.1.0"),
	}
	if origins := os.Getenv("TBCG_GAME_ALLOWED_ORIGINS"); origins != "" {
		cfg.AllowedOrigins = strings.Split(origins, ",")
	}
	if cfg.DatabaseURL == "" {
		return cfg, errors.New("TBCG_GAME_DATABASE_URL is required")
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return cfg, errors.New("TBCG_GAME_TLS_CERT and TBCG_GAME_TLS_KEY must be set together")
	}
	if cfg.TLSCert == "" && (!loopback(cfg.WSBind) || !loopback(cfg.HTTPBind)) {
		return cfg, errors.New("non-TLS game server may only bind loopback addresses")
	}
	if cfg.ArtifactDir == "" && !cfg.DevContent {
		return cfg, errors.New("TBCG_BUILD_ARTIFACT_DIR is required unless TBCG_GAME_DEV_CONTENT=1")
	}
	return cfg, nil
}

func loopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatalf("game server: %v", err)
	}
}

func run(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	root, err := filepath.Abs(cfg.ProjectRoot)
	if err != nil {
		return err
	}
	contracts, err := os.OpenRoot(filepath.Join(root, "contracts"))
	if err != nil {
		return fmt.Errorf("open contracts: %w", err)
	}
	defer func() { _ = contracts.Close() }()
	registry, err := schema.NewRegistry(contracts.FS())
	if err != nil {
		return err
	}
	geometry, err := mapdata.NewValidator(contracts.FS())
	if err != nil {
		return err
	}
	protocol, err := rt.LoadCatalog(filepath.Join(root, "contracts", "protocol", "message_catalog.json"))
	if err != nil {
		return err
	}
	loader := &rt.ReleaseLoader{Registry: registry, Geometry: geometry}
	if cfg.ArtifactDir != "" {
		store, err := contentbuild.OpenArtifactStore(cfg.ArtifactDir)
		if err != nil {
			return err
		}
		defer func() { _ = store.Close() }()
		loader.Store = store
	}
	pgConfig, err := pgx.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("parse database URL: %w", err)
	}
	if !loopback(net.JoinHostPort(pgConfig.Host, "0")) {
		// sslmode=prefer/allow 会附带明文回落；非 loopback 时要求所有候选连接都启用 TLS。
		if pgConfig.TLSConfig == nil {
			return errors.New("non-loopback PostgreSQL connections must use TLS")
		}
		for _, fallback := range pgConfig.Fallbacks {
			if fallback.TLSConfig == nil {
				return errors.New("non-loopback PostgreSQL connections must not allow plaintext fallback")
			}
		}
	}
	db := stdlib.OpenDB(*pgConfig)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(16)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	ownerConn, epoch, err := rt.AcquireOwnership(ctx, db, cfg.GameID, cfg.RealmID, cfg.Bootstrap)
	if err != nil {
		return err
	}
	defer func() { _ = ownerConn.Close() }()
	realm := rt.NewRealm(cfg.GameID, cfg.RealmID, db, protocol, loader)
	realm.Epoch = epoch
	if err := realm.LoadActive(ctx); err != nil {
		return err
	}
	var devBundle []byte
	if cfg.DevContent && realm.Content() == nil {
		devDir, err := os.OpenRoot(filepath.Join(root, "games", cfg.GameID, "content"))
		if err != nil {
			return fmt.Errorf("open dev content: %w", err)
		}
		defer func() { _ = devDir.Close() }()
		loader.DevDir = devDir.FS()
		content, info, bundle, err := loader.LoadDev(cfg.GameID)
		if err != nil {
			return err
		}
		realm.UseDevContent(content, info)
		devBundle = bundle
		log.Printf("WARNING: serving dev-local content from games/%s/content (not a built Release)", cfg.GameID)
	}
	auth, err := rt.NewAuth(db, cfg.GameID)
	if err != nil {
		return err
	}
	go realm.Run(ctx)
	go keepOwnership(ctx, ownerConn, abortOnOwnershipLoss)

	api := &rt.HTTPAPI{Realm: realm, Auth: auth, Loader: loader, DevBundle: devBundle}
	httpServer := &http.Server{Addr: cfg.HTTPBind, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	go func() {
		var err error
		if cfg.TLSCert != "" {
			err = httpServer.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			err = httpServer.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("game HTTP server stopped: %v", err)
		}
	}()
	wsServer := &network.WSServer{Addr: cfg.WSBind, MaxConnNum: 1024, PendingWriteNum: 256, MaxMsgLen: rt.MaxMessageBytes,
		HTTPTimeout: 10 * time.Second, CertFile: cfg.TLSCert, KeyFile: cfg.TLSKey, CheckOrigin: originChecker(cfg.AllowedOrigins),
		NewAgent: func(conn *network.WSConn) network.Agent { return rt.NewSession(conn, realm, auth, cfg.ClientMinBuild) }}
	wsServer.Start()
	scheme := "ws"
	if cfg.TLSCert != "" {
		scheme = "wss"
	}
	log.Printf("game server realm=%s/%s epoch=%d release=%s ws=%s://%s http=%s", cfg.GameID, cfg.RealmID, epoch, realm.Release().ReleaseID, scheme, cfg.WSBind, cfg.HTTPBind)
	<-ctx.Done()
	wsServer.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdown)
}

// keepOwnership 定期检查 advisory lock 连接；连接丢失意味着所有权可能被他人取得，进程必须退出。
func keepOwnership(ctx context.Context, conn *sql.Conn, abort func()) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := conn.PingContext(ctx); err != nil {
				log.Printf("realm ownership connection lost: %v", err)
				abort()
				return
			}
		}
	}
}

func abortOnOwnershipLoss() {
	log.Printf("stopping: realm ownership lost")
	os.Exit(3)
}

func originChecker(allowed []string) func(*http.Request) bool {
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // 原生 Godot 客户端不发送 Origin；浏览器来源必须在允许列表中。
		}
		for _, candidate := range allowed {
			if strings.TrimSpace(candidate) == origin {
				return true
			}
		}
		return false
	}
}
