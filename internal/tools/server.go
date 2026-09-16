// Package tools is the MCP surface: behind muster the tools appear as
// x_giantswarm-repo-manager_<tool>. get_info proves the identity chain; the
// write tools go through the framework in write.go.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-repo-manager/internal/broker"
	"github.com/giantswarm/giantswarm-repo-manager/internal/collect"
	"github.com/giantswarm/giantswarm-repo-manager/internal/gh"
	"github.com/giantswarm/giantswarm-repo-manager/internal/identity"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/review"
)

// ToolPrefix is the MCPServer name muster registers this server under.
const ToolPrefix = "giantswarm-repo-manager"

// Tool names.
const (
	ToolGetInfo          = "get_info"
	ToolCreateRepository = "create_repository"
)

// WriteToolNames lists every tool registered through the write framework.
func WriteToolNames() []string {
	return []string{ToolCreateRepository, ToolUpdateRepository, ToolTransferRepository, ToolSetLifecycle, ToolApproveChange, ToolReconcileRepository}
}

// engineModule is the devctl module the engine package comes from; its version
// is read from the build info.
const engineModule = "github.com/giantswarm/devctl/v8"

// Deps are the identities and stores the tools use. Any of them may be nil:
// get_info then reports what is missing instead of failing.
type Deps struct {
	Version string
	// GitHubAPIURL is the API base URL person and App calls go to (empty:
	// api.github.com; the fake in tests).
	GitHubAPIURL string
	Broker       *broker.Client
	App          *gh.App
	// Reader is the unattended read identity when it is not the App (the
	// development token); nil when App is set or nothing reads.
	Reader    *gh.Reader
	Inventory *inventory.Store
	// Collector fills the inventory; nil when the store or a GitHub read
	// identity is missing (refresh_repository then says so).
	Collector *collect.Collector
	// CircleCIConfigured says whether CIRCLECI_API_TOKEN is set; the token is
	// not used before the reconciler slice.
	CircleCIConfigured bool
	// TeamFilesRepository and TeamFilesRef are where the team files live
	// (giantswarm/github at main; a fixture in tests).
	TeamFilesRepository, TeamFilesRef string
	// Review is klaus-gateway's team-review endpoint; nil leaves the asks
	// undelivered and reported as such.
	Review *review.Client
	Log    *slog.Logger
}

// NewMCPServer builds the MCP server with every tool registered.
func NewMCPServer(d Deps) *mcpserver.MCPServer { return New(d).MCPServer() }

// Tools is the tool set: the MCP server and the hooks other components call
// (the completion message after a reconciler run).
type Tools struct{ t *tools }

// New builds the tool set.
func New(d Deps) *Tools {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Tools{t: &tools{d: d}}
}

// MCPServer registers every tool on a new MCP server.
func (ts *Tools) MCPServer() *mcpserver.MCPServer {
	d, t := ts.t.d, ts.t
	s := mcpserver.NewMCPServer(ToolPrefix, d.Version,
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithInstructions("Giant Swarm's repository set-up service. The team files in giantswarm/github (repositories/team-*.yaml) are the desired state of every repository; GitHub is the reality. Call get_info first: it reports who you are to this server, whether your GitHub grant could be obtained through muster (connect GitHub in muster if not), the App identity used for unattended reads and the inventory store. The inventory (list_repositories, get_repository) is one record per repository of the org — declaration, GitHub reality, set-up state, orphan score with reasons, findings — refreshed by a scheduled sweep, after every reconciler run and on refresh_repository; every record carries its age. Every write tool takes dryRun and mode; the only write mode is commit — a team-file pull request opened as you — and apply is refused."),
	)
	s.AddTool(mcp.NewTool(ToolGetInfo,
		mcp.WithDescription("Read-only. Report the service version and the identity chain of this call: the caller muster forwarded (subject, email, groups), whether the caller's GitHub grant was obtained from muster's token broker and the GitHub login it belongs to (proven with a read call), the App identity used for unattended inventory reads, the inventory store, the engine (devctl reposetup package) and the write modes. Call first."),
		mcp.WithReadOnlyHintAnnotation(true),
	), t.getInfo)
	t.registerInventory(s)
	t.registerValidate(s)
	for _, wt := range []WriteTool{t.createRepository(), t.updateRepository(), t.transferRepository(), t.setLifecycle(), t.approveChange(), t.reconcileRepository()} {
		registerWrite(s, wt)
	}
	return s
}

type tools struct{ d Deps }

// Info is get_info's result.
type Info struct {
	Version      string             `json:"version"`
	ToolPrefix   string             `json:"toolPrefix"`
	Caller       *identity.Identity `json:"caller"`
	GitHub       GitHubInfo         `json:"github"`
	Inventory    InventoryInfo      `json:"inventory"`
	Engine       EngineInfo         `json:"engine"`
	Capabilities Capabilities       `json:"capabilities"`
}

// GitHubInfo is the person's grant and the App identity.
type GitHubInfo struct {
	APIURL string       `json:"apiUrl"`
	Grant  GrantInfo    `json:"grant"`
	App    *gh.Identity `json:"app,omitempty"`
	// AppError says why the App identity is missing.
	AppError string `json:"appError,omitempty"`
	// CircleCIConfigured says whether the CircleCI token is set.
	CircleCIConfigured bool `json:"circleciConfigured"`
}

// GrantInfo is the outcome of the broker exchange for this call.
type GrantInfo struct {
	Obtained bool `json:"obtained"`
	// Login is the GitHub login the released grant belongs to (GET /user).
	Login     string `json:"login,omitempty"`
	ExpiresIn string `json:"expiresIn,omitempty"`
	Audience  string `json:"audience,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// InventoryInfo is the store's state.
type InventoryInfo struct {
	Address   string `json:"address,omitempty"`
	Connected bool   `json:"connected"`
	Records   int    `json:"records"`
	Error     string `json:"error,omitempty"`
}

// EngineInfo names the engine package and its version.
type EngineInfo struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	Package string `json:"package"`
}

// Capabilities are the write modes.
type Capabilities struct {
	Modes        []string `json:"modes"`
	ApplyRefused bool     `json:"applyRefused"`
	WriteTools   []string `json:"writeTools"`
}

func (t *tools) getInfo(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	info := Info{
		Version:    t.d.Version,
		ToolPrefix: ToolPrefix,
		GitHub:     GitHubInfo{APIURL: apiURL(t.d.GitHubAPIURL), CircleCIConfigured: t.d.CircleCIConfigured},
		Engine:     EngineInfo{Module: engineModule, Version: EngineVersion(), Package: engineModule + "/pkg/reposetup"},
		Capabilities: Capabilities{
			Modes:        []string{string(ModeCommit)},
			ApplyRefused: true,
			WriteTools:   WriteToolNames(),
		},
	}
	if id, ok := identity.FromContext(ctx); ok {
		info.Caller = id
	}
	info.GitHub.Grant = t.grant(ctx)
	if t.d.App != nil {
		app, err := t.d.App.Identity(ctx)
		if err != nil {
			info.GitHub.AppError = err.Error()
		}
		info.GitHub.App = app
	} else {
		info.GitHub.AppError = "GitHub App not configured (GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, GITHUB_APP_PRIVATE_KEY_FILE)"
	}
	info.Inventory = t.inventory(ctx)
	return result(info, nil)
}

// grant runs the identity chain for this call: the forwarded id_token is
// exchanged at muster's broker for the caller's GitHub grant, and the grant
// is proven with GET /user.
func (t *tools) grant(ctx context.Context) GrantInfo {
	if t.d.Broker == nil {
		return GrantInfo{Reason: "broker client not configured (MUSTER_URL, BROKER_CLIENT_ID, BROKER_CLIENT_SECRET)"}
	}
	g := GrantInfo{Audience: t.d.Broker.Audience()}
	tok, ok := identity.TokenFromContext(ctx)
	if !ok {
		g.Reason = "the request carried no IdP id_token to exchange (is the server behind muster with OAuth on?)"
		return g
	}
	grant, err := t.d.Broker.Exchange(ctx, tok)
	if err != nil {
		g.Reason = err.Error()
		if errors.Is(err, broker.ErrNoGrant) {
			g.Reason = "no GitHub grant for you yet: connect GitHub in muster (core_auth_login on the GitHub server), then call again"
		}
		return g
	}
	g.Obtained = true
	g.ExpiresIn = grant.ExpiresIn.Round(time.Second).String()
	login, err := gh.Login(ctx, t.d.GitHubAPIURL, grant.AccessToken)
	if err != nil {
		g.Reason = "grant obtained, but the read call with it failed: " + err.Error()
		return g
	}
	g.Login = login
	return g
}

func (t *tools) inventory(ctx context.Context) InventoryInfo {
	if t.d.Inventory == nil {
		return InventoryInfo{Error: "inventory store not configured (VALKEY_ADDR)"}
	}
	inv := InventoryInfo{Address: t.d.Inventory.Address()}
	if err := t.d.Inventory.Ping(ctx); err != nil {
		inv.Error = err.Error()
		return inv
	}
	inv.Connected = true
	n, err := t.d.Inventory.Count(ctx)
	if err != nil {
		inv.Error = err.Error()
		return inv
	}
	inv.Records = n
	return inv
}

// EngineVersion is the devctl module version this binary was built with,
// from the build info; "unknown" outside a module build.
func EngineVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range bi.Deps {
		if dep.Path == engineModule {
			if dep.Replace != nil {
				return dep.Replace.Version
			}
			return dep.Version
		}
	}
	return "unknown"
}

func apiURL(u string) string {
	if u == "" {
		return "https://api.github.com"
	}
	return u
}

// result renders v as the tool's JSON text, or err as a tool error.
func result(v any, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError("encode result: " + err.Error()), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}
