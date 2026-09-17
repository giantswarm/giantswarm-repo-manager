// Package e2e proves the identity chain against fakes: muster puts the
// person's GitHub user token — their authorization of the App
// giantswarm-repo-manager — on every call as the bearer; the server verifies
// it with GET /user (once per token, then from its cache), refuses a request
// without a bearer or with one GitHub refuses, and reads and writes GitHub as
// the person; the App answers the unattended reads; the inventory lives in a
// seeded Valkey (VALKEY_ADDR — the CI service container — or an in-process
// miniredis).
package e2e

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-repo-manager/internal/collect"
	"github.com/giantswarm/giantswarm-repo-manager/internal/gh"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/review"
	"github.com/giantswarm/giantswarm-repo-manager/internal/server"
	"github.com/giantswarm/giantswarm-repo-manager/internal/tools"
)

const (
	team     = "team-bumblebee"
	argTeam  = "team"
	argEntry = "entry"
	alice    = "alice"
	// The user tokens muster puts on the calls: alice's and carol's are
	// GitHub's, bob's is one GitHub refuses (never authorized, or revoked).
	aliceToken = "alice-token"
	carolToken = "carol-token"
	bobToken   = "bob-token"
	daveToken  = "dave-token"
	// carol has a token but is in no team of the fixture's files: the
	// outsider of the guard notices and approve_change.
	carol = "carol"
	// dave is in no team at all.
	dave        = "dave"
	testVersion = "test"
	// baseURL is where muster reaches the server: the resource of the
	// protected-resource metadata (the listener is httptest's).
	baseURL = "http://giantswarm-repo-manager.test:8080"
)

type stack struct {
	ghs     *fakeGitHub
	gw      *fakeGateway
	srv     *httptest.Server
	app     *gh.App
	store   *inventory.Store
	col     *collect.Collector
	checker *fakeChecker
	log     *slog.Logger
	// offset moves the collectors' clock: advance lets a pending run's
	// window run out without waiting.
	offset atomic.Int64
}

// newCollector builds another collector over the same store and fakes, with
// a GraphQL budget floor.
func (st *stack) newCollector(floor int) *collect.Collector {
	return collect.New(collect.Options{Org: org, EngineChecks: true, Concurrency: 2, BudgetFloor: floor, Now: st.now,
		Reconciler: collect.ReconcilerOptions{Repository: org + "/github"}}, st.app.Reader(), st.store, st.checker, st.log)
}

// now is the collectors' clock.
func (st *stack) now() time.Time { return time.Now().Add(time.Duration(st.offset.Load())) }

// advance moves the collectors' clock forward by d.
func (st *stack) advance(d time.Duration) { st.offset.Add(int64(d)) }

// poll runs one reconciler poll.
func (st *stack) poll(t *testing.T) *collect.ReconcilerPoll {
	t.Helper()
	p, err := st.col.PollReconciler(context.Background())
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	return p
}

func newStack(t *testing.T) *stack {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ghs := newFakeGitHub(t, map[string]string{aliceToken: alice, carolToken: carol, daveToken: dave})
	ghs.teams[alice] = []string{team}
	ghs.teams[carol] = []string{teamOther}
	// alice and carol are owners of the org, dave a member: the one the
	// creation refuses.
	ghs.roles[alice], ghs.roles[carol], ghs.roles[dave] = roleAdmin, roleAdmin, roleMember
	apiURL := ghs.URL + "/api/v3"
	gw := &fakeGateway{token: "sa-token"}
	gws := httptest.NewServer(gw.handler())
	t.Cleanup(gws.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(gw.token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	addr := os.Getenv("VALKEY_ADDR")
	if addr == "" {
		mr := miniredis.RunT(t)
		addr = mr.Addr()
	}
	store, err := inventory.Open(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	// The CI job's Valkey is shared by every test: start from nothing.
	if err := store.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A record no sweep of the fake org produces: get_info counts it, the
	// sweep removes it.
	if err := store.Put(context.Background(), &inventory.Record{Repository: org + "/giantswarm-repo-manager", Name: "giantswarm-repo-manager", Declaration: &inventory.Declaration{Team: team}, RefreshedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	app, err := gh.NewApp(gh.AppConfig{APIURL: apiURL, AppID: 42, InstallationID: 7, PrivateKey: appKeyPEM(t)})
	if err != nil {
		t.Fatal(err)
	}
	st := &stack{ghs: ghs, gw: gw, app: app, store: store, checker: &fakeChecker{}, log: log}
	st.col = st.newCollector(0)
	ts := tools.New(tools.Deps{Version: testVersion, GitHubAPIURL: apiURL, AuthorizationServer: server.DefaultAuthorizationServer, App: app, Inventory: store, Collector: st.col, Log: log,
		TeamFilesRepository: org + "/github", TeamFilesRef: mainBranch, SweepTeams: []string{team, teamPlaneteers},
		Review:   review.New(review.Config{BaseURL: gws.URL, TokenFile: tokenFile, Channels: map[string]string{teamPlaneteers: planeteersChannel}}),
		Scaffold: fakeScaffold{}})
	st.col.OnReconciled(ts.Reconciled)
	mcpSrv := ts.MCPServer()

	s, err := server.New(server.Config{Addr: "127.0.0.1:0", MCPPath: "/mcp",
		OAuth: &server.OAuthConfig{BaseURL: baseURL, AuthorizationServer: server.DefaultAuthorizationServer, GitHubAPIURL: apiURL}}, mcpSrv, log)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	st.srv = hs
	return st
}

// as connects an MCP client the way muster does for the person: their GitHub
// user token as the bearer.
func (st *stack) as(t *testing.T, token string) *client.Client {
	t.Helper()
	c, err := client.NewStreamableHttpClient(st.srv.URL+"/mcp", transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Initialize(context.Background(), mcp.InitializeRequest{}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func call(t *testing.T, c *client.Client, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool, Arguments: args}})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var b strings.Builder
	for _, cnt := range res.Content {
		if tc, ok := cnt.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func getInfo(t *testing.T, c *client.Client) tools.Info {
	t.Helper()
	text, isErr := call(t, c, tools.ToolGetInfo, nil)
	if isErr {
		t.Fatalf("get_info: %s", text)
	}
	t.Logf("get_info: %s", text)
	var info tools.Info
	if err := json.Unmarshal([]byte(text), &info); err != nil {
		t.Fatalf("get_info: %v\n%s", err, text)
	}
	return info
}

// rawMCP posts one request to the MCP endpoint with the given Authorization
// header and returns the response (the client's view of a refusal).
func (st *stack) rawMCP(t *testing.T, authorization string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, st.srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

// TestCallerFromTheBearer: alice's call carries her user token; GET /user
// names her once — the second call is answered from the cache — and get_info
// reports her, the bearer mode with the pinned authorization server, the App
// and the seeded inventory.
func TestCallerFromTheBearer(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	info := getInfo(t, c)

	if info.Caller == nil || info.Caller.Login != alice || info.Caller.ID != userID(alice) {
		t.Errorf("caller: %+v", info.Caller)
	}
	if a := info.Auth; a.Mode != tools.AuthModeBearer || a.AuthorizationServer != server.DefaultAuthorizationServer || a.Reason != "" {
		t.Errorf("auth: %+v", a)
	}
	if a := info.GitHub.App; a == nil || a.Slug != inventoryApp || a.ID != 17164699 || a.InstallationID != 7 || info.GitHub.AppError != "" {
		t.Errorf("app: %+v (%s)", a, info.GitHub.AppError)
	}
	if inv := info.Inventory; !inv.Connected || inv.Records != 1 || inv.Error != "" || inv.Identity != "app "+inventoryApp+" (installation 7)" {
		t.Errorf("inventory: %+v", inv)
	}
	if info.CircleCI.Source != inventory.CircleCISourceBoth {
		t.Errorf("circleci source: %+v", info.CircleCI)
	}
	if tf := info.TeamFiles; tf.Readable != tools.ReadableTrue || tf.Repository != org+"/github" || tf.Ref != mainBranch || tf.Reason != "" {
		t.Errorf("team files as alice: %+v", tf)
	}
	// The engine's version comes from the binary's build info; whether a
	// *test* binary lists module deps depends on the toolchain (Go 1.27 does,
	// 1.26 does not), so only the module and package are asserted here — the
	// built binary is checked with `go version -m`.
	if !info.Capabilities.ApplyRefused || info.Engine.Version == "" ||
		info.Engine.Module != "github.com/giantswarm/devctl/v8" || !strings.HasSuffix(info.Engine.Package, "/pkg/reposetup") {
		t.Errorf("info: caps=%+v engine=%+v", info.Capabilities, info.Engine)
	}
	// initialize and two get_info calls: the token was verified once.
	getInfo(t, c)
	if n := st.ghs.userCalls.Load(); n != 1 {
		t.Errorf("GET /user was called %d times for one token, want 1 (the cache)", n)
	}
}

// TestUnauthenticatedCallIsRefused: without a bearer the MCP endpoint is a
// bare 401 whose challenge names the protected-resource metadata, and the
// metadata names the pinned authorization server — nothing runs as the
// ServiceAccount.
func TestUnauthenticatedCallIsRefused(t *testing.T) {
	st := newStack(t)
	resp, body := st.rawMCP(t, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d %s, want 401", resp.StatusCode, body)
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	if !strings.HasPrefix(challenge, `Bearer realm="giantswarm-repo-manager"`) || !strings.Contains(challenge, `resource_metadata="`+baseURL+`/.well-known/oauth-protected-resource/mcp"`) || strings.Contains(challenge, "error=") {
		t.Errorf("challenge: %q", challenge)
	}
	if !strings.Contains(body, "core_auth_login server=giantswarm-repo-manager") {
		t.Errorf("body: %q", body)
	}

	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		res, err := http.Get(st.srv.URL + path) // #nosec G107 -- test server URL
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
		}
		err = json.NewDecoder(res.Body).Decode(&doc)
		_ = res.Body.Close()
		if err != nil || res.StatusCode != http.StatusOK || doc.Resource != baseURL+"/mcp" || len(doc.AuthorizationServers) != 1 || doc.AuthorizationServers[0] != server.DefaultAuthorizationServer {
			t.Errorf("%s: %d %+v %v", path, res.StatusCode, doc, err)
		}
	}

	c, err := client.NewStreamableHttpClient(st.srv.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	// mcp-go words the 401 as "authorization required".
	if _, err := c.Initialize(context.Background(), mcp.InitializeRequest{}); err == nil || !strings.Contains(err.Error(), "authorization required") {
		t.Errorf("initialize without a bearer: %v, want a 401", err)
	}
}

// TestRefusedBearerNamesTheSignIn: bob's token is one GitHub refuses (never
// authorized the App, or revoked): 401 with invalid_token, the body naming
// the sign-in on this server; the MCP client sees the 401 too.
func TestRefusedBearerNamesTheSignIn(t *testing.T) {
	st := newStack(t)
	resp, body := st.rawMCP(t, "Bearer "+bobToken)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refused bearer: %d %s, want 401", resp.StatusCode, body)
	}
	if ch := resp.Header.Get("WWW-Authenticate"); !strings.Contains(ch, `error="invalid_token"`) || !strings.Contains(ch, "resource_metadata=") {
		t.Errorf("challenge: %q", ch)
	}
	if !strings.HasPrefix(strings.TrimSpace(body), "GitHub refused the bearer token (401): no GitHub authorization for you yet") || !strings.Contains(body, "core_auth_login server=giantswarm-repo-manager") {
		t.Errorf("body: %q", body)
	}

	c, err := client.NewStreamableHttpClient(st.srv.URL+"/mcp", transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + bobToken}))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Initialize(context.Background(), mcp.InitializeRequest{}); err == nil || !strings.Contains(err.Error(), "authorization required") {
		t.Errorf("initialize with a refused bearer: %v, want a 401", err)
	}
}

// TestWritesAsThePerson: apply is refused for the authenticated person too,
// and the dry run runs the engine with the App answering the name check.
func TestWritesAsThePerson(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	entry := map[string]any{kName: "example-service", "componentType": kService, kGen: map[string]any{kLanguage: kGo, kFlavours: []any{kApp}, kCI: map[string]any{kChartName: "example-service"}}}

	text, isErr := call(t, c, tools.ToolCreateRepository, map[string]any{tools.ArgMode: "apply", argTeam: team, argEntry: entry})
	if !isErr || text != tools.ApplyRefusal {
		t.Errorf("apply: isError=%v %q", isErr, text)
	}
	text, isErr = call(t, c, tools.ToolCreateRepository, map[string]any{tools.ArgDryRun: true, argTeam: team, argEntry: entry})
	if isErr || !strings.Contains(text, `"accepted": true`) || !strings.Contains(text, `"verdict": "free"`) {
		t.Errorf("dry run with the App's name check: isError=%v %s", isErr, text)
	}
}
