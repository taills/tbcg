package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsSchemaSymlinkOutsideTrustedRoot(t *testing.T) {
	projectRoot := t.TempDir()
	contentRoot := filepath.Join(projectRoot, "contracts", "content")
	if err := os.MkdirAll(contentRoot, 0o700); err != nil {
		t.Fatalf("创建测试专属 Schema 根目录失败：%v", err)
	}
	for _, name := range []string{"item.schema.json", "item-public.schema.json"} {
		data, err := os.ReadFile(filepath.Join("../../../contracts/content", name))
		if err != nil {
			t.Fatalf("读取仓库受信任测试 Schema 失败：%v", err)
		}
		path := filepath.Join(contentRoot, name)
		if name == "item.schema.json" {
			outside := filepath.Join(t.TempDir(), "outside-sentinel.schema.json")
			if err := os.WriteFile(outside, data, 0o600); err != nil {
				t.Fatalf("创建无敏感数据的专属越界 sentinel 失败：%v", err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatalf("创建测试专属越界 Schema symlink 失败：%v", err)
			}
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("创建受信任测试 Schema 失败：%v", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, "games", "demo", "assets"), 0o700); err != nil {
		t.Fatalf("创建测试资产根目录失败：%v", err)
	}
	values := map[string]string{
		"TBCG_ADMIN_DATABASE_URL": "postgresql://user:pass@127.0.0.1:55432/test_db?sslmode=disable",
		"TBCG_ADMIN_TLS_CERT":     "/tmp/not-loaded-before-schema-boundary.crt",
		"TBCG_ADMIN_TLS_KEY":      "/tmp/not-loaded-before-schema-boundary.key",
		"TBCG_PROJECT_ROOT":       projectRoot,
	}
	err := run(context.Background(), func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err == nil || !strings.Contains(err.Error(), "open trusted schema") {
		t.Fatalf("越界 Schema symlink 未在打开文件时拒绝：%v", err)
	}
}
