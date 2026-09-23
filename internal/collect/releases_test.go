package collect

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/circleciclient"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// Test constants of the release watch.
const (
	stateFailure    = "failure"
	statusRunning   = "running"
	statusTimedOut  = "timedout"
	kItems          = "items"
	pipelineTag     = "p-tag"
	pipelineBranch  = "p-branch"
	workflowBuild   = "build"
	backstage11508  = "https://app.circleci.com/pipelines/github/giantswarm/backstage/11508"
	circleCIFailure = "CircleCI failure"
	wfBuild         = "wf-build"
	neverRebuilt    = "a tag is never rebuilt"
)

// fakeTagPipelines is CircleCI as the watch reads it: one project's
// pipelines, their workflows and jobs; every other project is 404.
type fakeTagPipelines struct {
	project   string
	pipelines []circleciclient.Pipeline
	workflows map[string][]circleciclient.Workflow
	jobs      map[string][]circleciclient.Job
	calls     []string
}

func (f *fakeTagPipelines) handler() http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /api/v2/project/gh/{org}/{repo}/pipeline", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.URL.Path)
		if r.PathValue("org")+"/"+r.PathValue("repo") != f.project {
			w.WriteHeader(http.StatusNotFound)
			write(w, map[string]string{"message": "Project not found"})
			return
		}
		write(w, map[string]any{kItems: f.pipelines, "next_page_token": nil})
	})
	mux.HandleFunc("GET /api/v2/pipeline/{id}/workflow", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.URL.Path)
		write(w, map[string]any{kItems: f.workflows[r.PathValue("id")]})
	})
	mux.HandleFunc("GET /api/v2/workflow/{id}/job", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.URL.Path)
		write(w, map[string]any{kItems: f.jobs[r.PathValue("id")]})
	})
	return mux
}

// TestReadReleaseDecidesFromTheTagPipeline: the watch reads the tag's own
// pipeline — red with the failed jobs named and the failed workflow linked,
// built, a rerun from failed that went green, running, no pipeline within
// and after the grace period, a private project without a token.
func TestReadReleaseDecidesFromTheTagPipeline(t *testing.T) {
	const (
		org, name, tag = "giantswarm", "backstage", "v2.58.8"
		sha            = "a80db8ff"
	)
	now := time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC)
	t0 := now.Add(-40 * time.Minute)
	tagPipeline := circleciclient.Pipeline{ID: pipelineTag, Number: 11508, CreatedAt: t0, VCS: circleciclient.PipelineVCS{Tag: tag, Revision: sha}}
	branchPipeline := circleciclient.Pipeline{ID: pipelineBranch, Number: 11509, CreatedAt: t0.Add(2 * time.Minute), VCS: circleciclient.PipelineVCS{Branch: "changesets-ghcommit-temp/changeset-release/main", Revision: sha}}
	failedBuild := circleciclient.Workflow{ID: wfBuild, Name: workflowBuild, Status: "failed", PipelineNumber: 11508, CreatedAt: t0}
	greenSetup := circleciclient.Workflow{ID: "wf-setup", Name: "setup", Status: stateSuccess, PipelineNumber: 11508, CreatedAt: t0}
	redJobs := []circleciclient.Job{{Name: "node-build", Status: stateSuccess}, {Name: "build-image-amd64", Status: statusTimedOut}, {Name: "build-image-arm64", Status: statusTimedOut}, {Name: "push-to-registries-release", Status: "not_run"}}
	branchRed := circleciclient.Workflow{ID: "wf-branch", Name: workflowBuild, Status: "failed", PipelineNumber: 11509, CreatedAt: t0.Add(2 * time.Minute)}

	cases := []struct {
		name      string
		fake      *fakeTagPipelines
		anonymous bool
		createdAt time.Time
		want      inventory.ReleaseWatch
		calls     int
	}{
		{
			name: "red: the failed jobs and how, the failed workflow linked",
			fake: &fakeTagPipelines{project: org + "/" + name, pipelines: []circleciclient.Pipeline{branchPipeline, tagPipeline},
				workflows: map[string][]circleciclient.Workflow{pipelineTag: {greenSetup, failedBuild}, pipelineBranch: {branchRed}},
				jobs:      map[string][]circleciclient.Job{wfBuild: redJobs}},
			want: inventory.ReleaseWatch{State: inventory.ReleaseRed, FailedJobs: []string{"build-image-amd64 (timed out)", "build-image-arm64 (timed out)"},
				Pipeline: &inventory.ReleasePipeline{Number: 11508, URL: backstage11508, Workflow: backstage11508 + "/workflows/" + wfBuild}},
			calls: 3,
		},
		{
			name: "built: a green tag pipeline beside a red branch pipeline at the same commit",
			fake: &fakeTagPipelines{project: org + "/" + name, pipelines: []circleciclient.Pipeline{branchPipeline, tagPipeline},
				workflows: map[string][]circleciclient.Workflow{pipelineTag: {greenSetup, {ID: "wf-ok", Name: workflowBuild, Status: stateSuccess, CreatedAt: t0}}, pipelineBranch: {branchRed}},
				jobs:      map[string][]circleciclient.Job{"wf-branch": redJobs}},
			want:  inventory.ReleaseWatch{State: inventory.ReleaseBuilt, Pipeline: &inventory.ReleasePipeline{Number: 11508, URL: backstage11508}},
			calls: 2,
		},
		{
			name: "built: a rerun from failed that went green replaces the failed run",
			fake: &fakeTagPipelines{project: org + "/" + name, pipelines: []circleciclient.Pipeline{tagPipeline},
				workflows: map[string][]circleciclient.Workflow{pipelineTag: {failedBuild, greenSetup, {ID: "wf-rerun", Name: workflowBuild, Status: stateSuccess, CreatedAt: t0.Add(30 * time.Minute)}}},
				jobs:      map[string][]circleciclient.Job{wfBuild: redJobs}},
			want:  inventory.ReleaseWatch{State: inventory.ReleaseBuilt, Pipeline: &inventory.ReleasePipeline{Number: 11508, URL: backstage11508}},
			calls: 2,
		},
		{
			name: "watching: a workflow still running",
			fake: &fakeTagPipelines{project: org + "/" + name, pipelines: []circleciclient.Pipeline{tagPipeline},
				workflows: map[string][]circleciclient.Workflow{pipelineTag: {greenSetup, {ID: "wf-run", Name: workflowBuild, Status: statusRunning, CreatedAt: t0}}}},
			want:  inventory.ReleaseWatch{State: inventory.ReleaseWatching, Pipeline: &inventory.ReleasePipeline{Number: 11508, URL: backstage11508}},
			calls: 2,
		},
		{
			name: "watching: a workflow that failed while another runs is red at once",
			fake: &fakeTagPipelines{project: org + "/" + name, pipelines: []circleciclient.Pipeline{tagPipeline},
				workflows: map[string][]circleciclient.Workflow{pipelineTag: {failedBuild, {ID: "wf-run", Name: "setup", Status: statusRunning, CreatedAt: t0}}},
				jobs:      map[string][]circleciclient.Job{wfBuild: redJobs}},
			want: inventory.ReleaseWatch{State: inventory.ReleaseRed, FailedJobs: []string{"build-image-amd64 (timed out)", "build-image-arm64 (timed out)"},
				Pipeline: &inventory.ReleasePipeline{Number: 11508, URL: backstage11508, Workflow: backstage11508 + "/workflows/" + wfBuild}},
			calls: 3,
		},
		{
			name:      "watching: no pipeline yet, within the grace period",
			fake:      &fakeTagPipelines{project: org + "/" + name, pipelines: []circleciclient.Pipeline{branchPipeline}},
			createdAt: now.Add(-3 * time.Minute),
			want:      inventory.ReleaseWatch{State: inventory.ReleaseWatching},
			calls:     1,
		},
		{
			name:      "unbuilt: no pipeline after the grace period",
			fake:      &fakeTagPipelines{project: org + "/" + name, pipelines: []circleciclient.Pipeline{branchPipeline}},
			createdAt: now.Add(-11 * time.Minute),
			want:      inventory.ReleaseWatch{State: inventory.ReleaseUnbuilt},
			calls:     1,
		},
		{
			name:      "unchecked: a private project without a token is 404",
			fake:      &fakeTagPipelines{project: org + "/other"},
			anonymous: true,
			want:      inventory.ReleaseWatch{State: inventory.ReleaseUnchecked, Reason: "the repository is private and no CircleCI token is configured (circleci.existingSecret): the tag pipeline is out of the inventory's reach"},
			calls:     1,
		},
		{
			name:  "unchecked: a project CircleCI does not know, with a token",
			fake:  &fakeTagPipelines{project: org + "/other"},
			want:  inventory.ReleaseWatch{State: inventory.ReleaseUnchecked, Reason: "CircleCI does not know the project"},
			calls: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.fake.handler())
			defer srv.Close()
			cfg := circleciclient.Config{BaseURL: srv.URL, Token: "cci_test"}
			if tc.anonymous {
				cfg = circleciclient.Config{BaseURL: srv.URL, Anonymous: true}
			}
			cc, err := circleciclient.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			c := &Collector{opts: Options{Org: org, Releases: ReleaseOptions{Grace: 10 * time.Minute, CircleCI: cc, Anonymous: tc.anonymous}}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			createdAt := tc.createdAt
			if createdAt.IsZero() {
				createdAt = t0.Add(-time.Minute)
			}
			w := &inventory.ReleaseWatch{Tag: tag, CreatedAt: createdAt, State: inventory.ReleaseWatching}
			rec := &inventory.Record{Repository: org + "/" + name, Name: name}
			c.readRelease(context.Background(), rec, w, now)
			if w.State != tc.want.State || w.Reason != tc.want.Reason || strings.Join(w.FailedJobs, ",") != strings.Join(tc.want.FailedJobs, ",") {
				t.Errorf("state=%s reason=%q failedJobs=%v, want %s %q %v", w.State, w.Reason, w.FailedJobs, tc.want.State, tc.want.Reason, tc.want.FailedJobs)
			}
			switch {
			case tc.want.Pipeline == nil && w.Pipeline != nil:
				t.Errorf("pipeline: got %+v, want none", *w.Pipeline)
			case tc.want.Pipeline != nil && (w.Pipeline == nil || *w.Pipeline != *tc.want.Pipeline):
				t.Errorf("pipeline: got %+v, want %+v", w.Pipeline, *tc.want.Pipeline)
			}
			if w.CheckedAt != now || (w.Settled() && (w.SettledAt == nil || *w.SettledAt != now)) || (!w.Settled() && w.SettledAt != nil) {
				t.Errorf("times: checked %v settled %v", w.CheckedAt, w.SettledAt)
			}
			if len(tc.fake.calls) != tc.calls {
				t.Errorf("CircleCI calls: %d %v, want %d", len(tc.fake.calls), tc.fake.calls, tc.calls)
			}
		})
	}
}

func TestIsReleaseTagAndOutsideReason(t *testing.T) {
	for tag, want := range map[string]bool{"v1.2.3": true, "v0.1.0-rc.1": true, "v10.0.0+build.7": true, "1.2.3": false, "base/v0.1.0": false, "v1.2": false, "v01.2.3": false, "": false} {
		if got := isReleaseTag(tag); got != want {
			t.Errorf("isReleaseTag(%q) = %v, want %v", tag, got, want)
		}
	}
	withCI := &inventory.Record{Reality: &inventory.Reality{Has: inventory.Presence{CircleCI: true}}}
	withoutCI := &inventory.Record{Reality: &inventory.Reality{}}
	if r := outsideReason("v1.0.0", withCI); r != "" {
		t.Errorf("a release tag with a pipeline is watched, got %q", r)
	}
	if r := outsideReason("base/v1.0.0", withCI); !strings.Contains(r, "not a vX.Y.Z tag") {
		t.Errorf("a component tag: %q", r)
	}
	if r := outsideReason("v1.0.0", withoutCI); !strings.Contains(r, "no CircleCI pipeline") {
		t.Errorf("no pipeline: %q", r)
	}
}

// TestReleaseStepFromTheWatch: the record's release step comes from the
// watch once it followed the tag — built, red with the finding naming the
// failed jobs and the rerun, unbuilt with the missed build, running with the
// pipeline named — and from the other sources while the watch has nothing
// for the tag (unchecked, watching without a pipeline, another tag).
func TestReleaseStepFromTheWatch(t *testing.T) {
	const slug, tag = "giantswarm/x", "v1.2.0"
	at := time.Date(2026, 9, 23, 8, 13, 0, 0, time.UTC)
	pipe := &inventory.ReleasePipeline{Number: 11508, URL: "https://app.circleci.com/pipelines/github/giantswarm/x/11508"}
	statuses := &inventory.Release{Tag: tag, Build: &inventory.HeadStatus{State: stateFailure, Contexts: []string{releaseJob}, At: at}}
	watch := func(state string, jobs ...string) *inventory.ReleaseWatch {
		return &inventory.ReleaseWatch{Tag: tag, State: state, Pipeline: pipe, FailedJobs: jobs, CheckedAt: at}
	}
	cases := []struct {
		name    string
		w       *inventory.ReleaseWatch
		verdict reconcile.Verdict
		summary string
		finding reconcile.FindingKind
		fix     string
	}{
		{"built", watch(inventory.ReleaseBuilt), reconcile.VerdictOK, "release v1.2.0 built: the tag's pipeline 11508 succeeded", "", ""},
		{"red", watch(inventory.ReleaseRed, "build-image-arm64 (timed out)"), reconcile.VerdictReported, "the tag's pipeline 11508 failed", reconcile.FindingRedRelease, "rerun the workflow from failed on CircleCI, then `devctl release wait giantswarm/x v1.2.0` confirms it"},
		{"unbuilt", &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseUnbuilt, CheckedAt: at}, reconcile.VerdictReported, "no CircleCI pipeline for the tag", reconcile.FindingMissedTagBuild, "trigger the tag's pipeline by hand on CircleCI, then `devctl release wait giantswarm/x v1.2.0` confirms it"},
		{"running", watch(inventory.ReleaseWatching), reconcile.VerdictOK, "the tag's pipeline 11508 is running", "", ""},
		// The other sources: the tag commit's statuses say red.
		{"unchecked falls through", &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseUnchecked, CheckedAt: at}, reconcile.VerdictReported, circleCIFailure, reconcile.FindingRedRelease, neverRebuilt},
		{"watching without a pipeline falls through", &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseWatching, CheckedAt: at}, reconcile.VerdictReported, circleCIFailure, reconcile.FindingRedRelease, neverRebuilt},
		{"another tag falls through", &inventory.ReleaseWatch{Tag: "v1.3.0", State: inventory.ReleaseBuilt, Pipeline: pipe, CheckedAt: at}, reconcile.VerdictReported, circleCIFailure, reconcile.FindingRedRelease, neverRebuilt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sr := releaseStep(slug, tag, statuses, nil, tc.w)
			if sr.Verdict != tc.verdict || !strings.Contains(sr.Summary, tc.summary) {
				t.Errorf("verdict=%s summary=%q, want %s containing %q", sr.Verdict, sr.Summary, tc.verdict, tc.summary)
			}
			switch {
			case tc.finding == "" && len(sr.Findings) != 0:
				t.Errorf("findings: %+v, want none", sr.Findings)
			case tc.finding != "" && (len(sr.Findings) != 1 || sr.Findings[0].Kind != tc.finding || !strings.Contains(sr.Findings[0].Fix, tc.fix)):
				t.Errorf("findings: %+v, want one %s with fix containing %q", sr.Findings, tc.finding, tc.fix)
			}
		})
	}

	// rewriteReleaseStep writes the stored checks' release step from the
	// watch and recomputes the record's findings and convergence.
	rec := &inventory.Record{Repository: slug, Name: "x", Declaration: &inventory.Declaration{Team: "team-bumblebee"}, Reality: &inventory.Reality{LatestRelease: statuses},
		Setup: inventory.Setup{Release: watch(inventory.ReleaseBuilt), Checks: &reconcile.Result{Converged: false, Steps: []reconcile.StepResult{
			{Step: reconcile.StepSettings, Verdict: reconcile.VerdictOK},
			{Step: reconcile.StepRelease, Verdict: reconcile.VerdictReported, Summary: "release v1.2.0: CircleCI failure", Findings: []reconcile.Finding{{Kind: reconcile.FindingRedRelease}}},
		}}}}
	rec.Finalize()
	if len(rec.Findings) != 1 {
		t.Fatalf("before: %+v", rec.Findings)
	}
	rewriteReleaseStep(rec)
	rec.Finalize()
	if sr := rec.Setup.Checks.Step(reconcile.StepRelease); sr.Verdict != reconcile.VerdictOK || len(rec.Findings) != 0 || !rec.Setup.Checks.Converged {
		t.Errorf("after: step=%+v findings=%+v converged=%v", sr, rec.Findings, rec.Setup.Checks.Converged)
	}
}

func TestFailedJobsNameHow(t *testing.T) {
	jobs := []circleciclient.Job{{Name: "a", Status: stateSuccess}, {Name: "b", Status: statusTimedOut}, {Name: "c", Status: "infrastructure_fail"}, {Name: "d", Status: "not_run"}, {Name: "e", Status: "canceled"}}
	got := strings.Join(failedJobs(jobs), "; ")
	if got != "b (timed out); c (infrastructure failure); e (canceled)" {
		t.Errorf("failedJobs: %s", got)
	}
}

// TestToldByTheRun: a release the completion path told from a reconciler
// run's finding is found in setup.told by its opening words, for that tag
// alone.
func TestToldByTheRun(t *testing.T) {
	rec := &inventory.Record{Repository: "giantswarm/y", Name: "y", Setup: inventory.Setup{Told: []string{
		"y: the repository has the default icon — upload one under Settings",
		"y: release v0.1.0 of giantswarm/y has no CircleCI status on its commit: nothing was built or published for the tag — cut the next tag, or trigger the tag's pipeline by hand",
	}}}
	if got := toldByTheRun(rec, "v0.1.0"); !strings.HasPrefix(got, "y: release v0.1.0 of giantswarm/y has no CircleCI status") {
		t.Errorf("v0.1.0: %q", got)
	}
	if got := toldByTheRun(rec, "v0.2.0"); got != "" {
		t.Errorf("v0.2.0 was not told: %q", got)
	}
}
