// Package tools is the MCP surface: behind muster the tools appear as
// x_giantswarm-repo-manager_<tool>. get_info reports who the call runs as; the
// write tools go through the framework in write.go.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-repo-manager/internal/collect"
	"github.com/giantswarm/giantswarm-repo-manager/internal/gh"
	"github.com/giantswarm/giantswarm-repo-manager/internal/identity"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/review"
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
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
	return []string{ToolCreateRepository, ToolAdoptRepository, ToolUpdateRepository, ToolTransferRepository, ToolSetLifecycle, ToolApproveChange, ToolAlignRepository}
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
	// AuthorizationServer is the issuer identity muster pins for this server
	// (the App giantswarm-repo-manager's), reported by get_info; empty when
	// the server runs without OAuth.
	AuthorizationServer string
	// App is the read-only App giantswarm-repo-manager-inventory, the one
	// identity of the unattended reads; nil when it is not configured — the
	// tools that need it say so, nothing stands in.
	App       *gh.App
	Inventory *inventory.Store
	// Collector fills the inventory; nil when the store or the inventory App
	// is missing (refresh_repository and sweep_inventory then say so).
	Collector *collect.Collector
	// RenovateActive is the period a Renovate pull request or commit counts
	// as activity within (the renovate filter's active and inactive); 0 is
	// DefaultRenovateActive.
	RenovateActive time.Duration
	// SweepTeams are the GitHub team slugs whose members may start a sweep
	// with sweep_inventory: the teams that own the manager and the
	// reconciler. Empty leaves the tool refusing everyone.
	SweepTeams []string
	// TeamFilesRepository and TeamFilesRef are where the team files live
	// (giantswarm/github at main; a fixture in tests).
	TeamFilesRepository, TeamFilesRef string
	// ReconcilerWorkflow is the reconciler's workflow file in that
	// repository, the one align_repository dispatches and the inventory
	// reads the runs of; empty is teamfiles.ReconcilerWorkflow.
	ReconcilerWorkflow string
	// WatchInterval is how often watch_repository reads GitHub while it
	// follows a new repository; 0 is DefaultWatchInterval. WatchSettle is
	// how long the release's CircleCI statuses must stay unchanged before
	// the released phase is done; 0 is DefaultWatchSettle.
	WatchInterval, WatchSettle time.Duration
	// Review is klaus-gateway's team-review endpoint; nil leaves the asks
	// undelivered and reported as such.
	Review *review.Client
	// Scaffold renders the scaffold create_repository pushes as the caller.
	// nil is the engine's renderer over the templates on GitHub, downloaded
	// with the caller's token (giantswarm/template is private); tests set a
	// fixed one.
	Scaffold reconcile.ScaffoldRenderer
	Log      *slog.Logger
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
		mcpserver.WithInstructions("Giant Swarm's repository set-up service. The team files in giantswarm/github (repositories/team-*.yaml) are the desired state of every repository; GitHub is the reality. Call get_info first: it reports who you are to this server (the GitHub login of the token muster put on the call — your own authorization of the App giantswarm-repo-manager), the identity of the unattended reads — the read-only App giantswarm-repo-manager-inventory — and the inventory store. The inventory (list_repositories, get_repository) is one record per repository of the org — declaration, GitHub reality, set-up state, findings — refreshed by a scheduled sweep, after every reconciler run and on refresh_repository; every record carries its age. Every write tool takes dryRun and mode; the only write mode is commit — a team-file pull request opened as you — and apply is refused."),
	)
	s.AddTool(mcp.NewTool(ToolGetInfo,
		mcp.WithDescription("Read-only. Report the service version and how this call is authenticated: the caller (the GitHub login and id GET /user answered for the bearer muster put on the call — the person's own user token through the App giantswarm-repo-manager) and the authorization server pinned for it; whether your credential reaches the team files (teamFiles.readable); the identity of the unattended inventory reads (the read-only App giantswarm-repo-manager-inventory, or not configured) and the inventory store; where the inventory's CircleCI facts come from (commit statuses and the reconciler's run artifact — this server holds no CircleCI token); the team-review endpoint (reviews.configured, and reviews.debugChannel when one channel receives every ask and notice instead of the teams' channels); the engine (devctl reposetup package) and the write modes. Call first."),
		mcp.WithReadOnlyHintAnnotation(true),
	), t.getInfo)
	t.registerInventory(s)
	t.registerValidate(s)
	t.registerWatch(s)
	for _, wt := range []WriteTool{t.createRepository(), t.adoptRepository(), t.updateRepository(), t.transferRepository(), t.setLifecycle(), t.approveChange(), t.alignRepository()} {
		registerWrite(s, wt)
	}
	return s
}

// tools is the tool set's state: the dependencies and the per-token cache of
// the team-files probe (get_info's teamFiles.readable).
type tools struct {
	d      Deps
	probes probes
}

// reconcilerWorkflow is the reconciler's workflow file.
func (d Deps) reconcilerWorkflow() string {
	if d.ReconcilerWorkflow == "" {
		return teamfiles.ReconcilerWorkflow
	}
	return d.ReconcilerWorkflow
}

// Info is get_info's result.
type Info struct {
	Version    string `json:"version"`
	ToolPrefix string `json:"toolPrefix"`
	// Caller is the person the bearer belongs to; null without OAuth.
	Caller       *identity.Identity `json:"caller"`
	Auth         AuthInfo           `json:"auth"`
	GitHub       GitHubInfo         `json:"github"`
	TeamFiles    TeamFilesInfo      `json:"teamFiles"`
	Inventory    InventoryInfo      `json:"inventory"`
	CircleCI     CircleCIInfo       `json:"circleci"`
	Reviews      ReviewsInfo        `json:"reviews"`
	Engine       EngineInfo         `json:"engine"`
	Capabilities Capabilities       `json:"capabilities"`
}

// Authentication modes get_info reports.
const (
	// AuthModeBearer: the person's GitHub user token is the bearer of every
	// call, verified with GET /user.
	AuthModeBearer = "bearer"
	// AuthModeNone: the server runs without OAuth; nothing acts as a person.
	AuthModeNone = "none"
)

// AuthInfo is how this call was authenticated.
type AuthInfo struct {
	Mode string `json:"mode"`
	// AuthorizationServer is the issuer identity muster pins for this server:
	// the App giantswarm-repo-manager's.
	AuthorizationServer string `json:"authorizationServer,omitempty"`
	// Reason says why there is no caller.
	Reason string `json:"reason,omitempty"`
}

// GitHubInfo is the inventory App's identity and the API the calls go to.
type GitHubInfo struct {
	APIURL string `json:"apiUrl"`
	// App is the read-only App of the unattended reads as GET /app names it.
	App *gh.Identity `json:"app,omitempty"`
	// AppError says why the App identity is missing.
	AppError string `json:"appError,omitempty"`
}

// Readability of the team files with the caller's credential.
const (
	ReadableTrue    = "true"
	ReadableFalse   = "false"
	ReadableUnknown = "unknown"
)

// TeamFilesInfo is the team-files repository and whether the caller's
// credential reaches it (one probe of the repository, cached per token).
type TeamFilesInfo struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
	// Readable is true, false or unknown; false names the fix in Reason.
	Readable string `json:"readable"`
	Reason   string `json:"reason,omitempty"`
}

// InventoryInfo is the store's state and the identity of the unattended reads.
type InventoryInfo struct {
	// Identity is `app <slug> (installation <id>)` — the read-only App
	// giantswarm-repo-manager-inventory — or `not configured`.
	Identity  string `json:"identity"`
	Address   string `json:"address,omitempty"`
	Connected bool   `json:"connected"`
	Records   int    `json:"records"`
	Error     string `json:"error,omitempty"`
}

// CircleCIInfo says where the inventory's CircleCI facts come from.
type CircleCIInfo struct {
	// Source is statuses+artifact: the `ci/circleci:` commit statuses on the
	// default branch head and the reconciler's run artifact; no token.
	Source string `json:"source"`
}

// ReviewsInfo is the team-review endpoint: whether asks and notices are
// delivered at all, and the debug channel when one receives them all.
type ReviewsInfo struct {
	// Configured says whether the gateway is set (REVIEWS_URL).
	Configured bool `json:"configured"`
	// DebugChannel, when set, receives every ask and notice instead of the
	// channel the policy file names; the text names that channel.
	DebugChannel string `json:"debugChannel,omitempty"`
}

// IdentityNotConfigured is the inventory identity without the App.
const IdentityNotConfigured = "not configured"

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
		GitHub:     GitHubInfo{APIURL: apiURL(t.d.GitHubAPIURL)},
		CircleCI:   CircleCIInfo{Source: inventory.CircleCISourceBoth},
		Reviews:    ReviewsInfo{Configured: t.d.Review != nil, DebugChannel: t.d.Review.DebugChannel()},
		Engine:     EngineInfo{Module: engineModule, Version: EngineVersion(), Package: engineModule + "/pkg/reposetup"},
		Capabilities: Capabilities{
			Modes:        []string{string(ModeCommit)},
			ApplyRefused: true,
			WriteTools:   WriteToolNames(),
		},
	}
	if id, ok := identity.FromContext(ctx); ok {
		info.Caller = id
		info.Auth = AuthInfo{Mode: AuthModeBearer, AuthorizationServer: t.d.AuthorizationServer}
	} else {
		info.Auth = AuthInfo{Mode: AuthModeNone, Reason: "the request carried no verified bearer: the server runs without OAuth (oauth.enabled), nothing acts as a person"}
	}
	info.TeamFiles = t.teamFilesInfo(ctx)
	info.Inventory = t.inventory(ctx)
	if t.d.App != nil {
		app, err := t.d.App.Identity(ctx)
		if err != nil {
			info.GitHub.AppError = err.Error()
		}
		info.GitHub.App = app
		info.Inventory.Identity = appIdentity(app, err)
	} else {
		info.GitHub.AppError = ErrNoApp.Error()
		info.Inventory.Identity = IdentityNotConfigured
	}
	return result(info, nil)
}

// appIdentity renders the inventory identity: `app <slug> (installation
// <id>)`; when GET /app failed, the installation alone with the reason.
func appIdentity(app *gh.Identity, err error) string {
	if err != nil || app == nil {
		return fmt.Sprintf("app installation %d (GET /app failed: %v)", installationID(app), err)
	}
	return fmt.Sprintf("app %s (installation %d)", app.Slug, app.InstallationID)
}

func installationID(app *gh.Identity) int64 {
	if app == nil {
		return 0
	}
	return app.InstallationID
}

// teamFilesInfo probes whether the caller's credential reaches the team-files
// repository; without a caller nothing can be probed.
func (t *tools) teamFilesInfo(ctx context.Context) TeamFilesInfo {
	info := TeamFilesInfo{Repository: t.d.TeamFilesRepository, Ref: t.d.TeamFilesRef, Readable: ReadableUnknown}
	if info.Repository == "" {
		info.Repository = teamfiles.DefaultRepository
	}
	if info.Ref == "" {
		info.Ref = teamfiles.DefaultRef
	}
	p, err := t.caller(ctx)
	if err != nil {
		info.Reason = "no caller to probe as: " + err.Error()
		return info
	}
	reachable, err := t.probes.reachable(ctx, p)
	switch {
	case err != nil:
		info.Reason = err.Error()
	case reachable:
		info.Readable = ReadableTrue
	default:
		info.Readable = ReadableFalse
		info.Reason = fmt.Sprintf("your authorization of the App %s does not reach %s: the App must be installed on all repositories (an org owner's setting), or your own access does not include it", teamfiles.WriteApp, info.Repository)
	}
	return info
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
