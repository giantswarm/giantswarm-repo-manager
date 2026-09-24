package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// inventoryApp is the slug GET /app answers: the read-only App of the
// unattended reads; installationToken is the token the fake mints for its
// installation.
const (
	inventoryApp      = "giantswarm-repo-manager-inventory"
	installationToken = "installation-token"
	// The release watch's fakes.
	kURL           = "url"
	kItems         = "items"
	kTagName       = "tagName"
	kTagCommit     = "tagCommit"
	kOID           = "oid"
	jobPushRelease = "push-to-registries-release"
)

// fakeGitHub answers the calls of the identity chain: GET /user as the person
// (the bearer verification and the read as the person), GET /user/teams, GET
// /app as the inventory App (its JWT) and the installation token; every name
// check finds the repository free.
type fakeGitHub struct {
	*httptest.Server
	logins map[string]string // person user token → login
	// teams are each login's team slugs in the org (GET /user/teams).
	teams map[string][]string
	// userCalls counts GET /user: the server under test caches a verified
	// bearer, so the count proves the cache.
	userCalls atomic.Int64
	// org answers GraphQL for the fake org (the inventory reads).
	org *fakeOrg
	// files is the fake giantswarm/github: team files, policy files, pull
	// requests, reviews, dispatches; actions its reconciler workflow's runs
	// and their artifacts.
	files   *fakeTeamFiles
	actions *fakeActions
	// repos are the org's other repositories: the ones create_repository
	// creates and scaffolds, and the name checks' answers.
	repos *fakeRepos
	// roles are each login's role in the org (owner = admin); a login
	// without one is not a member.
	roles map[string]string
	// writes records every non-GET call, in order: the proof of what a call
	// wrote and in which order.
	mu     sync.Mutex
	writes []string
	// rest is the REST budget every REST answer reports in GitHub's
	// rate-limit headers; nil sends none.
	rest atomic.Pointer[fakeRESTBudget]
}

// fakeRESTBudget is the REST budget as GitHub's rate-limit headers carry it.
type fakeRESTBudget struct {
	remaining int
	reset     time.Time
}

func newFakeGitHub(t *testing.T, logins map[string]string) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{logins: logins, teams: map[string][]string{}, org: &fakeOrg{remaining: 5000, now: time.Now(), teamFile: teamFile}, files: newFakeTeamFiles(), actions: &fakeActions{}, repos: newFakeRepos(), roles: map[string]string{}}
	// The org's GraphQL knows the repositories the REST surface creates.
	g.org.repos = g.repos
	// The org's GraphQL reads the team files at the team-files fake's main.
	g.org.head = g.files.head
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v3/graphql", func(w http.ResponseWriter, r *http.Request) {
		// The team-files fake takes the auto-merge mutation; the org fake
		// answers every query.
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "enablePullRequestAutoMerge") {
			g.files.handleAutoMerge(w, body)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		g.org.handle(w, r)
	})
	g.files.register(mux, g)
	g.actions.register(mux, g)
	g.repos.register(mux, g)
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
		writeJSON(w, http.StatusOK, map[string]any{"id": 17164699, kSlug: inventoryApp})
	})
	mux.HandleFunc("POST /api/v3/app/installations/{id}/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusCreated, map[string]any{"token": installationToken, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	})
	mux.HandleFunc("GET /api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		g.userCalls.Add(1)
		login, ok := g.logins[bearer(r)]
		if !ok {
			ghMessage(w, http.StatusUnauthorized, "Bad credentials")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{kLogin: login, kID: userID(login)})
	})
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b := g.rest.Load(); b != nil && !strings.HasSuffix(r.URL.Path, "/graphql") {
			w.Header().Set("X-Ratelimit-Remaining", strconv.Itoa(b.remaining))
			w.Header().Set("X-Ratelimit-Limit", "5000")
			w.Header().Set("X-Ratelimit-Reset", strconv.FormatInt(b.reset.Unix(), 10))
		}
		if r.Method != http.MethodGet {
			g.mu.Lock()
			g.writes = append(g.writes, r.Method+" "+r.URL.Path)
			g.mu.Unlock()
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(g.Close)
	return g
}

// written are the non-GET calls so far, in order.
func (g *fakeGitHub) written() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.writes...)
}

// userID is a login's stable numeric id in the fake.
func userID(login string) int64 {
	var n int64
	for _, c := range login {
		n = n*31 + int64(c)
	}
	return n
}

func bearer(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

// asApp says the request carries the installation token — the `token`
// scheme ghinstallation uses, not a bearer.
func asApp(r *http.Request) bool {
	return r.Header.Get("Authorization") == "token "+installationToken
}

// ghMessage is GitHub's error body shape.
func ghMessage(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{kMessage: msg})
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
