package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type serverConfig struct {
	DatabaseURL  string
	BindAddress  string
	Origin       string
	TLSCert      string
	TLSKey       string
	CookieSecure bool
	SessionTTL   time.Duration
	IdleTTL      time.Duration
	MaxBodyBytes int64
	ProjectRoot  string
}

func parseServerConfig(values map[string]string) (serverConfig, error) {
	config := serverConfig{
		DatabaseURL:  strings.TrimSpace(values["TBCG_ADMIN_DATABASE_URL"]),
		BindAddress:  strings.TrimSpace(values["TBCG_ADMIN_BIND"]),
		Origin:       strings.TrimSpace(values["TBCG_ADMIN_ORIGIN"]),
		TLSCert:      strings.TrimSpace(values["TBCG_ADMIN_TLS_CERT"]),
		TLSKey:       strings.TrimSpace(values["TBCG_ADMIN_TLS_KEY"]),
		ProjectRoot:  strings.TrimSpace(values["TBCG_PROJECT_ROOT"]),
		SessionTTL:   12 * time.Hour,
		IdleTTL:      30 * time.Minute,
		MaxBodyBytes: 1 << 20,
	}
	if config.DatabaseURL == "" {
		return serverConfig{}, errors.New("TBCG_ADMIN_DATABASE_URL is required")
	}
	if config.BindAddress == "" {
		config.BindAddress = "127.0.0.1:8443"
	}
	if config.ProjectRoot == "" {
		root, err := discoverProjectRoot()
		if err != nil {
			return serverConfig{}, fmt.Errorf("resolve project root: %w", err)
		}
		config.ProjectRoot = root
	}
	if config.TLSCert == "" || config.TLSKey == "" {
		return serverConfig{}, errors.New("TBCG_ADMIN_TLS_CERT and TBCG_ADMIN_TLS_KEY are required")
	}
	_, _, err := net.SplitHostPort(config.BindAddress)
	if err != nil {
		return serverConfig{}, fmt.Errorf("TBCG_ADMIN_BIND must be host:port: %w", err)
	}
	config.CookieSecure = true
	if config.Origin == "" {
		config.Origin = "https://" + config.BindAddress
	}
	origin, parseErr := url.Parse(config.Origin)
	if parseErr != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return serverConfig{}, errors.New("TLS listener requires an exact https TBCG_ADMIN_ORIGIN")
	}
	if duration := strings.TrimSpace(values["TBCG_ADMIN_SESSION_TTL"]); duration != "" {
		config.SessionTTL, err = time.ParseDuration(duration)
		if err != nil || config.SessionTTL <= 0 {
			return serverConfig{}, errors.New("TBCG_ADMIN_SESSION_TTL must be a positive duration")
		}
	}
	if duration := strings.TrimSpace(values["TBCG_ADMIN_IDLE_TTL"]); duration != "" {
		config.IdleTTL, err = time.ParseDuration(duration)
		if err != nil || config.IdleTTL <= 0 {
			return serverConfig{}, errors.New("TBCG_ADMIN_IDLE_TTL must be a positive duration")
		}
	}
	if config.IdleTTL > config.SessionTTL {
		return serverConfig{}, errors.New("TBCG_ADMIN_IDLE_TTL cannot exceed absolute session TTL")
	}
	if bodyLimit := strings.TrimSpace(values["TBCG_ADMIN_MAX_BODY_BYTES"]); bodyLimit != "" {
		config.MaxBodyBytes, err = strconv.ParseInt(bodyLimit, 10, 64)
		if err != nil || config.MaxBodyBytes < 1024 || config.MaxBodyBytes > 8<<20 {
			return serverConfig{}, errors.New("TBCG_ADMIN_MAX_BODY_BYTES must be between 1024 and 8388608")
		}
	}
	return config, nil
}

func discoverProjectRoot() (string, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for directory := workingDirectory; ; directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, "contracts", "content", "item.schema.json")); err == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("project root with contracts/content/item.schema.json was not found")
		}
	}
}
