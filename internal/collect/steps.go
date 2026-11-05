package collect

import (
	"fmt"
	"strings"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

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
// watch's state. The engine's release step reads the tag commit's statuses
// from the CircleCI GitHub App (devctl pkg/reposetup/reconcile,
// verifyRelease); the watch reads the tag's own pipeline, the finer answer,
// so for the tag the engine verified the watch's answer (watchedReleaseStep)
// decides — except a tag the watch settled unbuilt that the engine read
// since: a person may have run the tag on the GitHub App pipeline after the
// watch settled (`devctl repo follow-circleci`), and the engine's read is
// the newer. A tag published after the checks ran is the watch's alone: its
// answer, or waiting while it has no pipeline yet. The engine gives up on a
// tag after reconcile.TagBuildGrace, the watch only after its own longer
// grace, so a missed build the engine reports while the watch still waits
// for the pipeline is waiting too. A waiting step the watch can no longer
// answer (it settled the tag unchecked) says the tag is out of reach until
// the next check. A step the engine skipped for the repository (a flavour,
// a lifecycle, no pipeline) is not the watch's to answer; one it skipped
// naming the tag (built on an OAuth follow) is. A watch without an answer,
// or one about another tag published before the checks — an older release,
// read again after the checks or carried over by keepRelease — leaves the
// engine's step. Converged follows.
func rewriteReleaseStep(rec *inventory.Record) {
	w := rec.Setup.Release
	if w == nil || rec.Setup.Checks == nil || rec.Setup.CheckedAt == nil {
		return
	}
	sr := rec.Setup.Checks.Step(reconcile.StepRelease)
	if sr == nil || sr.Verdict == reconcile.VerdictSkipped && !strings.HasPrefix(sr.Summary, "release ") {
		return
	}
	checked := *rec.Setup.CheckedAt
	// ours is a waiting step written here, which names the tag it waits
	// for but is no read of the engine's.
	ours := strings.HasSuffix(sr.Summary, waitingForPipeline)
	named, cutSince := verifies(sr, w.Tag), w.CreatedAt.After(checked)
	sameTag := named && !ours
	if !named && !cutSince {
		return
	}
	done, ok := watchedReleaseStep(rec.Repository, w.Tag, w)
	switch {
	case !ok && w.State == inventory.ReleaseWatching && (ours || !sameTag || reports(sr, reconcile.FindingMissedTagBuild)):
		done = reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictOK,
			Summary: "release " + w.Tag + waitingForPipeline}
	case !ok && ours:
		done = reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictReported,
			Summary: fmt.Sprintf("release %s: not checked (%s)", w.Tag, w.Reason),
			Findings: []reconcile.Finding{finding(reconcile.FindingUnchecked,
				fmt.Sprintf("whether CircleCI built the tag %s of %s is out of the inventory's reach until its next check of the repository: %s", w.Tag, rec.Repository, w.Reason),
				"refresh the repository")}}
	case !ok:
		return
	case sameTag && w.State == inventory.ReleaseUnbuilt && !w.CheckedAt.After(checked):
		return
	}
	*sr = done
	converge(rec.Setup.Checks)
}

// waitingForPipeline ends the release step written while the watch waits
// for a tag's pipeline; the engine never words a step so.
const waitingForPipeline = ": waiting for the tag's pipeline"

// reports says whether the step carries a finding of kind.
func reports(sr *reconcile.StepResult, kind reconcile.FindingKind) bool {
	for _, f := range sr.Findings {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

// verifies says whether the engine's release step is about tag: its
// summary names the release as the engine words it, "release <tag>: …" or
// "release <tag> built: …".
func verifies(sr *reconcile.StepResult, tag string) bool {
	return tag != "" && (strings.Contains(sr.Summary, "release "+tag+":") || strings.Contains(sr.Summary, "release "+tag+" "))
}

// finding is a finding of kind with its message and fix, advisory as the
// engine says of the kind, so a step written here reads like the engine's.
func finding(kind reconcile.FindingKind, message, fix string) reconcile.Finding {
	return reconcile.Finding{Kind: kind, Message: message, Fix: fix, Advisory: kind.Advisory()}
}

// watchedReleaseStep is the release step from the release watch: built,
// red (the finding red-release naming the failed jobs, the fix the rerun
// from failed), unbuilt (the finding missed-tag-build, the fix the
// person's `devctl repo follow-circleci`, which runs the tag on the GitHub
// App pipeline as them), running (the pipeline named). An unchecked
// release, or one watched without a pipeline yet, has no answer (ok false).
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
			fmt.Sprintf("`devctl repo follow-circleci %s` as a member of the owning team: it runs the tag on the GitHub App pipeline with your CircleCI login, creating the pipeline first when there is none; %s", slug, confirm))}
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
