// Package identity carries the authenticated caller through a request: who
// the OAuth layer validated (subject, email, groups) and the caller's own IdP
// token, which the broker client exchanges for the person's GitHub grant.
// giantswarm-repo-manager does nothing on GitHub as itself for a person — every
// write lands as the caller — so a context without an identity only exists in
// tests and in a server that runs without OAuth.
package identity

import (
	"context"
	"log/slog"
)

// Source says how the caller was authenticated.
type Source string

const (
	// SourceSSO is an IdP id_token forwarded by muster (MCPServer
	// auth.forwardToken), validated against the IdP's JWKS.
	SourceSSO Source = "sso"
	// SourceOAuth is an access token issued by this server's own OAuth 2.1
	// flow (a client that authenticated directly, e.g. mcp-debug).
	SourceOAuth Source = "oauth"
)

// Identity is the authenticated caller.
type Identity struct {
	Subject string   `json:"subject"`
	Email   string   `json:"email,omitempty"`
	Name    string   `json:"name,omitempty"`
	Groups  []string `json:"groups,omitempty"`
	Source  Source   `json:"source"`
}

// String is the caller as logged: the email, else the subject.
func (id *Identity) String() string {
	if id == nil {
		return ""
	}
	if id.Email != "" {
		return id.Email
	}
	return id.Subject
}

type ctxKey int

const (
	identityKey ctxKey = iota
	tokenKey
)

// ContextWith returns ctx carrying id.
func ContextWith(ctx context.Context, id *Identity) context.Context {
	if id == nil {
		return ctx
	}
	return context.WithValue(ctx, identityKey, id)
}

// FromContext returns the caller, if any.
func FromContext(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey).(*Identity)
	return id, ok && id != nil
}

// Caller is the caller's String(), or "" without one.
func Caller(ctx context.Context) string {
	id, _ := FromContext(ctx)
	return id.String()
}

// LogAttr is the structured-log attribute every write carries.
func LogAttr(ctx context.Context) slog.Attr {
	if c := Caller(ctx); c != "" {
		return slog.String("caller", c)
	}
	return slog.String("caller", "anonymous")
}

// ContextWithToken returns ctx carrying the caller's IdP id_token — the
// subject token of the broker exchange.
func ContextWithToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, tokenKey, token)
}

// TokenFromContext returns the caller's IdP id_token, if any.
func TokenFromContext(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(tokenKey).(string)
	return t, ok && t != ""
}
