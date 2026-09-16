package server

import (
	"context"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	oauth "github.com/giantswarm/mcp-oauth"
	"github.com/giantswarm/mcp-oauth/handler"
	"github.com/giantswarm/mcp-oauth/providers/dex"
	"github.com/giantswarm/mcp-oauth/security"
	"github.com/giantswarm/mcp-oauth/storage/memory"

	"github.com/giantswarm/giantswarm-repo-manager/internal/identity"
)

// OAuthConfig makes the server an OAuth 2.1 resource server (mcp-oauth) in
// front of the MCP endpoint. The platform's Dex is the authority: muster
// forwards the session's id_token byte-identical (MCPServer auth.forwardToken)
// and it is validated here against Dex's JWKS when its audience is one of
// TrustedAudiences. The caller's identity and the token then travel with the
// request — the token is what the broker client exchanges for the person's
// GitHub grant.
type OAuthConfig struct {
	// BaseURL is the public URL of this server: the issuer of its own OAuth
	// metadata (https, or http on loopback).
	BaseURL string
	// Dex issuer and the client this server is registered as.
	DexIssuerURL    string
	DexClientID     string
	DexClientSecret string
	// DexCAFile is a PEM bundle for a Dex with a private certificate.
	DexCAFile string
	// DexAllowPrivateIP lets the issuer resolve to a private or loopback
	// address (an in-cluster Dex, the lab).
	DexAllowPrivateIP bool
	// TrustedAudiences are the OAuth client ids whose id_tokens are accepted
	// as bearers: the platform client and the audiences the MCPServer requires.
	TrustedAudiences []string
	// SSOAllowPrivateIPs lets the JWKS endpoint resolve to a private address.
	SSOAllowPrivateIPs bool
	// AllowPublicClientRegistration lets MCP clients register over DCR
	// without a token (labs only).
	AllowPublicClientRegistration bool
}

// Validate checks required fields.
func (c OAuthConfig) Validate() error {
	if c.BaseURL == "" {
		return fmt.Errorf("oauth: base URL is required")
	}
	if err := validateHTTPS(c.BaseURL); err != nil {
		return err
	}
	if c.DexIssuerURL == "" || c.DexClientID == "" || c.DexClientSecret == "" {
		return fmt.Errorf("oauth: dex issuer URL, client ID and client secret are required")
	}
	for _, a := range c.TrustedAudiences {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("oauth: trusted audiences must not be empty strings")
		}
	}
	return nil
}

type oauthRuntime struct {
	server  *oauth.Server
	handler *handler.Handler
	store   *memory.Store
	cfg     OAuthConfig
	mcpPath string
	log     *slog.Logger
}

func newOAuth(cfg OAuthConfig, mcpPath string, log *slog.Logger) (*oauthRuntime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	rootCAs, err := loadRootCAs(cfg.DexCAFile)
	if err != nil {
		return nil, fmt.Errorf("oauth: %w", err)
	}
	provider, err := dex.NewProvider(&dex.Config{
		IssuerURL:      cfg.DexIssuerURL,
		ClientID:       cfg.DexClientID,
		ClientSecret:   cfg.DexClientSecret,
		RedirectURL:    strings.TrimSuffix(cfg.BaseURL, "/") + "/oauth/callback",
		AllowPrivateIP: cfg.DexAllowPrivateIP,
		RootCAs:        rootCAs,
		Logger:         log,
	})
	if err != nil {
		return nil, fmt.Errorf("oauth: dex provider: %w", err)
	}
	store := memory.New()
	srv, err := oauth.NewServer(provider, store, store, store, &oauth.ServerConfig{
		Issuer:                        cfg.BaseURL,
		AllowRefreshTokenRotation:     true,
		AllowPublicClientRegistration: cfg.AllowPublicClientRegistration,
		MaxClientsPerIP:               10,
		TrustedAudiences:              cfg.TrustedAudiences,
		AllowPrivateIPJWKS:            cfg.SSOAllowPrivateIPs,
		JWKSRootCAs:                   rootCAs,
	}, log, oauth.WithAuditor(security.NewAuditor(log, true)))
	if err != nil {
		return nil, fmt.Errorf("oauth: server: %w", err)
	}
	log.Info("OAuth resource server enabled", "issuer", cfg.BaseURL, "dex", cfg.DexIssuerURL, "trustedAudiences", cfg.TrustedAudiences)
	return &oauthRuntime{server: srv, handler: handler.New(srv, log), store: store, cfg: cfg, mcpPath: mcpPath, log: log}, nil
}

// loadRootCAs is the system pool plus the PEM bundle at caFile; (nil, nil)
// without a file, selecting the system trust everywhere the pool is used.
func loadRootCAs(caFile string) (*x509.CertPool, error) {
	if caFile == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(caFile) // #nosec G304 -- operator-provided path
	if err != nil {
		return nil, fmt.Errorf("read CA file %s: %w", caFile, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no CA certificate in %s", caFile)
	}
	return pool, nil
}

func (o *oauthRuntime) register(mux *http.ServeMux) {
	o.handler.RegisterAuthorizationServerMetadataRoutes(mux)
	o.handler.RegisterProtectedResourceMetadataRoutes(mux, o.mcpPath)
	mux.HandleFunc("/oauth/authorize", o.handler.ServeAuthorization)
	mux.HandleFunc("/oauth/token", o.handler.ServeToken)
	mux.HandleFunc("/oauth/callback", o.handler.ServeCallback)
	mux.HandleFunc("/oauth/register", o.handler.ServeClientRegistration)
	mux.HandleFunc("/oauth/revoke", o.handler.ServeTokenRevocation)
	mux.HandleFunc("/oauth/introspect", o.handler.ServeTokenIntrospection)
}

// protect requires a valid bearer (this server's own access tokens, or a
// forwarded id_token whose audience is trusted) and attaches the caller and
// the IdP token to the request.
func (o *oauthRuntime) protect(next http.Handler) http.Handler {
	return o.handler.ValidateToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		info, ok := handler.UserInfoFromContext(ctx)
		if !ok || info == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id := &identity.Identity{Subject: info.ID, Email: info.Email, Name: info.Name, Groups: info.Groups, Source: identity.SourceOAuth}
		if info.IsSSO() {
			id.Source = identity.SourceSSO
		}
		ctx = identity.ContextWith(ctx, id)
		ctx = identity.ContextWithToken(ctx, o.idToken(ctx, r, info.IsSSO()))
		o.log.Debug("authenticated request", "caller", id.String(), "source", id.Source, "path", r.URL.Path)
		next.ServeHTTP(w, r.WithContext(ctx))
	}))
}

// idToken is the caller's IdP id_token: a forwarded bearer is the token
// itself; for a token this server issued, the provider's id_token is in the
// store. Empty when there is none — get_info then reports that no grant can
// be obtained for the session.
func (o *oauthRuntime) idToken(ctx context.Context, r *http.Request, sso bool) string {
	bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if bearer == "" || sso {
		return bearer
	}
	tok, err := o.store.GetToken(ctx, bearer)
	if err != nil || tok == nil {
		return ""
	}
	idToken, _ := tok.Extra("id_token").(string)
	return idToken
}

func (o *oauthRuntime) shutdown(ctx context.Context) {
	if err := o.server.Shutdown(ctx); err != nil {
		o.log.Warn("oauth server shutdown", "error", err)
	}
}

// validateHTTPS enforces OAuth 2.1's HTTPS requirement, allowing plain HTTP
// only for loopback development.
func validateHTTPS(baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("oauth: invalid base URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if h := u.Hostname(); h == "localhost" || h == "127.0.0.1" || h == "::1" {
			return nil
		}
		return fmt.Errorf("oauth: base URL must use https (http is allowed for loopback only): %s", baseURL)
	default:
		return fmt.Errorf("oauth: base URL scheme must be http or https: %s", baseURL)
	}
}
