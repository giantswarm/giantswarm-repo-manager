package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthEndpoints(t *testing.T) {
	handler := newHandler()

	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d, want %d", path, rec.Code, http.StatusOK)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /: status %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestRunStopsWhenContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, "127.0.0.1:0") }()

	time.AfterFunc(50*time.Millisecond, cancel)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after the context was cancelled")
	}
}

func TestRunReportsAnUnusableAddress(t *testing.T) {
	if err := run(context.Background(), "256.256.256.256:1"); err == nil {
		t.Fatal("run: expected an error for an unusable listen address")
	}
}
