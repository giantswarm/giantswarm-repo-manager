// Package broker is the muster token-exchange broker client: this server is a
// confidential broker client of muster (tokenExchangeBroker.brokerClients) and
// exchanges the caller's forwarded id_token (RFC 8693, audience "github") for
// the person's own GitHub access token — the grant the person filed when they
// connected GitHub in muster. Every write on GitHub then lands as the person;
// a person without a grant is told to connect GitHub first.
package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange" // #nosec G101 -- RFC 8693 URN
	tokenTypeIDToken       = "urn:ietf:params:oauth:token-type:id_token"       // #nosec G101 -- RFC 8693 URN
	tokenTypeAccessToken   = "urn:ietf:params:oauth:token-type:access_token"   // #nosec G101 -- RFC 8693 URN

	// DefaultAudience is the broker target that releases the person's GitHub
	// grant (tokenExchangeBroker.targets.github.grantIssuer).
	DefaultAudience = "github"
)

// ErrNoGrant is returned when the person holds no grant for the audience
// (muster answers invalid_target): the person has not connected GitHub.
var ErrNoGrant = errors.New("no GitHub grant for the caller: connect GitHub in muster first")

// Config configures the client.
type Config struct {
	// MusterURL is muster's base URL; the exchange goes to <MusterURL>/oauth/token.
	MusterURL string
	// ClientID and ClientSecret authenticate this server as a broker client
	// (client_secret_basic).
	ClientID     string
	ClientSecret string
	// Audience is the broker target; empty means DefaultAudience.
	Audience string
	// HTTPClient, when nil, is a client with a 15 s timeout.
	HTTPClient *http.Client
}

// Grant is the person's released access token and its remaining lifetime.
type Grant struct {
	AccessToken string
	TokenType   string
	ExpiresIn   time.Duration
}

// Client exchanges tokens at muster.
type Client struct {
	cfg  Config
	http *http.Client
}

// New returns a client; it fails on an incomplete configuration.
func New(cfg Config) (*Client, error) {
	if cfg.MusterURL == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("broker: muster URL, client ID and client secret are required")
	}
	if cfg.Audience == "" {
		cfg.Audience = DefaultAudience
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{cfg: cfg, http: hc}, nil
}

// Audience is the configured broker target.
func (c *Client) Audience() string { return c.cfg.Audience }

// TokenEndpoint is muster's token endpoint the exchange goes to.
func (c *Client) TokenEndpoint() string {
	return strings.TrimSuffix(c.cfg.MusterURL, "/") + "/oauth/token"
}

// Exchange releases the caller's grant for subjectToken (the forwarded
// id_token). ErrNoGrant when the person has none; any other refusal is an
// error naming muster's OAuth error code.
func (c *Client) Exchange(ctx context.Context, subjectToken string) (*Grant, error) {
	if subjectToken == "" {
		return nil, fmt.Errorf("broker: no subject token: the request carried no IdP id_token")
	}
	form := url.Values{
		"grant_type":           {grantTypeTokenExchange},
		"subject_token":        {subjectToken},
		"subject_token_type":   {tokenTypeIDToken},
		"requested_token_type": {tokenTypeAccessToken},
		"audience":             {c.cfg.Audience},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenEndpoint(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("broker: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(c.cfg.ClientID), url.QueryEscape(c.cfg.ClientSecret))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("broker: exchange at %s: %w", c.TokenEndpoint(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("broker: read response: %w", err)
	}
	var out struct {
		AccessToken      string `json:"access_token"`
		TokenType        string `json:"token_type"`
		ExpiresIn        int64  `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("broker: %s answered %d with a non-JSON body", c.TokenEndpoint(), resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || out.Error != "" {
		if out.Error == "invalid_target" {
			return nil, ErrNoGrant
		}
		return nil, fmt.Errorf("broker: exchange refused (%d %s): %s", resp.StatusCode, out.Error, out.ErrorDescription)
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("broker: exchange answered without an access token")
	}
	return &Grant{AccessToken: out.AccessToken, TokenType: out.TokenType, ExpiresIn: time.Duration(out.ExpiresIn) * time.Second}, nil
}
