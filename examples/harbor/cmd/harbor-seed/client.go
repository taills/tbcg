package main

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"time"
)

// adminClient 像管理前端一样调用管理 API：会话 Cookie、会话绑定的 CSRF 令牌与精确 Origin。
type adminClient struct {
	base string
	http *http.Client
	csrf string
}

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Meta map[string]any `json:"meta"`
}

// apiError 保留 HTTP 状态与错误码，调用方据此区分「已完成」与真正的失败。
type apiError struct {
	Status int
	Code   string
	Detail string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, e.Detail)
}

func newAdminClient(base, caFile string) (*adminClient, error) {
	pem, err := os.ReadFile(caFile) // #nosec G304 -- CA 路径由部署配置显式提供。
	if err != nil {
		return nil, fmt.Errorf("read admin CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("admin CA %s contains no certificate", caFile)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	return &adminClient{base: base, http: &http.Client{Jar: jar, Transport: transport, Timeout: 30 * time.Second}}, nil
}

func randomKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "harbor-seed-" + hex.EncodeToString(b)
}

// rateLimitWait 是被管理服务限流（每 IP 每分钟 120 次）后的退避间隔。
const rateLimitWait = 5 * time.Second

// do 发送请求并解出 data；被限流时退避后重发同一请求（同一个 Idempotency-Key）。
func (c *adminClient) do(method, path string, body any, idempotent bool, out any) (map[string]any, error) {
	key := ""
	if idempotent {
		key = randomKey()
	}
	for attempt := 1; ; attempt++ {
		meta, err := c.once(method, path, body, key, out)
		var failure *apiError
		if !errors.As(err, &failure) || failure.Status != http.StatusTooManyRequests || attempt >= 60 {
			return meta, err
		}
		if attempt == 1 {
			log.Printf("admin API rate limit reached; pacing requests")
		}
		time.Sleep(rateLimitWait)
	}
}

func (c *adminClient) once(method, path string, body any, idempotencyKey string, out any) (map[string]any, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		request.Header.Set("Origin", c.base)
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	var decoded envelope
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("%s %s: HTTP %d with undecodable body: %w", method, path, response.StatusCode, err)
	}
	if !decoded.Success || response.StatusCode >= 300 {
		failure := &apiError{Status: response.StatusCode}
		if decoded.Error != nil {
			failure.Code, failure.Detail = decoded.Error.Code, decoded.Error.Message
		}
		return nil, failure
	}
	if out != nil {
		if err := json.Unmarshal(decoded.Data, out); err != nil {
			return nil, fmt.Errorf("%s %s: decode data: %w", method, path, err)
		}
	}
	return decoded.Meta, nil
}

type session struct {
	Authenticated bool   `json:"authenticated"`
	CSRFToken     string `json:"csrf_token"`
}

// login 先取匿名会话的 CSRF 令牌，再登录（登录会轮换会话与令牌）。
func (c *adminClient) login(username, password string) error {
	var anonymous session
	if _, err := c.do(http.MethodGet, "/api/v1/session", nil, false, &anonymous); err != nil {
		return err
	}
	c.csrf = anonymous.CSRFToken
	var current session
	if _, err := c.do(http.MethodPost, "/api/v1/login", map[string]string{"username": username, "password": password}, false, &current); err != nil {
		return fmt.Errorf("login as %s: %w", username, err)
	}
	if !current.Authenticated {
		return fmt.Errorf("login as %s did not authenticate", username)
	}
	c.csrf = current.CSRFToken
	return nil
}

// waitReady 等待管理服务开始接受 TLS 连接（容器编排中它可能比本工具晚就绪）。
func (c *adminClient) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := c.do(http.MethodGet, "/api/v1/session", nil, false, nil)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("admin server not ready: %w", err)
		}
		time.Sleep(2 * time.Second)
	}
}
