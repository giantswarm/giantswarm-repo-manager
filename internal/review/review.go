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

// DefaultAudience is the projected token's audience the gateway admits by default.
const DefaultAudience = "klaus-gateway"

// ErrNotConfigured says the gateway client is off (no URL).
var ErrNotConfigured = errors.New("team-review endpoint not configured (REVIEWS_URL)")

// Config configures the client.
type Config struct {
	// BaseURL is the gateway's in-cluster URL (http://klaus-gateway.<ns>.svc:<port>).
	BaseURL string
	// TokenFile is the projected ServiceAccount token, read per request.
	TokenFile string
	// Audience is the projected token's audience, the gateway's
	// reviews.audience: the kubelet mints the token for it, get_info reports it.
	Audience string
	// Channels maps a policy file's channel names — slackChannel for asks,
	// standupChannel for notices — to their Slack IDs: the gateway refuses
	// names, and the policy files carry names today.
	Channels map[string]string
	// DebugChannel, when set, receives every ask and notice instead of the
	// channel the policy file names, the text naming that channel — a test
	// round without disturbing the teams. A name Channels resolves or an ID;
	// empty delivers to the policy file's channel.
	DebugChannel string
	// HTTPClient defaults to a 15 s client.
	HTTPClient *http.Client
}

// Client posts asks and notices.
type Client struct {
	cfg  Config
	http *http.Client
	// debug is the debug channel's ID, empty without one.
	debug string
	// names are Channels reversed: the policy file's name by channel ID,
	// for the text of a redirected message.
	names map[string]string
}

// New returns a client, or nil when BaseURL is empty (the endpoint is off);
// a debug channel neither an ID nor a name Channels resolves is an error.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, nil
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	c := &Client{cfg: cfg, http: hc, names: make(map[string]string, len(cfg.Channels))}
	for name, id := range cfg.Channels {
		// Two names for one ID: the first in order, so the text is stable.
		if prev, ok := c.names[id]; !ok || name < prev {
			c.names[id] = name
		}
	}
	if cfg.DebugChannel != "" {
		id, err := c.ChannelID(cfg.DebugChannel)
		if err != nil {
			return nil, fmt.Errorf("debug channel: %w", err)
		}
		c.debug = id
	}
	return c, nil
}

// DebugChannel is the debug channel as configured, empty without one.
func (c *Client) DebugChannel() string {
	if c == nil {
		return ""
	}
	return c.cfg.DebugChannel
}

// URL is the gateway's base URL, empty when the endpoint is off.
func (c *Client) URL() string {
	if c == nil {
		return ""
	}
	return c.cfg.BaseURL
}

// Audience is the projected token's audience, empty when the endpoint is off.
func (c *Client) Audience() string {
	if c == nil {
		return ""
	}
	return c.cfg.Audience
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

// ChannelID resolves a policy file's channel (slackChannel or
// standupChannel) to the ID the gateway accepts: an ID as written, else
// through Config.Channels.
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
	a.Channel, a.Text = c.route(a.Channel, a.Text)
	return c.post(ctx, "/reviews", a)
}

// Notify posts a notice.
func (c *Client) Notify(ctx context.Context, n Notice) (*Posted, error) {
	n.Channel, n.Text = c.route(n.Channel, n.Text)
	return c.post(ctx, "/notices", n)
}

// route is where a message goes and what it says: the channel and text as
// given, or — with a debug channel — that channel and the text closing with
// the channel the policy file chose, "(for #team-x)", so a reader tells the
// redirect from a misconfiguration.
func (c *Client) route(channel, text string) (string, string) {
	if c == nil || c.debug == "" {
		return channel, text
	}
	return c.debug, text + " (for " + c.channelName(channel) + ")"
}

// channelName is a channel as the policy file names it: "#<name>" when
// Channels maps a name to the ID, else the ID.
func (c *Client) channelName(id string) string {
	if name, ok := c.names[id]; ok {
		return "#" + name
	}
	return id
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
