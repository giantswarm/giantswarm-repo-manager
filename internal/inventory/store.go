// Package inventory is the Valkey-backed store of one record per repository
// of the org (D8). The store is a cache: the team files stay the desired
// state and GitHub the reality, so a lost store costs a rebuild and nothing
// else. This slice ships the connection and the record count get_info
// reports; the records themselves follow with the inventory slice.
package inventory

import (
	"context"
	"fmt"
	"time"

	"github.com/valkey-io/valkey-go"
)

// KeyPrefix is the key space of repository records: <KeyPrefix><owner>/<name>.
const KeyPrefix = "repo:"

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

// Count is the number of repository records.
func (s *Store) Count(ctx context.Context) (int, error) {
	var cursor uint64
	n := 0
	for {
		res, err := s.client.Do(ctx, s.client.B().Scan().Cursor(cursor).Match(KeyPrefix+"*").Count(500).Build()).AsScanEntry()
		if err != nil {
			return 0, fmt.Errorf("inventory: scan: %w", err)
		}
		n += len(res.Elements)
		if res.Cursor == 0 {
			return n, nil
		}
		cursor = res.Cursor
	}
}

// Put writes one record (a JSON document) under KeyPrefix<fullName>.
func (s *Store) Put(ctx context.Context, fullName string, record []byte) error {
	if err := s.client.Do(ctx, s.client.B().Set().Key(KeyPrefix+fullName).Value(string(record)).Build()).Error(); err != nil {
		return fmt.Errorf("inventory: put %s: %w", fullName, err)
	}
	return nil
}

// Close releases the connection.
func (s *Store) Close() { s.client.Close() }
