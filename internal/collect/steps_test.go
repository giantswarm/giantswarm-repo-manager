package collect

import (
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// TestFillClientlessSteps: the engine's circleci and release steps, skipped
// for want of a CircleCI client, are written from the record — the head's
// statuses and the reconciler's last run — the way the reconciler's run
// would report them; every other step, and a step skipped for another
// reason, stays as the engine left it; converged follows.
func TestFillClientlessSteps(t *testing.T) {
	const (
		slug       = "giantswarm/x"
		success    = "success"
		buildsMain = "CircleCI builds main"
		createKey  = "create a deploy key"
		enableSW   = "enable setup workflows"
		triggerTag = "trigger the missed tag build for v1.2.0"
	)
	ran := time.Date(2026, 9, 17, 22, 17, 0, 0, time.UTC)
	built := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	head := &inventory.HeadStatus{State: success, Contexts: []string{"ci/circleci: go-build", "ci/circleci: push"}, At: built}
	yes := true
	engine := func(steps ...reconcile.StepResult) *reconcile.Result {
		res := &reconcile.Result{Repository: slug, Declared: slug, Mode: reconcile.ModeCheck, Converged: true}
		res.Steps = append([]reconcile.StepResult{{Step: reconcile.StepSettings, Verdict: reconcile.VerdictOK, Summary: "settings match"}}, steps...)
		return res
	}
	skipCircle := reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictSkipped, Summary: noCircleCIClient}
	skipRelease := reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictSkipped, Summary: "release v1.2.0: no CircleCI client to verify the pipeline"}
	run := func(mode reconcile.Mode, steps ...reconcile.StepResult) *inventory.LastRun {
		return &inventory.LastRun{Timestamp: ran, RunURL: "https://example.test/run/1", Result: reconcile.Result{Mode: mode, Steps: steps}}
	}
	record := func(cc *inventory.CircleCI, last *inventory.LastRun) *inventory.Record {
		return &inventory.Record{Repository: slug, Name: "x", Reality: &inventory.Reality{DefaultBranch: "main"}, CircleCI: cc, Setup: inventory.Setup{LastRun: last}}
	}
	statuses := &inventory.CircleCI{Followed: true, Head: head, Source: inventory.CircleCISourceStatuses, Unknown: []string{inventory.CircleCIFactSetupWorkflows}}
	none := &inventory.CircleCI{Source: inventory.CircleCISourceStatuses, Unknown: []string{inventory.CircleCIFactSetupWorkflows}}
	truncated := &inventory.CircleCI{Source: inventory.CircleCISourceStatuses, Unknown: []string{inventory.CircleCIFactSetupWorkflows, inventory.CircleCIFactFollowed}}
	both := &inventory.CircleCI{Followed: true, SetupWorkflows: &yes, Head: head, Source: inventory.CircleCISourceBoth}

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
		{"circleci: statuses alone — built, settings unchecked", engine(skipCircle), record(statuses, nil), reconcile.StepCircleCI,
			want{reconcile.VerdictReported, []string{"CircleCI builds main: success (2 jobs, 2026-09-18T08:00:00Z)"}, nil, []reconcile.FindingKind{reconcile.FindingUnchecked}, true}, false},
		{"circleci: no statuses, no run — not built, the engine's plan", engine(skipCircle), record(none, nil), reconcile.StepCircleCI,
			want{reconcile.VerdictDrift, []string{"no CircleCI status on main's head", "does not build giantswarm/x"}, []string{"follow giantswarm/x", enableSW, createKey}, nil, false}, false},
		{"circleci: statuses truncated, no run — followed unknown", engine(skipCircle), record(truncated, nil), reconcile.StepCircleCI,
			want{reconcile.VerdictReported, []string{"truncated"}, nil, []reconcile.FindingKind{reconcile.FindingUnchecked}, true}, false},
		{"circleci: the reconciler's run converged", engine(skipCircle), record(both, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictOK, Summary: convergedCircleCI})), reconcile.StepCircleCI,
			want{reconcile.VerdictOK, []string{convergedCircleCI, "reconciler run of 2026-09-17T22:17:00Z", buildsMain}, nil, nil, true}, false},
		{"circleci: the reconciler's run repaired — the state holds", engine(skipCircle), record(both, run(reconcile.ModeRepair, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictRepaired, Changes: []string{enableSW}})), reconcile.StepCircleCI,
			want{reconcile.VerdictOK, []string{convergedCircleCI}, nil, nil, true}, false},
		{"circleci: the reconciler's check found drift — its plan", engine(skipCircle), record(statuses, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictDrift, Changes: []string{enableSW, createKey}})), reconcile.StepCircleCI,
			want{reconcile.VerdictDrift, []string{"drift found by the reconciler run of 2026-09-17T22:17:00Z", buildsMain}, []string{enableSW, createKey}, nil, false}, false},
		{"circleci: the reconciler's step failed", engine(skipCircle), record(statuses, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictFailed, Summary: "api error: GET /api/v2/project/gh/giantswarm/x/settings: 502"})), reconcile.StepCircleCI,
			want{reconcile.VerdictFailed, []string{"the reconciler's circleci step failed: api error", buildsMain}, nil, nil, false}, false},
		{"circleci: a run that skipped the step is no run", engine(skipCircle), record(statuses, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictSkipped, Summary: noCircleCIClient})), reconcile.StepCircleCI,
			want{reconcile.VerdictReported, []string{buildsMain}, nil, []reconcile.FindingKind{reconcile.FindingUnchecked}, true}, false},
		{"release: no run — unchecked, the tag named", engine(skipRelease), record(statuses, nil), reconcile.StepRelease,
			want{reconcile.VerdictReported, []string{"release v1.2.0: not verified by a reconciler run yet"}, nil, []reconcile.FindingKind{reconcile.FindingUnchecked}, true}, false},
		{"release: the run built this tag", engine(skipRelease), record(statuses, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictOK, Summary: "release v1.2.0 built: pipeline 991, workflows build"})), reconcile.StepRelease,
			want{reconcile.VerdictOK, []string{"release v1.2.0 built: pipeline 991", "reconciler run of 2026-09-17T22:17:00Z"}, nil, nil, true}, false},
		{"release: the run planned the missed build of this tag", engine(skipRelease), record(statuses, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictDrift, Changes: []string{triggerTag}})), reconcile.StepRelease,
			want{reconcile.VerdictDrift, []string{"release v1.2.0 (reconciler run of"}, []string{triggerTag}, nil, false}, false},
		{"release: the run triggered the missed build — ok until the next run", engine(skipRelease), record(statuses, run(reconcile.ModeRepair, reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictRepaired, Changes: []string{triggerTag}})), reconcile.StepRelease,
			want{reconcile.VerdictOK, []string{"release v1.2.0: build triggered by the reconciler"}, []string{triggerTag}, nil, true}, false},
		{"release: the run reported this tag red", engine(skipRelease), record(statuses, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictReported, Findings: []reconcile.Finding{{Kind: reconcile.FindingRedRelease, Message: "the tag pipeline of v1.2.0 failed", Fix: "the next tag"}}})), reconcile.StepRelease,
			want{reconcile.VerdictReported, []string{"release v1.2.0 (reconciler run of"}, nil, []reconcile.FindingKind{reconcile.FindingRedRelease}, true}, false},
		{"release: the run verified an older tag — this one is unchecked", engine(skipRelease), record(statuses, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictOK, Summary: "release v1.1.0 built: pipeline 900, workflows build"})), reconcile.StepRelease,
			want{reconcile.VerdictReported, []string{"release v1.2.0: not verified"}, nil, []reconcile.FindingKind{reconcile.FindingUnchecked}, true}, false},
		{"a step skipped for another reason stays", engine(reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictSkipped, Summary: "lifecycle: archived"}), record(statuses, nil), reconcile.StepCircleCI,
			want{reconcile.VerdictSkipped, []string{"lifecycle: archived"}, nil, nil, true}, true},
		{"a step the engine checked stays", engine(reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictOK, Summary: convergedCircleCI}), record(none, nil), reconcile.StepCircleCI,
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
