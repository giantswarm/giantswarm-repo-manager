package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// /readyz is the check's: no check or a nil error is 200, an error is 503
// with the reason, and /healthz stays 200 regardless — the pod is unready,
// not dead.
func TestReadyzReflectsTheCheck(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ready func(context.Context) error
		want  int
		body  string
	}{
		{"no check", nil, http.StatusOK, "ok"},
		{"ready", func(context.Context) error { return nil }, http.StatusOK, "ok"},
		{"not ready", func(context.Context) error { return errors.New("inventory unavailable: valkey:6379 not connected") }, http.StatusServiceUnavailable, "not ready: inventory unavailable: valkey:6379 not connected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New(Config{Addr: "127.0.0.1:0", Ready: tc.ready}, mcpserver.NewMCPServer("test", "0"), nil)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.body) {
				t.Fatalf("/readyz: %d %q, want %d containing %q", rec.Code, rec.Body.String(), tc.want, tc.body)
			}
			rec = httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("/healthz: %d, want 200", rec.Code)
			}
		})
	}
}
