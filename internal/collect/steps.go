package collect

import (
	"fmt"
	"strings"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// noCircleCIClient is what the engine's circleci and release steps say when
// the runner has no CircleCI client (devctl pkg/reposetup/reconcile,
// step_circleci.go): the whole summary of the circleci step, the tail of the
// release step's ("release <tag>: no CircleCI client to verify the pipeline").
const noCircleCIClient = "no CircleCI client"

// fillClientlessSteps writes the two steps the engine skips for want of a
// CircleCI client from what the record knows instead. This service holds no
// CircleCI token, so the engine's circleci and release steps are always
// "skipped: no CircleCI client" — which reads as CircleCI missing, while
// the record has the facts from two sources: the `ci/circleci:` statuses on
// the default branch head (CircleCI builds the repository) and the
// reconciler's last run over the repository (followed, setup workflows,
// checkout key, the tag build — checked with its CircleCI token). The steps
// are written the way the reconciler's run would report them, each summary
// naming the source; what no source yields is the finding `unchecked` with
// Align now as the fix, never a guess. A step the engine skipped for another
// reason (an archived repository, an empty one) stays as it is. Converged
// follows the rewritten steps.
func fillClientlessSteps(res *reconcile.Result, rec *inventory.Record) {
	if res == nil || rec == nil || rec.Reality == nil {
		return
	}
	slug := rec.Repository
	var last *inventory.LastRun
	if rec.Setup.LastRun != nil {
		last = rec.Setup.LastRun
	}
	if sr := res.Step(reconcile.StepCircleCI); sr != nil && clientless(sr) {
		*sr = circleCIStep(slug, rec.Reality.DefaultBranch, rec.CircleCI, last)
	}
	if sr := res.Step(reconcile.StepRelease); sr != nil && clientless(sr) {
		*sr = releaseStep(slug, releaseTag(sr.Summary), last)
	}
	res.Converged = true
	for _, sr := range res.Steps {
		if sr.Verdict == reconcile.VerdictDrift || sr.Verdict == reconcile.VerdictFailed {
			res.Converged = false
		}
	}
}

// clientless says whether the engine skipped the step for want of a CircleCI
// client, as opposed to any other reason (archived, empty, missing).
func clientless(sr *reconcile.StepResult) bool {
	return sr.Verdict == reconcile.VerdictSkipped && strings.Contains(sr.Summary, noCircleCIClient)
}

// releaseTag is the tag the engine named in its skipped release step,
// "release <tag>: no CircleCI client to verify the pipeline".
func releaseTag(summary string) string {
	tag := strings.TrimPrefix(summary, "release ")
	if i := strings.Index(tag, ": "); i >= 0 {
		tag = tag[:i]
	}
	return tag
}

// runStep is the reconciler's last run's result for step, with the run it
// came from; nil when no run reports the step or that run skipped it too.
func runStep(last *inventory.LastRun, step reconcile.Step) *reconcile.StepResult {
	if last == nil {
		return nil
	}
	sr := last.Result.Step(step)
	if sr == nil || sr.Verdict == reconcile.VerdictSkipped {
		return nil
	}
	return sr
}

// alignFix is the fix of every unchecked finding here: a CircleCI token for
// the engine's checks, or a reconciler run in the meantime.
const alignFix = "configure the inventory's CircleCI token (chart value circleci.existingSecret): the engine then checks it on every sweep and refresh; until then a reconciler run reports it — Align now starts one at once, and for a team that has not opted in it checks and changes nothing"

// convergedCircleCI is the engine's summary of a converged circleci step.
const convergedCircleCI = "followed, setup workflows on, checkout key present"

// circleCIStep is the circleci step from the record's CircleCI state: the
// reconciler's run decides followed, setup workflows and checkout key when
// it ran; the head's statuses say whether CircleCI builds the branch.
func circleCIStep(slug, branch string, cc *inventory.CircleCI, last *inventory.LastRun) reconcile.StepResult {
	sr := reconcile.StepResult{Step: reconcile.StepCircleCI}
	if cc == nil {
		sr.Verdict = reconcile.VerdictSkipped
		sr.Summary = "repository not on GitHub"
		return sr
	}
	builds := buildsSentence(slug, branch, cc.Head)
	if run := runStep(last, reconcile.StepCircleCI); run != nil {
		from := "reconciler run of " + last.Timestamp.UTC().Format(time.RFC3339)
		switch run.Verdict {
		case reconcile.VerdictFailed:
			sr.Verdict = reconcile.VerdictFailed
			sr.Summary = join("the reconciler's circleci step failed: "+run.Summary+" ("+from+")", builds)
		case reconcile.VerdictOK, reconcile.VerdictRepaired:
			sr.Verdict = reconcile.VerdictOK
			sr.Summary = join(convergedCircleCI+" ("+from+")", builds)
		case reconcile.VerdictReported:
			sr.Verdict = reconcile.VerdictReported
			sr.Summary = join(convergedCircleCI+" ("+from+")", builds)
			sr.Findings = append([]reconcile.Finding(nil), run.Findings...)
		default:
			// Drift: the check found what a repair would do, and no repair ran
			// since — the reconciler's plan is the step's.
			sr.Verdict = reconcile.VerdictDrift
			sr.Summary = join("drift found by the "+from, builds)
			sr.Changes = append([]string(nil), run.Changes...)
		}
		return sr
	}
	switch {
	case cc.Head != nil:
		sr.Verdict = reconcile.VerdictReported
		sr.Summary = builds
		sr.Findings = []reconcile.Finding{{Kind: reconcile.FindingUnchecked,
			Message: fmt.Sprintf("%s is built by CircleCI; whether setup workflows are on and a checkout key exists is out of the inventory's reach: no reconciler run has checked %s yet", slug, slug),
			Fix:     alignFix}}
	case contains(cc.Unknown, inventory.CircleCIFactFollowed):
		sr.Verdict = reconcile.VerdictReported
		sr.Summary = fmt.Sprintf("the statuses on %s's head were truncated before a CircleCI one", branch)
		sr.Findings = []reconcile.Finding{{Kind: reconcile.FindingUnchecked,
			Message: fmt.Sprintf("whether CircleCI builds %s is out of the inventory's reach: the head's status contexts were truncated before a CircleCI one, and no reconciler run has checked %s yet", slug, slug),
			Fix:     alignFix}}
	default:
		// No CircleCI status on the head and no run: the project is not built,
		// the check-mode plan is the engine's for an unfollowed project.
		sr.Verdict = reconcile.VerdictDrift
		sr.Summary = builds
		sr.Changes = []string{"follow " + slug, "enable setup workflows", "create a deploy key"}
	}
	return sr
}

// buildsSentence says what the head's `ci/circleci:` statuses tell.
func buildsSentence(slug, branch string, head *inventory.HeadStatus) string {
	if head == nil {
		return fmt.Sprintf("no CircleCI status on %s's head: CircleCI does not build %s", branch, slug)
	}
	jobs := "1 job"
	if n := len(head.Contexts); n != 1 {
		jobs = fmt.Sprintf("%d jobs", n)
	}
	return fmt.Sprintf("CircleCI builds %s: %s (%s, %s)", branch, head.State, jobs, head.At.UTC().Format(time.RFC3339))
}

// releaseStep is the release step for tag from the reconciler's last run,
// when that run verified this very tag; else the tag is unchecked.
func releaseStep(slug, tag string, last *inventory.LastRun) reconcile.StepResult {
	sr := reconcile.StepResult{Step: reconcile.StepRelease}
	if run := runStep(last, reconcile.StepRelease); run != nil && mentions(run, tag) {
		from := " (reconciler run of " + last.Timestamp.UTC().Format(time.RFC3339) + ")"
		sr.Verdict = run.Verdict
		sr.Changes = append([]string(nil), run.Changes...)
		sr.Findings = append([]reconcile.Finding(nil), run.Findings...)
		switch {
		case run.Verdict == reconcile.VerdictRepaired:
			// The run triggered the missed build; the next run reports it.
			sr.Verdict = reconcile.VerdictOK
			sr.Summary = fmt.Sprintf("release %s: build triggered by the reconciler%s", tag, from)
		case run.Summary != "":
			sr.Summary = run.Summary + from
		default:
			sr.Summary = fmt.Sprintf("release %s%s", tag, from)
		}
		return sr
	}
	sr.Verdict = reconcile.VerdictReported
	sr.Summary = fmt.Sprintf("release %s: not verified by a reconciler run yet", tag)
	sr.Findings = []reconcile.Finding{{Kind: reconcile.FindingUnchecked,
		Message: fmt.Sprintf("whether CircleCI built the tag %s of %s is out of the inventory's reach: no reconciler run has verified it", tag, slug),
		Fix:     alignFix + "; a missed tag build is triggered by that run"}}
	return sr
}

// mentions says whether the step's summary, changes or findings name tag.
func mentions(sr *reconcile.StepResult, tag string) bool {
	if tag == "" {
		return false
	}
	if strings.Contains(sr.Summary, tag) {
		return true
	}
	for _, c := range sr.Changes {
		if strings.Contains(c, tag) {
			return true
		}
	}
	for _, f := range sr.Findings {
		if strings.Contains(f.Message, tag) {
			return true
		}
	}
	return false
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
