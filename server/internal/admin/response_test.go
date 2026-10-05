package admin

import (
	"errors"
	"net/http"
	"testing"
)

type failedResponseWriter struct {
	header http.Header
}

func (w *failedResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (*failedResponseWriter) WriteHeader(int) {}

func (*failedResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("test response writer failure")
}

func TestClientIPAndResponseWriteFailureBranches(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatalf("create client IP request: %v", err)
	}
	request.RemoteAddr = "127.0.0.1:43210"
	if got := clientIP(request); got != "127.0.0.1" {
		t.Fatalf("client IP with port = %q", got)
	}
	request.RemoteAddr = "malformed-remote-addr"
	if got := clientIP(request); got != "malformed-remote-addr" {
		t.Fatalf("client IP without host/port = %q", got)
	}

	writeJSON(&failedResponseWriter{}, http.StatusOK, envelope{Success: true, Data: map[string]any{}, Meta: map[string]any{}})
}
