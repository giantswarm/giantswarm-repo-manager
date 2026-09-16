// Package inventory is the Valkey-backed store of one record per repository
// of the org (D8). The store is a cache: the team files stay the desired
// state and GitHub the reality, so a lost store costs a rebuild and nothing
// else.
package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Keys: repository records under KeyPrefix<owner>/<name>, the last sweep's
// summary under SweepKey.
const (
	KeyPrefix = "repo:"
	SweepKey  = "inventory:sweep"
)

// ErrNotFound is returned for a repository without a record.
var ErrNotFound = errors.New("inventory: no record")

// Store is the inventory store.
type Store struct {
	addr   string
	client valkey.Client
}

// Open connects to the Valkey at addr (host:port).
func Open(addr string) (*Store, error) {
	if addr == "" {
		return nil, fmt.Errorf("inventory: Valkey address is required")
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, DisableCache: true, ConnWriteTimeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("inventory: connect to %s: %w", addr, err)
	}
	return &Store{addr: addr, client: c}, nil
}

// Address is the store's address.
func (s *Store) Address() string { return s.addr }

// Ping checks the connection.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.client.Do(ctx, s.client.B().Ping().Build()).Error(); err != nil {
		return fmt.Errorf("inventory: ping %s: %w", s.addr, err)
	}
	return nil
}

// Keys lists every repository (owner/name) with a record, sorted.
func (s *Store) Keys(ctx context.Context) ([]string, error) {
	var cursor uint64
	var out []string
	for {
		res, err := s.client.Do(ctx, s.client.B().Scan().Cursor(cursor).Match(KeyPrefix+"*").Count(500).Build()).AsScanEntry()
		if err != nil {
			return nil, fmt.Errorf("inventory: scan: %w", err)
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
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("inventory: encode %s: %w", r.Repository, err)
	}
	if err := s.client.Do(ctx, s.client.B().Set().Key(KeyPrefix+r.Repository).Value(string(b)).Build()).Error(); err != nil {
		return fmt.Errorf("inventory: put %s: %w", r.Repository, err)
	}
	return nil
}

// Get reads one record; ErrNotFound when there is none.
func (s *Store) Get(ctx context.Context, repository string) (*Record, error) {
	b, err := s.client.Do(ctx, s.client.B().Get().Key(KeyPrefix+repository).Build()).AsBytes()
	if err != nil {
		if valkey.IsValkeyNil(err) {
			return nil, fmt.Errorf("%w for %s", ErrNotFound, repository)
		}
		return nil, fmt.Errorf("inventory: get %s: %w", repository, err)
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
	out := make([]Record, 0, len(keys))
	// One GET per key, pipelined: the client refuses a multi-key MGET across
	// hash slots.
	for start := 0; start < len(keys); start += 200 {
		end := min(start+200, len(keys))
		cmds := make(valkey.Commands, 0, end-start)
		for _, k := range keys[start:end] {
			cmds = append(cmds, s.client.B().Get().Key(KeyPrefix+k).Build())
		}
		for i, res := range s.client.DoMulti(ctx, cmds...) {
			b, err := res.AsBytes()
			if err != nil {
				if valkey.IsValkeyNil(err) {
					continue // deleted between scan and read
				}
				return nil, fmt.Errorf("inventory: get %s: %w", keys[start+i], err)
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
	keys := make([]string, 0, len(repositories))
	for _, r := range repositories {
		keys = append(keys, KeyPrefix+r)
	}
	if err := s.client.Do(ctx, s.client.B().Del().Key(keys...).Build()).Error(); err != nil {
		return fmt.Errorf("inventory: delete: %w", err)
	}
	return nil
}

// PutSweep stores the last sweep's summary.
func (s *Store) PutSweep(ctx context.Context, sum *SweepSummary) error {
	b, err := json.Marshal(sum)
	if err != nil {
		return fmt.Errorf("inventory: encode sweep: %w", err)
	}
	if err := s.client.Do(ctx, s.client.B().Set().Key(SweepKey).Value(string(b)).Build()).Error(); err != nil {
		return fmt.Errorf("inventory: put sweep: %w", err)
	}
	return nil
}

// Sweep reads the last sweep's summary; nil without one.
func (s *Store) Sweep(ctx context.Context) (*SweepSummary, error) {
	b, err := s.client.Do(ctx, s.client.B().Get().Key(SweepKey).Build()).AsBytes()
	if err != nil {
		if valkey.IsValkeyNil(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("inventory: get sweep: %w", err)
	}
	var sum SweepSummary
	if err := json.Unmarshal(b, &sum); err != nil {
		return nil, fmt.Errorf("inventory: decode sweep: %w", err)
	}
	return &sum, nil
}

// Close releases the connection.
func (s *Store) Close() { s.client.Close() }
