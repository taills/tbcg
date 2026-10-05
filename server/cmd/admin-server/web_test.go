package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebHandlerServesBuiltSPAAndOnlyDemoAssets(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "admin", "dist", "index.html"), "<html>admin-shell</html>")
	writeTestFile(t, filepath.Join(root, "admin", "dist", "assets", "app.js"), "console.info('app')")
	writeTestFile(t, filepath.Join(root, "games", "demo", "assets", "manifest.json"), `{"schema_version":1,"assets":[]}`)
	writeTestFile(t, filepath.Join(root, "games", "demo", "assets", "icons", "placeholder.svg"), "<svg></svg>")
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false}`))
	})
	web, err := newWebHandler(api, root)
	if err != nil {
		t.Fatalf("build local admin web handler: %v", err)
	}
	t.Cleanup(func() {
		if err := web.Close(); err != nil {
			t.Errorf("close local admin web roots: %v", err)
		}
	})

	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/", want: "admin-shell"},
		{path: "/?draftId=abc", want: "admin-shell"},
		{path: "/assets/app.js", want: "console.info"},
		{path: "/assets/demo/icons/placeholder.svg", want: "<svg"},
	} {
		response := httptest.NewRecorder()
		web.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.want) {
			t.Errorf("GET %s response = %d %q", test.path, response.Code, response.Body.String())
		}
		if response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s omitted security headers", test.path)
		}
	}
	apiResponse := httptest.NewRecorder()
	web.ServeHTTP(apiResponse, httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil))
	if apiResponse.Code != http.StatusNotFound || apiResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("API fallback response = %d cache=%q", apiResponse.Code, apiResponse.Header().Get("Cache-Control"))
	}
}

func TestWebHandlerRequiresExistingAssetDirectories(t *testing.T) {
	t.Run("missing dist root", func(t *testing.T) {
		if _, err := newWebHandler(http.NotFoundHandler(), t.TempDir()); err == nil {
			t.Fatal("web handler accepted a missing dist root")
		}
	})
	t.Run("missing asset root", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "admin", "dist", "index.html"), "<html>admin</html>")
		if _, err := newWebHandler(http.NotFoundHandler(), root); err == nil {
			t.Fatal("web handler accepted a missing asset root")
		}
	})
}

func TestWebHandlerRequiresBuiltIndexAndAssetManifest(t *testing.T) {
	for _, test := range []struct {
		name      string
		writeFile string
	}{
		{name: "missing index"},
		{name: "missing asset manifest", writeFile: "admin/dist/index.html"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "admin", "dist"), 0o700); err != nil {
				t.Fatalf("create admin dist root: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(root, "games", "demo", "assets"), 0o700); err != nil {
				t.Fatalf("create demo asset root: %v", err)
			}
			if test.writeFile != "" {
				writeTestFile(t, filepath.Join(root, test.writeFile), "<html>admin</html>")
			}
			if _, err := newWebHandler(http.NotFoundHandler(), root); err == nil {
				t.Fatal("web handler accepted a missing required root file")
			}
		})
	}
}

func TestWebHandlerDoesNotFollowAssetSymlinksOutsideRoot(t *testing.T) {
	projectRoot := t.TempDir()
	outsideRoot := t.TempDir()
	secretPath := filepath.Join(outsideRoot, "sentinel.txt")
	writeTestFile(t, filepath.Join(projectRoot, "admin", "dist", "index.html"), "<html>admin-shell</html>")
	writeTestFile(t, filepath.Join(projectRoot, "games", "demo", "assets", "manifest.json"), `{"schema_version":1,"assets":[]}`)
	writeTestFile(t, secretPath, "outside-root-sentinel")
	if err := os.Symlink(secretPath, filepath.Join(projectRoot, "games", "demo", "assets", "sentinel.txt")); err != nil {
		t.Fatalf("create asset symlink fixture: %v", err)
	}
	web, err := newWebHandler(http.NotFoundHandler(), projectRoot)
	if err != nil {
		t.Fatalf("build local admin web handler: %v", err)
	}
	t.Cleanup(func() {
		if err := web.Close(); err != nil {
			t.Errorf("close local admin web roots: %v", err)
		}
	})
	response := httptest.NewRecorder()
	web.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/demo/sentinel.txt", nil))
	if strings.Contains(response.Body.String(), "outside-root-sentinel") {
		t.Fatal("asset handler read a symlink target outside its trusted root")
	}
}

func writeTestFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatalf("create test static directory: %v", err)
	}
	if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
		t.Fatalf("write test static file: %v", err)
	}
}
