// Package inventory is the Valkey-backed store of one record per repository
// of the org (D8). The store is a cache: the team files stay the desired
// state and GitHub the reality, so a lost store costs a rebuild and nothing
// else — and a store that is not there yet must not take the server down.
package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Keys: repository records under KeyPrefix<owner>/<name>, the last sweep's
// summary under SweepKey, the reconciler poller's cursor under ReconcilerKey.
const (
	KeyPrefix     = "repo:"
	SweepKey      = "inventory:sweep"
	ReconcilerKey = "inventory:reconciler"
)

// Connection timing: one dial is bounded by dialTimeout, and WaitConnected
// pauses between attempts with a doubling backoff between the two bounds.
const (
	dialTimeout           = 5 * time.Second
	connectInitialBackoff = time.Second
	connectMaxBackoff     = 10 * time.Second
)

// ErrNotFound is returned for a repository without a record.
var ErrNotFound = errors.New("inventory: no record")

// ErrUnavailable wraps every error that means the store cannot be reached:
// no connection yet, or a transport error once there was one. Callers tell a
// lost store from a bad request by it; the text starts with "inventory
// unavailable".
var ErrUnavailable = errors.New("inventory unavailable")

// Store is the inventory store. New creates it without a connection; Connect
// dials once and WaitConnected retries with backoff for a window. Every
// operation before the first connection returns ErrUnavailable. Once
// connected, the client re-dials a lost Valkey on the next command by itself;
// operations in between fail with ErrUnavailable and the transport error.
type Store struct {
	addr string
	log  *slog.Logger

	mu      sync.RWMutex
	client  valkey.Client // nil until Connect succeeded
	lastErr error         // the last failed Connect, reported while there is no client
}

// New creates the store for the Valkey at addr (host:port) without dialing.
func New(addr string, log *slog.Logger) (*Store, error) {
	if addr == "" {
		return nil, fmt.Errorf("inventory: Valkey address is required")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Store{addr: addr, log: log}, nil
}

// Open creates the store and connects at once, one attempt: the shape for
// tests and tools that need the store now.
func Open(addr string) (*Store, error) {
	s, err := New(addr, nil)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	if err := s.Connect(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Connect dials the store once. Success replaces any earlier client; failure
// is remembered and reported by every operation until a Connect succeeds.
func (s *Store) Connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// No command retry: the client would re-issue a command on a transport
	// error until the caller's context ends, so a lost Valkey would hang the
	// tools instead of answering "inventory unavailable". The store is a
	// cache; a failed command fails now, and the next one re-dials.
	c, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{s.addr}, DisableCache: true, DisableRetry: true,
		Dialer: net.Dialer{Timeout: dialTimeout}, ConnWriteTimeout: dialTimeout,
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastErr = err
		return fmt.Errorf("inventory: connect to %s: %w", s.addr, err)
	}
	if s.client != nil {
		s.client.Close()
	}
	s.client, s.lastErr = c, nil
	return nil
}

// WaitConnected retries Connect with backoff, logging every failed attempt,
// until the store answers, ctx ends, or the window passes (0: no window). It
// returns nil once connected; the error names the attempts otherwise.
func (s *Store) WaitConnected(ctx context.Context, window time.Duration) error {
	if window > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, window)
		defer cancel()
	}
	backoff := connectInitialBackoff
	for attempt := 1; ; attempt++ {
		err := s.Connect(ctx)
		if err == nil {
			if attempt > 1 {
				s.log.Info("inventory store answered", "address", s.addr, "attempts", attempt)
			}
			return nil
		}
		gaveUp := fmt.Errorf("inventory: %s did not answer within %s (%d attempts, last error: %w)", s.addr, window, attempt, err)
		pause := backoff
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return gaveUp
			}
			pause = min(pause, remaining)
		}
		s.log.Warn("inventory store did not answer, retrying", "address", s.addr, "attempt", attempt, "retryIn", pause, "error", err)
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			if window > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return gaveUp
			}
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(backoff*2, connectMaxBackoff)
	}
}

// conn is the client, or ErrUnavailable with the last connect error.
func (s *Store) conn() (valkey.Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.client == nil {
		if s.lastErr != nil {
			return nil, fmt.Errorf("%w: %s not connected (%w)", ErrUnavailable, s.addr, s.lastErr)
		}
		return nil, fmt.Errorf("%w: %s not connected", ErrUnavailable, s.addr)
	}
	return s.client, nil
}

// fail wraps an operation's error. A Valkey reply (a nil, a server error) is
// the operation's own; anything else — a dial, a broken connection — is
// ErrUnavailable. A caller's cancellation stays what it is.
func fail(op string, err error) error {
	if _, isReply := valkey.IsValkeyErr(err); isReply || valkey.IsValkeyNil(err) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("inventory: %s: %w", op, err)
	}
	return fmt.Errorf("%w: %s: %w", ErrUnavailable, op, err)
}

// Address is the store's address.
func (s *Store) Address() string { return s.addr }

// Ping checks the connection; ErrUnavailable when the store cannot answer.
func (s *Store) Ping(ctx context.Context) error {
	c, err := s.conn()
	if err != nil {
		return err
	}
	if err := c.Do(ctx, c.B().Ping().Build()).Error(); err != nil {
		return fail("ping "+s.addr, err)
	}
	return nil
}

// Keys lists every repository (owner/name) with a record, sorted.
func (s *Store) Keys(ctx context.Context) ([]string, error) {
	c, err := s.conn()
	if err != nil {
		return nil, err
	}
	var cursor uint64
	var out []string
	for {
		res, err := c.Do(ctx, c.B().Scan().Cursor(cursor).Match(KeyPrefix+"*").Count(500).Build()).AsScanEntry()
		if err != nil {
			return nil, fail("scan", err)
		}
		for _, k := range res.Elements {
			out = append(out, strings.TrimPrefix(k, KeyPrefix))
		}
		if res.Cursor == 0 {
			sort.Strings(out)
			return out, nil
		}
		cursor = res.Cursor
	}
}

// Count is the number of repository records.
func (s *Store) Count(ctx context.Context) (int, error) {
	keys, err := s.Keys(ctx)
	return len(keys), err
}

// Put writes one record under KeyPrefix<repository>.
func (s *Store) Put(ctx context.Context, r *Record) error {
	c, err := s.conn()
	if err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("inventory: encode %s: %w", r.Repository, err)
	}
	if err := c.Do(ctx, c.B().Set().Key(KeyPrefix+r.Repository).Value(string(b)).Build()).Error(); err != nil {
		return fail("put "+r.Repository, err)
	}
	return nil
}

// Get reads one record; ErrNotFound when there is none.
func (s *Store) Get(ctx context.Context, repository string) (*Record, error) {
	c, err := s.conn()
	if err != nil {
		return nil, err
	}
	b, err := c.Do(ctx, c.B().Get().Key(KeyPrefix+repository).Build()).AsBytes()
	if err != nil {
		if valkey.IsValkeyNil(err) {
			return nil, fmt.Errorf("%w for %s", ErrNotFound, repository)
		}
		return nil, fail("get "+repository, err)
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("inventory: decode %s: %w", repository, err)
	}
	return &r, nil
}

// List reads every record, sorted by repository.
func (s *Store) List(ctx context.Context) ([]Record, error) {
	keys, err := s.Keys(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.conn()
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(keys))
	// One GET per key, pipelined: the client refuses a multi-key MGET across
	// hash slots.
	for start := 0; start < len(keys); start += 200 {
		end := min(start+200, len(keys))
		cmds := make(valkey.Commands, 0, end-start)
		for _, k := range keys[start:end] {
			cmds = append(cmds, c.B().Get().Key(KeyPrefix+k).Build())
		}
		for i, res := range c.DoMulti(ctx, cmds...) {
			b, err := res.AsBytes()
			if err != nil {
				if valkey.IsValkeyNil(err) {
					continue // deleted between scan and read
				}
				return nil, fail("get "+keys[start+i], err)
			}
			var r Record
			if err := json.Unmarshal(b, &r); err != nil {
				return nil, fmt.Errorf("inventory: decode %s: %w", keys[start+i], err)
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// Delete removes the records of the repositories named.
func (s *Store) Delete(ctx context.Context, repositories ...string) error {
	if len(repositories) == 0 {
		return nil
	}
	c, err := s.conn()
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(repositories))
	for _, r := range repositories {
		keys = append(keys, KeyPrefix+r)
	}
	if err := c.Do(ctx, c.B().Del().Key(keys...).Build()).Error(); err != nil {
		return fail("delete", err)
	}
	return nil
}

// PutSweep stores the last sweep's summary.
func (s *Store) PutSweep(ctx context.Context, sum *SweepSummary) error {
	return s.putJSON(ctx, SweepKey, "sweep", sum)
}

// Sweep reads the last sweep's summary; nil without one.
func (s *Store) Sweep(ctx context.Context) (*SweepSummary, error) {
	var sum SweepSummary
	ok, err := s.getJSON(ctx, SweepKey, "sweep", &sum)
	if err != nil || !ok {
		return nil, err
	}
	return &sum, nil
}

// ReconcilerCursor is where the reconciler poller stands: every run created
// before Watermark is consumed or given up; Consumed names the run attempts
// at or after it that are consumed already ("<run id>/<attempt>"), with their
// run's creation time.
type ReconcilerCursor struct {
	Watermark time.Time            `json:"watermark"`
	Consumed  map[string]time.Time `json:"consumed,omitempty"`
	PolledAt  time.Time            `json:"polledAt"`
}

// PutReconcilerCursor stores the reconciler poller's cursor.
func (s *Store) PutReconcilerCursor(ctx context.Context, cur *ReconcilerCursor) error {
	return s.putJSON(ctx, ReconcilerKey, "reconciler cursor", cur)
}

// ReconcilerCursor reads the reconciler poller's cursor; nil without one.
func (s *Store) ReconcilerCursor(ctx context.Context) (*ReconcilerCursor, error) {
	var cur ReconcilerCursor
	ok, err := s.getJSON(ctx, ReconcilerKey, "reconciler cursor", &cur)
	if err != nil || !ok {
		return nil, err
	}
	return &cur, nil
}

// putJSON stores v as JSON under key; what names the value in errors.
func (s *Store) putJSON(ctx context.Context, key, what string, v any) error {
	c, err := s.conn()
	if err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("inventory: encode %s: %w", what, err)
	}
	if err := c.Do(ctx, c.B().Set().Key(key).Value(string(b)).Build()).Error(); err != nil {
		return fail("put "+what, err)
	}
	return nil
}

// getJSON decodes the JSON under key into out; false without a value.
func (s *Store) getJSON(ctx context.Context, key, what string, out any) (bool, error) {
	c, err := s.conn()
	if err != nil {
		return false, err
	}
	b, err := c.Do(ctx, c.B().Get().Key(key).Build()).AsBytes()
	if err != nil {
		if valkey.IsValkeyNil(err) {
			return false, nil
		}
		return false, fail("get "+what, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return false, fmt.Errorf("inventory: decode %s: %w", what, err)
	}
	return true, nil
}

// Clear removes every record, the sweep summary and the reconciler cursor —
// the store is a cache, so a forced rebuild (and a test on a shared Valkey)
// starts from nothing.
func (s *Store) Clear(ctx context.Context) error {
	keys, err := s.Keys(ctx)
	if err != nil {
		return err
	}
	if err := s.Delete(ctx, keys...); err != nil {
		return err
	}
	c, err := s.conn()
	if err != nil {
		return err
	}
	// One DEL per key: a multi-key command needs one hash slot.
	for _, key := range []string{SweepKey, ReconcilerKey} {
		if err := c.Do(ctx, c.B().Del().Key(key).Build()).Error(); err != nil {
			return fail("delete "+key, err)
		}
	}
	return nil
}

// Close releases the connection, if there is one.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
}
