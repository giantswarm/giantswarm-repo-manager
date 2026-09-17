package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// The inventory tools.
const (
	ToolListRepositories  = "list_repositories"
	ToolGetRepository     = "get_repository"
	ToolRefreshRepository = "refresh_repository"
	ToolSweepInventory    = "sweep_inventory"
)

const (
	argRepository = "repository"
	argUndeclared = "undeclared"
	argFinding    = "finding"
	argLimit      = "limit"
	argScope      = "scope"
	argSearch     = "search"
	argRenovate   = "renovate"
	argVisibility = "visibility"
	argFork       = "fork"
	argArchived   = "archived"
	argInactive   = "inactiveDays"

	defaultLimit = 100
)

// Scopes of list_repositories (PRD D7, D9).
const (
	ScopeMine       = "mine"
	ScopeTeam       = "team"
	ScopeUnassigned = "unassigned"
	ScopeAll        = "all"
)

// Renovate filter values.
const (
	RenovateConfigured = "configured"
	RenovateMissing    = "missing"
	RenovateActive     = "active"
	RenovateInactive   = "inactive"
)

// None is the filter value for "without": team none (undeclared) — and the
// teams source of a caller without teams.
const None = "none"

// TeamNone selects the repositories without a declaration.
const TeamNone = None

// DefaultRenovateActive is the period Renovate is judged active against when
// the server is not told otherwise: 180 days.
const DefaultRenovateActive = 180 * 24 * time.Hour

func (t *tools) registerInventory(s *mcpserver.MCPServer) {
	s.AddTool(mcp.NewTool(ToolListRepositories,
		mcp.WithDescription("Read-only. The inventory of the org's repositories from the store: one row per repository with team, lifecycle, "+
			"visibility, archived (on GitHub), fork, Renovate state, finding kinds, set-up state and record age, sorted by repository name, plus the last sweep's summary. "+
			"Scope per caller: mine (the teams you belong to on GitHub, read as you), team (the team "+
			"argument, or your teams), unassigned (on GitHub without a declaration), all. Filters as on the Repositories page: search, team (in every scope: "+
			"under mine one of your teams — another selects no rows and note says so; none under all: undeclared), renovate, visibility, fork, lifecycle, archived, "+
			"inactiveDays, finding. get_repository has the full record."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString(argScope, mcp.Enum(ScopeMine, ScopeTeam, ScopeUnassigned, ScopeAll), mcp.Description("mine | team | unassigned | all (default all).")),
		mcp.WithString(argTeam, mcp.Description("Only repositories declared by this team (slug: team-bumblebee). Under all, none: only undeclared repositories; under mine: one of your teams (another selects no rows, and note says so); under unassigned: ignored.")),
		mcp.WithBoolean(argUndeclared, mcp.Description("Only repositories on GitHub without a declaration (same as scope unassigned).")),
		mcp.WithString(argSearch, mcp.Description("Only repositories whose name or description contains this text (case-insensitive).")),
		mcp.WithString(argRenovate, mcp.Enum(RenovateConfigured, RenovateMissing, RenovateActive, RenovateInactive), mcp.Description("Renovate state: configured (a renovate.json5), missing, active (a Renovate pull request or commit within the server's Renovate activity period, default 180 days), inactive.")),
		mcp.WithString(argVisibility, mcp.Enum("public", "private"), mcp.Description("Only public or only private repositories.")),
		mcp.WithBoolean(argFork, mcp.Description("Only forks (true) or only non-forks (false).")),
		mcp.WithString(argLifecycle, mcp.Description("Only repositories with this lifecycle: active (none declared, and not archived on GitHub), deprecated (declared), archived (declared archived, or archived on GitHub); another value is matched against the declared lifecycle.")),
		mcp.WithBoolean(argArchived, mcp.Description("Only repositories that are archived — declared archived or archived on GitHub (true) — or only those that are not (false). Independent of lifecycle.")),
		mcp.WithNumber(argInactive, mcp.Description("Only repositories whose last commit by a person is older than this many days (or that have none).")),
		mcp.WithString(argFinding, mcp.Description("Only repositories with a finding of this kind (declared-but-gone, undeclared-on-github, entry-refused, gen-circleci-refused, default-icon, …).")),
		mcp.WithNumber(argLimit, mcp.Description(fmt.Sprintf("Rows to return (default %d).", defaultLimit))),
	), t.listRepositories)
	s.AddTool(mcp.NewTool(ToolGetRepository,
		mcp.WithDescription("Read-only. The full inventory record of one repository: declaration, GitHub reality, CircleCI, Renovate, catalog and mapping, "+
			"set-up state (the engine's read-mode checks and the last reconciler run), findings and age."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString(argRepository, mcp.Required(), mcp.Description("Repository name, with or without the org (giantswarm/muster or muster).")),
	), t.getRepository)
	s.AddTool(mcp.NewTool(ToolRefreshRepository,
		mcp.WithDescription("Rebuild one repository's inventory record now from GitHub, CircleCI and the team files, run the engine's checks in read mode, "+
			"and return it. Writes the inventory cache only, nothing on GitHub."),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString(argRepository, mcp.Required(), mcp.Description("Repository name, with or without the org.")),
	), t.refreshRepository)
	s.AddTool(mcp.NewTool(ToolSweepInventory,
		mcp.WithDescription("Start the full inventory sweep over the org now — every repository's record rebuilt from GitHub and the team files "+
			"the way the schedule does it — for a member of the teams that own this service (the server's sweep teams, checked on GitHub as you); "+
			"a non-member is refused. The sweep runs in the background: the answer carries started (false while one already runs), "+
			"running and the last sweep's summary; list_repositories shows the new one when it is done. Writes the inventory cache only, nothing on GitHub."),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	), t.sweepInventory)
}

// Row is one line of list_repositories.
type Row struct {
	Repository       string   `json:"repository"`
	Team             string   `json:"team,omitempty"`
	Lifecycle        string   `json:"lifecycle,omitempty"`
	Visibility       string   `json:"visibility,omitempty"`
	Archived         bool     `json:"archived"`
	Fork             bool     `json:"fork,omitempty"`
	Gone             bool     `json:"gone,omitempty"`
	Renovate         string   `json:"renovate,omitempty"`
	LastPersonCommit string   `json:"lastPersonCommit,omitempty"`
	Findings         []string `json:"findings,omitempty"`
	Setup            RowSetup `json:"setup"`
	Age              string   `json:"age"`
}

// RowSetup is the set-up state in one line.
type RowSetup struct {
	Converged *bool `json:"converged,omitempty"`
	// Refused says the engine refused the entry: the checks are its Refused
	// result, no step ran (findings entry-refused, gen-circleci-refused).
	Refused   bool   `json:"refused,omitempty"`
	CheckedAt string `json:"checkedAt,omitempty"`
	LastRun   string `json:"lastRun,omitempty"`
	// PendingRun is an Align now waiting for its run's artifact.
	PendingRun *inventory.PendingRun `json:"pendingRun,omitempty"`
	Error      string                `json:"error,omitempty"`
}

// Listing is list_repositories' result.
type Listing struct {
	Scope string `json:"scope"`
	// Teams are the caller's teams a mine/team scope was resolved to, and
	// where they came from (github, argument, none).
	Teams       []string `json:"teams,omitempty"`
	TeamsSource string   `json:"teamsSource,omitempty"`
	// Note says why the selection is what it is when the arguments alone
	// do not: a team under mine the caller is not in.
	Note         string                  `json:"note,omitempty"`
	Sweep        *inventory.SweepSummary `json:"sweep"`
	SweepRunning bool                    `json:"sweepRunning"`
	Total        int                     `json:"total"`
	Matched      int                     `json:"matched"`
	Shown        int                     `json:"shown"`
	Repositories []Row                   `json:"repositories"`
}

func (t *tools) listRepositories(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if t.d.Inventory == nil {
		return result(nil, errors.New("inventory store not configured (VALKEY_ADDR)"))
	}
	args := req.GetArguments()
	f, err := t.listFilter(ctx, args)
	if err != nil {
		return result(nil, err)
	}
	limit := int(number(args, argLimit, defaultLimit))

	records, err := t.d.Inventory.List(ctx)
	if err != nil {
		return result(nil, err)
	}
	now, period := time.Now(), t.renovatePeriod()
	out := Listing{Scope: f.scope, Teams: f.teams, TeamsSource: f.teamsSource, Note: f.note, Total: len(records), Repositories: []Row{}}
	if out.Sweep, err = t.d.Inventory.Sweep(ctx); err != nil {
		return result(nil, err)
	}
	if t.d.Collector != nil {
		out.SweepRunning = t.d.Collector.Running()
	}
	for i := range records {
		r := &records[i]
		if !f.matches(r, period, now) {
			continue
		}
		out.Matched++
		out.Repositories = append(out.Repositories, row(r, period, now))
	}
	sortRows(out.Repositories)
	if limit > 0 && len(out.Repositories) > limit {
		out.Repositories = out.Repositories[:limit]
	}
	out.Shown = len(out.Repositories)
	return result(out, nil)
}

// sortRows orders the rows by repository name, case-insensitive ascending.
func sortRows(rows []Row) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := strings.ToLower(rows[i].Repository), strings.ToLower(rows[j].Repository)
		if a != b {
			return a < b
		}
		return rows[i].Repository < rows[j].Repository
	})
}

// renovatePeriod is the period Renovate is judged active against.
func (t *tools) renovatePeriod() time.Duration {
	if t.d.RenovateActive > 0 {
		return t.d.RenovateActive
	}
	return DefaultRenovateActive
}

func row(r *inventory.Record, period time.Duration, now time.Time) Row {
	row := Row{Repository: r.Repository, Age: now.Sub(r.RefreshedAt).Round(time.Second).String(), Gone: r.Reality == nil}
	if r.Declaration != nil {
		row.Team, row.Lifecycle = r.Declaration.Team, r.Declaration.Lifecycle
	}
	if r.Reality != nil {
		row.Visibility, row.Archived, row.Fork = r.Reality.Visibility, r.Reality.IsArchived, r.Reality.IsFork
		row.Renovate = renovateState(r, period, now)
		if c := r.Reality.LastPersonCommit; c != nil {
			row.LastPersonCommit = c.Date.Format("2006-01-02")
		}
	}
	for _, f := range r.Findings {
		row.Findings = append(row.Findings, f.Kind)
	}
	if r.Setup.Checks != nil {
		c := r.Setup.Checks.Converged
		row.Setup.Converged = &c
		row.Setup.Refused = r.Setup.Checks.Step(reconcile.StepEntry) != nil
	}
	if r.Setup.CheckedAt != nil {
		row.Setup.CheckedAt = r.Setup.CheckedAt.Format(time.RFC3339)
	}
	if r.Setup.LastRun != nil {
		row.Setup.LastRun = r.Setup.LastRun.RunURL
	}
	row.Setup.PendingRun = r.Setup.PendingRun
	row.Setup.Error = r.Setup.CheckError
	return row
}

func hasFinding(r *inventory.Record, kind string) bool {
	for _, f := range r.Findings {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

func (t *tools) getRepository(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if t.d.Inventory == nil {
		return result(nil, errors.New("inventory store not configured (VALKEY_ADDR)"))
	}
	args := req.GetArguments()
	key, err := t.repositoryKey(args)
	if err != nil {
		return result(nil, err)
	}
	rec, err := t.d.Inventory.Get(ctx, key)
	if err != nil {
		return result(nil, err)
	}
	return result(rec.WithAge(time.Now()), nil)
}

// errNoCollector is refresh_repository's and sweep_inventory's answer without
// a collector.
var errNoCollector = errors.New("inventory collector not configured: it needs the store (VALKEY_ADDR) and the inventory App giantswarm-repo-manager-inventory (GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, GITHUB_APP_PRIVATE_KEY_FILE)")

func (t *tools) refreshRepository(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if t.d.Collector == nil {
		return result(nil, errNoCollector)
	}
	key, err := t.repositoryKey(req.GetArguments())
	if err != nil {
		return result(nil, err)
	}
	rec, err := t.d.Collector.Refresh(ctx, key, nil, inventory.SourceRefresh)
	if err != nil {
		return result(nil, err)
	}
	return result(rec.WithAge(time.Now()), nil)
}

// DefaultSweepTeams are the teams whose members may start a sweep when the
// server is not told otherwise: the owners of the manager and the reconciler.
const DefaultSweepTeams = "team-bumblebee,team-planeteers"

// Sweep is sweep_inventory's result.
type Sweep struct {
	// Running says a sweep is under way — the one just started, or the one
	// that was already running.
	Running bool `json:"running"`
	// Started is false when a sweep was already running: none was started.
	Started bool `json:"started"`
	// Last is the last completed sweep's summary; the running one replaces it
	// when it is done.
	Last *inventory.SweepSummary `json:"last,omitempty"`
	// Login and Teams are the caller and the teams the membership was
	// checked against.
	Login string   `json:"login"`
	Teams []string `json:"teams"`
}

func (t *tools) sweepInventory(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if t.d.Collector == nil {
		return result(nil, errNoCollector)
	}
	if len(t.d.SweepTeams) == 0 {
		return result(nil, errors.New("sweep_inventory is off: no team is configured to start a sweep (SWEEP_TEAMS)"))
	}
	p, err := t.person(ctx)
	if err != nil {
		return result(nil, err)
	}
	if !p.memberOfAny(t.d.SweepTeams) {
		return result(nil, p.notAMember(strings.Join(t.d.SweepTeams, " or "), "the sweep is not yours to start"))
	}
	started := t.d.Collector.StartSweep()
	last, err := t.d.Inventory.Sweep(ctx)
	if err != nil {
		return result(nil, err)
	}
	t.d.Log.Info("sweep requested", "started", started, "by", p.login)
	return result(&Sweep{Running: true, Started: started, Last: last, Login: p.login, Teams: t.d.SweepTeams}, nil)
}

// repositoryKey is owner/name for the repository argument.
func (t *tools) repositoryKey(args map[string]any) (string, error) {
	name, _ := args[argRepository].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%s is required", argRepository)
	}
	if !strings.Contains(name, "/") {
		name = t.org() + "/" + name
	}
	return name, nil
}

func (t *tools) org() string {
	if t.d.Collector != nil {
		return t.d.Collector.Options().Org
	}
	return "giantswarm"
}

func number(args map[string]any, key string, def float64) float64 {
	if v, ok := args[key].(float64); ok {
		return v
	}
	return def
}
