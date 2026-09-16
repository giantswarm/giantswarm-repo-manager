package e2e

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeIdP is the platform Dex: OIDC discovery, a JWKS and RS256 id_tokens for
// the people of the test. The server under test validates the forwarded
// tokens against it (mcp-oauth's SSO path).
type fakeIdP struct {
	*httptest.Server
	key      *rsa.PrivateKey
	clientID string
	// issuer is https://localhost:<port>: mcp-oauth refuses a literal loopback
	// IP as the issuer host, and the certificate below carries that SAN.
	issuer string
}

func newFakeIdP(t *testing.T, clientID string) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{key: key, clientID: clientID}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer": idp.issuer, "authorization_endpoint": idp.issuer + "/auth", "token_endpoint": idp.issuer + "/token",
			"jwks_uri": idp.issuer + "/keys", "userinfo_endpoint": idp.issuer + "/userinfo",
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{kPublic},
			"id_token_signing_alg_values_supported": []string{"RS256"}, "scopes_supported": []string{"openid", "email", "profile", "groups"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": keyID,
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	// Dex must be https (mcp-oauth refuses an http issuer); the server under
	// test trusts the certificate through its CA file.
	idp.Server = httptest.NewUnstartedServer(mux)
	idp.TLS = &tls.Config{Certificates: []tls.Certificate{localhostCert(t, key)}, MinVersion: tls.VersionTLS12}
	idp.StartTLS()
	t.Cleanup(idp.Close)
	idp.issuer = "https://localhost:" + idp.URL[strings.LastIndex(idp.URL, ":")+1:]
	return idp
}

// localhostCert is a self-signed certificate for localhost.
func localhostCert(t *testing.T, key *rsa.PrivateKey) tls.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true,
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// caFile writes the IdP's certificate as the CA bundle the server trusts.
func (i *fakeIdP) caFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "idp-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: i.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// mint is the id_token muster would forward for the person.
func (i *fakeIdP) mint(t *testing.T, sub, email string) string {
	t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": i.issuer, "sub": sub, "aud": i.clientID, "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
		kEmail: email, "email_verified": true, kName: strings.ToUpper(sub[:1]) + sub[1:], "groups": []string{"team-bumblebee"},
	})
	tok.Header["kid"] = keyID
	s, err := tok.SignedString(i.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const keyID = "test"

const tokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token" // #nosec G101 -- RFC 8693 token-type URN, not a credential

// fakeBroker is muster's token-exchange broker: a confidential client
// exchanges a person's id_token (audience github) for the person's GitHub
// grant when they filed one, invalid_target otherwise.
type fakeBroker struct {
	*httptest.Server
	clientID, clientSecret string
	mu                     sync.Mutex
	grants                 map[string]string // sub → GitHub access token
	exchanges              int
}

func newFakeBroker(t *testing.T, clientID, clientSecret string, grants map[string]string) *fakeBroker {
	t.Helper()
	b := &fakeBroker{clientID: clientID, clientSecret: clientSecret, grants: grants}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != b.clientID || secret != b.clientSecret {
			oauthError(w, http.StatusUnauthorized, "invalid_client", "")
			return
		}
		if r.FormValue("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" || r.FormValue("audience") != kGitHub {
			oauthError(w, http.StatusBadRequest, "invalid_request", "grant_type or audience")
			return
		}
		claims := jwt.MapClaims{}
		if _, _, err := jwt.NewParser().ParseUnverified(r.FormValue("subject_token"), claims); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "")
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		b.exchanges++
		sub, _ := claims["sub"].(string)
		tok, ok := b.grants[sub]
		if !ok {
			oauthError(w, http.StatusBadRequest, "invalid_target", "no grant")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": tok, "issued_token_type": tokenTypeAccessToken, "token_type": "Bearer", "expires_in": 3500})
	})
	b.Server = httptest.NewServer(mux)
	t.Cleanup(b.Close)
	return b
}

// fakeGitHub answers the three calls the identity chain makes: GET /app as the
// App (its JWT), the installation token, and GET /user as the person; every
// name check finds the repository free.
type fakeGitHub struct {
	*httptest.Server
	logins map[string]string // person access token → login
	// teams are each login's team slugs in the org (GET /user/teams).
	teams map[string][]string
	// org answers GraphQL for the fake org (the inventory reads).
	org *fakeOrg
	// files is the fake giantswarm/github: team files, policy files, pull
	// requests, reviews, dispatches.
	files *fakeTeamFiles
}

func newFakeGitHub(t *testing.T, logins map[string]string) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{logins: logins, teams: map[string][]string{}, org: &fakeOrg{remaining: 5000, now: time.Now()}, files: newFakeTeamFiles()}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/graphql", g.org.handle)
	g.files.register(mux, g)
	mux.HandleFunc("GET /api/v3/user/teams", func(w http.ResponseWriter, r *http.Request) {
		login, ok := g.logins[bearer(r)]
		if !ok {
			ghMessage(w, http.StatusUnauthorized, "Bad credentials")
			return
		}
		var out []map[string]any
		for _, slug := range g.teams[login] {
			out = append(out, map[string]any{kSlug: slug, "organization": map[string]any{kLogin: org}})
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /api/v3/app", func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) == "" {
			ghMessage(w, http.StatusUnauthorized, "app JWT required")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": 17164699, kSlug: "giantswarm-align-files"})
	})
	mux.HandleFunc("POST /api/v3/app/installations/{id}/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusCreated, map[string]any{"token": "installation-token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	})
	mux.HandleFunc("GET /api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		login, ok := g.logins[bearer(r)]
		if !ok {
			ghMessage(w, http.StatusUnauthorized, "Bad credentials")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{kLogin: login, "id": 1})
	})
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}", func(w http.ResponseWriter, _ *http.Request) {
		ghMessage(w, http.StatusNotFound, "Not Found")
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

func bearer(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

// oauthError is an RFC 6749 error response.
func oauthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]any{"error": code, "error_description": description})
}

// ghMessage is GitHub's error body shape.
func ghMessage(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"message": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// appKeyPEM is a fresh App private key.
func appKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}
