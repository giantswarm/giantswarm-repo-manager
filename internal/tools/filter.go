package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

// Lifecycle filter values: the team-file schema's (deprecated, archived) and
// active, the state of a repository without a declared lifecycle.
const (
	LifecycleActive     = "active"
	LifecycleDeprecated = teamfiles.LifecycleDeprecated
	LifecycleArchived   = teamfiles.LifecycleArchived
)

// Where a listing's teams came from.
const (
	teamsSourceGitHub   = "github"
	teamsSourceArgument = "argument"
)

// listFilter is one list_repositories call's selection: the scope resolved
// for the caller and the page's filters.
type listFilter struct {
	scope       string
	teams       []string // the teams a mine/team scope selects
	teamsSource string
	// none selects nothing — a team under mine the caller is not in; note
	// says why.
	none       bool
	note       string
	undeclared bool
	teamNone   bool
	search     string
	renovate   string
	visibility string
	fork       *bool
	lifecycle  string
	archived   *bool
	inactive   time.Duration
	finding    string
	orb        string
	arm64      *bool
	chinaPush  string
	signing    string
}

func (t *tools) listFilter(ctx context.Context, args map[string]any) (*listFilter, error) {
	f := &listFilter{}
	f.scope, _ = args[argScope].(string)
	if f.scope == "" {
		f.scope = ScopeAll
	}
	team := stringArg(args, argTeam)
	switch f.scope {
	case ScopeAll:
		if team == TeamNone {
			f.teamNone = true
		} else if team != "" {
			f.teams, f.teamsSource = []string{team}, teamsSourceArgument
		}
	case ScopeUnassigned:
		f.undeclared = true
	case ScopeTeam:
		if team != "" && team != TeamNone {
			f.teams, f.teamsSource = []string{team}, teamsSourceArgument
		} else {
			f.teams, f.teamsSource = t.callerTeams(ctx)
		}
	case ScopeMine:
		f.teams, f.teamsSource = t.callerTeams(ctx)
		if team != "" && len(f.teams) > 0 {
			f.narrowToOwn(team)
		}
	default:
		return nil, fmt.Errorf("%s %q is not known: %s, %s, %s or %s", argScope, f.scope, ScopeMine, ScopeTeam, ScopeUnassigned, ScopeAll)
	}
	if (f.scope == ScopeMine || f.scope == ScopeTeam) && len(f.teams) == 0 {
		return nil, fmt.Errorf("scope %s: no team known for you (%s) — pass team, or check that your teams in the org are readable as you (the App giantswarm-repo-manager's Organization members: read)", f.scope, f.teamsSource)
	}
	if u, _ := args[argUndeclared].(bool); u {
		f.undeclared = true
	}
	f.search = strings.ToLower(stringArg(args, argSearch))
	f.renovate = stringArg(args, argRenovate)
	f.visibility = strings.ToLower(stringArg(args, argVisibility))
	f.orb = stringArg(args, argOrb)
	if v, ok := args[argARM64].(bool); ok {
		f.arm64 = &v
	}
	f.chinaPush = stringArg(args, argChinaPush)
	f.signing = stringArg(args, argSigning)
	if v, ok := args[argFork].(bool); ok {
		f.fork = &v
	}
	f.lifecycle = stringArg(args, argLifecycle)
	if v, ok := args[argArchived].(bool); ok {
		f.archived = &v
	}
	if d := number(args, argInactive, 0); d > 0 {
		f.inactive = time.Duration(d*24) * time.Hour
	}
	f.finding = stringArg(args, argFinding)
	return f, nil
}

// narrowToOwn narrows a mine scope to one of the caller's teams. A team the
// caller is not in selects nothing, and the note says so — no error: the
// page sends the team it shows.
func (f *listFilter) narrowToOwn(team string) {
	if containsFold(f.teams, team) {
		f.teams, f.teamsSource = []string{team}, teamsSourceArgument
		return
	}
	f.none = true
	f.note = fmt.Sprintf("%s is not one of your teams (%s): no rows", team, strings.Join(f.teams, ", "))
}

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

// matches applies the selection to one record; period is the Renovate
// activity period.
func (f *listFilter) matches(r *inventory.Record, period time.Duration, now time.Time) bool {
	if f.none {
		return false
	}
	if f.undeclared && (r.Declaration != nil || r.Reality == nil) {
		return false
	}
	if f.teamNone && r.Declaration != nil {
		return false
	}
	if len(f.teams) > 0 && (r.Declaration == nil || !containsFold(f.teams, r.Declaration.Team)) {
		return false
	}
	if f.search != "" && !strings.Contains(strings.ToLower(r.Repository), f.search) &&
		(r.Reality == nil || !strings.Contains(strings.ToLower(r.Reality.Description), f.search)) {
		return false
	}
	if f.renovate != "" && renovateState(r, period, now) != f.renovate && (f.renovate != RenovateConfigured || !r.Renovate.Configured) {
		return false
	}
	if f.visibility != "" && (r.Reality == nil || strings.ToLower(r.Reality.Visibility) != f.visibility) {
		return false
	}
	if f.fork != nil && (r.Reality == nil || r.Reality.IsFork != *f.fork) {
		return false
	}
	if f.lifecycle != "" && !matchesLifecycle(r, f.lifecycle) {
		return false
	}
	if f.archived != nil && archived(r) != *f.archived {
		return false
	}
	if f.inactive > 0 && r.Reality != nil && r.Reality.LastPersonCommit != nil && now.Sub(r.Reality.LastPersonCommit.Date) < f.inactive {
		return false
	}
	if f.finding != "" && !hasFinding(r, f.finding) {
		return false
	}
	if f.orb != "" && (r.CI == nil || !orbMatches(r.CI.Orb, f.orb)) {
		return false
	}
	if f.arm64 != nil && (r.CI == nil || r.CI.ARM64 == nil || *r.CI.ARM64 != *f.arm64) {
		return false
	}
	if f.chinaPush != "" && (r.CI == nil || r.CI.ChinaPush != f.chinaPush) {
		return false
	}
	if f.signing != "" && (r.CI == nil || r.CI.Signing != f.signing) {
		return false
	}
	return true
}

// orbMatches says whether the pinned orb version is want, or starts with it
// at a version boundary: 10 matches 10.5.0 and not 100.0.0.
func orbMatches(have, want string) bool {
	return have == want || strings.HasPrefix(have, want+".")
}

// matchesLifecycle: archived is declared archived or archived on GitHub;
// active is no lifecycle declared (or active declared) and not archived on
// GitHub; any other value is the declared lifecycle.
func matchesLifecycle(r *inventory.Record, lifecycle string) bool {
	declared := declaredLifecycle(r)
	switch {
	case strings.EqualFold(lifecycle, LifecycleArchived):
		return archived(r)
	case strings.EqualFold(lifecycle, LifecycleActive):
		return (declared == "" || strings.EqualFold(declared, LifecycleActive)) && !archived(r)
	default:
		return strings.EqualFold(declared, lifecycle)
	}
}

func declaredLifecycle(r *inventory.Record) string {
	if r.Declaration == nil {
		return ""
	}
	return r.Declaration.Lifecycle
}

// archived says the repository is archived: declared so, or so on GitHub.
func archived(r *inventory.Record) bool {
	return strings.EqualFold(declaredLifecycle(r), LifecycleArchived) || (r.Reality != nil && r.Reality.IsArchived)
}

// renovateState is the row's Renovate state: missing without a config,
// active when Renovate opened a pull request or committed within the period,
// else inactive (configured but quiet).
func renovateState(r *inventory.Record, period time.Duration, now time.Time) string {
	if !r.Renovate.Configured {
		return RenovateMissing
	}
	if c := r.Renovate.LastCommit; c != nil && now.Sub(*c) < period {
		return RenovateActive
	}
	if pr := r.Renovate.LastPullRequest; pr != nil && now.Sub(pr.CreatedAt) < period {
		return RenovateActive
	}
	return RenovateInactive
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
