package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v92/github"

	"github.com/giantswarm/giantswarm-repo-manager/internal/gh"
	"github.com/giantswarm/giantswarm-repo-manager/internal/identity"
)

// OAuthConfig makes the MCP endpoint an OAuth protected resource in the
// bearer-only shape: every request carries a GitHub user token — behind
// muster the person's, obtained through the App giantswarm-repo-manager once
// and put on every call — and the token is verified with GET /user. This
// server issues no tokens and runs no sign-in of its own; its
// protected-resource metadata names the authorization server muster pins, so a
// client that discovers instead of pinning learns where to sign in.
type OAuthConfig struct {
	// BaseURL is where this server is reached, without the MCP path (muster's
	// MCPServer url): the resource of the protected-resource metadata.
	BaseURL string
	// AuthorizationServer is the issuer identity the metadata names — the
	// App's, https://github.com/apps/giantswarm-repo-manager, the same value
	// the MCPServer pins.
	AuthorizationServer string
	// GitHubAPIURL is the API base URL GET /user goes to (empty:
	// api.github.com; the fake in tests).
	GitHubAPIURL string
	// CacheTTL bounds how long a verified token is trusted without asking
	// GitHub again: a user token's expiry is not readable from the token, and
	// a revoked one must stop working soon. Zero is DefaultCacheTTL.
	CacheTTL time.Duration
}

// DefaultAuthorizationServer is the issuer identity of the App
// giantswarm-repo-manager: what the chart pins on the MCPServer and what the
// metadata names.
const DefaultAuthorizationServer = "https://github.com/apps/giantswarm-repo-manager"

// DefaultCacheTTL is how long a verified bearer is trusted without a second
// GET /user.
const DefaultCacheTTL = 15 * time.Minute

// cacheMax bounds the verified-token cache; beyond it expired entries are
// swept and, when none is, new tokens are verified without being cached.
const cacheMax = 10_000

// realm names this server in the WWW-Authenticate challenge.
const realm = "giantswarm-repo-manager"

// Validate checks required fields.
func (c OAuthConfig) Validate() error {
	if err := validateURL("oauth: base URL", c.BaseURL); err != nil {
		return err
	}
	return validateURL("oauth: authorization server", c.AuthorizationServer)
}

func validateURL(what, raw string) error {
	if raw == "" {
		return fmt.Errorf("%s is required", what)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("%s must be an absolute http(s) URL: %q", what, raw)
	}
	return nil
}

// bearerGuard verifies the bearer of every MCP request with GitHub and puts the
// caller and the token on the request.
type bearerGuard struct {
	cfg     OAuthConfig
	mcpPath string
	log     *slog.Logger

	mu    sync.Mutex
	cache map[[sha256.Size]byte]cacheEntry
}

type cacheEntry struct {
	id    *identity.Identity
	until time.Time
}

func newBearerGuard(cfg OAuthConfig, mcpPath string, log *slog.Logger) (*bearerGuard, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = DefaultCacheTTL
	}
	log.Info("OAuth bearer guard enabled", "resource", cfg.BaseURL+mcpPath, "authorizationServer", cfg.AuthorizationServer, "cacheTTL", cfg.CacheTTL)
	return &bearerGuard{cfg: cfg, mcpPath: mcpPath, log: log, cache: map[[sha256.Size]byte]cacheEntry{}}, nil
}

// metadataPath is where the protected-resource metadata of the MCP endpoint
// lives (RFC 9728: the well-known prefix, then the resource's path).
func (g *bearerGuard) metadataPath() string {
	return "/.well-known/oauth-protected-resource" + g.mcpPath
}

// register serves the protected-resource metadata: the MCP endpoint as the
// resource and the pinned authorization server as the one place to sign in.
func (g *bearerGuard) register(mux *http.ServeMux) {
	doc, err := json.Marshal(map[string]any{
		"resource":                 g.cfg.BaseURL + g.mcpPath,
		"resource_name":            realm,
		"authorization_servers":    []string{g.cfg.AuthorizationServer},
		"bearer_methods_supported": []string{"header"},
	})
	if err != nil {
		panic(err) // a map of strings always marshals
	}
	serve := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(doc)
	}
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", serve)
	mux.HandleFunc("GET "+g.metadataPath(), serve)
}

// protect admits a request whose bearer GitHub accepts and attaches the caller
// and the token; without a bearer, or with one GitHub refuses, it answers 401
// with the challenge that names the metadata and the sign-in.
func (g *bearerGuard) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerOf(r)
		if token == "" {
			g.challenge(w, "", "no bearer token: "+identity.SignIn)
			return
		}
		who, err := g.verify(r.Context(), token)
		if err != nil {
			if status, refused := refusedBy(err); refused {
				g.challenge(w, "invalid_token", fmt.Sprintf("GitHub refused the bearer token (%d): no GitHub authorization for you yet — %s", status, identity.SignIn))
				return
			}
			g.log.Warn("bearer verification failed", "error", err)
			http.Error(w, "could not verify the bearer token with GitHub: "+err.Error(), http.StatusBadGateway)
			return
		}
		ctx := identity.ContextWith(r.Context(), who)
		ctx = identity.ContextWithToken(ctx, token)
		g.log.Debug("authenticated request", "caller", who.Login, "path", r.URL.Path)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// verify is the caller GET /user answers for token, from the cache while the
// entry lives.
func (g *bearerGuard) verify(ctx context.Context, token string) (*identity.Identity, error) {
	key := sha256.Sum256([]byte(token))
	now := time.Now()
	g.mu.Lock()
	e, ok := g.cache[key]
	g.mu.Unlock()
	if ok && now.Before(e.until) {
		return e.id, nil
	}
	login, id, err := gh.User(ctx, g.cfg.GitHubAPIURL, token)
	if err != nil {
		return nil, err
	}
	who := &identity.Identity{Login: login, ID: id}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.cache) >= cacheMax {
		for k, e := range g.cache {
			if !now.Before(e.until) {
				delete(g.cache, k)
			}
		}
	}
	if len(g.cache) < cacheMax {
		g.cache[key] = cacheEntry{id: who, until: now.Add(g.cfg.CacheTTL)}
	}
	return who, nil
}

// challenge is the 401: the RFC 6750 challenge naming the resource metadata,
// the error code when a token was presented, and the reason as the body.
func (g *bearerGuard) challenge(w http.ResponseWriter, code, description string) {
	challenge := fmt.Sprintf(`Bearer realm=%q, resource_metadata=%q`, realm, g.cfg.BaseURL+g.metadataPath())
	if code != "" {
		challenge += fmt.Sprintf(`, error=%q, error_description=%q`, code, description)
	}
	w.Header().Set("WWW-Authenticate", challenge)
	http.Error(w, description, http.StatusUnauthorized)
}

// refusedBy says whether err is GitHub refusing the token (401, 403) rather
// than GitHub being unreachable.
func refusedBy(err error) (int, bool) {
	var ghErr *github.ErrorResponse
	if errors.As(err, &ghErr) && ghErr.Response != nil {
		switch ghErr.Response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ghErr.Response.StatusCode, true
		}
	}
	return 0, false
}

// bearerOf is the request's bearer token, "" without one.
func bearerOf(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}
