package tools

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// The representative records every filter value is tested against.
const (
	fxDeclared     = "giantswarm/declared"      // team-a, no lifecycle, public, Renovate active, a person committed lately, finding default-icon
	fxDeprecated   = "giantswarm/deprecated"    // team-a, deprecated, private, Renovate inactive, last person commit long ago
	fxDeclaredArch = "giantswarm/declared-arch" // team-b, declared archived, archived on GitHub too
	fxGitHubArch   = "giantswarm/github-arch"   // team-b, no lifecycle declared, archived on GitHub
	fxStrayArch    = "giantswarm/Stray-Arch"    // undeclared, archived on GitHub
	fxStray        = "giantswarm/stray"         // undeclared, a fork, no Renovate, no person commit
	fxGone         = "giantswarm/gone"          // team-a, declared, not on GitHub

	fxTeamA = "team-a"
	fxTeamB = "team-b"
)

func filterFixtures(now time.Time) []inventory.Record {
	days := func(n int) time.Time { return now.AddDate(0, 0, -n) }
	reality := func(visibility string, archived, fork bool, lastPerson *time.Time, description string) *inventory.Reality {
		r := &inventory.Reality{Visibility: visibility, IsArchived: archived, IsFork: fork, Description: description}
		if lastPerson != nil {
			r.LastPersonCommit = &inventory.Commit{Date: *lastPerson}
		}
		return r
	}
	t5, t400, t1000 := days(5), days(400), days(1000)
	t10, t30, t200 := days(10), days(30), days(200)
	return []inventory.Record{
		{Repository: fxDeclared, Declaration: &inventory.Declaration{Team: fxTeamA}, Reality: reality("public", false, false, &t5, "the service"),
			Renovate: inventory.Renovate{Configured: true, Enabled: true, LastCommit: &t10}, Findings: []inventory.Finding{{Kind: "default-icon"}}},
		{Repository: fxDeprecated, Declaration: &inventory.Declaration{Team: fxTeamA, Lifecycle: LifecycleDeprecated}, Reality: reality("private", false, false, &t400, ""),
			Renovate: inventory.Renovate{Configured: true, Enabled: true, LastPullRequest: &inventory.PullRequest{CreatedAt: t200}}},
		{Repository: fxDeclaredArch, Declaration: &inventory.Declaration{Team: fxTeamB, Lifecycle: LifecycleArchived}, Reality: reality("private", true, false, &t1000, "")},
		{Repository: fxGitHubArch, Declaration: &inventory.Declaration{Team: fxTeamB}, Reality: reality("public", true, false, &t1000, ""),
			Renovate: inventory.Renovate{Configured: true, Enabled: true, LastPullRequest: &inventory.PullRequest{CreatedAt: t30}}},
		{Repository: fxStrayArch, Reality: reality("private", true, false, nil, ""), Findings: []inventory.Finding{{Kind: inventory.FindingUndeclaredOnGitHub}}},
		{Repository: fxStray, Reality: reality("public", false, true, nil, "A stray thing"), Findings: []inventory.Finding{{Kind: inventory.FindingUndeclaredOnGitHub}}},
		{Repository: fxGone, Declaration: &inventory.Declaration{Team: fxTeamA}, Findings: []inventory.Finding{{Kind: inventory.FindingDeclaredButGone}}},
	}
}

// TestListFilterValues: every filter value of list_repositories against the
// representative records — the contract of the Repositories page.
func TestListFilterValues(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	records := filterFixtures(now)
	all := []string{fxDeclaredArch, fxDeclared, fxDeprecated, fxGitHubArch, fxGone, fxStray, fxStrayArch}
	tests := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"no filter", nil, all},
		{"team under all", map[string]any{argTeam: fxTeamA}, []string{fxDeclared, fxDeprecated, fxGone}},
		{"team is matched case-insensitively", map[string]any{argTeam: "Team-B"}, []string{fxDeclaredArch, fxGitHubArch}},
		{"team none under all: undeclared", map[string]any{argTeam: TeamNone}, []string{fxStray, fxStrayArch}},
		{"team under scope team", map[string]any{argScope: ScopeTeam, argTeam: fxTeamA}, []string{fxDeclared, fxDeprecated, fxGone}},
		{"team under unassigned is ignored", map[string]any{argScope: ScopeUnassigned, argTeam: fxTeamA}, []string{fxStray, fxStrayArch}},
		{"undeclared", map[string]any{argUndeclared: true}, []string{fxStray, fxStrayArch}},
		{"search by name", map[string]any{argSearch: "DEPRE"}, []string{fxDeprecated}},
		{"search by description", map[string]any{argSearch: "stray thing"}, []string{fxStray}},
		{"renovate configured", map[string]any{argRenovate: RenovateConfigured}, []string{fxDeclared, fxDeprecated, fxGitHubArch}},
		{"renovate missing", map[string]any{argRenovate: RenovateMissing}, []string{fxDeclaredArch, fxGone, fxStray, fxStrayArch}},
		{"renovate active", map[string]any{argRenovate: RenovateActive}, []string{fxDeclared, fxGitHubArch}},
		{"renovate inactive", map[string]any{argRenovate: RenovateInactive}, []string{fxDeprecated}},
		{"visibility public", map[string]any{argVisibility: "public"}, []string{fxDeclared, fxGitHubArch, fxStray}},
		{"visibility private", map[string]any{argVisibility: "Private"}, []string{fxDeclaredArch, fxDeprecated, fxStrayArch}},
		{"fork true", map[string]any{argFork: true}, []string{fxStray}},
		{"fork false", map[string]any{argFork: false}, []string{fxDeclaredArch, fxDeclared, fxDeprecated, fxGitHubArch, fxStrayArch}},
		{"lifecycle active: nothing declared and not archived on GitHub", map[string]any{argLifecycle: LifecycleActive}, []string{fxDeclared, fxGone, fxStray}},
		{"lifecycle deprecated: declared", map[string]any{argLifecycle: LifecycleDeprecated}, []string{fxDeprecated}},
		{"lifecycle archived: declared or on GitHub", map[string]any{argLifecycle: LifecycleArchived}, []string{fxDeclaredArch, fxGitHubArch, fxStrayArch}},
		{"lifecycle is matched case-insensitively", map[string]any{argLifecycle: "Archived"}, []string{fxDeclaredArch, fxGitHubArch, fxStrayArch}},
		{"lifecycle none is no value any more", map[string]any{argLifecycle: None}, []string{}},
		{"archived false", map[string]any{argArchived: false}, []string{fxDeclared, fxDeprecated, fxGone, fxStray}},
		{"archived true", map[string]any{argArchived: true}, []string{fxDeclaredArch, fxGitHubArch, fxStrayArch}},
		{"archived is independent of lifecycle", map[string]any{argArchived: true, argLifecycle: LifecycleDeprecated}, []string{}},
		{"archived false with lifecycle archived", map[string]any{argArchived: false, argLifecycle: LifecycleArchived}, []string{}},
		{"inactiveDays", map[string]any{argInactive: 100.0}, []string{fxDeclaredArch, fxDeprecated, fxGitHubArch, fxGone, fxStray, fxStrayArch}},
		{"inactiveDays without the archived", map[string]any{argInactive: 100.0, argArchived: false}, []string{fxDeprecated, fxGone, fxStray}},
		{"finding declared-but-gone", map[string]any{argFinding: inventory.FindingDeclaredButGone}, []string{fxGone}},
		{"finding undeclared-on-github", map[string]any{argFinding: inventory.FindingUndeclaredOnGitHub}, []string{fxStray, fxStrayArch}},
		{"finding of the engine", map[string]any{argFinding: "default-icon"}, []string{fxDeclared}},
		{"the page's default: team under all, not archived", map[string]any{argTeam: fxTeamB, argArchived: false}, []string{}},
	}
	tl := &tools{d: Deps{}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := tl.listFilter(context.Background(), tc.args)
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for i := range records {
				if f.matches(&records[i], tl.renovatePeriod(), now) {
					got = append(got, records[i].Repository)
				}
			}
			sort.Strings(got)
			sort.Strings(tc.want)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("got  %v\nwant %v", got, tc.want)
			}
		})
	}
}

// TestListFilterRefusals: an unknown scope, and a team scope without a team
// for a caller the server cannot read teams for.
func TestListFilterRefusals(t *testing.T) {
	tl := &tools{d: Deps{}}
	if _, err := tl.listFilter(context.Background(), map[string]any{argScope: "ours"}); err == nil || !strings.Contains(err.Error(), "not known") {
		t.Errorf("unknown scope: %v", err)
	}
	for _, scope := range []string{ScopeMine, ScopeTeam} {
		if _, err := tl.listFilter(context.Background(), map[string]any{argScope: scope}); err == nil || !strings.Contains(err.Error(), "no team known for you") {
			t.Errorf("%s without teams: %v", scope, err)
		}
	}
}

// TestNarrowToOwn: team under mine is one of the caller's teams, or nothing
// with a note.
func TestNarrowToOwn(t *testing.T) {
	f := &listFilter{teams: []string{fxTeamA, fxTeamB}, teamsSource: teamsSourceGitHub}
	f.narrowToOwn("Team-B")
	if f.none || f.note != "" || strings.Join(f.teams, ",") != "Team-B" || f.teamsSource != teamsSourceArgument {
		t.Errorf("own team: %+v", f)
	}
	f = &listFilter{teams: []string{fxTeamA, fxTeamB}, teamsSource: teamsSourceGitHub}
	f.narrowToOwn("team-x")
	if !f.none || f.note != "team-x is not one of your teams (team-a, team-b): no rows" || strings.Join(f.teams, ",") != "team-a,team-b" || f.teamsSource != teamsSourceGitHub {
		t.Errorf("another team: %+v", f)
	}
	if f.matches(&inventory.Record{Repository: fxDeclared, Declaration: &inventory.Declaration{Team: "team-x"}}, DefaultRenovateActive, time.Now()) {
		t.Error("a selection of nothing matched a row")
	}
}

// TestRowsSortByNameCaseInsensitively: the rows come by repository name,
// case-insensitive ascending.
func TestRowsSortByNameCaseInsensitively(t *testing.T) {
	rows := []Row{{Repository: "giantswarm/Zeta"}, {Repository: "giantswarm/alpha"}, {Repository: "giantswarm/Beta"}, {Repository: "giantswarm/beta"}}
	sortRows(rows)
	var names []string
	for _, r := range rows {
		names = append(names, r.Repository)
	}
	if got := strings.Join(names, " "); got != "giantswarm/alpha giantswarm/Beta giantswarm/beta giantswarm/Zeta" {
		t.Errorf("order: %s", got)
	}
}

// TestRenovatePeriodDefaults: the Renovate activity period is the server's,
// 180 days when it is not told otherwise.
func TestRenovatePeriodDefaults(t *testing.T) {
	if p := (&tools{d: Deps{}}).renovatePeriod(); p != 180*24*time.Hour {
		t.Errorf("default: %s", p)
	}
	if p := (&tools{d: Deps{RenovateActive: 24 * time.Hour}}).renovatePeriod(); p != 24*time.Hour {
		t.Errorf("configured: %s", p)
	}
}
