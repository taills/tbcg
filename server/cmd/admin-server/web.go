package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type webHandler struct {
	handler   http.Handler
	distRoot  *os.Root
	assetRoot *os.Root
}

func newWebHandler(api http.Handler, projectRoot string) (*webHandler, error) {
	distRoot, err := os.OpenRoot(filepath.Join(projectRoot, "admin", "dist"))
	if err != nil {
		return nil, fmt.Errorf("open built admin asset root: %w", err)
	}
	assetRoot, err := os.OpenRoot(filepath.Join(projectRoot, "games", "demo", "assets"))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open demo asset root: %w", err), distRoot.Close())
	}
	if err := requireRootFile(distRoot, "index.html"); err != nil {
		return nil, errors.Join(fmt.Errorf("locate built admin index: %w", err), distRoot.Close(), assetRoot.Close())
	}
	if err := requireRootFile(assetRoot, "manifest.json"); err != nil {
		return nil, errors.Join(fmt.Errorf("locate demo asset manifest: %w", err), distRoot.Close(), assetRoot.Close())
	}

	static := http.FileServer(http.FS(distRoot.FS()))
	assets := http.StripPrefix("/assets/demo/", http.FileServer(http.FS(assetRoot.FS())))
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/assets/demo/", assets)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "\\") {
			r.URL.Path = "/"
			static.ServeHTTP(w, r)
			return
		}
		requested := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if requested != "." && requested != "" && rootFileExists(distRoot, requested) {
			static.ServeHTTP(w, r)
			return
		}
		r.URL.Path = "/"
		static.ServeHTTP(w, r)
	})
	return &webHandler{handler: securityHeaders(mux), distRoot: distRoot, assetRoot: assetRoot}, nil
}

func (h *webHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handler.ServeHTTP(w, r)
}

func (h *webHandler) Close() error {
	return errors.Join(h.distRoot.Close(), h.assetRoot.Close())
}

func requireRootFile(root *os.Root, name string) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a directory", name)
	}
	return nil
}

func rootFileExists(root *os.Root, name string) bool {
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	return err == nil && !info.IsDir()
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
