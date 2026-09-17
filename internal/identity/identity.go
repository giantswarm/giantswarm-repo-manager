// Package identity carries the authenticated caller through a request: the
// person GitHub named when this server verified the bearer — the GitHub user
// token muster obtained for them through the App giantswarm-repo-manager and
// puts on every call — and that token, which every GitHub call of the request
// runs with. giantswarm-repo-manager does nothing on GitHub as itself for a
// person — every write lands as the caller — so a context without an identity
// only exists in tests and in a server that runs without OAuth.
package identity

import (
	"context"
	"log/slog"
)

// SignIn names the one way to a token this server accepts: the consent muster
// runs for the App giantswarm-repo-manager, once per person.
const SignIn = "connect giantswarm-repo-manager in muster (core_auth_login server=giantswarm-repo-manager), then call again"

// Identity is the authenticated caller as GET /user answered for the bearer.
type Identity struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

// String is the caller as logged: the GitHub login.
func (id *Identity) String() string {
	if id == nil {
		return ""
	}
	return id.Login
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

// ContextWithToken returns ctx carrying the caller's GitHub user token — the
// bearer of the request, and the token every GitHub call runs with.
func ContextWithToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, tokenKey, token)
}

// TokenFromContext returns the caller's GitHub user token, if any.
func TokenFromContext(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(tokenKey).(string)
	return t, ok && t != ""
}
