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
	stateSuccess = "success"
	releaseJob   = "ci/circleci: push-to-registries-release"
)

// TestFillClientlessSteps: the engine's circleci and release steps, skipped
// for want of a CircleCI client, are written from the record — the head's
// statuses, the tag commit's statuses and the reconciler's last run — as the
// answers to "does CircleCI build it" and "did the release build"; every
// other step, and a step skipped for another reason, stays as the engine
// left it; converged follows.
func TestFillClientlessSteps(t *testing.T) {
	const (
		slug       = "giantswarm/x"
		tag        = "v1.2.0"
		buildsMain = "CircleCI builds main"
		createKey  = "create a deploy key"
		enableSW   = "enable setup workflows"
		triggerTag = "trigger the missed tag build for v1.2.0"
	)
	ran := time.Date(2026, 9, 17, 22, 17, 0, 0, time.UTC)
	built := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	head := &inventory.HeadStatus{State: stateSuccess, Contexts: []string{"ci/circleci: go-build", "ci/circleci: push"}, At: built}
	yes := true
	engine := func(steps ...reconcile.StepResult) *reconcile.Result {
		res := &reconcile.Result{Repository: slug, Declared: slug, Mode: reconcile.ModeCheck, Converged: true}
		res.Steps = append([]reconcile.StepResult{{Step: reconcile.StepSettings, Verdict: reconcile.VerdictOK, Summary: "settings match"}}, steps...)
		return res
	}
	skipCircle := reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictSkipped, Summary: noCircleCIClient}
	skipRelease := reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictSkipped, Summary: "release " + tag + ": no CircleCI client to verify the pipeline"}
	run := func(mode reconcile.Mode, steps ...reconcile.StepResult) *inventory.LastRun {
		return &inventory.LastRun{Timestamp: ran, RunURL: "https://example.test/run/1", Result: reconcile.Result{Mode: mode, Steps: steps}}
	}
	release := func(t string, build *inventory.HeadStatus, truncated bool) *inventory.Release {
		return &inventory.Release{Tag: t, PublishedAt: built.Add(-time.Hour), Build: build, BuildTruncated: truncated}
	}
	record := func(cc *inventory.CircleCI, rel *inventory.Release, last *inventory.LastRun) *inventory.Record {
		return &inventory.Record{Repository: slug, Name: "x", Reality: &inventory.Reality{DefaultBranch: "main", LatestRelease: rel}, CircleCI: cc, Setup: inventory.Setup{LastRun: last}}
	}
	statuses := &inventory.CircleCI{Followed: true, Head: head, Source: inventory.CircleCISourceStatuses, Unknown: []string{inventory.CircleCIFactSetupWorkflows}}
	none := &inventory.CircleCI{Source: inventory.CircleCISourceStatuses, Unknown: []string{inventory.CircleCIFactSetupWorkflows}}
	truncated := &inventory.CircleCI{Source: inventory.CircleCISourceStatuses, Unknown: []string{inventory.CircleCIFactSetupWorkflows, inventory.CircleCIFactFollowed}}
	both := &inventory.CircleCI{Followed: true, SetupWorkflows: &yes, Head: head, Source: inventory.CircleCISourceBoth}
	green := &inventory.HeadStatus{State: stateSuccess, Contexts: []string{releaseJob}, At: built}
	red := &inventory.HeadStatus{State: stateFailure, Contexts: []string{releaseJob}, At: built}
	running := &inventory.HeadStatus{State: "pending", Contexts: []string{"ci/circleci: go-build", releaseJob}, At: built}

	type want struct {
		verdict   reconcile.Verdict
		summary   []string // substrings
		changes   []string
		findings  []reconcile.FindingKind
		converged bool
	}
	cases := []struct {
		name    string
		res     *reconcile.Result
		rec     *inventory.Record
		step    reconcile.Step
		want    want
		untouch bool
	}{
		{"circleci: the head builds — ok, no finding about settings", engine(skipCircle), record(statuses, nil, nil), reconcile.StepCircleCI,
			want{reconcile.VerdictOK, []string{"CircleCI builds main: success (2 jobs, 2026-09-18T08:00:00Z)"}, nil, nil, true}, false},
		{"circleci: no statuses, no run — not built, the engine's plan", engine(skipCircle), record(none, nil, nil), reconcile.StepCircleCI,
			want{reconcile.VerdictDrift, []string{"no CircleCI status on main's head", "does not build giantswarm/x"}, []string{"follow giantswarm/x", enableSW, createKey}, nil, false}, false},
		{"circleci: statuses truncated, no run — followed unknown", engine(skipCircle), record(truncated, nil, nil), reconcile.StepCircleCI,
			want{reconcile.VerdictReported, []string{"truncated"}, nil, []reconcile.FindingKind{reconcile.FindingUnchecked}, false}, false},
		{"circleci: the reconciler's run converged", engine(skipCircle), record(both, nil, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictOK, Summary: convergedCircleCI})), reconcile.StepCircleCI,
			want{reconcile.VerdictOK, []string{buildsMain, convergedCircleCI, "reconciler run of 2026-09-17T22:17:00Z"}, nil, nil, true}, false},
		{"circleci: the run converged, the head not built yet — no 'does not build' beside an ok", engine(skipCircle), record(none, nil, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictOK, Summary: convergedCircleCI})), reconcile.StepCircleCI,
			want{reconcile.VerdictOK, []string{"no CircleCI status on main's head yet", convergedCircleCI}, nil, nil, true}, false},
		{"circleci: the reconciler's run repaired — the state holds", engine(skipCircle), record(both, nil, run(reconcile.ModeRepair, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictRepaired, Changes: []string{enableSW}})), reconcile.StepCircleCI,
			want{reconcile.VerdictOK, []string{convergedCircleCI}, nil, nil, true}, false},
		{"circleci: the reconciler's check found drift — its plan", engine(skipCircle), record(statuses, nil, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictDrift, Changes: []string{enableSW, createKey}})), reconcile.StepCircleCI,
			want{reconcile.VerdictDrift, []string{buildsMain, "drift found by the reconciler run of 2026-09-17T22:17:00Z"}, []string{enableSW, createKey}, nil, false}, false},
		{"circleci: the reconciler's step failed", engine(skipCircle), record(statuses, nil, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictFailed, Summary: "api error: GET /api/v2/project/gh/giantswarm/x/settings: 502"})), reconcile.StepCircleCI,
			want{reconcile.VerdictFailed, []string{"the reconciler's circleci step failed: api error", buildsMain}, nil, nil, false}, false},
		{"circleci: a run that skipped the step is no run", engine(skipCircle), record(statuses, nil, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictSkipped, Summary: noCircleCIClient})), reconcile.StepCircleCI,
			want{reconcile.VerdictOK, []string{buildsMain}, nil, nil, true}, false},
		{"release: the tag commit built green", engine(skipRelease), record(statuses, release(tag, green, false), nil), reconcile.StepRelease,
			want{reconcile.VerdictOK, []string{"release v1.2.0 built: CircleCI success (1 job, 2026-09-18T08:00:00Z)"}, nil, nil, true}, false},
		{"release: the tag commit's pipeline is red", engine(skipRelease), record(statuses, release(tag, red, false), nil), reconcile.StepRelease,
			want{reconcile.VerdictReported, []string{"release v1.2.0: CircleCI failure"}, nil, []reconcile.FindingKind{reconcile.FindingRedRelease}, false}, false},
		{"release: the tag commit's pipeline is running", engine(skipRelease), record(statuses, release(tag, running, false), nil), reconcile.StepRelease,
			want{reconcile.VerdictOK, []string{"release v1.2.0: CircleCI pipeline running (2 jobs"}, nil, nil, true}, false},
		{"release: no status on the tag commit, no run — the missed build", engine(skipRelease), record(statuses, release(tag, nil, false), nil), reconcile.StepRelease,
			want{reconcile.VerdictReported, []string{"no CircleCI status on v1.2.0's commit: the tag was not built"}, nil, []reconcile.FindingKind{reconcile.FindingMissedTagBuild}, false}, false},
		{"release: no status yet, the run built this tag", engine(skipRelease), record(statuses, release(tag, nil, false), run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictOK, Summary: "release v1.2.0 built: pipeline 991, workflows build"})), reconcile.StepRelease,
			want{reconcile.VerdictOK, []string{"release v1.2.0 built: pipeline 991", "reconciler run of 2026-09-17T22:17:00Z"}, nil, nil, true}, false},
		{"release: the statuses win over an older run's verdict", engine(skipRelease), record(statuses, release(tag, green, false), run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictDrift, Changes: []string{triggerTag}})), reconcile.StepRelease,
			want{reconcile.VerdictOK, []string{"release v1.2.0 built: CircleCI success"}, nil, nil, true}, false},
		{"release: the run verified an older tag, no status — the missed build", engine(skipRelease), record(statuses, release(tag, nil, false), run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictOK, Summary: "release v1.1.0 built: pipeline 900, workflows build"})), reconcile.StepRelease,
			want{reconcile.VerdictReported, []string{"the tag was not built"}, nil, []reconcile.FindingKind{reconcile.FindingMissedTagBuild}, false}, false},
		{"release: the tag commit's statuses were truncated", engine(skipRelease), record(statuses, release(tag, nil, true), nil), reconcile.StepRelease,
			want{reconcile.VerdictReported, []string{"truncated"}, nil, []reconcile.FindingKind{reconcile.FindingUnchecked}, false}, false},
		{"release: the inventory read another latest release", engine(skipRelease), record(statuses, release("v1.3.0", green, false), nil), reconcile.StepRelease,
			want{reconcile.VerdictReported, []string{"release v1.2.0: not the latest release the inventory read (v1.3.0)"}, nil, []reconcile.FindingKind{reconcile.FindingUnchecked}, false}, false},
		{"a step skipped for another reason stays", engine(reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictSkipped, Summary: "lifecycle: archived"}), record(statuses, nil, nil), reconcile.StepCircleCI,
			want{reconcile.VerdictSkipped, []string{"lifecycle: archived"}, nil, nil, true}, true},
		{"a step the engine checked stays", engine(reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictOK, Summary: convergedCircleCI}), record(none, nil, nil), reconcile.StepCircleCI,
			want{reconcile.VerdictOK, []string{convergedCircleCI}, nil, nil, true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := *tc.res.Step(tc.step)
			fillClientlessSteps(tc.res, tc.rec)
			got := tc.res.Step(tc.step)
			if tc.untouch && (got.Verdict != before.Verdict || got.Summary != before.Summary) {
				t.Fatalf("step changed: %+v -> %+v", before, *got)
			}
			if got.Verdict != tc.want.verdict {
				t.Errorf("verdict %s, want %s (%+v)", got.Verdict, tc.want.verdict, *got)
			}
			for _, s := range tc.want.summary {
				if !strings.Contains(got.Summary, s) {
					t.Errorf("summary %q lacks %q", got.Summary, s)
				}
			}
			if strings.Join(got.Changes, "|") != strings.Join(tc.want.changes, "|") {
				t.Errorf("changes %v, want %v", got.Changes, tc.want.changes)
			}
			var kinds []reconcile.FindingKind
			for _, f := range got.Findings {
				kinds = append(kinds, f.Kind)
				if f.Message == "" || f.Fix == "" {
					t.Errorf("finding without message or fix: %+v", f)
				}
			}
			if len(kinds) != len(tc.want.findings) {
				t.Errorf("findings %v, want %v", kinds, tc.want.findings)
			} else {
				for i := range kinds {
					if kinds[i] != tc.want.findings[i] {
						t.Errorf("finding %d %s, want %s", i, kinds[i], tc.want.findings[i])
					}
				}
			}
			if tc.res.Converged != tc.want.converged {
				t.Errorf("converged %v, want %v", tc.res.Converged, tc.want.converged)
			}
			if s := tc.res.Step(reconcile.StepSettings); s.Verdict != reconcile.VerdictOK || s.Summary != "settings match" {
				t.Errorf("another step changed: %+v", *s)
			}
		})
	}
}

// TestFillClientlessStepsWithoutFacts: a gone repository or a result
// without the two steps leaves the result as it is.
func TestFillClientlessStepsWithoutFacts(t *testing.T) {
	res := &reconcile.Result{Converged: true, Steps: []reconcile.StepResult{{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictSkipped, Summary: noCircleCIClient}}}
	fillClientlessSteps(res, &inventory.Record{Repository: "giantswarm/gone"})
	if s := res.Step(reconcile.StepCircleCI); s.Verdict != reconcile.VerdictSkipped || !res.Converged {
		t.Errorf("gone repository: %+v converged %v", *s, res.Converged)
	}
	fillClientlessSteps(nil, nil)
	only := &reconcile.Result{Converged: false, Steps: []reconcile.StepResult{{Step: reconcile.StepSettings, Verdict: reconcile.VerdictDrift}}}
	fillClientlessSteps(only, &inventory.Record{Repository: "giantswarm/x", Reality: &inventory.Reality{}})
	if only.Converged || len(only.Steps) != 1 {
		t.Errorf("result without the steps changed: %+v", only)
	}
}

// TestFillClientlessStepsAdvisory: converged is the engine's rule — an
// advisory finding on a reported step leaves it, any other finding clears it.
func TestFillClientlessStepsAdvisory(t *testing.T) {
	head := &inventory.HeadStatus{State: stateSuccess, Contexts: []string{"ci/circleci: build"}, At: time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)}
	rec := &inventory.Record{Repository: "giantswarm/y", Name: "y", Reality: &inventory.Reality{DefaultBranch: "trunk"},
		CircleCI: &inventory.CircleCI{Followed: true, Head: head, Source: inventory.CircleCISourceStatuses}}
	for _, tc := range []struct {
		name      string
		finding   reconcile.Finding
		converged bool
	}{
		{"advisory", reconcile.Finding{Kind: reconcile.FindingDefaultIcon, Message: "the icon is GitHub's default", Fix: "upload one", Advisory: true}, true},
		{"for a person to fix", reconcile.Finding{Kind: reconcile.FindingRenovateNotScanned, Message: "Renovate has not scanned", Fix: "enable it"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := &reconcile.Result{Converged: !tc.converged, Steps: []reconcile.StepResult{
				{Step: reconcile.StepSettings, Verdict: reconcile.VerdictReported, Findings: []reconcile.Finding{tc.finding}},
				{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictSkipped, Summary: noCircleCIClient},
			}}
			fillClientlessSteps(res, rec)
			if s := res.Step(reconcile.StepCircleCI); s.Verdict != reconcile.VerdictOK {
				t.Errorf("circleci step %+v, want ok", *s)
			}
			if res.Converged != tc.converged {
				t.Errorf("converged %v, want %v", res.Converged, tc.converged)
			}
		})
	}
}
