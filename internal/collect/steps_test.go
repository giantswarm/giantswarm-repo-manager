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
	amd64Leg       = "build-image-amd64"
	mainBranch     = "main"
	vTags          = "/^v.*/"
	// noBranch is the branches filter of a job that runs on tags alone.
	noBranch = "/.*/"
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
	yes, no := true, false
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
		return &inventory.Record{Repository: slug, Name: "x", Reality: &inventory.Reality{DefaultBranch: mainBranch, LatestRelease: rel}, CircleCI: cc, Setup: inventory.Setup{LastRun: last}}
	}
	statuses := &inventory.CircleCI{Followed: true, Head: head, Source: inventory.CircleCISourceStatuses, Unknown: []string{inventory.CircleCIFactSetupWorkflows}}
	none := &inventory.CircleCI{Source: inventory.CircleCISourceStatuses, Unknown: []string{inventory.CircleCIFactSetupWorkflows}}
	truncated := &inventory.CircleCI{Source: inventory.CircleCISourceStatuses, Unknown: []string{inventory.CircleCIFactSetupWorkflows, inventory.CircleCIFactFollowed}}
	both := &inventory.CircleCI{Followed: true, SetupWorkflows: &yes, Head: head, Source: inventory.CircleCISourceBoth}
	present := &inventory.CircleCI{Followed: true, SetupWorkflows: &yes, Webhook: &yes, Head: head, Source: inventory.CircleCISourceBoth}
	deaf := &inventory.CircleCI{Followed: true, SetupWorkflows: &yes, Webhook: &no, Head: head, Source: inventory.CircleCISourceBoth}
	webhookMissing := finding(reconcile.FindingCircleCIWebhookMissing, "giantswarm/x is followed on CircleCI but carries no active CircleCI webhook",
		"follow the project as a GitHub admin of the repository whose CircleCI grant carries the hook scope")
	missing := []reconcile.FindingKind{reconcile.FindingCircleCIWebhookMissing}
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
			want{reconcile.VerdictDrift, []string{"no CircleCI status on main's head", "does not build giantswarm/x"}, []string{followX, enableSW, createKey}, nil, false}, false},
		{"circleci: statuses truncated, no run — followed unknown", engine(skipCircle), record(truncated, nil, nil), reconcile.StepCircleCI,
			want{reconcile.VerdictReported, []string{"truncated"}, nil, []reconcile.FindingKind{reconcile.FindingUnchecked}, false}, false},
		{"circleci: the reconciler's run converged", engine(skipCircle), record(both, nil, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictOK, Summary: convergedCircleCI})), reconcile.StepCircleCI,
			want{reconcile.VerdictOK, []string{buildsMain, convergedCircleCI, "reconciler run of 2026-09-17T22:17:00Z"}, nil, nil, true}, false},
		{"circleci: the run converged, the head not built yet — no 'does not build' beside an ok", engine(skipCircle), record(none, nil, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictOK, Summary: convergedCircleCI})), reconcile.StepCircleCI,
			want{reconcile.VerdictOK, []string{"no CircleCI status on main's head yet", convergedCircleCI}, nil, nil, true}, false},
		{"circleci: the reconciler's run repaired — the state holds", engine(skipCircle), record(both, nil, run(reconcile.ModeRepair, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictRepaired, Changes: []string{enableSW}})), reconcile.StepCircleCI,
			want{reconcile.VerdictOK, []string{convergedCircleCI}, nil, nil, true}, false},
		{"circleci: the run converged, the webhook present", engine(skipCircle), record(present, nil, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictOK, Summary: convergedCircleCI + ", " + webhookPresent})), reconcile.StepCircleCI,
			want{reconcile.VerdictOK, []string{buildsMain, convergedCircleCI + ", webhook present (reconciler run of 2026-09-17T22:17:00Z)"}, nil, nil, true}, false},
		{"circleci: the run reported the webhook missing — not converged", engine(skipCircle), record(deaf, nil, run(reconcile.ModeRepair, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictReported, Summary: convergedCircleCI + ", webhook missing", Findings: []reconcile.Finding{webhookMissing}})), reconcile.StepCircleCI,
			want{reconcile.VerdictReported, []string{convergedCircleCI + ", webhook missing (reconciler run of"}, nil, missing, false}, false},
		{"circleci: a repair that followed and left the project deaf reports it", engine(skipCircle), record(deaf, nil, run(reconcile.ModeRepair, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictRepaired, Changes: []string{followX}, Findings: []reconcile.Finding{webhookMissing}})), reconcile.StepCircleCI,
			want{reconcile.VerdictReported, []string{"webhook missing"}, nil, missing, false}, false},
		{"circleci: a check's drift keeps the webhook finding", engine(skipCircle), record(deaf, nil, run(reconcile.ModeCheck, reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictDrift, Changes: []string{enableSW}, Findings: []reconcile.Finding{webhookMissing}})), reconcile.StepCircleCI,
			want{reconcile.VerdictDrift, []string{"drift found by the reconciler run"}, []string{enableSW}, missing, false}, false},
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

// TestStatusesReleaseStep: the release step from a tag commit's statuses
// against the declaration — a branch pipeline's statuses at the release
// commit are ignored (backstage v2.58.8), a failed tag-only job is the tag
// pipeline's failure, a failed job that runs on branches too cannot be
// attributed beside a branch pipeline's statuses and reads unchecked
// (tunnelport v1.6.7), and without a branch pipeline's statuses every
// failure is the tag pipeline's; a record of the earlier shape, without the
// failed and pending lists, reads as it did.
func TestStatusesReleaseStep(t *testing.T) {
	const slug, tag = "giantswarm/x", "v2.58.8"
	at := time.Date(2026, 9, 23, 8, 13, 0, 0, time.UTC)
	tagOnly := func(n string) inventory.CIJob {
		return inventory.CIJob{Name: n, TagsOnly: []string{vTags}, BranchesIgnore: []string{noBranch}}
	}
	shared := func(n string) inventory.CIJob { return inventory.CIJob{Name: n, TagsOnly: []string{vTags}} }
	branchOnly := func(n string) inventory.CIJob { return inventory.CIJob{Name: n, BranchesIgnore: []string{mainBranch}} }
	ci := &inventory.CI{Jobs: []inventory.CIJob{
		shared("node-build"), shared("build-chart"), shared(setupJob),
		branchOnly(amd64Leg), branchOnly("build-image-arm64"), branchOnly("push-to-registries"), branchOnly("execute-chart-tests"), branchOnly("push-chart"),
		tagOnly("build-image-release-amd64"), tagOnly("build-image-release-arm64"), tagOnly(releaseJobName), tagOnly("sync-china-registry"), tagOnly("push-chart-release"),
	}}
	ctx := func(jobs ...string) []string {
		out := make([]string, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, "ci/circleci: "+j)
		}
		return out
	}
	tagPipeline := ctx("build-chart", "build-image-release-amd64", "build-image-release-arm64", "node-build", "push-chart-release", releaseJobName, setupJob, "sync-china-registry")
	branchLegs := ctx(amd64Leg, "build-image-arm64")
	status := func(state string, contexts, failed, pending []string) *inventory.HeadStatus {
		return &inventory.HeadStatus{State: state, Contexts: contexts, Failed: failed, Pending: pending, At: at}
	}
	cases := []struct {
		name     string
		build    *inventory.HeadStatus
		ci       *inventory.CI
		verdict  reconcile.Verdict
		summary  []string
		findings []reconcile.FindingKind
		absent   []string // substrings the finding messages must not carry
	}{
		{"backstage v2.58.8: a green tag pipeline beside a red branch pipeline reads built",
			status(stateFailure, append(append([]string{}, branchLegs...), tagPipeline...), branchLegs, nil), ci,
			reconcile.VerdictOK, []string{"release v2.58.8 built: CircleCI success (8 jobs, 2026-09-23T08:13:00Z); 2 statuses of a branch pipeline at the commit ignored"}, nil, nil},
		{"a red tag-only job is the tag pipeline's failure, the branch legs are not named",
			status(stateFailure, append(append([]string{}, branchLegs...), tagPipeline...), append(append([]string{}, branchLegs...), releaseJob), nil), ci,
			reconcile.VerdictReported, []string{"release v2.58.8: CircleCI failure in push-to-registries-release (8 jobs"}, []reconcile.FindingKind{reconcile.FindingRedRelease}, []string{amd64Leg}},
		{"tunnelport v1.6.7: a canceled branch pipeline's shared jobs beside a green tag pipeline read unchecked",
			status(statusError, ctx("chart-test", "go-build", "go-test", releaseJobName), ctx("chart-test", "go-build", "go-test"), nil),
			&inventory.CI{Jobs: []inventory.CIJob{shared("go-build"), shared("go-test"), branchOnly("chart-test"), tagOnly(releaseJobName)}},
			reconcile.VerdictReported, []string{"release v2.58.8: two pipelines' statuses on its commit, go-build, go-test failed"}, []reconcile.FindingKind{reconcile.FindingUnchecked}, []string{"chart-test"}},
		{"a failed shared job without a branch pipeline's statuses is the tag pipeline's",
			status(stateFailure, ctx("go-build", releaseJobName), ctx("go-build"), nil),
			&inventory.CI{Jobs: []inventory.CIJob{shared("go-build"), tagOnly(releaseJobName)}},
			reconcile.VerdictReported, []string{"release v2.58.8: CircleCI failure in go-build (2 jobs"}, []reconcile.FindingKind{reconcile.FindingRedRelease}, nil},
		{"a pending tag job beside a finished branch pipeline reads running",
			status("pending", append(append([]string{}, branchLegs...), tagPipeline...), branchLegs, ctx("push-chart-release")), ci,
			reconcile.VerdictOK, []string{"release v2.58.8: CircleCI pipeline running (8 jobs", "2 statuses of a branch pipeline at the commit ignored"}, nil, nil},
		{"without a declaration every status counts",
			status(stateFailure, append(append([]string{}, branchLegs...), tagPipeline...), branchLegs, nil), nil,
			reconcile.VerdictReported, []string{"release v2.58.8: CircleCI failure in build-image-amd64, build-image-arm64 (10 jobs, 2026-09-23T08:13:00Z)"}, []reconcile.FindingKind{reconcile.FindingRedRelease}, nil},
		{"a declaration naming none of the jobs as the tag's is no evidence",
			status(stateSuccess, ctx("build", "publish"), nil, nil), &inventory.CI{Jobs: []inventory.CIJob{branchOnly("build"), branchOnly("publish")}},
			reconcile.VerdictOK, []string{"release v2.58.8 built: CircleCI success (2 jobs, 2026-09-23T08:13:00Z)"}, nil, nil},
		{"the earlier record shape: a red state names every context",
			&inventory.HeadStatus{State: stateFailure, Contexts: ctx("go-build", releaseJobName), At: at}, nil,
			reconcile.VerdictReported, []string{"release v2.58.8: CircleCI failure in go-build, push-to-registries-release"}, []reconcile.FindingKind{reconcile.FindingRedRelease}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := statusesReleaseStep(slug, tag, tc.build, tc.ci, tc.ci.TagOnly(tag, mainBranch))
			if got.Verdict != tc.verdict {
				t.Errorf("verdict %s, want %s (%+v)", got.Verdict, tc.verdict, got)
			}
			for _, s := range tc.summary {
				if !strings.Contains(got.Summary, s) {
					t.Errorf("summary %q lacks %q", got.Summary, s)
				}
			}
			if len(got.Findings) != len(tc.findings) {
				t.Fatalf("findings %+v, want kinds %v", got.Findings, tc.findings)
			}
			for i, f := range got.Findings {
				if f.Kind != tc.findings[i] || f.Message == "" || f.Fix == "" {
					t.Errorf("finding %d %+v, want %s with a message and a fix", i, f, tc.findings[i])
				}
				for _, s := range tc.absent {
					if strings.Contains(f.Message, s) {
						t.Errorf("finding names %q: %q", s, f.Message)
					}
				}
			}
		})
	}
}

// TestBuildsSentenceIgnoresOtherPipelines: the circleci step's sentence
// about the default branch head counts the branch's own statuses — the tag
// pipeline's release jobs and a bot's branch pipeline whose jobs skip the
// default branch are another pipeline's.
func TestBuildsSentenceIgnoresOtherPipelines(t *testing.T) {
	at := time.Date(2026, 9, 23, 9, 6, 2, 0, time.UTC)
	ci := &inventory.CI{Jobs: []inventory.CIJob{
		{Name: "node-build", TagsOnly: []string{vTags}},
		{Name: amd64Leg, BranchesIgnore: []string{mainBranch}},
		{Name: releaseJobName, TagsOnly: []string{vTags}, BranchesIgnore: []string{noBranch}},
	}}
	head := &inventory.HeadStatus{State: stateFailure, At: at,
		Contexts: []string{"ci/circleci: " + amd64Leg, "ci/circleci: node-build", releaseJob},
		Failed:   []string{"ci/circleci: " + amd64Leg}}
	want := "CircleCI builds main: success (1 job, 2026-09-23T09:06:02Z; 2 statuses of other pipelines at the head ignored)"
	if got := buildsSentence("giantswarm/x", mainBranch, head, ci, true); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// Without a declaration the sentence is the head's as before.
	if got := buildsSentence("giantswarm/x", mainBranch, head, nil, true); got != "CircleCI builds main: failure (3 jobs, 2026-09-23T09:06:02Z)" {
		t.Errorf("without a declaration: %q", got)
	}
}

// TestReleaseStepOfATagNoPipelineBuilt (devctl#2408): a chart repository's
// first release is cut on the default branch head before the reconciler
// follows the project, so no tag pipeline builds it, and the follow's build
// of main posts setup and go-build green on the release's commit. Those
// jobs run on main too and are no evidence for the tag: with the reconciler
// run's missed-tag-build for the tag, the run decides; without a run, the
// statuses read unchecked, never built. Once the tag pipeline's own jobs
// (build-chart, push-chart-release) report, the statuses decide again.
func TestReleaseStepOfATagNoPipelineBuilt(t *testing.T) {
	const slug, tag = "giantswarm/x", "v0.1.0"
	at := time.Date(2026, 9, 24, 6, 9, 34, 0, time.UTC)
	ci := &inventory.CI{Jobs: []inventory.CIJob{
		{Name: setupJob, TagsOnly: []string{vTags}},
		{Name: "go-build", TagsOnly: []string{vTags}},
		{Name: "build-chart", TagsOnly: []string{vTags}, BranchesIgnore: []string{mainBranch}},
		{Name: "execute-chart-tests", BranchesIgnore: []string{mainBranch}},
		{Name: "push-chart-release", TagsOnly: []string{vTags}, BranchesIgnore: []string{noBranch}},
	}}
	release := func(jobs ...string) *inventory.Release {
		contexts := make([]string, 0, len(jobs))
		for _, j := range jobs {
			contexts = append(contexts, "ci/circleci: "+j)
		}
		return &inventory.Release{Tag: tag, Build: &inventory.HeadStatus{State: stateSuccess, Contexts: contexts, At: at}}
	}
	missed := "release v0.1.0 of giantswarm/x has no pipeline: nothing was built or published for the tag"
	run := &inventory.LastRun{Timestamp: at, Result: reconcile.Result{Steps: []reconcile.StepResult{{Step: reconcile.StepRelease, Verdict: reconcile.VerdictReported,
		Findings: []reconcile.Finding{{Kind: reconcile.FindingMissedTagBuild, Message: missed, Fix: "cut the next tag, or trigger the tag's pipeline by hand"}}}}}}

	sr := releaseStep(slug, tag, mainBranch, release(setupJob, "go-build"), ci, run, nil)
	if sr.Verdict != reconcile.VerdictReported || len(sr.Findings) != 1 || sr.Findings[0].Kind != reconcile.FindingMissedTagBuild || !strings.Contains(sr.Summary, "(reconciler run of ") {
		t.Errorf("main's build beside the run's missed build: %+v, want the run's missed-tag-build", sr)
	}
	sr = releaseStep(slug, tag, mainBranch, release(setupJob, "go-build"), ci, nil, nil)
	if sr.Verdict != reconcile.VerdictReported || len(sr.Findings) != 1 || sr.Findings[0].Kind != reconcile.FindingUnchecked ||
		!strings.Contains(sr.Summary, "none of the tag pipeline's own jobs (build-chart, push-chart-release) on its commit") {
		t.Errorf("main's build without a run: %+v, want unchecked naming the tag pipeline's own jobs", sr)
	}
	sr = releaseStep(slug, tag, mainBranch, release(setupJob, "go-build", "build-chart", "push-chart-release"), ci, run, nil)
	if sr.Verdict != reconcile.VerdictOK || len(sr.Findings) != 0 || !strings.HasPrefix(sr.Summary, "release v0.1.0 built: CircleCI success (4 jobs") {
		t.Errorf("the tag pipeline triggered by hand: %+v, want built", sr)
	}
}
