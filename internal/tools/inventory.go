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

	"github.com/giantswarm/giantswarm-repo-manager/internal/identity"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// The inventory tools.
const (
	ToolListRepositories  = "list_repositories"
	ToolGetRepository     = "get_repository"
	ToolRefreshRepository = "refresh_repository"
	ToolDecideRepository  = "decide_repository"
)

const (
	argRepository = "repository"
	argStaleDays  = "stalePeriodDays"
	argUndeclared = "undeclared"
	argMinScore   = "minOrphanScore"
	argFinding    = "finding"
	argLimit      = "limit"
	argVerdict    = "verdict"
	argNote       = "note"
	argScope      = "scope"
	argSearch     = "search"
	argRenovate   = "renovate"
	argVisibility = "visibility"
	argFork       = "fork"
	argInactive   = "inactiveDays"
	argDecision   = "decision"

	defaultLimit = 100
)

// DecisionKeep is the one verdict defined.
const DecisionKeep = "keep"

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

// None is the filter value for "without": team none (undeclared), lifecycle
// none, decision none — and the teams source of a caller without teams.
const None = "none"

// TeamNone and DecisionNone select the repositories without a declaration or
// without a decision.
const (
	TeamNone     = None
	DecisionNone = None
)

func (t *tools) registerInventory(s *mcpserver.MCPServer) {
	stale := mcp.WithNumber(argStaleDays, mcp.Description("Judge the orphan score against this stale period in days instead of the server's (the score is recomputed from the record's facts)."))
	s.AddTool(mcp.NewTool(ToolListRepositories,
		mcp.WithDescription("Read-only. The inventory of the org's repositories from the store: one row per repository with team, lifecycle, "+
			"visibility, orphan score and reasons, finding kinds, set-up state and record age, sorted by orphan score, plus the last sweep's summary. "+
			"Scope per caller: mine (the teams you belong to — your GitHub teams through your grant, else your IdP groups), team (the team "+
			"argument, or your teams), unassigned (on GitHub without a declaration), all. Filters as on the Repositories page: search, renovate, "+
			"team including none, visibility, fork, lifecycle, inactiveDays, minOrphanScore, decision, finding. get_repository has the full record."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString(argScope, mcp.Enum(ScopeMine, ScopeTeam, ScopeUnassigned, ScopeAll), mcp.Description("mine | team | unassigned | all (default all).")),
		mcp.WithString(argTeam, mcp.Description("Only repositories declared by this team (slug: team-bumblebee); none: only undeclared repositories.")),
		mcp.WithBoolean(argUndeclared, mcp.Description("Only repositories on GitHub without a declaration (same as scope unassigned).")),
		mcp.WithString(argSearch, mcp.Description("Only repositories whose name or description contains this text (case-insensitive).")),
		mcp.WithString(argRenovate, mcp.Enum(RenovateConfigured, RenovateMissing, RenovateActive, RenovateInactive), mcp.Description("Renovate state: configured (a renovate.json5), missing, active (a Renovate PR or commit within the stale period), inactive.")),
		mcp.WithString(argVisibility, mcp.Enum("public", "private"), mcp.Description("Only public or only private repositories.")),
		mcp.WithBoolean(argFork, mcp.Description("Only forks (true) or only non-forks (false).")),
		mcp.WithString(argLifecycle, mcp.Description("Only repositories declared with this lifecycle (deprecated, archived, …); none: no lifecycle set.")),
		mcp.WithNumber(argInactive, mcp.Description("Only repositories whose last commit by a person is older than this many days (or that have none).")),
		mcp.WithString(argDecision, mcp.Description("Only repositories with this decision (keep), or none.")),
		mcp.WithNumber(argMinScore, mcp.Description("Only repositories with at least this orphan score (0-100).")),
		mcp.WithString(argFinding, mcp.Description("Only repositories with a finding of this kind (declared-but-gone, undeclared-on-github, entry-refused, gen-circleci-refused, default-icon, …).")),
		mcp.WithNumber(argLimit, mcp.Description(fmt.Sprintf("Rows to return (default %d).", defaultLimit))),
		stale,
	), t.listRepositories)
	s.AddTool(mcp.NewTool(ToolGetRepository,
		mcp.WithDescription("Read-only. The full inventory record of one repository: declaration, GitHub reality, CircleCI, Renovate, catalog and mapping, "+
			"set-up state (the engine's read-mode checks and the last reconciler run), orphan score with reasons, findings, decision and age."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString(argRepository, mcp.Required(), mcp.Description("Repository name, with or without the org (giantswarm/muster or muster).")),
		stale,
	), t.getRepository)
	s.AddTool(mcp.NewTool(ToolRefreshRepository,
		mcp.WithDescription("Rebuild one repository's inventory record now from GitHub, CircleCI and the team files, run the engine's checks in read mode, "+
			"and return it. Writes the inventory cache only, nothing on GitHub."),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString(argRepository, mcp.Required(), mcp.Description("Repository name, with or without the org.")),
	), t.refreshRepository)
	s.AddTool(mcp.NewTool(ToolDecideRepository,
		mcp.WithDescription("Leave a decision note on a repository's inventory record as you (verdict keep, with text); it survives every refresh. "+
			"An annotation of the inventory cache, not a change on GitHub — no dryRun or mode."),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString(argRepository, mcp.Required(), mcp.Description("Repository name, with or without the org.")),
		mcp.WithString(argVerdict, mcp.Required(), mcp.Enum(DecisionKeep), mcp.Description("The verdict: keep.")),
		mcp.WithString(argNote, mcp.Description("Why.")),
	), t.decideRepository)
}

// Row is one line of list_repositories.
type Row struct {
	Repository       string           `json:"repository"`
	Team             string           `json:"team,omitempty"`
	Lifecycle        string           `json:"lifecycle,omitempty"`
	Visibility       string           `json:"visibility,omitempty"`
	Archived         bool             `json:"archived"`
	Fork             bool             `json:"fork,omitempty"`
	Gone             bool             `json:"gone,omitempty"`
	Renovate         string           `json:"renovate,omitempty"`
	LastPersonCommit string           `json:"lastPersonCommit,omitempty"`
	Orphan           inventory.Orphan `json:"orphan"`
	Findings         []string         `json:"findings,omitempty"`
	Setup            RowSetup         `json:"setup"`
	Decision         string           `json:"decision,omitempty"`
	Age              string           `json:"age"`
}

// RowSetup is the set-up state in one line.
type RowSetup struct {
	Converged *bool `json:"converged,omitempty"`
	// Refused says the engine refused the entry: the checks are its Refused
	// result, no step ran (findings entry-refused, gen-circleci-refused).
	Refused   bool   `json:"refused,omitempty"`
	CheckedAt string `json:"checkedAt,omitempty"`
	LastRun   string `json:"lastRun,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Listing is list_repositories' result.
type Listing struct {
	Scope string `json:"scope"`
	// Teams are the caller's teams a mine/team scope was resolved to, and
	// where they came from (github, idp-groups, argument, none).
	Teams        []string                `json:"teams,omitempty"`
	TeamsSource  string                  `json:"teamsSource,omitempty"`
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
	stale, ok := t.stale(args)

	records, err := t.d.Inventory.List(ctx)
	if err != nil {
		return result(nil, err)
	}
	now := time.Now()
	out := Listing{Scope: f.scope, Teams: f.teams, TeamsSource: f.teamsSource, Total: len(records), Repositories: []Row{}}
	if out.Sweep, err = t.d.Inventory.Sweep(ctx); err != nil {
		return result(nil, err)
	}
	if t.d.Collector != nil {
		out.SweepRunning = t.d.Collector.Running()
	}
	for i := range records {
		r := &records[i]
		if ok {
			r.Orphan = inventory.Score(r, stale, now)
		}
		if !f.matches(r, stale, now) {
			continue
		}
		out.Matched++
		out.Repositories = append(out.Repositories, row(r, now))
	}
	sort.SliceStable(out.Repositories, func(i, j int) bool {
		if out.Repositories[i].Orphan.Score != out.Repositories[j].Orphan.Score {
			return out.Repositories[i].Orphan.Score > out.Repositories[j].Orphan.Score
		}
		return out.Repositories[i].Repository < out.Repositories[j].Repository
	})
	if limit > 0 && len(out.Repositories) > limit {
		out.Repositories = out.Repositories[:limit]
	}
	out.Shown = len(out.Repositories)
	return result(out, nil)
}

func row(r *inventory.Record, now time.Time) Row {
	row := Row{Repository: r.Repository, Orphan: r.Orphan, Age: now.Sub(r.RefreshedAt).Round(time.Second).String(), Gone: r.Reality == nil}
	if r.Declaration != nil {
		row.Team, row.Lifecycle = r.Declaration.Team, r.Declaration.Lifecycle
	}
	if r.Reality != nil {
		row.Visibility, row.Archived, row.Fork = r.Reality.Visibility, r.Reality.IsArchived, r.Reality.IsFork
		row.Renovate = renovateState(r, r.Orphan.StalePeriod, now)
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
	row.Setup.Error = r.Setup.CheckError
	if r.Decision != nil {
		row.Decision = r.Decision.Verdict
	}
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
	now := time.Now()
	if stale, ok := t.stale(args); ok {
		rec.Orphan = inventory.Score(rec, stale, now)
	}
	return result(rec.WithAge(now), nil)
}

func (t *tools) refreshRepository(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if t.d.Collector == nil {
		return result(nil, errors.New("inventory collector not configured: it needs the store (VALKEY_ADDR) and a GitHub read identity (the App, or GITHUB_TOKEN in development)"))
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

func (t *tools) decideRepository(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if t.d.Inventory == nil {
		return result(nil, errors.New("inventory store not configured (VALKEY_ADDR)"))
	}
	args := req.GetArguments()
	key, err := t.repositoryKey(args)
	if err != nil {
		return result(nil, err)
	}
	verdict, _ := args[argVerdict].(string)
	if verdict != DecisionKeep {
		return result(nil, fmt.Errorf("verdict %q is not defined: %q is the one verdict", verdict, DecisionKeep))
	}
	id, ok := identity.FromContext(ctx)
	if !ok {
		return result(nil, errors.New("a decision needs a caller: the request carried no identity"))
	}
	rec, err := t.d.Inventory.Get(ctx, key)
	if err != nil {
		return result(nil, err)
	}
	note, _ := args[argNote].(string)
	rec.Decision = &inventory.Decision{Verdict: verdict, Note: strings.TrimSpace(note), By: id.String(), At: time.Now().UTC()}
	if err := t.d.Inventory.Put(ctx, rec); err != nil {
		return result(nil, err)
	}
	return result(rec.WithAge(time.Now()), nil)
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

// stale is the stale period a call asked for, if any.
func (t *tools) stale(args map[string]any) (time.Duration, bool) {
	days := number(args, argStaleDays, 0)
	if days <= 0 {
		return 0, false
	}
	return time.Duration(days*24) * time.Hour, true
}

func number(args map[string]any, key string, def float64) float64 {
	if v, ok := args[key].(float64); ok {
		return v
	}
	return def
}
