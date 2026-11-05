package collect

import (
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// Test constants of this package's CircleCI derivations.
const (
	stateSuccess   = "success"
	releaseJobName = "push-to-registries-release"
	releaseJob     = "ci/circleci: " + releaseJobName
	setupJob       = "setup"
	goBuildJob     = "go-build"
	chartTestsJob  = "execute-chart-tests"
	amd64Leg       = "build-image-amd64"
	mainBranch     = "main"
)

// TestRewriteReleaseStep: the watch's answer decides the tag the engine
// verified; a tag the watch settled unbuilt that the engine read since
// keeps the engine's step; a tag published after the checks is the watch's,
// waiting while it has no pipeline; a watch without an answer, or about
// another tag published before the checks — even one read after them —
// leaves the engine's step. Converged follows.
func TestRewriteReleaseStep(t *testing.T) {
	const slug, tag = "giantswarm/x", "v1.2.0"
	checked := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	after, before := checked.Add(time.Minute), checked.Add(-time.Minute)
	published := checked.Add(-time.Hour)
	pipe := &inventory.ReleasePipeline{Number: 11508}
	missed := reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictReported, Summary: "release v1.2.0: no tag build",
		Findings: []reconcile.Finding{{Kind: reconcile.FindingMissedTagBuild, Message: "release v1.2.0 of giantswarm/x carries no CircleCI status"}}}
	built := reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictOK, Summary: "release v1.2.0 built: push-to-registries-release succeeded"}
	older := reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictOK, Summary: "release v1.1.0 built: push-to-registries-release succeeded"}
	newer := reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictOK, Summary: "release v1.3.0 built: push-to-registries-release succeeded"}
	cases := []struct {
		name      string
		engine    reconcile.StepResult
		w         *inventory.ReleaseWatch
		summary   string
		converged bool
	}{
		{"built, the same tag: the watch decides", missed, &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseBuilt, Pipeline: pipe, CheckedAt: before}, "release v1.2.0 built: the tag's pipeline 11508 succeeded", true},
		{"red, the same tag, checked since: the watch decides", built, &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseRed, Pipeline: pipe, FailedJobs: []string{"a"}, CheckedAt: before}, "the tag's pipeline 11508 failed", false},
		{"running after the checks: the watch decides", missed, &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseWatching, Pipeline: pipe, CheckedAt: after}, "the tag's pipeline 11508 is running", true},
		{"unbuilt, checked since: the engine's step stands", built, &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseUnbuilt, CheckedAt: before}, built.Summary, true},
		{"unbuilt after the checks: the watch decides", missed, &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseUnbuilt, CheckedAt: after}, "no CircleCI pipeline for the tag", false},
		{"a tag published after the checks, no pipeline yet: waiting", older, &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseWatching, CreatedAt: after, CheckedAt: after}, "release v1.2.0: waiting for the tag's pipeline", true},
		{"a tag published after the checks, built: the watch decides", older, &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseBuilt, Pipeline: pipe, CreatedAt: after, CheckedAt: after}, "release v1.2.0 built: the tag's pipeline", true},
		{"the watch's tag older than the engine's: the engine's step stands", newer, &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseBuilt, Pipeline: pipe, CreatedAt: published, CheckedAt: before}, newer.Summary, true},
		{"the watch's older tag read after the checks: the engine's missed build stands", reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictReported, Summary: "release v1.3.0: no tag build",
			Findings: []reconcile.Finding{{Kind: reconcile.FindingMissedTagBuild, Message: "release v1.3.0 carries no CircleCI status"}}},
			&inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseBuilt, Pipeline: pipe, CreatedAt: published, CheckedAt: after}, "release v1.3.0: no tag build", false},
		{"the watch's older red tag re-read after the checks: the engine's built step stands", newer,
			&inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseRed, Pipeline: pipe, FailedJobs: []string{"a"}, CreatedAt: published, CheckedAt: after}, newer.Summary, true},
		{"unchecked: the engine's step stands", missed, &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseUnchecked, CheckedAt: after}, missed.Summary, false},
		{"watching without a pipeline, the same tag: the engine's step stands", missed, &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseWatching, CheckedAt: after}, missed.Summary, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := checked
			rec := &inventory.Record{Repository: slug, Name: "x", Declaration: &inventory.Declaration{Team: teamBumblebee}, Reality: &inventory.Reality{},
				Setup: inventory.Setup{Release: tc.w, CheckedAt: &at, Checks: &reconcile.Result{Steps: []reconcile.StepResult{
					{Step: reconcile.StepSettings, Verdict: reconcile.VerdictOK}, tc.engine,
				}}}}
			rec.Setup.Checks.Converged = tc.engine.Converges()
			rewriteReleaseStep(rec)
			sr := rec.Setup.Checks.Step(reconcile.StepRelease)
			if !strings.Contains(sr.Summary, tc.summary) || rec.Setup.Checks.Converged != tc.converged {
				t.Errorf("step=%+v converged=%v, want a summary containing %q, converged %v", sr, rec.Setup.Checks.Converged, tc.summary, tc.converged)
			}
		})
	}
}
