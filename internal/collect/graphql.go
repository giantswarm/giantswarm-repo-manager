package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// varOrg is the GraphQL variable every query takes.
const varOrg = "org"

// ErrBudget is returned when the GraphQL budget fell below the floor; the
// sweep stops cleanly and records how far it got.
var ErrBudget = errors.New("graphql: rate budget below the floor")

// graphQL is the GitHub GraphQL client of the read identity. Every query
// carries `rateLimit { cost remaining limit resetAt }`; the client sums the
// cost and stops at the floor.
type graphQL struct {
	url   string
	http  *http.Client
	floor int

	mu    sync.Mutex
	usage inventory.Budget
}

func newGraphQL(url string, hc *http.Client, floor int) *graphQL {
	return &graphQL{url: url, http: hc, floor: floor}
}

type rateLimit struct {
	Cost      int       `json:"cost"`
	Remaining int       `json:"remaining"`
	Limit     int       `json:"limit"`
	ResetAt   time.Time `json:"resetAt"`
}

type gqlError struct {
	Message string   `json:"message"`
	Path    []any    `json:"path"`
	Type    string   `json:"type"`
	Loc     []any    `json:"locations"`
	Extra   struct{} `json:"-"`
}

// do runs one query; out receives the data (minus rateLimit). Transient
// failures (502/503/504, a timeout) are retried with backoff; GraphQL errors
// on a path are tolerated when the data came along (a missing repository in
// an aliased batch is one of them) and returned as partial.
func (g *graphQL) do(ctx context.Context, query string, vars map[string]any, out any) (partial []string, err error) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []gqlError                 `json:"errors"`
	}
	backoff := 2 * time.Second
	for attempt := 1; ; attempt++ {
		status, raw, rerr := g.post(ctx, body)
		switch {
		case rerr != nil && ctx.Err() != nil:
			return nil, ctx.Err()
		case rerr != nil, status == http.StatusBadGateway, status == http.StatusServiceUnavailable, status == http.StatusGatewayTimeout:
			if attempt >= 4 {
				return nil, fmt.Errorf("graphql: %d after %d attempts: %v", status, attempt, rerr)
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			backoff *= 2
			continue
		case status != http.StatusOK:
			return nil, fmt.Errorf("graphql: HTTP %d: %s", status, truncate(raw, 300))
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("graphql: decode: %w", err)
		}
		break
	}
	if resp.Data == nil {
		msg := "no data"
		if len(resp.Errors) > 0 {
			msg = resp.Errors[0].Message
		}
		return nil, fmt.Errorf("graphql: %s", msg)
	}
	var rl rateLimit
	if r, ok := resp.Data["rateLimit"]; ok {
		_ = json.Unmarshal(r, &rl)
		delete(resp.Data, "rateLimit")
	}
	g.record(rl)
	for _, e := range resp.Errors {
		partial = append(partial, e.Message)
	}
	data, err := json.Marshal(resp.Data)
	if err != nil {
		return partial, err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return partial, fmt.Errorf("graphql: decode data: %w", err)
	}
	if g.floor > 0 && rl.Limit > 0 && rl.Remaining < g.floor {
		return partial, fmt.Errorf("%w: %d remaining (< %d), resets %s", ErrBudget, rl.Remaining, g.floor, rl.ResetAt.Format(time.RFC3339))
	}
	return partial, nil
}

func (g *graphQL) post(ctx context.Context, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return resp.StatusCode, raw, err
}

func (g *graphQL) record(rl rateLimit) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.usage.Calls++
	g.usage.Cost += rl.Cost
	if rl.Limit > 0 {
		g.usage.Remaining = rl.Remaining
		g.usage.Limit = rl.Limit
		t := rl.ResetAt
		g.usage.ResetAt = &t
	}
}

// usage is the budget drawn so far.
func (g *graphQL) snapshot() inventory.Budget {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.usage
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
