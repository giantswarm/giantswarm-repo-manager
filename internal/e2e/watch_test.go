package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/mark3labs/mcp-go/client"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/tools"
)

// watch_repository against the fakes: a creation followed phase by phase as
// GitHub and the inventory show them — to ready, to a failed step, to a run
// that never reported, to a red release, through a read GitHub refuses,
// through the jobs the declaration implies and the settle window, through a
// merge arriving mid-call that ends the call with the phase — and the
// timeout that answers with what is pending.

const (
	argTimeout = "timeout"
	firstTag   = "v0.1.0"
	shinyURL   = "https://github.com/" + org + "/" + shinyService
	// throughMerged are the phases done once the pull request is merged,
	// throughSetUp once the reconciler run has reported.
	throughMerged = "created scaffolded declared merged"
	throughSetUp  = throughMerged + " setUp"
	allPhases     = throughSetUp + " released"
	// watchSettle is the stack's settle window: how long the release's
	// statuses stay unchanged before released is done (60 s in service).
	// GitHub dates a status to the second, so the window is over a second.
	watchSettle = 1500 * time.Millisecond
	// settling is a call short enough to end inside the settle window.
	settling = 0.05
	// jobImagePush is the image push job as the watch names it awaited.
	jobImagePush = "push-to-registries"
)

// greenRelease are the statuses of a Go + app release once every job has
// reported green.
var greenRelease = []string{circleSetup, circleBuild, circleChart, circlePushChart, circlePush}

// summaryFollowed is the circleci step's summary of a repaired follow.
const summaryFollowed = "followed"

// createShiny creates shiny-service as alice from newEntry and merges nothing.
func (st *stack) createShiny(t *testing.T, c *client.Client) (tools.Created, string) {
	t.Helper()
	return st.create(t, c, newEntry)
}

// create creates the repository entry declares as alice and merges nothing.
func (st *stack) create(t *testing.T, c *client.Client, entry map[string]any) (tools.Created, string) {
	t.Helper()
	var out tools.Created
	st.callJSON(t, c, tools.ToolCreateRepository, map[string]any{argMode: modeCommit, argTeam: team, argEntry: entry}, &out)
	if out.PullRequest == nil {
		t.Fatalf("create_repository: %+v", out)
	}
	return out, fmt.Sprintf("https://github.com/%s/github/pull/%d", org, out.PullRequest.Number)
}

// declared merges the pull request and lands its entry at main, as GitHub
// shows a merge, minutes before the reconciler run reports: the inventory
// reads the team files afresh for that run.
func (st *stack) declared(pr int, entry string) {
	st.ghs.files.merge(pr)
	st.ghs.org.declare(entry)
	st.advance(6 * time.Minute)
}

// watch calls watch_repository for shiny-service and its pull request.
func (st *stack) watch(t *testing.T, c *client.Client, pr int, timeout float64) tools.Watch {
	t.Helper()
	var w tools.Watch
	st.callJSON(t, c, tools.ToolWatchRepository, map[string]any{kRepository: shinyService, argPullRequest: pr, argTimeout: timeout}, &w)
	return w
}

// reported adds a completed reconciler run over shiny-service following the
// creation's pull request and lets the poller read it.
func (st *stack) reported(t *testing.T, pr int, prURL string, steps ...reconcile.StepResult) {
	t.Helper()
	finished := time.Now().UTC().Truncate(time.Second)
	res := reconcile.Result{Steps: steps}
	res.Converged = len(res.Failed()) == 0
	st.ghs.actions.addRun(t, runStatusCompleted, finished, artifactReport{name: shinyService, finishedAt: finished, result: res,
		change: &inventory.Change{Kind: inventory.ChangeCreated, By: alice, PullRequest: &inventory.ChangePullRequest{Number: pr, URL: prURL}}})
	if p := st.poll(t); p.Artifacts != 1 || p.Pending != 0 {
		t.Fatalf("poll with the creation's run: %+v", p)
	}
}

// names joins the phases' names.
func names(ps []tools.Phase) string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return strings.Join(out, " ")
}

// TestWatchRepositoryFollowsACreationToReadiness: the creation leaves the
// record expecting the run of its pull request — without a deadline while
// the pull request is open, so the poller does not hurry; before the merge
// the watch answers with merged pending and the three phases done; a merge arriving mid-call ends the call within
// a poll interval with the merged phase and changed, setUp pending; after
// the run, with the release there but CircleCI silent, released stays
// pending, as it does while CircleCI's statuses are pending — a call that
// runs out without a new phase is not changed; its green statuses during the
// wait make it ready with every phase, changed, and a call that starts ready
// is ready at once, unchanged. The statuses are read as the inventory App:
// the fake refuses them to a person's token.
func TestWatchRepositoryFollowsACreationToReadiness(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	created, prURL := st.createShiny(t, c)
	pr := created.PullRequest.Number
	if p := created.Repositories[0].PendingRun; p == nil || p.Kind != inventory.ChangeCreated || !p.Follows(pr) || p.By != alice || !strings.Contains(created.Then, tools.ToolWatchRepository) {
		t.Fatalf("the creation's pending run: %+v then=%q", p, created.Then)
	}
	if rec := st.record(t, shinyService); !rec.Setup.PendingRun.Follows(pr) || rec.Reality == nil {
		t.Fatalf("record after the creation: %+v", rec.Setup)
	}
	if p := st.poll(t); p.Pending != 0 || p.Missing != 0 {
		t.Errorf("poll while the pull request is open: %+v", p)
	}
	if text, isErr := call(t, c, tools.ToolWatchRepository, map[string]any{kRepository: shinyService, argPullRequest: 99999}); !isErr || !strings.Contains(text, "#99999 does not exist") {
		t.Errorf("an unknown pull request: %v %s", isErr, text)
	}

	w := st.watch(t, c, pr, 0.3)
	if w.Ready || w.Changed || w.Pending != tools.PhaseMerged || w.Failure != nil || names(w.Phases) != "created scaffolded declared" || w.Repository != shinyURL || w.PullRequest != prURL {
		t.Fatalf("before the merge: %+v", w)
	}
	for i, ph := range w.Phases {
		if ph.At.IsZero() || (i > 0 && ph.At.Before(w.Phases[i-1].At)) || ph.Seconds < 0 {
			t.Errorf("phase %d: %+v", i, ph)
		}
	}

	go func() {
		time.Sleep(150 * time.Millisecond)
		st.ghs.files.merge(pr)
	}()
	w = st.watch(t, c, pr, 5)
	if !w.Changed || w.Ready || w.Pending != tools.PhaseSetUp || w.PendingReason != "" || w.Failure != nil || names(w.Phases) != throughMerged || w.Waited > 1 {
		t.Fatalf("the merge mid-call: %+v", w)
	}
	st.reported(t, pr, prURL,
		reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictRepaired, Summary: summaryFollowed, Changes: []string{"follow the project"}},
		reconcile.StepResult{Step: reconcile.StepMetadata, Verdict: reconcile.VerdictReported, Findings: []reconcile.Finding{{Kind: reconcile.FindingDefaultIcon, Advisory: true, Message: "the chart carries the template's icon", Fix: "replace the icon"}}},
		reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictRepaired, Summary: "trigger the missed tag build for " + firstTag})
	if rec := st.record(t, shinyService); rec.Setup.PendingRun != nil || rec.Setup.LastRun == nil {
		t.Fatalf("record after the run: %+v", rec.Setup)
	}
	st.ghs.repos.publish(shinyService, firstTag)
	w = st.watch(t, c, pr, 0.3)
	if w.Ready || w.Pending != tools.PhaseReleased || w.PendingReason != "" || w.Failure != nil || names(w.Phases) != throughSetUp ||
		w.Release == nil || w.Release.Tag != firstTag || w.Release.URL != shinyURL+"/releases/tag/"+firstTag || len(w.Findings) != 1 || w.Findings[0].Kind != reconcile.FindingDefaultIcon {
		t.Fatalf("before CircleCI reported: %+v release=%+v", w, w.Release)
	}
	st.ghs.repos.report(shinyService, statePending, circleBuild)
	if w = st.watch(t, c, pr, 0.3); w.Ready || w.Changed || w.Pending != tools.PhaseReleased || w.Failure != nil || w.Waited != 0 ||
		w.PendingReason != "the CircleCI statuses on "+firstTag+": "+circleBuild+" (pending) — waiting for the pending ones" {
		t.Fatalf("CircleCI pending: %+v", w)
	}

	go func() {
		time.Sleep(150 * time.Millisecond)
		st.ghs.repos.report(shinyService, stateSuccess, greenRelease...)
	}()
	w = st.watch(t, c, pr, 5)
	if !w.Ready || !w.Changed || w.Pending != "" || w.PendingReason != "" || w.Failure != nil || names(w.Phases) != allPhases || w.Waited > 3 {
		t.Fatalf("ready: %+v", w)
	}
	if w = st.watch(t, c, pr, 5); !w.Ready || w.Changed || w.Waited != 0 {
		t.Fatalf("a call that starts ready: %+v", w)
	}
}

// TestWatchRepositoryWaitsForTheDeclaredJobsAndTheSettleWindow: CircleCI
// posts one status per job as the job starts, so green setup and go-build
// statuses on a Go + app repository with a Dockerfile are not the verdict:
// released stays pending naming the chart job and the image push as awaited,
// then — every job green — until the set has stayed unchanged for the settle
// window; a red build-chart fails the phase at once.
func TestWatchRepositoryWaitsForTheDeclaredJobsAndTheSettleWindow(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	created, prURL := st.createShiny(t, c)
	pr := created.PullRequest.Number
	st.ghs.repos.put(shinyService, "Dockerfile", "FROM scratch\n")
	st.declared(pr, "- name: "+shinyService+"\n  componentType: service\n  gen:\n    language: go\n    flavours: [app]\n    ci:\n      chartName: "+shinyService+"\n")
	st.reported(t, pr, prURL, reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictRepaired, Summary: "trigger the missed tag build for " + firstTag})
	st.ghs.repos.publish(shinyService, firstTag)
	line := "the CircleCI statuses on " + firstTag + ": "

	st.ghs.repos.report(shinyService, stateSuccess, circleSetup, circleBuild)
	w := st.watch(t, c, pr, 0.3)
	if w.Ready || w.Failure != nil || w.Pending != tools.PhaseReleased || names(w.Phases) != throughSetUp ||
		w.PendingReason != line+circleSetup+" (success), "+circleBuild+" (success) — awaiting a chart job, "+jobImagePush {
		t.Fatalf("setup and go-build green: %+v", w)
	}
	st.ghs.repos.report(shinyService, stateSuccess, circleChart, circlePushChart)
	if w = st.watch(t, c, pr, settling); w.Ready || w.Failure != nil || !strings.HasSuffix(w.PendingReason, " — awaiting "+jobImagePush) {
		t.Fatalf("the chart jobs green, no image push: %+v", w)
	}
	st.ghs.repos.report(shinyService, stateSuccess, circlePush)
	if w = st.watch(t, c, pr, settling); w.Ready || w.Failure != nil || !strings.Contains(w.PendingReason, circlePush+" (success) — green for ") ||
		!strings.Contains(w.PendingReason, "released once unchanged for "+watchSettle.String()) {
		t.Fatalf("every job green, settling: %+v", w)
	}
	if w = st.watch(t, c, pr, 3); !w.Ready || w.Pending != "" || w.PendingReason != "" || w.Failure != nil || names(w.Phases) != allPhases {
		t.Fatalf("settled: %+v", w)
	}

	st.ghs.repos.report(shinyService, stateFailure, circleChart)
	w = st.watch(t, c, pr, 0.3)
	if w.Ready || w.Failure == nil || w.Failure.Phase != tools.PhaseReleased || w.Failure.Reason != line[:len(line)-2]+" are failure: "+circleChart+" ("+stateFailure+")" {
		t.Fatalf("build-chart red: %+v failure=%+v", w, w.Failure)
	}
}

// TestWatchRepositoryReadiesAPlainRepositoryOnceSettled: a declaration
// without a chart on a default branch without a Dockerfile implies no job
// beyond the build: released is done once its statuses are green and have
// stayed unchanged for the settle window.
func TestWatchRepositoryReadiesAPlainRepositoryOnceSettled(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	created, prURL := st.create(t, c, map[string]any{kName: shinyService, kComponentType: kService, kGen: map[string]any{kLanguage: kGo, kFlavours: []any{"generic"}}})
	pr := created.PullRequest.Number
	st.declared(pr, "- name: "+shinyService+"\n  componentType: service\n  gen:\n    language: go\n    flavours: [generic]\n")
	st.reported(t, pr, prURL, reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictRepaired, Summary: "trigger the missed tag build for " + firstTag})
	st.ghs.repos.publish(shinyService, firstTag)
	st.ghs.repos.report(shinyService, stateSuccess, circleSetup, circleBuild)
	if w := st.watch(t, c, pr, settling); w.Ready || w.Failure != nil || !strings.Contains(w.PendingReason, "released once unchanged for") {
		t.Fatalf("green, settling: %+v", w)
	}
	w := st.watch(t, c, pr, 3)
	if !w.Ready || w.Pending != "" || w.PendingReason != "" || w.Failure != nil || names(w.Phases) != allPhases {
		t.Fatalf("settled: %+v", w)
	}
}

// TestWatchRepositoryReportsAFailedStep: the run of the pull request had a
// failed step: the setUp phase fails with the step and its cause, the phases
// before it are done.
func TestWatchRepositoryReportsAFailedStep(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	created, prURL := st.createShiny(t, c)
	pr := created.PullRequest.Number
	st.ghs.files.merge(pr)
	st.reported(t, pr, prURL, reconcile.StepResult{Step: reconcile.StepProtection, Verdict: reconcile.VerdictFailed, Summary: "PUT branch protection: 403 Resource not accessible by integration"})
	w := st.watch(t, c, pr, 0.3)
	if w.Ready || w.Changed || w.Pending != "" || w.Failure == nil || w.Failure.Phase != tools.PhaseSetUp || names(w.Phases) != throughMerged ||
		!strings.Contains(w.Failure.Reason, "the protection step failed: PUT branch protection: 403") || !strings.Contains(w.Failure.Reason, "/actions/runs/") {
		t.Fatalf("a failed step: %+v failure=%+v", w, w.Failure)
	}
}

// TestWatchRepositoryAwaitsTheRunFromTheMerge: the pending window counts
// from the merge, not from the opening. A pull request open for twenty
// minutes leaves the record expecting its run without a finding and the
// watch pending on merged; the poller reads the merge from the pull
// request and the window starts there; no run within it is given up with
// the finding reconcile-run-missing, worded for a creation with the merge
// time, and the setUp phase fails with it; the run's late artifact clears
// the finding and the watch goes on.
func TestWatchRepositoryAwaitsTheRunFromTheMerge(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	created, prURL := st.createShiny(t, c)
	pr := created.PullRequest.Number
	st.advance(20 * time.Minute)
	if p := st.poll(t); p.Pending != 0 || p.Missing != 0 {
		t.Fatalf("poll with the pull request open for 20 minutes: %+v", p)
	}
	if rec := st.record(t, shinyService); !rec.Setup.PendingRun.Follows(pr) || rec.Setup.PendingRun.MergedAt != nil || rec.Setup.MissingRun != nil || hasKind(rec, inventory.FindingReconcileRunMissing) {
		t.Fatalf("record with the pull request open: %+v findings %+v", rec.Setup, rec.Findings)
	}
	if w := st.watch(t, c, pr, 0.3); w.Failure != nil || w.Pending != tools.PhaseMerged || names(w.Phases) != "created scaffolded declared" {
		t.Fatalf("watch with the pull request open: %+v failure=%+v", w, w.Failure)
	}

	merged := st.now().UTC().Truncate(time.Second)
	st.ghs.files.mergeAt(pr, merged)
	if p := st.poll(t); p.Pending != 1 || p.Missing != 0 {
		t.Fatalf("poll after the merge: %+v", p)
	}
	if rec := st.record(t, shinyService); !rec.Setup.PendingRun.Follows(pr) || rec.Setup.PendingRun.MergedAt == nil || !rec.Setup.PendingRun.MergedAt.Equal(merged) {
		t.Fatalf("record after the merge: %+v", rec.Setup.PendingRun)
	}
	if w := st.watch(t, c, pr, 0.3); w.Failure != nil || w.Pending != tools.PhaseSetUp || names(w.Phases) != throughMerged {
		t.Fatalf("watch after the merge: %+v failure=%+v", w, w.Failure)
	}
	st.advance(14 * time.Minute)
	if p := st.poll(t); p.Pending != 1 || p.Missing != 0 {
		t.Fatalf("poll 14 minutes after the merge: %+v", p)
	}
	st.advance(2 * time.Minute)
	if p := st.poll(t); p.Pending != 0 || p.Missing != 1 {
		t.Fatalf("poll past the window from the merge: %+v", p)
	}
	rec := st.record(t, shinyService)
	f := rec.MissingRunFinding()
	if f == nil || !strings.Contains(f.Message, prURL) || !strings.Contains(f.Message, alice+" opened") || !strings.Contains(f.Message, "was merged at "+merged.Format(time.RFC3339)) ||
		!strings.Contains(f.Fix, "follows the pull request's merge") || !strings.Contains(f.Fix, "/actions/workflows/"+reconcilerWorkflow) || !hasKind(rec, inventory.FindingReconcileRunMissing) {
		t.Fatalf("the missing run's finding: %+v (%+v)", f, rec.Findings)
	}
	w := st.watch(t, c, pr, 0.3)
	if w.Ready || w.Failure == nil || w.Failure.Phase != tools.PhaseSetUp || w.Failure.Reason != f.Message || names(w.Phases) != throughMerged {
		t.Fatalf("a missing run: %+v failure=%+v", w, w.Failure)
	}

	// The run reports after all: the finding goes, the watch goes on.
	st.reported(t, pr, prURL, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictRepaired, Summary: summaryFollowed})
	if rec := st.record(t, shinyService); rec.Setup.MissingRun != nil || rec.Setup.PendingRun != nil || hasKind(rec, inventory.FindingReconcileRunMissing) {
		t.Fatalf("record after the late run: %+v findings %+v", rec.Setup, rec.Findings)
	}
	if w = st.watch(t, c, pr, 0.3); w.Failure != nil || w.Pending != tools.PhaseReleased || names(w.Phases) != throughSetUp {
		t.Fatalf("watch after the late run: %+v failure=%+v", w, w.Failure)
	}
}

// TestWatchRepositoryFailsAtOnceWhenTheMergeRunUploadsNoReport: the push
// run of the merged pull request — the run whose head is the merge commit —
// completing without an artifact for the repository ends the pending run in
// the poll that reads it, the finding worded for the creation with the
// merge time and the run's conclusion, and the setUp phase fails with it at
// once; a push run of another merge over the same repository is not this
// pull request's. The run's late artifact clears the finding.
func TestWatchRepositoryFailsAtOnceWhenTheMergeRunUploadsNoReport(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	created, prURL := st.createShiny(t, c)
	pr := created.PullRequest.Number
	merged := st.now().UTC().Truncate(time.Second)
	st.ghs.files.mergeAt(pr, merged)
	// Another merge's push run over the repository failed without a report.
	st.ghs.actions.addRunWithoutReport(t, eventPush, st.now(), "other000", shinyService)
	if p := st.poll(t); p.Pending != 1 || p.Missing != 0 || len(p.Errors) != 0 {
		t.Fatalf("poll with another merge's failed run: %+v", p)
	}
	if rec := st.record(t, shinyService); !rec.Setup.PendingRun.Follows(pr) || rec.Setup.MissingRun != nil {
		t.Fatalf("record after another merge's run: %+v", rec.Setup)
	}
	failed := st.ghs.actions.addRunWithoutReport(t, eventPush, st.now(), st.ghs.files.mergeSHA(pr), shinyService)
	if p := st.poll(t); p.Pending != 0 || p.Missing != 1 || len(p.Errors) != 0 {
		t.Fatalf("poll with the merge's failed run: %+v", p)
	}
	rec := st.record(t, shinyService)
	f := rec.MissingRunFinding()
	if rec.Setup.PendingRun != nil || rec.Setup.MissingRun == nil || rec.Setup.MissingRun.RunURL != runURL(failed.ID) || rec.Setup.MissingRun.Conclusion != conclusionFailure ||
		f == nil || !strings.Contains(f.Message, prURL) || !strings.Contains(f.Message, "was merged at "+merged.Format(time.RFC3339)) ||
		!strings.HasSuffix(f.Message, "completed with conclusion failure and uploaded no report: "+runURL(failed.ID)) || !strings.Contains(f.Fix, "look at the run "+runURL(failed.ID)) {
		t.Fatalf("the failed run on the record: %+v finding %+v", rec.Setup.MissingRun, f)
	}
	w := st.watch(t, c, pr, 0.3)
	if w.Ready || w.Failure == nil || w.Failure.Phase != tools.PhaseSetUp || w.Failure.Reason != f.Message || names(w.Phases) != throughMerged {
		t.Fatalf("the watch with the failed run: %+v failure=%+v", w, w.Failure)
	}
	st.reported(t, pr, prURL, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictRepaired, Summary: summaryFollowed})
	if rec := st.record(t, shinyService); rec.Setup.MissingRun != nil || rec.Setup.PendingRun != nil || hasKind(rec, inventory.FindingReconcileRunMissing) {
		t.Fatalf("record after the late run: %+v findings %+v", rec.Setup, rec.Findings)
	}
	if w = st.watch(t, c, pr, 0.3); w.Failure != nil || w.Pending != tools.PhaseReleased || names(w.Phases) != throughSetUp {
		t.Fatalf("watch after the late run: %+v failure=%+v", w, w.Failure)
	}
}

// TestWatchRepositoryForgetsTheRunOfAClosedPullRequest: a pull request
// closed without a merge is followed by no run — the poller drops the mark
// without a finding; the watch fails on merged, as before.
func TestWatchRepositoryForgetsTheRunOfAClosedPullRequest(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	created, prURL := st.createShiny(t, c)
	pr := created.PullRequest.Number
	st.ghs.files.close(pr)
	st.advance(20 * time.Minute)
	if p := st.poll(t); p.Pending != 0 || p.Missing != 0 {
		t.Fatalf("poll with the pull request closed: %+v", p)
	}
	if rec := st.record(t, shinyService); rec.Setup.PendingRun != nil || rec.Setup.MissingRun != nil || hasKind(rec, inventory.FindingReconcileRunMissing) {
		t.Fatalf("record with the pull request closed: %+v findings %+v", rec.Setup, rec.Findings)
	}
	if w := st.watch(t, c, pr, 0.3); w.Failure == nil || w.Failure.Phase != tools.PhaseMerged || !strings.Contains(w.Failure.Reason, prURL+" was closed without being merged") {
		t.Fatalf("watch with the pull request closed: %+v failure=%+v", w, w.Failure)
	}
}

// TestWatchRepositoryReportsARedRelease: without a CircleCI status on the
// release the reconciler's release step decides — its red-release finding
// fails the phase; once CircleCI reports a failure, the statuses do.
func TestWatchRepositoryReportsARedRelease(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	created, prURL := st.createShiny(t, c)
	pr := created.PullRequest.Number
	st.ghs.files.merge(pr)
	red := "release " + firstTag + " of " + org + "/" + shinyService + " is red: pipeline 1, build (failed: go-build)"
	st.reported(t, pr, prURL, reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictReported,
		Findings: []reconcile.Finding{{Kind: reconcile.FindingRedRelease, Message: red, Fix: firstTag + " is a dead tag"}}})
	st.ghs.repos.publish(shinyService, firstTag)
	w := st.watch(t, c, pr, 0.3)
	if w.Ready || w.Failure == nil || w.Failure.Phase != tools.PhaseReleased || w.Failure.Reason != red || names(w.Phases) != throughSetUp || w.Release == nil {
		t.Fatalf("the reconciler's red release: %+v failure=%+v", w, w.Failure)
	}
	st.ghs.repos.report(shinyService, stateFailure, circleBuild)
	w = st.watch(t, c, pr, 0.3)
	if w.Failure == nil || w.Failure.Phase != tools.PhaseReleased || w.Failure.Reason != "the CircleCI statuses on "+firstTag+" are failure: "+circleBuild+" ("+stateFailure+")" {
		t.Fatalf("CircleCI's red status: %+v", w.Failure)
	}
}

// TestWatchRepositoryReportsARefusedRead: the inventory App's installation
// does not cover the repository: GitHub refuses the statuses read with 403,
// and the watch reports the released phase pending with the refusal — a fact
// about the phase, never the tool's error; once the App reaches the
// repository, the green statuses make it ready.
func TestWatchRepositoryReportsARefusedRead(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	created, prURL := st.createShiny(t, c)
	pr := created.PullRequest.Number
	st.ghs.files.merge(pr)
	st.reported(t, pr, prURL, reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictRepaired, Summary: "trigger the missed tag build for " + firstTag})
	st.ghs.repos.publish(shinyService, firstTag)
	st.ghs.repos.report(shinyService, stateSuccess, greenRelease...)
	st.ghs.repos.installed(shinyService, false)
	w := st.watch(t, c, pr, 0.3)
	if w.Ready || w.Failure != nil || w.Pending != tools.PhaseReleased || names(w.Phases) != throughSetUp ||
		!strings.Contains(w.PendingReason, "read the statuses of "+org+"/"+shinyService+"@"+firstTag+" as the inventory App") || !strings.Contains(w.PendingReason, "403") {
		t.Fatalf("a refused statuses read: %+v", w)
	}
	st.ghs.repos.installed(shinyService, true)
	if w = st.watch(t, c, pr, 3); !w.Ready || w.PendingReason != "" {
		t.Fatalf("after the App reaches the repository: %+v", w)
	}
}
