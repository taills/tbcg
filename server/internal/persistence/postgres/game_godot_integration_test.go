package postgres

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/taills/leaf/v2/network"
	"github.com/taills/tbcg/server/internal/content/mapdata"
	"github.com/taills/tbcg/server/internal/content/schema"
	rt "github.com/taills/tbcg/server/modules/runtime"
)

// TestGodotClientAgainstGameServer 以 headless Godot 运行真实客户端框架连接真实 game-server。
func TestGodotClientAgainstGameServer(t *testing.T) {
	godot := os.Getenv("TBCG_GODOT_BIN")
	if godot == "" {
		godot = "/Applications/Godot.app/Contents/MacOS/Godot"
	}
	if _, err := os.Stat(godot); err != nil {
		t.Skip("Godot 4.7.1 binary not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db := newGameTestDatabase(t, ctx)
	if _, err := db.ExecContext(ctx, `INSERT INTO games (game_id, display_name) VALUES ('demo','Demo')`); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(findServerRoot(t))
	contracts := os.DirFS(filepath.Join(root, "contracts"))
	registry, err := schema.NewRegistry(contracts)
	if err != nil {
		t.Fatal(err)
	}
	geometry, err := mapdata.NewValidator(contracts)
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := rt.LoadCatalog(filepath.Join(root, "contracts/protocol/message_catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	loader := &rt.ReleaseLoader{Registry: registry, Geometry: geometry, DevDir: os.DirFS(filepath.Join(root, "games/demo/content"))}
	ownerConn, epoch, err := rt.AcquireOwnership(ctx, db, "demo", "main", true)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerConn.Close()
	realm := rt.NewRealm("demo", "main", db, protocol, loader)
	realm.Epoch = epoch
	content, info, bundle, err := loader.LoadDev("demo")
	if err != nil {
		t.Fatal(err)
	}
	realm.UseDevContent(content, info)
	go realm.Run(ctx)
	runGodotProbe(t, ctx, godot, root, db, realm, loader, bundle)
}

// runGodotProbe 启动 HTTP/WSS 服务并以 headless Godot 执行客户端探针。
func runGodotProbe(t *testing.T, ctx context.Context, godot, root string, db *sql.DB, realm *rt.Realm, loader *rt.ReleaseLoader, bundle []byte) {
	t.Helper()
	auth, err := rt.NewAuth(db, "demo")
	if err != nil {
		t.Fatal(err)
	}
	httpAddress, wsAddress := freeAddress(t), freeAddress(t)
	server := &http.Server{Addr: httpAddress, Handler: (&rt.HTTPAPI{Realm: realm, Auth: auth, Loader: loader, DevBundle: bundle}).Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.ListenAndServe() }()
	defer server.Close()
	wsServer := &network.WSServer{Addr: wsAddress, MaxConnNum: 8, PendingWriteNum: 128, MaxMsgLen: rt.MaxMessageBytes, HTTPTimeout: 5 * time.Second,
		NewAgent: func(conn *network.WSConn) network.Agent { return rt.NewSession(conn, realm, auth, "0.1.0") }}
	wsServer.Start()
	defer wsServer.Close()
	time.Sleep(300 * time.Millisecond)
	command := exec.CommandContext(ctx, godot, "--headless", "--path", filepath.Join(root, "client"), "--script", "res://tests/network/game_client_probe.gd")
	command.Env = append(os.Environ(), "TBCG_PROBE_HTTP=http://"+httpAddress, "TBCG_PROBE_WS=ws://"+wsAddress)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "game_client_probe failures=0") {
		t.Fatalf("Godot probe failed: %v\n%s", err, output)
	}
	t.Logf("%s", output)
}
