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
// that never reported, to a red release, through a read GitHub refuses — and
// the timeout that answers with what is pending.

const (
	argTimeout = "timeout"
	firstTag   = "v0.1.0"
	shinyURL   = "https://github.com/" + org + "/" + shinyService
	// throughSetUp are the phases done once the reconciler run has reported.
	throughSetUp = "created scaffolded declared merged setUp"
)

// createShiny creates shiny-service as alice and merges nothing.
func (st *stack) createShiny(t *testing.T, c *client.Client) (tools.Created, string) {
	t.Helper()
	var out tools.Created
	st.callJSON(t, c, tools.ToolCreateRepository, map[string]any{argMode: modeCommit, argTeam: team, argEntry: newEntry}, &out)
	if out.PullRequest == nil {
		t.Fatalf("create_repository: %+v", out)
	}
	return out, fmt.Sprintf("https://github.com/%s/github/pull/%d", org, out.PullRequest.Number)
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
// record expecting the run of its pull request, so the poller runs at its
// pending interval; before the merge the watch answers with merged pending
// and the three phases done; after the merge and the run, with the release
// there but CircleCI silent, released stays pending, as it does while
// CircleCI's statuses are pending; its green statuses during the wait make
// it ready with every phase. The statuses are read as the inventory App: the
// fake refuses them to a person's token.
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
	if p := st.poll(t); p.Pending != 1 {
		t.Errorf("poll while the creation's run is expected: %+v", p)
	}
	if text, isErr := call(t, c, tools.ToolWatchRepository, map[string]any{kRepository: shinyService, argPullRequest: 99999}); !isErr || !strings.Contains(text, "#99999 does not exist") {
		t.Errorf("an unknown pull request: %v %s", isErr, text)
	}

	w := st.watch(t, c, pr, 0.3)
	if w.Ready || w.Pending != tools.PhaseMerged || w.Failure != nil || names(w.Phases) != "created scaffolded declared" || w.Repository != shinyURL || w.PullRequest != prURL {
		t.Fatalf("before the merge: %+v", w)
	}
	for i, ph := range w.Phases {
		if ph.At.IsZero() || (i > 0 && ph.At.Before(w.Phases[i-1].At)) || ph.Seconds < 0 {
			t.Errorf("phase %d: %+v", i, ph)
		}
	}

	st.ghs.files.merge(pr)
	st.reported(t, pr, prURL,
		reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictRepaired, Summary: "followed", Changes: []string{"follow the project"}},
		reconcile.StepResult{Step: reconcile.StepMetadata, Verdict: reconcile.VerdictReported, Findings: []reconcile.Finding{{Kind: reconcile.FindingDefaultIcon, Message: "the chart carries the template's icon", Fix: "replace the icon"}}},
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
	if w = st.watch(t, c, pr, 0.3); w.Ready || w.Pending != tools.PhaseReleased || w.PendingReason != "" || w.Failure != nil {
		t.Fatalf("CircleCI pending: %+v", w)
	}

	go func() {
		time.Sleep(150 * time.Millisecond)
		st.ghs.repos.report(shinyService, stateSuccess, circleBuild, circlePush)
	}()
	w = st.watch(t, c, pr, 5)
	if !w.Ready || w.Pending != "" || w.Failure != nil || names(w.Phases) != "created scaffolded declared merged setUp released" || w.Waited > 2 {
		t.Fatalf("ready: %+v", w)
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
	if w.Ready || w.Pending != "" || w.Failure == nil || w.Failure.Phase != tools.PhaseSetUp || names(w.Phases) != "created scaffolded declared merged" ||
		!strings.Contains(w.Failure.Reason, "the protection step failed: PUT branch protection: 403") || !strings.Contains(w.Failure.Reason, "/actions/runs/") {
		t.Fatalf("a failed step: %+v failure=%+v", w, w.Failure)
	}
}

// TestWatchRepositoryReportsAMissingRun: the pull request merged and no run
// reported within the pending window: the poller gives the creation's run
// up with the finding reconcile-run-missing, worded for a creation, and the
// setUp phase fails with it.
func TestWatchRepositoryReportsAMissingRun(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	created, prURL := st.createShiny(t, c)
	pr := created.PullRequest.Number
	st.ghs.files.merge(pr)
	st.advance(16 * time.Minute)
	if p := st.poll(t); p.Missing != 1 {
		t.Fatalf("poll past the window: %+v", p)
	}
	rec := st.record(t, shinyService)
	f := rec.MissingRunFinding()
	if f == nil || !strings.Contains(f.Message, prURL) || !strings.Contains(f.Message, alice+" opened") || !strings.Contains(f.Fix, "merge it") || !hasKind(rec, inventory.FindingReconcileRunMissing) {
		t.Fatalf("the missing run's finding: %+v (%+v)", f, rec.Findings)
	}
	w := st.watch(t, c, pr, 0.3)
	if w.Ready || w.Failure == nil || w.Failure.Phase != tools.PhaseSetUp || w.Failure.Reason != f.Message || names(w.Phases) != "created scaffolded declared merged" {
		t.Fatalf("a missing run: %+v failure=%+v", w, w.Failure)
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
	st.ghs.repos.report(shinyService, stateSuccess, circleBuild, circlePush)
	st.ghs.repos.installed(shinyService, false)
	w := st.watch(t, c, pr, 0.3)
	if w.Ready || w.Failure != nil || w.Pending != tools.PhaseReleased || names(w.Phases) != throughSetUp ||
		!strings.Contains(w.PendingReason, "read the statuses of "+org+"/"+shinyService+"@"+firstTag+" as the inventory App") || !strings.Contains(w.PendingReason, "403") {
		t.Fatalf("a refused statuses read: %+v", w)
	}
	st.ghs.repos.installed(shinyService, true)
	if w = st.watch(t, c, pr, 0.3); !w.Ready || w.PendingReason != "" {
		t.Fatalf("after the App reaches the repository: %+v", w)
	}
}
