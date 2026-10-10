package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// A rollout's readiness, end to end on a real listener: /readyz answers 200
// only once the server listens — the MCP endpoint answering on the same port —
// and after the context ends the server drains: /readyz is 503 while the MCP
// endpoint still answers, and only after the shutdown delay is the port closed.
// The chart's maxSurge 1 / maxUnavailable 0 then keeps one serving pod behind
// the Service throughout.
func TestReadyzFollowsTheListenerAndDrains(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	const delay = 500 * time.Millisecond
	s, err := New(Config{Addr: addr, ShutdownDelay: delay, Ready: func(context.Context) error { return nil }}, mcpserver.NewMCPServer("test", "0"), nil)
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + addr
	client := &http.Client{Timeout: 2 * time.Second}
	get := func(path string) (int, string, error) {
		resp, err := client.Get(base + path)
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), nil
	}
	initialize := func() (int, error) {
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}`
		req, _ := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}

	if _, _, err := get("/readyz"); err == nil {
		t.Fatal("/readyz answered before the server listens")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		code, _, err := get("/readyz")
		if err == nil {
			if code != http.StatusOK {
				t.Fatalf("/readyz on a listening, ready server: %d, want 200", code)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("/readyz never answered: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, err := initialize(); err != nil || code != http.StatusOK {
		t.Fatalf("MCP initialize while ready: %d %v, want 200", code, err)
	}

	cancel()
	stopped := time.Now()
	deadline = time.Now().Add(delay / 2)
	for {
		code, body, err := get("/readyz")
		if err != nil {
			t.Fatalf("/readyz refused during the drain: %v", err)
		}
		if code == http.StatusServiceUnavailable {
			if !strings.Contains(body, "shutting down") {
				t.Fatalf("/readyz while draining: %q, want it to say shutting down", body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("/readyz still %d after the context ended", code)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, err := initialize(); err != nil || code != http.StatusOK {
		t.Fatalf("MCP initialize while draining: %d %v, want 200", code, err)
	}

	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if waited := time.Since(stopped); waited < delay {
		t.Fatalf("Run returned %v after the context ended, before the shutdown delay %v", waited, delay)
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("the port still accepts connections after Run returned")
	}
}
