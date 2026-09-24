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
// follows the rewritten steps by the engine's rule: no step drifted or
// failed and every finding is advisory.
func fillClientlessSteps(res *reconcile.Result, rec *inventory.Record) {
	if res == nil || rec == nil || rec.Reality == nil {
		return
	}
	last := rec.Setup.LastRun
	if sr := res.Step(reconcile.StepCircleCI); sr != nil && clientless(sr) {
		*sr = circleCIStep(rec.Repository, rec.Reality.DefaultBranch, rec.CircleCI, rec.CI, last)
	}
	if sr := res.Step(reconcile.StepRelease); sr != nil && clientless(sr) {
		*sr = releaseStep(rec.Repository, releaseTag(sr.Summary), rec.Reality.DefaultBranch, rec.Reality.LatestRelease, rec.CI, last, rec.Setup.Release)
	}
	converge(res)
}

// converge sets the result's Converged from its steps, the engine's rule:
// no step drifted or failed and every finding is advisory.
func converge(res *reconcile.Result) {
	res.Converged = true
	for i := range res.Steps {
		if !res.Steps[i].Converges() {
			res.Converged = false
		}
	}
}

// rewriteReleaseStep writes the record's release step from the release
// watch's state, when the record has checks with a release step and the
// watch has a release: the watch read the tag's own pipeline, which is the
// answer the step asks for, and it read it after the checks ran. Converged
// follows.
func rewriteReleaseStep(rec *inventory.Record) {
	w := rec.Setup.Release
	if w == nil || rec.Setup.Checks == nil || rec.Reality == nil {
		return
	}
	sr := rec.Setup.Checks.Step(reconcile.StepRelease)
	if sr == nil {
		return
	}
	*sr = releaseStep(rec.Repository, w.Tag, rec.Reality.DefaultBranch, rec.Reality.LatestRelease, rec.CI, rec.Setup.LastRun, w)
	converge(rec.Setup.Checks)
}

// finding is a finding of kind with its message and fix, advisory as the
// engine says of the kind, so a step written here reads like the engine's.
func finding(kind reconcile.FindingKind, message, fix string) reconcile.Finding {
	return reconcile.Finding{Kind: kind, Message: message, Fix: fix, Advisory: kind.Advisory()}
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
const alignFix = "run Align now: the reconciler's check reads CircleCI directly, and for a repository that has not opted in to alignment (`align: true` in its entry) it changes nothing"

// convergedCircleCI is the engine's summary of a converged circleci step up
// to its word on the webhook (webhookClause).
const convergedCircleCI = "followed, setup workflows on, checkout key present"

// circleCIStep is the circleci step from the record's CircleCI state: the
// head's statuses say whether CircleCI builds the branch — the fact the
// step is for — and the reconciler's run adds the settings and the webhook
// it read when it ran, with its findings: a missing webhook keeps the step
// from converging whatever else the run did. The declaration (ci) tells the
// branch's own statuses from another pipeline's at the head commit.
func circleCIStep(slug, branch string, cc *inventory.CircleCI, ci *inventory.CI, last *inventory.LastRun) reconcile.StepResult {
	sr := reconcile.StepResult{Step: reconcile.StepCircleCI}
	if cc == nil {
		sr.Verdict = reconcile.VerdictSkipped
		sr.Summary = "repository not on GitHub"
		return sr
	}
	if run := runStep(last, reconcile.StepCircleCI); run != nil {
		// The run read the project itself: a head without a status is a head
		// not built yet, not a project CircleCI does not build.
		builds := buildsSentence(slug, branch, cc.Head, ci, false)
		from := "reconciler run of " + last.Timestamp.UTC().Format(time.RFC3339)
		sr.Findings = append([]reconcile.Finding(nil), run.Findings...)
		switch run.Verdict {
		case reconcile.VerdictFailed:
			sr.Verdict = reconcile.VerdictFailed
			sr.Summary = join("the reconciler's circleci step failed: "+run.Summary+" ("+from+")", builds)
		case reconcile.VerdictDrift:
			// The check found what a repair would do, and no repair ran
			// since — the reconciler's plan is the step's.
			sr.Verdict = reconcile.VerdictDrift
			sr.Summary = join(builds, "drift found by the "+from)
			sr.Changes = append([]string(nil), run.Changes...)
		default:
			// Converged, reported or repaired: after the run the project is
			// set up as far as the engine can set it up; what it cannot set
			// up is a finding.
			sr.Verdict = reconcile.VerdictOK
			if len(sr.Findings) > 0 {
				sr.Verdict = reconcile.VerdictReported
			}
			sr.Summary = join(builds, convergedCircleCI+webhookClause(cc.Webhook)+" ("+from+")")
		}
		return sr
	}
	builds := buildsSentence(slug, branch, cc.Head, ci, true)
	switch {
	case cc.Head != nil:
		sr.Verdict = reconcile.VerdictOK
		sr.Summary = builds
	case contains(cc.Unknown, inventory.CircleCIFactFollowed):
		sr.Verdict = reconcile.VerdictReported
		sr.Summary = fmt.Sprintf("the statuses on %s's head were truncated before a CircleCI one", branch)
		sr.Findings = []reconcile.Finding{finding(reconcile.FindingUnchecked,
			fmt.Sprintf("whether CircleCI builds %s is out of the inventory's reach: the head's status contexts were truncated before a CircleCI one", slug),
			alignFix)}
	default:
		// No CircleCI status on the head and no run: the project is not built,
		// the check-mode plan is the engine's for an unfollowed project.
		sr.Verdict = reconcile.VerdictDrift
		sr.Summary = builds
		sr.Changes = []string{"follow " + slug, "enable setup workflows", "create a deploy key"}
	}
	return sr
}

// webhookClause is the converged summary's word on CircleCI's webhook, as
// the engine words it; empty when no run tells.
func webhookClause(present *bool) string {
	switch {
	case present == nil:
		return ""
	case *present:
		return ", " + webhookPresent
	}
	return ", webhook missing"
}

// buildsSentence says what the head's `ci/circleci:` statuses tell; conclude
// draws the conclusion from a head without any — CircleCI does not build the
// repository — which holds only when nothing else has read the project.
// Statuses of jobs the declaration never runs on the branch (the tag
// pipeline's release jobs at a release commit, a bot's branch pipeline whose
// jobs skip the default branch) are another pipeline's and do not colour the
// branch's state.
func buildsSentence(slug, branch string, head *inventory.HeadStatus, ci *inventory.CI, conclude bool) string {
	if head == nil {
		if conclude {
			return fmt.Sprintf("no CircleCI status on %s's head: CircleCI does not build %s", branch, slug)
		}
		return fmt.Sprintf("no CircleCI status on %s's head yet", branch)
	}
	at := head.At.UTC().Format(time.RFC3339)
	own, foreign := splitStatuses(head, ci, func(j inventory.CIJob) bool { return j.RunsOnBranch(branch) })
	if len(foreign) == 0 {
		return fmt.Sprintf("CircleCI builds %s: %s (%s, %s)", branch, head.State, jobs(head), at)
	}
	failed, pending := statesOf(head, own)
	state := "success"
	switch {
	case len(failed) > 0:
		state = "failure"
	case len(pending) > 0:
		state = "pending"
	}
	return fmt.Sprintf("CircleCI builds %s: %s (%s, %s; %s of other pipelines at the head ignored)", branch, state, plural(len(own), "job"), at, plural(len(foreign), "status"))
}

func jobs(s *inventory.HeadStatus) string {
	if n := len(s.Contexts); n != 1 {
		return fmt.Sprintf("%d jobs", n)
	}
	return "1 job"
}

// releaseStep is the release step for tag. The release watch's state comes
// first when it followed the tag: it read the tag's own pipeline on
// CircleCI, which the commit's statuses cannot tell from a branch
// pipeline's at the same commit. Else the tag commit's `ci/circleci:`
// statuses say whether CircleCI built the release, read against the
// declaration (statusesReleaseStep) — except while none of the jobs only
// the tag pipeline runs has reported there (inventory.CI.TagOnly): a
// release cut on the default branch head carries that branch's pipeline's
// statuses too, and the reconciler run that named the tag unbuilt or red,
// having read the tag's own pipeline with its token, decides then. The run
// also stands in while the commit carries no status; a commit without any
// CircleCI status is the missed tag build the engine reports (finding
// missed-tag-build).
func releaseStep(slug, tag, branch string, rel *inventory.Release, ci *inventory.CI, last *inventory.LastRun, w *inventory.ReleaseWatch) reconcile.StepResult {
	sr := reconcile.StepResult{Step: reconcile.StepRelease}
	if w != nil && w.Tag == tag {
		if done, ok := watchedReleaseStep(slug, tag, w); ok {
			return done
		}
	}
	run := runStep(last, reconcile.StepRelease)
	if run != nil && !mentions(run, tag) {
		run = nil
	}
	if rel != nil && rel.Tag == tag && rel.Build != nil {
		tagOnly := ci.TagOnly(tag, branch)
		if run != nil && unbuilt(run) && !inventory.Reported(rel.Build.Contexts, tagOnly) {
			return runReleaseStep(run, last, tag)
		}
		return statusesReleaseStep(slug, tag, rel.Build, ci, tagOnly)
	}
	if run != nil {
		return runReleaseStep(run, last, tag)
	}
	switch {
	case rel == nil || rel.Tag != tag:
		latest := "none"
		if rel != nil {
			latest = rel.Tag
		}
		sr.Verdict = reconcile.VerdictReported
		sr.Summary = fmt.Sprintf("release %s: not the latest release the inventory read (%s)", tag, latest)
		sr.Findings = []reconcile.Finding{finding(reconcile.FindingUnchecked,
			fmt.Sprintf("whether CircleCI built the tag %s of %s is out of the inventory's reach until its next read of the repository", tag, slug),
			"refresh the repository, or "+alignFix)}
	case rel.BuildTruncated:
		sr.Verdict = reconcile.VerdictReported
		sr.Summary = fmt.Sprintf("release %s: the statuses on its commit were truncated before a CircleCI one", tag)
		sr.Findings = []reconcile.Finding{finding(reconcile.FindingUnchecked,
			fmt.Sprintf("whether CircleCI built the tag %s of %s is out of the inventory's reach: the commit's status contexts were truncated before a CircleCI one", tag, slug),
			alignFix)}
	default:
		sr.Verdict = reconcile.VerdictReported
		sr.Summary = fmt.Sprintf("no CircleCI status on %s's commit: the tag was not built", tag)
		sr.Findings = []reconcile.Finding{finding(reconcile.FindingMissedTagBuild,
			fmt.Sprintf("release %s of %s has no CircleCI status on its commit: nothing was built or published for the tag", tag, slug),
			"cut the next tag, or trigger the tag's pipeline by hand")}
	}
	return sr
}

// runReleaseStep is the reconciler run's release step for tag, dated.
func runReleaseStep(run *reconcile.StepResult, last *inventory.LastRun, tag string) reconcile.StepResult {
	from := " (reconciler run of " + last.Timestamp.UTC().Format(time.RFC3339) + ")"
	sr := reconcile.StepResult{Step: reconcile.StepRelease, Verdict: run.Verdict,
		Changes:  append([]string(nil), run.Changes...),
		Findings: append([]reconcile.Finding(nil), run.Findings...),
		Summary:  fmt.Sprintf("release %s%s", tag, from),
	}
	if run.Summary != "" {
		sr.Summary = run.Summary + from
	}
	return sr
}

// unbuilt says whether the run's release step found the tag not built: the
// step failed, or it reports the tag without a pipeline (missed-tag-build)
// or its pipeline red (red-release).
func unbuilt(run *reconcile.StepResult) bool {
	if run.Verdict == reconcile.VerdictFailed {
		return true
	}
	for _, f := range run.Findings {
		if f.Kind == reconcile.FindingMissedTagBuild || f.Kind == reconcile.FindingRedRelease {
			return true
		}
	}
	return false
}

// watchedReleaseStep is the release step from the release watch: built,
// red (the finding red-release naming the failed jobs, the fix the rerun
// from failed), unbuilt (the finding missed-tag-build), running (the
// pipeline named). An unchecked release, or one watched without a pipeline
// yet, is left to the other sources (ok false).
func watchedReleaseStep(slug, tag string, w *inventory.ReleaseWatch) (reconcile.StepResult, bool) {
	sr := reconcile.StepResult{Step: reconcile.StepRelease}
	at := w.CheckedAt.UTC().Format(time.RFC3339)
	confirm := fmt.Sprintf("then `devctl release wait %s %s` confirms it", slug, tag)
	switch w.State {
	case inventory.ReleaseBuilt:
		sr.Verdict = reconcile.VerdictOK
		sr.Summary = fmt.Sprintf("release %s built: the tag's pipeline %d succeeded (%s)", tag, w.Pipeline.Number, at)
	case inventory.ReleaseRed:
		sr.Verdict = reconcile.VerdictReported
		sr.Summary = fmt.Sprintf("release %s: the tag's pipeline %d failed (%s)", tag, w.Pipeline.Number, at)
		sr.Findings = []reconcile.Finding{finding(reconcile.FindingRedRelease,
			fmt.Sprintf("the tag pipeline %d of %s %s failed in %s: nothing was published for the tag", w.Pipeline.Number, slug, tag, strings.Join(w.FailedJobs, ", ")),
			"rerun the workflow from failed on CircleCI, "+confirm)}
	case inventory.ReleaseUnbuilt:
		sr.Verdict = reconcile.VerdictReported
		sr.Summary = fmt.Sprintf("release %s: no CircleCI pipeline for the tag (%s)", tag, at)
		sr.Findings = []reconcile.Finding{finding(reconcile.FindingMissedTagBuild,
			fmt.Sprintf("release %s of %s has no CircleCI pipeline: nothing was built or published for the tag", tag, slug),
			"trigger the tag's pipeline by hand on CircleCI, "+confirm)}
	case inventory.ReleaseWatching:
		if w.Pipeline == nil {
			return sr, false
		}
		sr.Verdict = reconcile.VerdictOK
		sr.Summary = fmt.Sprintf("release %s: the tag's pipeline %d is running (%s)", tag, w.Pipeline.Number, at)
	default:
		return sr, false
	}
	return sr, true
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
