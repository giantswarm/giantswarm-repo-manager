// Package gh holds the two GitHub identities this server uses: the
// giantswarm-align-files App installation (unattended, read-only inventory
// reads on its own rate budget) and the person, through the grant the broker
// client released. Nothing here writes as the App on a person's behalf.
package gh

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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
// GET /app) and one as the installation (for inventory reads).
type App struct {
	cfg          AppConfig
	app          *github.Client
	installation *github.Client
}

// NewApp builds the App identity; it fails on a malformed key.
func NewApp(cfg AppConfig) (*App, error) {
	if cfg.AppID == 0 || cfg.InstallationID == 0 || len(cfg.PrivateKey) == 0 {
		return nil, fmt.Errorf("github app: app ID, installation ID and private key are required")
	}
	appTr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, cfg.AppID, cfg.PrivateKey)
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
	instClient, err := newClient(cfg.APIURL, github.WithTransport(instTr))
	if err != nil {
		return nil, err
	}
	return &App{cfg: cfg, app: appClient, installation: instClient}, nil
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
func (a *App) Installation() *github.Client { return a.installation }

// AsPerson returns a client that calls GitHub with the person's released
// grant — every write goes through it.
func AsPerson(apiURL, accessToken string) (*github.Client, error) {
	return newClient(apiURL, github.WithAuthToken(accessToken))
}

// Login is the login of the person the token belongs to (GET /user) — the
// read call that proves a released grant works.
func Login(ctx context.Context, apiURL, accessToken string) (string, error) {
	c, err := AsPerson(apiURL, accessToken)
	if err != nil {
		return "", err
	}
	u, _, err := c.Users.Get(ctx, "")
	if err != nil {
		return "", fmt.Errorf("github: GET /user as the person: %w", err)
	}
	return u.GetLogin(), nil
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
