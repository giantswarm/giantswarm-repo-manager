// Package e2e proves the identity chain against fakes: muster forwards the
// person's id_token, the server validates it against the IdP, exchanges it at
// muster's broker as its confidential client for the person's GitHub grant
// and reads GitHub as the person; the App answers the unattended reads; the
// inventory lives in a seeded Valkey (VALKEY_ADDR — the CI service container —
// or an in-process miniredis).
package e2e

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-repo-manager/internal/broker"
	"github.com/giantswarm/giantswarm-repo-manager/internal/collect"
	"github.com/giantswarm/giantswarm-repo-manager/internal/gh"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/review"
	"github.com/giantswarm/giantswarm-repo-manager/internal/server"
	"github.com/giantswarm/giantswarm-repo-manager/internal/tools"
)

const (
	dexClient    = "dex-k8s-authenticator"
	brokerClient = "giantswarm-repo-manager"
	brokerSecret = "broker-secret"
	team         = "team-bumblebee"
	argTeam      = "team"
	argEntry     = "entry"
	alice        = "alice"
	aliceEmail   = "alice@example.com"
	// carol has a grant but is in no team of the fixture's files: the
	// outsider of the guard notices and approve_change.
	carol       = "carol"
	testVersion = "test"
	// internalToken authenticates the reconciler's trigger.
	internalToken = "internal-secret" // #nosec G101 -- test fixture
)

type stack struct {
	idp     *fakeIdP
	brk     *fakeBroker
	ghs     *fakeGitHub
	gw      *fakeGateway
	srv     *httptest.Server
	app     *gh.App
	store   *inventory.Store
	col     *collect.Collector
	checker *fakeChecker
	circle  *fakeCircleCI
	log     *slog.Logger
}

// newCollector builds another collector over the same store and fakes, with
// a GraphQL budget floor.
func (st *stack) newCollector(floor int) *collect.Collector {
	return collect.New(collect.Options{Org: org, EngineChecks: true, Concurrency: 2, BudgetFloor: floor}, st.app.Reader(), st.store, st.checker, st.circle, st.log)
}

func newStack(t *testing.T) *stack {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	idp := newFakeIdP(t, dexClient)
	brk := newFakeBroker(t, brokerClient, brokerSecret, map[string]string{alice: "alice-token", carol: "carol-token"})
	ghs := newFakeGitHub(t, map[string]string{"alice-token": alice, "carol-token": carol})
	ghs.teams[alice] = []string{team}
	ghs.teams[carol] = []string{teamOther}
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

	bc, err := broker.New(broker.Config{MusterURL: brk.URL, ClientID: brokerClient, ClientSecret: brokerSecret})
	if err != nil {
		t.Fatal(err)
	}
	app, err := gh.NewApp(gh.AppConfig{APIURL: apiURL, AppID: 42, InstallationID: 7, PrivateKey: appKeyPEM(t)})
	if err != nil {
		t.Fatal(err)
	}
	st := &stack{idp: idp, brk: brk, ghs: ghs, gw: gw, app: app, store: store, checker: &fakeChecker{}, circle: &fakeCircleCI{}, log: log}
	st.col = st.newCollector(0)
	ts := tools.New(tools.Deps{Version: testVersion, GitHubAPIURL: apiURL, Broker: bc, App: app, Inventory: store, Collector: st.col, CircleCIConfigured: true, Log: log,
		TeamFilesRepository: org + "/github", TeamFilesRef: mainBranch,
		Review: review.New(review.Config{BaseURL: gws.URL, TokenFile: tokenFile, Channels: map[string]string{teamPlaneteers: planeteersChannel}})})
	st.col.OnReconciled(ts.Reconciled)
	mcpSrv := ts.MCPServer()

	// The OAuth base URL is this server's own; the listener is httptest's,
	// so the metadata issuer is loopback http.
	s, err := server.New(server.Config{Addr: "127.0.0.1:0", MCPPath: "/mcp", OAuth: &server.OAuthConfig{
		BaseURL: "http://127.0.0.1:1", DexIssuerURL: idp.issuer, DexClientID: "giantswarm-repo-manager", DexClientSecret: "x", DexCAFile: idp.caFile(t),
		DexAllowPrivateIP: true, TrustedAudiences: []string{dexClient}, SSOAllowPrivateIPs: true,
	}, Internal: st.col.InternalHandler(internalToken)}, mcpSrv, log)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	st.srv = hs
	return st
}

// as connects an MCP client the way muster does for the person: the forwarded
// id_token as the bearer.
func (st *stack) as(t *testing.T, idToken string) *client.Client {
	t.Helper()
	c, err := client.NewStreamableHttpClient(st.srv.URL+"/mcp", transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + idToken}))
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
	var info tools.Info
	if err := json.Unmarshal([]byte(text), &info); err != nil {
		t.Fatalf("get_info: %v\n%s", err, text)
	}
	return info
}

// TestIdentityChain: alice connected GitHub in muster; her call carries her
// identity, her grant is released and proven with GET /user, the App and the
// seeded inventory are reported.
func TestIdentityChain(t *testing.T) {
	st := newStack(t)
	info := getInfo(t, st.as(t, st.idp.mint(t, alice, aliceEmail)))

	if info.Caller == nil || info.Caller.Subject != alice || info.Caller.Email != aliceEmail || info.Caller.Source != "sso" {
		t.Errorf("caller: %+v", info.Caller)
	}
	if g := info.GitHub.Grant; !g.Obtained || g.Login != alice || g.Audience != kGitHub || g.ExpiresIn == "" {
		t.Errorf("grant: %+v", g)
	}
	if a := info.GitHub.App; a == nil || a.Slug != "giantswarm-align-files" || a.ID != 17164699 || a.InstallationID != 7 || info.GitHub.AppError != "" {
		t.Errorf("app: %+v (%s)", a, info.GitHub.AppError)
	}
	if inv := info.Inventory; !inv.Connected || inv.Records != 1 || inv.Error != "" {
		t.Errorf("inventory: %+v", inv)
	}
	// The engine's version comes from the binary's build info; whether a
	// *test* binary lists module deps depends on the toolchain (Go 1.27 does,
	// 1.26 does not), so only the module and package are asserted here — the
	// built binary is checked with `go version -m`.
	if !info.GitHub.CircleCIConfigured || !info.Capabilities.ApplyRefused || info.Engine.Version == "" ||
		info.Engine.Module != "github.com/giantswarm/devctl/v8" || !strings.HasSuffix(info.Engine.Package, "/pkg/reposetup") {
		t.Errorf("info: circleci=%v caps=%+v engine=%+v", info.GitHub.CircleCIConfigured, info.Capabilities, info.Engine)
	}
}

// TestPersonWithoutGrantIsToldToConnect: bob is authenticated but never
// connected GitHub — no grant, a pointer to muster, nothing else fails.
func TestPersonWithoutGrantIsToldToConnect(t *testing.T) {
	st := newStack(t)
	info := getInfo(t, st.as(t, st.idp.mint(t, "bob", "bob@example.com")))
	if info.Caller == nil || info.Caller.Subject != "bob" {
		t.Errorf("caller: %+v", info.Caller)
	}
	if g := info.GitHub.Grant; g.Obtained || !strings.Contains(g.Reason, "connect GitHub in muster") {
		t.Errorf("grant: %+v", g)
	}
	if !info.Inventory.Connected {
		t.Errorf("inventory: %+v", info.Inventory)
	}
}

// TestUnauthenticatedCallIsRefused: without a bearer the MCP endpoint is a
// 401 — nothing runs as the ServiceAccount.
func TestUnauthenticatedCallIsRefused(t *testing.T) {
	st := newStack(t)
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
	if _, err := c.Initialize(context.Background(), mcp.InitializeRequest{}); err == nil {
		t.Error("a second initialize without a bearer succeeded")
	}
}

// TestWritesAsThePerson: apply is refused for the authenticated person too,
// and the dry run runs the engine with the App answering the name check.
func TestWritesAsThePerson(t *testing.T) {
	st := newStack(t)
	c := st.as(t, st.idp.mint(t, alice, aliceEmail))
	entry := map[string]any{kName: "example-service", "componentType": kService, "gen": map[string]any{"language": "go", kFlavours: []any{kApp}, "ci": map[string]any{kChartName: "example-service"}}}

	text, isErr := call(t, c, tools.ToolCreateRepository, map[string]any{tools.ArgMode: "apply", argTeam: team, argEntry: entry})
	if !isErr || text != tools.ApplyRefusal {
		t.Errorf("apply: isError=%v %q", isErr, text)
	}
	text, isErr = call(t, c, tools.ToolCreateRepository, map[string]any{tools.ArgDryRun: true, argTeam: team, argEntry: entry})
	if isErr || !strings.Contains(text, `"accepted": true`) || !strings.Contains(text, `"verdict": "free"`) {
		t.Errorf("dry run with the App's name check: isError=%v %s", isErr, text)
	}
}
