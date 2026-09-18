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
// CircleCI client from what the record knows. The steps answer what a
// person reading them asks — does CircleCI build this repository, did the
// latest release build — and both answers are on GitHub: the `ci/circleci:`
// statuses on the default branch head and on the release's tag commit,
// read with the repository. The reconciler's stored run, made with its own
// CircleCI token, decides when it ran: it also saw the settings only a token
// reaches (setup workflows, checkout key). What no source yields is the
// finding `unchecked`, never a guess; a step the engine skipped for another
// reason (an archived repository, an empty one) stays as it is. Converged
// follows the rewritten steps.
func fillClientlessSteps(res *reconcile.Result, rec *inventory.Record) {
	if res == nil || rec == nil || rec.Reality == nil {
		return
	}
	last := rec.Setup.LastRun
	if sr := res.Step(reconcile.StepCircleCI); sr != nil && clientless(sr) {
		*sr = circleCIStep(rec.Repository, rec.Reality.DefaultBranch, rec.CircleCI, last)
	}
	if sr := res.Step(reconcile.StepRelease); sr != nil && clientless(sr) {
		*sr = releaseStep(rec.Repository, releaseTag(sr.Summary), rec.Reality.LatestRelease, last)
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

// runStep is the reconciler's last run's result for step; nil when no run
// reports the step or that run skipped it too.
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

// alignFix is the fix of an unchecked finding here: the reconciler's run
// reads CircleCI directly.
const alignFix = "run Align now: the reconciler's check reads CircleCI directly, and for a team that has not opted in it changes nothing"

// convergedCircleCI is the engine's summary of a converged circleci step.
const convergedCircleCI = "followed, setup workflows on, checkout key present"

// circleCIStep is the circleci step from the record's CircleCI state: the
// head's statuses say whether CircleCI builds the branch — the fact the
// step is for — and the reconciler's run adds the settings it read when it
// ran.
func circleCIStep(slug, branch string, cc *inventory.CircleCI, last *inventory.LastRun) reconcile.StepResult {
	sr := reconcile.StepResult{Step: reconcile.StepCircleCI}
	if cc == nil {
		sr.Verdict = reconcile.VerdictSkipped
		sr.Summary = "repository not on GitHub"
		return sr
	}
	if run := runStep(last, reconcile.StepCircleCI); run != nil {
		// The run read the project itself: a head without a status is a head
		// not built yet, not a project CircleCI does not build.
		builds := buildsSentence(slug, branch, cc.Head, false)
		from := "reconciler run of " + last.Timestamp.UTC().Format(time.RFC3339)
		switch run.Verdict {
		case reconcile.VerdictFailed:
			sr.Verdict = reconcile.VerdictFailed
			sr.Summary = join("the reconciler's circleci step failed: "+run.Summary+" ("+from+")", builds)
		case reconcile.VerdictOK, reconcile.VerdictRepaired:
			sr.Verdict = reconcile.VerdictOK
			sr.Summary = join(builds, convergedCircleCI+" ("+from+")")
		case reconcile.VerdictReported:
			sr.Verdict = reconcile.VerdictReported
			sr.Summary = join(builds, convergedCircleCI+" ("+from+")")
			sr.Findings = append([]reconcile.Finding(nil), run.Findings...)
		default:
			// Drift: the check found what a repair would do, and no repair ran
			// since — the reconciler's plan is the step's.
			sr.Verdict = reconcile.VerdictDrift
			sr.Summary = join(builds, "drift found by the "+from)
			sr.Changes = append([]string(nil), run.Changes...)
		}
		return sr
	}
	builds := buildsSentence(slug, branch, cc.Head, true)
	switch {
	case cc.Head != nil:
		sr.Verdict = reconcile.VerdictOK
		sr.Summary = builds
	case contains(cc.Unknown, inventory.CircleCIFactFollowed):
		sr.Verdict = reconcile.VerdictReported
		sr.Summary = fmt.Sprintf("the statuses on %s's head were truncated before a CircleCI one", branch)
		sr.Findings = []reconcile.Finding{{Kind: reconcile.FindingUnchecked,
			Message: fmt.Sprintf("whether CircleCI builds %s is out of the inventory's reach: the head's status contexts were truncated before a CircleCI one", slug),
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

// buildsSentence says what the head's `ci/circleci:` statuses tell; conclude
// draws the conclusion from a head without any — CircleCI does not build the
// repository — which holds only when nothing else has read the project.
func buildsSentence(slug, branch string, head *inventory.HeadStatus, conclude bool) string {
	if head == nil {
		if conclude {
			return fmt.Sprintf("no CircleCI status on %s's head: CircleCI does not build %s", branch, slug)
		}
		return fmt.Sprintf("no CircleCI status on %s's head yet", branch)
	}
	return fmt.Sprintf("CircleCI builds %s: %s (%s, %s)", branch, head.State, jobs(head), head.At.UTC().Format(time.RFC3339))
}

func jobs(s *inventory.HeadStatus) string {
	if n := len(s.Contexts); n != 1 {
		return fmt.Sprintf("%d jobs", n)
	}
	return "1 job"
}

// releaseStep is the release step for tag: the tag commit's `ci/circleci:`
// statuses say whether CircleCI built the release; the reconciler's run
// stands in while the commit carries none and that run named the tag; a
// commit without any CircleCI status is the missed tag build the engine
// would trigger.
func releaseStep(slug, tag string, rel *inventory.Release, last *inventory.LastRun) reconcile.StepResult {
	sr := reconcile.StepResult{Step: reconcile.StepRelease}
	if rel != nil && rel.Tag == tag && rel.Build != nil {
		b := rel.Build
		at := b.At.UTC().Format(time.RFC3339)
		switch b.State {
		case "success":
			sr.Verdict = reconcile.VerdictOK
			sr.Summary = fmt.Sprintf("release %s built: CircleCI success (%s, %s)", tag, jobs(b), at)
		case "pending", "expected":
			sr.Verdict = reconcile.VerdictOK
			sr.Summary = fmt.Sprintf("release %s: CircleCI pipeline running (%s, %s)", tag, jobs(b), at)
		default:
			sr.Verdict = reconcile.VerdictReported
			sr.Summary = fmt.Sprintf("release %s: CircleCI %s (%s, %s)", tag, b.State, jobs(b), at)
			sr.Findings = []reconcile.Finding{{Kind: reconcile.FindingRedRelease,
				Message: fmt.Sprintf("the tag pipeline of %s %s failed (%s)", slug, tag, strings.Join(b.Contexts, ", ")),
				Fix:     "a tag is never rebuilt: fix the pipeline and cut the next release"}}
		}
		return sr
	}
	if run := runStep(last, reconcile.StepRelease); run != nil && mentions(run, tag) {
		from := " (reconciler run of " + last.Timestamp.UTC().Format(time.RFC3339) + ")"
		sr.Verdict = run.Verdict
		sr.Changes = append([]string(nil), run.Changes...)
		sr.Findings = append([]reconcile.Finding(nil), run.Findings...)
		switch {
		case run.Verdict == reconcile.VerdictRepaired:
			// The run triggered the missed build; the statuses report it next.
			sr.Verdict = reconcile.VerdictOK
			sr.Summary = fmt.Sprintf("release %s: build triggered by the reconciler%s", tag, from)
		case run.Summary != "":
			sr.Summary = run.Summary + from
		default:
			sr.Summary = fmt.Sprintf("release %s%s", tag, from)
		}
		return sr
	}
	switch {
	case rel == nil || rel.Tag != tag:
		latest := "none"
		if rel != nil {
			latest = rel.Tag
		}
		sr.Verdict = reconcile.VerdictReported
		sr.Summary = fmt.Sprintf("release %s: not the latest release the inventory read (%s)", tag, latest)
		sr.Findings = []reconcile.Finding{{Kind: reconcile.FindingUnchecked,
			Message: fmt.Sprintf("whether CircleCI built the tag %s of %s is out of the inventory's reach until its next read of the repository", tag, slug),
			Fix:     "refresh the repository, or " + alignFix}}
	case rel.BuildTruncated:
		sr.Verdict = reconcile.VerdictReported
		sr.Summary = fmt.Sprintf("release %s: the statuses on its commit were truncated before a CircleCI one", tag)
		sr.Findings = []reconcile.Finding{{Kind: reconcile.FindingUnchecked,
			Message: fmt.Sprintf("whether CircleCI built the tag %s of %s is out of the inventory's reach: the commit's status contexts were truncated before a CircleCI one", tag, slug),
			Fix:     alignFix}}
	default:
		sr.Verdict = reconcile.VerdictDrift
		sr.Summary = fmt.Sprintf("no CircleCI status on %s's commit: the tag was not built", tag)
		sr.Changes = []string{"trigger the missed tag build for " + tag}
	}
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
