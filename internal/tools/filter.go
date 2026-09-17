package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// listFilter is one list_repositories call's selection: the scope resolved
// for the caller and the page's filters.
type listFilter struct {
	scope       string
	teams       []string // the teams a mine/team scope selects
	teamsSource string
	undeclared  bool
	teamNone    bool
	search      string
	renovate    string
	visibility  string
	fork        *bool
	lifecycle   string
	inactive    time.Duration
	minScore    float64
	decision    string
	finding     string
}

func (t *tools) listFilter(ctx context.Context, args map[string]any) (*listFilter, error) {
	f := &listFilter{minScore: number(args, argMinScore, 0)}
	f.scope, _ = args[argScope].(string)
	if f.scope == "" {
		f.scope = ScopeAll
	}
	team, _ := args[argTeam].(string)
	team = strings.TrimSpace(team)
	switch f.scope {
	case ScopeAll:
		if team == TeamNone {
			f.teamNone = true
		} else if team != "" {
			f.teams, f.teamsSource = []string{team}, "argument"
		}
	case ScopeUnassigned:
		f.undeclared = true
	case ScopeTeam:
		if team != "" && team != TeamNone {
			f.teams, f.teamsSource = []string{team}, "argument"
		} else {
			f.teams, f.teamsSource = t.callerTeams(ctx)
		}
	case ScopeMine:
		f.teams, f.teamsSource = t.callerTeams(ctx)
	default:
		return nil, fmt.Errorf("%s %q is not known: %s, %s, %s or %s", argScope, f.scope, ScopeMine, ScopeTeam, ScopeUnassigned, ScopeAll)
	}
	if (f.scope == ScopeMine || f.scope == ScopeTeam) && len(f.teams) == 0 {
		return nil, fmt.Errorf("scope %s: no team known for you (%s) — pass team, or check that your teams in the org are readable as you (the App giantswarm-repo-manager's Organization members: read)", f.scope, f.teamsSource)
	}
	if u, _ := args[argUndeclared].(bool); u {
		f.undeclared = true
	}
	f.search = strings.ToLower(strings.TrimSpace(stringArg(args, argSearch)))
	f.renovate = stringArg(args, argRenovate)
	f.visibility = strings.ToLower(stringArg(args, argVisibility))
	if v, ok := args[argFork].(bool); ok {
		f.fork = &v
	}
	f.lifecycle = stringArg(args, argLifecycle)
	if d := number(args, argInactive, 0); d > 0 {
		f.inactive = time.Duration(d*24) * time.Hour
	}
	f.decision = stringArg(args, argDecision)
	f.finding = stringArg(args, argFinding)
	return f, nil
}

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

// matches applies the selection to one record; stale is the caller's stale
// period when given (else the record's).
func (f *listFilter) matches(r *inventory.Record, stale time.Duration, now time.Time) bool {
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
	if f.renovate != "" && renovateState(r, stalePeriod(r, stale), now) != f.renovate && (f.renovate != RenovateConfigured || !r.Renovate.Configured) {
		return false
	}
	if f.visibility != "" && (r.Reality == nil || strings.ToLower(r.Reality.Visibility) != f.visibility) {
		return false
	}
	if f.fork != nil && (r.Reality == nil || r.Reality.IsFork != *f.fork) {
		return false
	}
	if f.lifecycle != "" {
		lc := ""
		if r.Declaration != nil {
			lc = r.Declaration.Lifecycle
		}
		if (f.lifecycle == None && lc != "") || (f.lifecycle != None && lc != f.lifecycle) {
			return false
		}
	}
	if f.inactive > 0 && r.Reality != nil && r.Reality.LastPersonCommit != nil && now.Sub(r.Reality.LastPersonCommit.Date) < f.inactive {
		return false
	}
	if float64(r.Orphan.Score) < f.minScore {
		return false
	}
	if f.decision != "" {
		has := r.Decision != nil && r.Decision.Verdict == f.decision
		if (f.decision == DecisionNone && r.Decision != nil) || (f.decision != DecisionNone && !has) {
			return false
		}
	}
	if f.finding != "" && !hasFinding(r, f.finding) {
		return false
	}
	return true
}

// renovateState is the row's Renovate state: missing without a config,
// active when Renovate opened a PR or committed within the stale period,
// else inactive (configured but quiet).
func renovateState(r *inventory.Record, stale string, now time.Time) string {
	if !r.Renovate.Configured {
		return RenovateMissing
	}
	period, err := time.ParseDuration(stale)
	if err != nil || period <= 0 {
		period = 180 * 24 * time.Hour
	}
	if r.Renovate.LastCommit != nil && now.Sub(*r.Renovate.LastCommit) < period {
		return RenovateActive
	}
	if pr := r.Renovate.LastPullRequest; pr != nil && now.Sub(pr.CreatedAt) < period {
		return RenovateActive
	}
	return RenovateInactive
}

func stalePeriod(r *inventory.Record, stale time.Duration) string {
	if stale > 0 {
		return stale.String()
	}
	return r.Orphan.StalePeriod
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
