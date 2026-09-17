// Package gh holds the GitHub identities this server uses: the installation
// of the read-only App giantswarm-repo-manager-inventory for unattended
// inventory reads on its own rate budget, and the person, through the GitHub
// user token muster puts on every call (the person's authorization of the
// App giantswarm-repo-manager). Nothing here writes as an App on a person's
// behalf, and nothing stands in for either identity: without the inventory
// App there are no unattended reads.
package gh

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v92/github"
)

// AppConfig configures the App identity.
type AppConfig struct {
	// APIURL is the GitHub API base URL; empty means api.github.com.
	APIURL string
	// AppID and InstallationID identify the App and its installation on the
	// giantswarm org.
	AppID          int64
	InstallationID int64
	// PrivateKey is the App's PEM private key.
	PrivateKey []byte
}

// App is the App identity: a client authenticated as the App (its JWT, for
// GET /app) and a Reader as the installation (for inventory reads).
type App struct {
	cfg    AppConfig
	app    *github.Client
	reader *Reader
}

// NewApp builds the App identity; it fails on a malformed key.
func NewApp(cfg AppConfig) (*App, error) {
	if cfg.AppID == 0 || cfg.InstallationID == 0 || len(cfg.PrivateKey) == 0 {
		return nil, fmt.Errorf("github app: app ID, installation ID and private key are required")
	}
	counter := &counter{base: http.DefaultTransport}
	appTr, err := ghinstallation.NewAppsTransport(counter, cfg.AppID, cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("github app: %w", err)
	}
	instTr := ghinstallation.NewFromAppsTransport(appTr, cfg.InstallationID)
	if cfg.APIURL != "" {
		appTr.BaseURL = strings.TrimSuffix(cfg.APIURL, "/")
		instTr.BaseURL = appTr.BaseURL
	}
	appClient, err := newClient(cfg.APIURL, github.WithTransport(appTr))
	if err != nil {
		return nil, err
	}
	reader, err := newReader(cfg.APIURL, instTr, counter, fmt.Sprintf("app installation %d", cfg.InstallationID), instTr.Token)
	if err != nil {
		return nil, err
	}
	return &App{cfg: cfg, app: appClient, reader: reader}, nil
}

// Identity is what GET /app says about the App.
type Identity struct {
	Slug           string `json:"slug"`
	ID             int64  `json:"id"`
	InstallationID int64  `json:"installationId"`
}

// Identity reads the App's own record as the App.
func (a *App) Identity(ctx context.Context) (*Identity, error) {
	app, _, err := a.app.Apps.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("github app: GET /app: %w", err)
	}
	return &Identity{Slug: app.GetSlug(), ID: app.GetID(), InstallationID: a.cfg.InstallationID}, nil
}

// Installation is the client authenticated as the installation.
func (a *App) Installation() *github.Client { return a.reader.REST() }

// Reader is the installation as the unattended read identity.
func (a *App) Reader() *Reader { return a.reader }

// Reader is the unattended read identity: REST and GraphQL as the App
// installation, with the REST budget it drew read from the response headers.
type Reader struct {
	apiURL  string
	rest    *github.Client
	http    *http.Client
	counter *counter
	name    string
	token   func(ctx context.Context) (string, error)
}

func newReader(apiURL string, tr http.RoundTripper, counter *counter, name string, token func(ctx context.Context) (string, error)) (*Reader, error) {
	rest, err := newClient(apiURL, github.WithTransport(tr))
	if err != nil {
		return nil, err
	}
	return &Reader{apiURL: apiURL, rest: rest, http: &http.Client{Transport: tr, Timeout: 90 * time.Second}, counter: counter, name: name, token: token}, nil
}

// REST is the go-github client.
func (r *Reader) REST() *github.Client { return r.rest }

// HTTP is an authenticated client (GraphQL).
func (r *Reader) HTTP() *http.Client { return r.http }

// Name says which identity reads: "app installation <id>".
func (r *Reader) Name() string { return r.name }

// APIURL is the REST base URL ("" for api.github.com).
func (r *Reader) APIURL() string { return r.apiURL }

// GraphQLURL is the GraphQL endpoint next to the REST base URL.
func (r *Reader) GraphQLURL() string {
	if r.apiURL == "" {
		return "https://api.github.com/graphql"
	}
	return strings.TrimSuffix(r.apiURL, "/") + "/graphql"
}

// Token is the current bearer token (the installation token, refreshed by
// ghinstallation) — for clients that take a token rather than a transport.
func (r *Reader) Token(ctx context.Context) (string, error) { return r.token(ctx) }

// Usage is what the REST reads drew so far.
func (r *Reader) Usage() Usage { return r.counter.snapshot() }

// Usage is the REST budget as the last response header reported it.
type Usage struct {
	Calls     int       `json:"calls"`
	Remaining int       `json:"remaining"`
	Limit     int       `json:"limit"`
	ResetAt   time.Time `json:"resetAt"`
}

// counter counts REST calls and keeps the last rate-limit headers; GraphQL
// responses are skipped (their budget is read from the response body).
type counter struct {
	base  http.RoundTripper
	mu    sync.Mutex
	usage Usage
}

func (c *counter) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.base.RoundTrip(req)
	if err != nil || strings.HasSuffix(req.URL.Path, "/graphql") {
		return resp, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usage.Calls++
	if v, err := strconv.Atoi(resp.Header.Get("X-Ratelimit-Remaining")); err == nil {
		c.usage.Remaining = v
	}
	if v, err := strconv.Atoi(resp.Header.Get("X-Ratelimit-Limit")); err == nil {
		c.usage.Limit = v
	}
	if v, err := strconv.ParseInt(resp.Header.Get("X-Ratelimit-Reset"), 10, 64); err == nil {
		c.usage.ResetAt = time.Unix(v, 0).UTC()
	}
	return resp, nil
}

func (c *counter) snapshot() Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usage
}

// AsPerson returns a client that calls GitHub with the person's user token —
// every write goes through it.
func AsPerson(apiURL, accessToken string) (*github.Client, error) {
	return newClient(apiURL, github.WithAuthToken(accessToken))
}

// User is the person the token belongs to (GET /user): the call that verifies
// a bearer. A refusal keeps GitHub's *github.ErrorResponse in the chain.
func User(ctx context.Context, apiURL, accessToken string) (login string, id int64, err error) {
	c, err := AsPerson(apiURL, accessToken)
	if err != nil {
		return "", 0, err
	}
	u, _, err := c.Users.Get(ctx, "")
	if err != nil {
		return "", 0, fmt.Errorf("github: GET /user as the person: %w", err)
	}
	return u.GetLogin(), u.GetID(), nil
}

// newClient builds a go-github client with a 15 s timeout, against apiURL
// when set (GitHub Enterprise shape; the fake in tests).
func newClient(apiURL string, opts ...github.ClientOptionsFunc) (*github.Client, error) {
	opts = append(opts, github.WithTimeout(15*time.Second))
	if apiURL != "" {
		opts = append(opts, github.WithEnterpriseURLs(apiURL, apiURL))
	}
	c, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("github: client for %q: %w", apiURL, err)
	}
	return c, nil
}
