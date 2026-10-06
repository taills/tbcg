// Command harbor-seed 通过框架管理 API 把「雾港奇谭」走一遍完整发布流程：
// 导入草稿 → 提交不可变 revision → 审核批准 → 受限 worker 构建快照 → 请求激活并等待游戏进程确认。
// 只使用公开 HTTP API，不直接写数据库；重复运行是安全的（Realm 已有活动 Release 时直接退出）。
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type config struct {
	adminURL, caFile, contentDir, gameID, realmID, username, password string
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.adminURL, "admin-url", env("HARBOR_ADMIN_URL", "https://127.0.0.1:8443"), "管理服务 Origin")
	flag.StringVar(&cfg.caFile, "ca", env("HARBOR_ADMIN_CA", "/certs/ca.crt"), "签发管理服务证书的 CA")
	flag.StringVar(&cfg.contentDir, "content", env("HARBOR_CONTENT_DIR", "/app/games/harbor/content"), "内容目录")
	flag.StringVar(&cfg.gameID, "game", "harbor", "Game ID")
	flag.StringVar(&cfg.realmID, "realm", env("HARBOR_REALM_ID", "main"), "要激活的 Realm")
	flag.Parse()
	cfg.username, cfg.password = os.Getenv("HARBOR_ADMIN_USERNAME"), os.Getenv("HARBOR_ADMIN_PASSWORD")
	if cfg.username == "" || cfg.password == "" {
		log.Fatal("HARBOR_ADMIN_USERNAME and HARBOR_ADMIN_PASSWORD are required")
	}
	if err := run(cfg); err != nil {
		log.Fatalf("harbor-seed: %v", err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

type releaseListing struct {
	Releases []struct {
		ReleaseID string `json:"release_id"`
		Status    string `json:"status"`
	} `json:"releases"`
	Realms []struct {
		RealmID         string  `json:"realm_id"`
		ActiveReleaseID *string `json:"active_release_id"`
	} `json:"realms"`
	Activations []struct {
		RealmID   string  `json:"realm_id"`
		ReleaseID string  `json:"release_id"`
		Status    string  `json:"status"`
		ErrorCode *string `json:"error_code"`
	} `json:"activations"`
}

func run(cfg config) error {
	client, err := newAdminClient(strings.TrimRight(cfg.adminURL, "/"), cfg.caFile)
	if err != nil {
		return err
	}
	if err := client.waitReady(3 * time.Minute); err != nil {
		return err
	}
	if err := client.login(cfg.username, cfg.password); err != nil {
		return err
	}
	game := "/api/v1/games/" + url.PathEscape(cfg.gameID)
	listing, err := releases(client, game)
	if err != nil {
		return err
	}
	if active := activeRelease(listing, cfg.realmID); active != "" {
		log.Printf("realm %s already serves release %s; nothing to do", cfg.realmID, active)
		return nil
	}
	releaseID := builtRelease(listing)
	if releaseID == "" {
		if err := importAndSubmit(client, game, cfg.contentDir); err != nil {
			return err
		}
		if err := approveQueue(client, game); err != nil {
			return err
		}
		if releaseID, err = buildSnapshot(client, game); err != nil {
			return err
		}
	}
	return activate(client, game, cfg.realmID, releaseID)
}

func releases(client *adminClient, game string) (releaseListing, error) {
	var listing releaseListing
	_, err := client.do(http.MethodGet, game+"/releases", nil, false, &listing)
	return listing, err
}

func activeRelease(listing releaseListing, realmID string) string {
	for _, realm := range listing.Realms {
		if realm.RealmID == realmID && realm.ActiveReleaseID != nil {
			return *realm.ActiveReleaseID
		}
	}
	return ""
}

func builtRelease(listing releaseListing) string {
	for _, release := range listing.Releases {
		if release.Status == "built" {
			return release.ReleaseID
		}
	}
	return ""
}

// importAndSubmit 以 Author 身份批量导入（只建新草稿，不覆盖），再把每份草稿提交为不可变 revision。
func importAndSubmit(client *adminClient, game, contentDir string) error {
	definitions, err := readDefinitions(contentDir)
	if err != nil {
		return fmt.Errorf("read content: %w", err)
	}
	var results []struct {
		ContentID string `json:"content_id"`
		Status    string `json:"status"`
		DraftID   string `json:"draft_id"`
		Errors    []any  `json:"errors"`
	}
	if _, err := client.do(http.MethodPost, game+"/content-imports", map[string]any{"dry_run": false, "definitions": definitions}, false, &results); err != nil {
		return fmt.Errorf("import: %w", err)
	}
	created, submitted := 0, 0
	for _, result := range results {
		switch result.Status {
		case "invalid":
			return fmt.Errorf("import rejected %s: %v", result.ContentID, result.Errors)
		case "created":
			created++
		}
		var draft struct {
			DraftVersion string `json:"draft_version"`
		}
		path := game + "/content-drafts/" + url.PathEscape(result.DraftID)
		if _, err := client.do(http.MethodGet, path, nil, false, &draft); err != nil {
			return fmt.Errorf("read draft %s: %w", result.ContentID, err)
		}
		_, err := client.do(http.MethodPost, path+"/submit", map[string]string{"expected_draft_version": draft.DraftVersion}, false, nil)
		var failure *apiError
		if errors.As(err, &failure) && failure.Status == http.StatusConflict {
			continue // 上次运行已提交过同一版本
		}
		if err != nil {
			return fmt.Errorf("submit %s: %w", result.ContentID, err)
		}
		submitted++
	}
	log.Printf("imported %d definitions (%d new drafts), submitted %d revisions", len(results), created, submitted)
	return nil
}

// readDefinitions 在 os.Root 内读取全部 *.json 定义，路径无法逃出内容目录；与框架一样拒绝符号链接文件。
func readDefinitions(contentDir string) ([]any, error) {
	root, err := os.OpenRoot(contentDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var definitions []any
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(name, ".json") {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("content file %s must not be a symlink", name)
		}
		data, err := fs.ReadFile(root.FS(), name)
		if err != nil {
			return err
		}
		definitions = append(definitions, json.RawMessage(data))
		return nil
	})
	return definitions, err
}

// approveQueue 以 Reviewer 身份逐条批准审核队列中的确切 revision 与 body hash。
func approveQueue(client *adminClient, game string) error {
	approved := 0
	for {
		var queue []struct {
			ContentID string `json:"content_id"`
			Revision  int    `json:"revision"`
			BodyHash  string `json:"body_hash"`
		}
		if _, err := client.do(http.MethodGet, game+"/review-queue?limit=100&offset=0", nil, false, &queue); err != nil {
			return fmt.Errorf("review queue: %w", err)
		}
		if len(queue) == 0 {
			log.Printf("approved %d revisions", approved)
			return nil
		}
		for _, revision := range queue {
			body := map[string]any{"content_id": revision.ContentID, "revision": revision.Revision, "body_hash": revision.BodyHash,
				"decision": "approved", "comment": "harbor-seed：示例内容初始发布"}
			if _, err := client.do(http.MethodPost, game+"/revision-approvals", body, false, nil); err != nil {
				return fmt.Errorf("approve %s r%d: %w", revision.ContentID, revision.Revision, err)
			}
			approved++
		}
	}
}

// buildSnapshot 以 Publisher 身份请求完整 Game 快照构建，并等待受限 worker 产出 built Release。
func buildSnapshot(client *adminClient, game string) (string, error) {
	var job struct {
		JobID     string `json:"job_id"`
		ReleaseID string `json:"release_id"`
		Status    string `json:"status"`
	}
	if _, err := client.do(http.MethodPost, game+"/snapshot-builds", nil, true, &job); err != nil {
		return "", fmt.Errorf("start snapshot build: %w", err)
	}
	log.Printf("snapshot build %s queued for release %s", job.JobID, job.ReleaseID)
	deadline := time.Now().Add(5 * time.Minute)
	for job.Status != "built" {
		if job.Status == "failed" {
			return "", fmt.Errorf("snapshot build %s failed", job.JobID)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("snapshot build %s still %s; is content-build-worker running?", job.JobID, job.Status)
		}
		time.Sleep(2 * time.Second)
		if _, err := client.do(http.MethodGet, game+"/snapshot-builds/"+url.PathEscape(job.JobID), nil, false, &job); err != nil {
			return "", fmt.Errorf("poll snapshot build: %w", err)
		}
	}
	log.Printf("release %s built", job.ReleaseID)
	return job.ReleaseID, nil
}

// activate 请求激活并等待游戏进程（Realm owner）校验后切换活动指针。
func activate(client *adminClient, game, realmID, releaseID string) error {
	path := game + "/realms/" + url.PathEscape(realmID) + "/activations"
	_, err := client.do(http.MethodPost, path, map[string]string{"release_id": releaseID}, true, nil)
	var failure *apiError
	if err != nil && !(errors.As(err, &failure) && failure.Code == "activation_pending") {
		return fmt.Errorf("request activation: %w", err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		listing, err := releases(client, game)
		if err != nil {
			return err
		}
		if activeRelease(listing, realmID) == releaseID {
			log.Printf("realm %s now serves release %s", realmID, releaseID)
			return nil
		}
		for _, request := range listing.Activations {
			if request.RealmID == realmID && request.ReleaseID == releaseID && request.Status == "rejected" {
				code := ""
				if request.ErrorCode != nil {
					code = *request.ErrorCode
				}
				return fmt.Errorf("game server rejected release %s: %s", releaseID, code)
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("release %s not confirmed by realm %s; is game-server running?", releaseID, realmID)
}
