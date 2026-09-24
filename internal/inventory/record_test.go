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
	if !strings.Contains(gone.Findings[0].Fix, "create the repository") || !strings.Contains(gone.Findings[0].Fix, "lifecycle: deleted") {
		t.Errorf("gone fix: %s", gone.Findings[0].Fix)
	}
	deleted := &Record{Repository: "giantswarm/deleted", Declaration: &Declaration{Team: "team-bumblebee", File: "repositories/team-bumblebee.yaml", Lifecycle: "deleted", Accepted: true}}
	deleted.Finalize()
	if len(deleted.Findings) != 0 {
		t.Errorf("an entry declared deleted is the record of the deletion, not a finding: %+v", deleted.Findings)
	}
	archivedGone := &Record{Repository: "giantswarm/archived-gone", Declaration: &Declaration{Team: "team-bumblebee", File: "repositories/team-bumblebee.yaml", Lifecycle: "archived", Accepted: true}}
	archivedGone.Finalize()
	if len(archivedGone.Findings) != 1 || archivedGone.Findings[0].Kind != FindingDeclaredButGone || strings.Contains(archivedGone.Findings[0].Fix, "create the repository") || !strings.Contains(archivedGone.Findings[0].Fix, "lifecycle: deleted") {
		t.Errorf("archived and gone: %+v", archivedGone.Findings)
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
		if p := r.Setup.PendingRun; p == nil || p.Kind != kind || !p.Follows(7) || p.By != "alice" || r.Setup.MissingRun != nil || !p.AwaitedFrom().IsZero() {
			t.Fatalf("%s: pending run %+v, missing run %+v", kind, p, r.Setup.MissingRun)
		}
		merged := now.Add(20 * time.Minute)
		r.Merged(merged)
		if from := r.Setup.PendingRun.AwaitedFrom(); !from.Equal(merged) {
			t.Fatalf("%s: awaited from %s, want the merge %s", kind, from, merged)
		}
		r.RunMissing(merged.Add(16*time.Minute), runs)
		f := r.MissingRunFinding()
		if f == nil || f.Kind != FindingReconcileRunMissing || r.Setup.PendingRun != nil ||
			!strings.Contains(f.Message, "whose "+noun+" pull request "+pr.URL+" alice opened at 2026-09-18T14:29:31Z was merged at 2026-09-18T14:49:31Z, had not reported by 2026-09-18T15:05:31Z") ||
			!strings.Contains(f.Fix, "follows the pull request's merge") || !strings.Contains(f.Fix, runs) {
			t.Errorf("%s: finding %+v", kind, f)
		}
	}
	r := &Record{Repository: repoX}
	r.Dispatched(now, "alice")
	if from := r.Setup.PendingRun.AwaitedFrom(); !from.Equal(now) {
		t.Errorf("dispatch: awaited from %s, want the dispatch %s", from, now)
	}
	r.RunMissing(now.Add(16*time.Minute), runs)
	if f := r.MissingRunFinding(); f == nil || !strings.Contains(f.Message, "alice dispatched at 2026-09-18T14:29:31Z") || strings.Contains(f.Message, "pull request") {
		t.Errorf("dispatch: finding %+v", f)
	}
	r = &Record{Repository: repoX}
	r.Opened(now, "alice", ChangeCreated, pr)
	r.Closed()
	if r.Setup.PendingRun != nil || r.Setup.MissingRun != nil || len(r.Findings) != 0 {
		t.Errorf("closed without a merge: %+v findings %+v", r.Setup, r.Findings)
	}
}

// TestConflictsAndMergeable: the poller notes when it first found the
// pending run's pull request conflicting with its base and keeps that time
// until the pull request is mergeable again; a record without a pending run
// takes the note without effect.
func TestConflictsAndMergeable(t *testing.T) {
	r := &Record{Repository: repoX, Name: "x"}
	first := time.Date(2026, 9, 18, 17, 20, 0, 0, time.UTC)
	r.Conflicts(first)
	if r.Setup.PendingRun.Conflicting() {
		t.Fatal("no pending run, yet conflicting")
	}
	r.Opened(first.Add(-6*time.Minute), "alice", ChangeArchived, ChangePullRequest{Number: 6167, URL: "https://github.com/giantswarm/github/pull/6167"})
	if r.Setup.PendingRun.Conflicting() {
		t.Fatal("conflicting at opening")
	}
	r.Conflicts(first)
	r.Conflicts(first.Add(5 * time.Minute))
	if p := r.Setup.PendingRun; !p.Conflicting() || !p.ConflictsSince.Equal(first) {
		t.Fatalf("conflicting since %v, want %v", p.ConflictsSince, first)
	}
	b, err := json.Marshal(r)
	if err != nil || !strings.Contains(string(b), `"conflictsSince":"2026-09-18T17:20:00Z"`) {
		t.Errorf("the note in the record's JSON: %s %v", b, err)
	}
	r.Mergeable()
	if r.Setup.PendingRun.Conflicting() || r.Setup.PendingRun == nil {
		t.Fatalf("after Mergeable: %+v", r.Setup.PendingRun)
	}
}

// TestRunFailedNamesTheRunAndItsConclusion: a pending run whose run
// completed without a report is given up at once, the finding naming the
// run and its conclusion — for an Align now and for a team-file pull request
// alike — and its fix the run and align_repository; the missing run carries
// the run beside the workflow's Actions page. A record without a pending
// run is unchanged; the next artifact or dispatch clears the finding.
func TestRunFailedNamesTheRunAndItsConclusion(t *testing.T) {
	now := time.Date(2026, 9, 21, 14, 44, 21, 0, time.UTC)
	noticed := now.Add(70 * time.Second)
	runs := "https://github.com/giantswarm/github/actions/workflows/reconcile-repositories.yaml"
	run := "https://github.com/giantswarm/github/actions/runs/35614169562"

	r := &Record{Repository: repoX}
	r.RunFailed(noticed, runs, run, "failure")
	if r.Setup.MissingRun != nil || len(r.Findings) != 0 {
		t.Fatalf("without a pending run: %+v", r.Setup)
	}

	r.Dispatched(now, "teemow")
	r.RunFailed(noticed, runs, run, "failure")
	m := r.Setup.MissingRun
	if r.Setup.PendingRun != nil || m == nil || m.RunURL != run || m.Conclusion != "failure" || m.RunsURL != runs || !m.NoticedAt.Equal(noticed) || m.By != "teemow" || m.Kind != ChangeDispatched {
		t.Fatalf("missing run after the failed dispatch run: %+v (pending %+v)", m, r.Setup.PendingRun)
	}
	f := r.MissingRunFinding()
	if f == nil || f.Kind != FindingReconcileRunMissing || f.Source != FindingSourceInventory ||
		f.Message != "the reconciler run teemow dispatched at 2026-09-21T14:44:21Z for giantswarm/x completed with conclusion failure and uploaded no report: "+run ||
		!strings.Contains(f.Fix, "look at the run "+run) || !strings.Contains(f.Fix, "dispatch again with align_repository") {
		t.Errorf("finding for the failed dispatch run: %+v", f)
	}
	if len(r.Findings) != 1 || r.Findings[0].Message != f.Message {
		t.Errorf("the record's findings: %+v", r.Findings)
	}
	b, err := json.Marshal(r)
	if err != nil || !strings.Contains(string(b), `"runUrl":"`+run+`"`) || !strings.Contains(string(b), `"conclusion":"failure"`) {
		t.Errorf("the run in the record's JSON: %s %v", b, err)
	}

	pr := ChangePullRequest{Number: 6218, URL: "https://github.com/giantswarm/github/pull/6218"}
	r = &Record{Repository: repoX}
	r.Opened(now, "alice", ChangeCreated, pr)
	r.Merged(now.Add(10 * time.Minute))
	r.RunFailed(now.Add(12*time.Minute), runs, run, "cancelled")
	f = r.MissingRunFinding()
	if f == nil || f.Message != "the reconciler run for giantswarm/x, whose declaration pull request "+pr.URL+" alice opened at 2026-09-21T14:44:21Z was merged at 2026-09-21T14:54:21Z, completed with conclusion cancelled and uploaded no report: "+run ||
		!strings.Contains(f.Fix, "look at the run "+run) || !strings.Contains(f.Fix, "align_repository") {
		t.Errorf("finding for the failed merge run: %+v", f)
	}

	// The window running out without a run to name keeps its wording.
	r = &Record{Repository: repoX}
	r.Dispatched(now, "teemow")
	r.RunMissing(now.Add(16*time.Minute), runs)
	if f = r.MissingRunFinding(); f == nil || f.Message != "the reconciler run teemow dispatched at 2026-09-21T14:44:21Z for giantswarm/x had not reported by 2026-09-21T15:00:21Z" ||
		!strings.Contains(f.Fix, "look for the run on "+runs) || r.Setup.MissingRun.RunURL != "" {
		t.Errorf("finding for the window: %+v (%+v)", f, r.Setup.MissingRun)
	}

	// The next dispatch forgets the failed run.
	r.Dispatched(now.Add(20*time.Minute), "teemow")
	if r.Setup.MissingRun != nil || r.Setup.PendingRun == nil || len(r.Findings) != 0 {
		t.Errorf("after the next dispatch: %+v findings %+v", r.Setup, r.Findings)
	}
}
