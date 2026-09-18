package inventory

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// closedAddr is an address nothing listens on: a miniredis started and
// closed again, so the port is known to be free.
func closedAddr(t *testing.T) string {
	t.Helper()
	mr := miniredis.NewMiniRedis()
	if err := mr.Start(); err != nil {
		t.Fatal(err)
	}
	addr := mr.Addr()
	mr.Close()
	return addr
}

// The gazelle case (#6): the store is not there when the server starts. The
// store reports ErrUnavailable meanwhile, WaitConnected keeps retrying, and
// the store comes up on its own when Valkey appears.
func TestWaitConnectedComesUpWhenValkeyAppears(t *testing.T) {
	addr := closedAddr(t)
	st, err := New(addr, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Ping(ctx); !errors.Is(err, ErrUnavailable) || !strings.HasPrefix(err.Error(), "inventory unavailable") {
		t.Fatalf("ping before any connection: %v", err)
	}
	if _, err := st.Get(ctx, repoX); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("get before any connection: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- st.WaitConnected(ctx, 30*time.Second) }()
	time.Sleep(1200 * time.Millisecond) // the first attempt and its 1 s retry fail
	if err := st.Ping(ctx); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "not connected (") {
		t.Fatalf("ping while retrying should carry the last connect error: %v", err)
	}
	mr := miniredis.NewMiniRedis()
	if err := mr.StartAddr(addr); err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitConnected: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("WaitConnected did not return after Valkey appeared")
	}
	if err := st.Ping(ctx); err != nil {
		t.Fatalf("ping after connecting: %v", err)
	}
	if err := st.Put(ctx, &Record{Repository: repoX, Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.Get(ctx, repoX); err != nil || got.Name != "x" {
		t.Fatalf("get after connecting: %v, %v", got, err)
	}
}

// A store that stays away for the whole window is an error naming the
// window and the attempts; the store keeps reporting ErrUnavailable.
func TestWaitConnectedGivesUpAfterTheWindow(t *testing.T) {
	st, err := New(closedAddr(t), quiet)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = st.WaitConnected(context.Background(), 1500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not answer within 1.5s (") || !strings.Contains(err.Error(), "attempts, last error:") {
		t.Fatalf("expected the give-up error, got %v", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("gave up after %s, the window was 1.5 s", took)
	}
	if err := st.Ping(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ping after giving up: %v", err)
	}
}

// A cancelled context ends the wait with the cancellation, not the give-up.
func TestWaitConnectedStopsWhenCancelled(t *testing.T) {
	st, err := New(closedAddr(t), quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	if err := st.WaitConnected(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// Valkey lost at runtime: operations report ErrUnavailable with the
// transport error, and the client re-dials by itself once Valkey is back —
// the records survive.
func TestReconnectsAfterValkeyRestarts(t *testing.T) {
	mr := miniredis.RunT(t)
	st, err := Open(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Put(ctx, &Record{Repository: "giantswarm/y", Name: "y"}); err != nil {
		t.Fatal(err)
	}

	mr.Close()
	if err := st.Ping(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ping with Valkey gone: %v", err)
	}
	if _, err := st.Get(ctx, "giantswarm/y"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("get with Valkey gone: %v", err)
	}
	if _, err := st.Get(ctx, "giantswarm/missing"); errors.Is(err, ErrNotFound) {
		t.Fatalf("a lost store must not read as a missing record: %v", err)
	}

	if err := mr.Restart(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		err := st.Ping(ctx)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no reconnect after Valkey restarted: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got, err := st.Get(ctx, "giantswarm/y"); err != nil || got.Name != "y" {
		t.Fatalf("record after the restart: %v, %v", got, err)
	}
}

// A Valkey reply that is not a transport error stays the operation's own.
func TestReplyErrorsAreNotUnavailable(t *testing.T) {
	mr := miniredis.RunT(t)
	st, err := Open(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Get(context.Background(), "giantswarm/none"); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing record: %v", err)
	}
}
