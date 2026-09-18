package inventory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
)

// TestARecordOfTheEarlierShapeUnmarshals: a record stored before the orphan
// score and the decision note were removed still loads — the unknown fields
// are ignored — and is written back without them.
// repoX is the repository the tests here record.
const repoX = "giantswarm/x"

func TestARecordOfTheEarlierShapeUnmarshals(t *testing.T) {
	old := `{"repository":"giantswarm/x","name":"x","declaration":null,
	  "reality":{"url":"https://github.com/giantswarm/x","visibility":"public","isArchived":true,"isFork":false,"isTemplate":false,"isEmpty":false,
	    "createdAt":"2020-01-01T00:00:00Z","historySampled":30,"botCommits":0,"openPullRequests":{"total":0,"people":0,"bots":0,"renovate":0},"openIssues":0,
	    "has":{"renovate":true,"dependabot":false,"circleci":true,"workflows":false,"dockerfile":false,"helm":false,"readme":true,"codeowners":false}},
	  "renovate":{"configured":true,"enabled":true,"preset":true},"catalog":{"present":false},"mapping":{"present":false},"setup":{},
	  "orphan":{"score":40,"reasons":["no team file declares the repository"],"stalePeriod":"4320h0m0s"},
	  "findings":[{"kind":"undeclared-on-github","message":"giantswarm/x exists on GitHub and no team file declares it","source":"inventory"}],
	  "decision":{"verdict":"keep","note":"ours","by":"alice","at":"2026-09-01T00:00:00Z"},
	  "refreshedAt":"2026-09-16T12:00:00Z","source":"sweep"}`
	var r Record
	if err := json.Unmarshal([]byte(old), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Repository != repoX || r.Reality == nil || !r.Reality.IsArchived || !r.Renovate.Configured || len(r.Findings) != 1 || r.Findings[0].Kind != FindingUndeclaredOnGitHub || r.Source != SourceSweep {
		t.Errorf("record: %+v", r)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"orphan"`, `"decision"`, `"score"`, `"stalePeriod"`, `"verdict"`} {
		if strings.Contains(string(b), field) {
			t.Errorf("%s written back: %s", field, b)
		}
	}
}

func TestFindingsDeclaredGoneAndUndeclared(t *testing.T) {
	now := time.Now()
	gone := &Record{Repository: "giantswarm/gone", Declaration: &Declaration{Team: "team-bumblebee", File: "repositories/team-bumblebee.yaml", Accepted: true}}
	gone.Finalize()
	if len(gone.Findings) != 1 || gone.Findings[0].Kind != FindingDeclaredButGone || gone.Findings[0].Source != FindingSourceInventory {
		t.Errorf("gone: %+v", gone.Findings)
	}
	stray := &Record{Repository: "giantswarm/stray", Reality: &Reality{}}
	stray.Finalize()
	if len(stray.Findings) != 1 || stray.Findings[0].Kind != FindingUndeclaredOnGitHub {
		t.Errorf("stray: %+v", stray.Findings)
	}
	entry := reposetup.Entry{Name: "legacy", Problems: []reposetup.Problem{{Field: "name", Message: "must not end in -app"}}}
	refused := &Record{Repository: "giantswarm/legacy", Reality: &Reality{}, Declaration: &Declaration{Team: "t", Problems: []string{"name: must not end in -app"}},
		Setup: Setup{Checks: reconcile.Refused(reconcile.Request{Team: "t", Entry: entry}, now)}}
	refused.Finalize()
	if len(refused.Findings) != 1 || refused.Findings[0].Kind != string(reconcile.FindingEntryRefused) || refused.Findings[0].Source != FindingSourceEngine {
		t.Errorf("refused: %+v", refused.Findings)
	}
}

// TestChangePersonMade: the kinds the reconciler derives from a merged pull
// request are a person's change; an Align now, the schedule and a missing
// block are the reconciler's own runs.
func TestChangePersonMade(t *testing.T) {
	for _, kind := range []string{ChangeCreated, ChangeAdded, ChangeTransferred, ChangeArchived, ChangeDeprecated, ChangeChanged} {
		if !(&Change{Kind: kind}).PersonMade() {
			t.Errorf("%s should be a person's change", kind)
		}
	}
	for _, kind := range []string{ChangeDispatched, ChangeNightly, ""} {
		if (&Change{Kind: kind}).PersonMade() {
			t.Errorf("%q should be the reconciler's own run", kind)
		}
	}
	var none *Change
	if none.PersonMade() {
		t.Error("an artifact without a change block should be the reconciler's own run")
	}
}

// TestOpenedExpectsTheRunAndTheMissingFindingNamesTheKind: a team-file pull
// request of any kind leaves the pending run following it and forgets an
// earlier missing run; given up, the finding names the pull request by its
// kind, where an Align now's names the dispatch.
func TestOpenedExpectsTheRunAndTheMissingFindingNamesTheKind(t *testing.T) {
	now := time.Date(2026, 9, 18, 14, 29, 31, 0, time.UTC)
	runs := "https://github.com/giantswarm/github/actions/workflows/reconcile.yaml"
	pr := ChangePullRequest{Number: 7, URL: "https://github.com/giantswarm/github/pull/7"}
	for kind, noun := range map[string]string{ChangeCreated: "declaration", ChangeArchived: "archive", ChangeDeprecated: "deprecation", ChangeTransferred: "transfer", ChangeChanged: "change"} {
		r := &Record{Repository: repoX, Setup: Setup{MissingRun: &MissingRun{}}}
		r.Opened(now, "alice", kind, pr)
		if p := r.Setup.PendingRun; p == nil || p.Kind != kind || !p.Follows(7) || p.By != "alice" || r.Setup.MissingRun != nil {
			t.Fatalf("%s: pending run %+v, missing run %+v", kind, p, r.Setup.MissingRun)
		}
		r.RunMissing(now.Add(16*time.Minute), runs)
		f := r.MissingRunFinding()
		if f == nil || f.Kind != FindingReconcileRunMissing || r.Setup.PendingRun != nil ||
			!strings.Contains(f.Message, "whose "+noun+" pull request "+pr.URL+" alice opened at 2026-09-18T14:29:31Z") || !strings.Contains(f.Fix, "merge it") || !strings.Contains(f.Fix, runs) {
			t.Errorf("%s: finding %+v", kind, f)
		}
	}
	r := &Record{Repository: repoX}
	r.Dispatched(now, "alice")
	r.RunMissing(now.Add(16*time.Minute), runs)
	if f := r.MissingRunFinding(); f == nil || !strings.Contains(f.Message, "alice dispatched at 2026-09-18T14:29:31Z") || strings.Contains(f.Message, "pull request") {
		t.Errorf("dispatch: finding %+v", f)
	}
}
