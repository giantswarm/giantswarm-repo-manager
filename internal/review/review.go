// Package review is the client of klaus-gateway's team-review endpoint
// (giantswarm/klaus-gateway#273, PRD D6): POST /reviews posts an ask with an
// Approve button into a team's Slack channel, POST /notices a message without
// buttons. The gateway admits this pod's projected ServiceAccount token
// (audience klaus-gateway) through a TokenReview; the file is read per call
// because the kubelet rotates it.
package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// channelID is the shape the gateway accepts: a Slack channel ID, not a name.
var channelID = regexp.MustCompile(`^[CDG][A-Z0-9]{5,}$`)

// ErrNotConfigured says the gateway client is off (no URL).
var ErrNotConfigured = errors.New("team-review endpoint not configured (REVIEWS_URL)")

// Config configures the client.
type Config struct {
	// BaseURL is the gateway's in-cluster URL (http://klaus-gateway.<ns>.svc:<port>).
	BaseURL string
	// TokenFile is the projected ServiceAccount token, read per request.
	TokenFile string
	// Channels maps a policy file's channel name to its Slack ID — the
	// gateway refuses names, and the policy files carry names today.
	Channels map[string]string
	// HTTPClient defaults to a 15 s client.
	HTTPClient *http.Client
}

// Client posts asks and notices.
type Client struct {
	cfg  Config
	http *http.Client
}

// New returns a client, or nil when BaseURL is empty (the endpoint is off).
func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		return nil
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{cfg: cfg, http: hc}
}

// Approve is the tool the Approve button calls as the clicking member.
type Approve struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// Ask is one review request.
type Ask struct {
	Team    string  `json:"team"`
	Channel string  `json:"channel"`
	Text    string  `json:"text"`
	Link    string  `json:"link,omitempty"`
	Approve Approve `json:"approve"`
}

// Notice is a message without buttons.
type Notice struct {
	Team    string `json:"team"`
	Channel string `json:"channel"`
	Text    string `json:"text"`
	Link    string `json:"link,omitempty"`
}

// Posted is the gateway's 201 answer.
type Posted struct {
	ID      string `json:"id,omitempty"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// ChannelID resolves a policy file's slackChannel to the ID the gateway
// accepts: an ID as written, else through Config.Channels.
func (c *Client) ChannelID(name string) (string, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "#")
	if channelID.MatchString(name) {
		return name, nil
	}
	if c != nil {
		if id, ok := c.cfg.Channels[name]; ok && channelID.MatchString(id) {
			return id, nil
		}
	}
	return "", fmt.Errorf("slack channel %q has no ID: the gateway takes channel IDs; map it in reviews.channels (%s: C…)", name, name)
}

// Review posts an ask.
func (c *Client) Review(ctx context.Context, a Ask) (*Posted, error) {
	return c.post(ctx, "/reviews", a)
}

// Notify posts a notice.
func (c *Client) Notify(ctx context.Context, n Notice) (*Posted, error) {
	return c.post(ctx, "/notices", n)
}

func (c *Client) post(ctx context.Context, path string, body any) (*Posted, error) {
	if c == nil {
		return nil, ErrNotConfigured
	}
	tok, err := os.ReadFile(c.cfg.TokenFile) // #nosec G304 -- operator-provided path (the projected token)
	if err != nil {
		return nil, fmt.Errorf("team-review: read the ServiceAccount token: %w", err)
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.cfg.BaseURL, "/")+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("team-review: POST %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("team-review: POST %s answered %d: %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var p Posted
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("team-review: decode answer: %w", err)
	}
	return &p, nil
}
