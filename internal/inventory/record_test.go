package inventory

import (
	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"strings"
	"testing"
	"time"
)

func TestScoreHonoursTheStalePeriodAndListsReasons(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, -8, 0)
	rec := &Record{Repository: "giantswarm/x", Name: "x", Reality: &Reality{
		LastCommit: &Commit{Date: now}, LastPersonCommit: &Commit{Date: old}, HistorySampled: 30,
		Has: Presence{CircleCI: true}, UnknownCodeownersTeams: []string{"team-dissolved"},
		OpenPullRequests: PullRequests{Total: 1, Bots: 1, OldestBotAt: &old, Onboarding: []PullRequest{{Number: 1}}},
	}, Renovate: Renovate{Configured: true, Enabled: true, LastCommit: &old}}

	six := Score(rec, 180*24*time.Hour, now)
	want := []string{"no team file", "older than", "Renovate is silent", "no release", "team the org does not have", "onboarding", "oldest open bot"}
	for _, w := range want {
		if !strings.Contains(strings.Join(six.Reasons, "\n"), w) {
			t.Errorf("180d: reason %q missing in %v", w, six.Reasons)
		}
	}
	if six.Score != 100 || six.StalePeriod != "4320h0m0s" {
		t.Errorf("180d: %+v", six)
	}
	year := Score(rec, 400*24*time.Hour, now)
	for _, r := range year.Reasons {
		if strings.Contains(r, "older than") || strings.Contains(r, "Renovate is silent") || strings.Contains(r, "oldest open bot") {
			t.Errorf("400d: stale reason kept: %s", r)
		}
	}
	if year.Score >= six.Score {
		t.Errorf("400d score %d not below 180d score %d", year.Score, six.Score)
	}

	rec.Reality.IsArchived = true
	if s := Score(rec, 0, now); s.Score != 0 || len(s.Reasons) != 1 {
		t.Errorf("archived: %+v", s)
	}
	if s := Score(&Record{}, 0, now); s.Score != 0 || !strings.Contains(s.Reasons[0], "gone") {
		t.Errorf("gone: %+v", s)
	}
}

func TestFindingsDeclaredGoneAndUndeclared(t *testing.T) {
	now := time.Now()
	gone := &Record{Repository: "giantswarm/gone", Declaration: &Declaration{Team: "team-bumblebee", File: "repositories/team-bumblebee.yaml", Accepted: true}}
	gone.Finalize(0, now)
	if len(gone.Findings) != 1 || gone.Findings[0].Kind != FindingDeclaredButGone || gone.Findings[0].Source != FindingSourceInventory {
		t.Errorf("gone: %+v", gone.Findings)
	}
	stray := &Record{Repository: "giantswarm/stray", Reality: &Reality{}}
	stray.Finalize(0, now)
	if len(stray.Findings) != 1 || stray.Findings[0].Kind != FindingUndeclaredOnGitHub {
		t.Errorf("stray: %+v", stray.Findings)
	}
	entry := reposetup.Entry{Name: "legacy", Problems: []reposetup.Problem{{Field: "name", Message: "must not end in -app"}}}
	refused := &Record{Repository: "giantswarm/legacy", Reality: &Reality{}, Declaration: &Declaration{Team: "t", Problems: []string{"name: must not end in -app"}},
		Setup: Setup{Checks: reconcile.Refused(reconcile.Request{Team: "t", Entry: entry}, now)}}
	refused.Finalize(0, now)
	if len(refused.Findings) != 1 || refused.Findings[0].Kind != string(reconcile.FindingEntryRefused) || refused.Findings[0].Source != FindingSourceEngine {
		t.Errorf("refused: %+v", refused.Findings)
	}
}
