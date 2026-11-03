package e2e

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/circleciclient"
)

// fakeCircleCI is CircleCI's API v2 as the release watch reads it: a
// project's pipelines (newest first), a pipeline's workflows, a workflow's
// jobs. A project it does not know is 404, as CircleCI answers an anonymous
// read of a private project.
type fakeCircleCI struct {
	*httptest.Server
	mu        sync.Mutex
	pipelines map[string][]circleciclient.Pipeline // org/repo → newest first
	workflows map[string][]circleciclient.Workflow // pipeline id
	jobs      map[string][]circleciclient.Job      // workflow id
	// app marks the pipelines the CircleCI GitHub App started: served without
	// vcs, what they built in trigger_parameters.git, as CircleCI serves them.
	app map[string]bool
	seq int
	// reads counts the requests, the watch's CircleCI budget.
	reads int
}

func newFakeCircleCI(t *testing.T) *fakeCircleCI {
	t.Helper()
	f := &fakeCircleCI{pipelines: map[string][]circleciclient.Pipeline{}, workflows: map[string][]circleciclient.Workflow{}, jobs: map[string][]circleciclient.Job{}, app: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/project/gh/{org}/{repo}/pipeline", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reads++
		items, ok := f.pipelines[r.PathValue("org")+"/"+r.PathValue("repo")]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{kMessage: "Project not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{kItems: f.items(items), "next_page_token": nil})
	})
	mux.HandleFunc("GET /api/v2/pipeline/{id}/workflow", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reads++
		writeJSON(w, http.StatusOK, map[string]any{kItems: f.workflows[r.PathValue("id")]})
	})
	mux.HandleFunc("GET /api/v2/workflow/{id}/job", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reads++
		writeJSON(w, http.StatusOK, map[string]any{kItems: f.jobs[r.PathValue("id")]})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// pipeline seeds a pipeline of vcs for repo, created at createdAt, with one
// workflow `build` of status whose jobs are jobs; it returns the workflow's
// id. A red status with no jobs given gets the two image jobs timed out.
func (f *fakeCircleCI) pipeline(repo string, vcs circleciclient.PipelineVCS, createdAt time.Time, status string, jobs ...circleciclient.Job) (wfID string) {
	return f.seed(repo, vcs, createdAt, status, false, jobs...)
}

// appPipeline seeds a pipeline like pipeline does, one the CircleCI GitHub
// App started: served without vcs, what it built in trigger_parameters.git.
func (f *fakeCircleCI) appPipeline(repo string, vcs circleciclient.PipelineVCS, createdAt time.Time, status string, jobs ...circleciclient.Job) (wfID string) {
	return f.seed(repo, vcs, createdAt, status, true, jobs...)
}

func (f *fakeCircleCI) seed(repo string, vcs circleciclient.PipelineVCS, createdAt time.Time, status string, app bool, jobs ...circleciclient.Job) (wfID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := "pipeline-" + itoa(f.seq)
	p := circleciclient.Pipeline{ID: id, Number: int64(11500 + f.seq), State: "created", CreatedAt: createdAt, VCS: vcs}
	f.pipelines[org+"/"+repo] = append([]circleciclient.Pipeline{p}, f.pipelines[org+"/"+repo]...)
	if app {
		f.app[id] = true
	}
	wfID = id + "-build"
	f.workflows[id] = []circleciclient.Workflow{{ID: wfID, Name: "build", Status: status, PipelineNumber: p.Number, CreatedAt: createdAt}}
	if len(jobs) == 0 && circleciclient.WorkflowFailed(status) {
		jobs = []circleciclient.Job{{Name: "node-build", Status: "success"}, {Name: "build-image-amd64", Status: "timedout"}, {Name: "build-image-arm64", Status: "timedout"}, {Name: jobPushRelease, Status: "not_run"}}
	}
	f.jobs[wfID] = jobs
	return wfID
}

// items is the pipeline page as CircleCI serves it: a pipeline the OAuth
// integration built with its vcs, one the GitHub App started without it,
// its checkout and ref under trigger_parameters.git (the shape of
// cluster-aws pipeline 8329, 2026-10-28). The caller holds the lock.
func (f *fakeCircleCI) items(pipelines []circleciclient.Pipeline) []map[string]any {
	items := make([]map[string]any, 0, len(pipelines))
	for _, p := range pipelines {
		item := map[string]any{"id": p.ID, kNumber: p.Number, "state": p.State, "created_at": p.CreatedAt, "trigger": map[string]any{"type": "webhook"}}
		if !f.app[p.ID] {
			item["vcs"] = p.VCS
			items = append(items, item)
			continue
		}
		git := map[string]any{"checkout_sha": p.VCS.Revision, "ref": "refs/heads/" + p.VCS.Branch, "branch": p.VCS.Branch}
		if p.VCS.Tag != "" {
			git = map[string]any{"checkout_sha": p.VCS.Revision, "ref": "refs/tags/" + p.VCS.Tag, "tag": p.VCS.Tag}
		}
		item["trigger_parameters"] = map[string]any{"circleci": map[string]any{"trigger_type": "github_app"}, "git": git}
		items = append(items, item)
	}
	return items
}

// rerun adds a newer run of the workflow `build` to repo's newest pipeline,
// of status with every job green: the rerun from failed.
func (f *fakeCircleCI) rerun(repo, status string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.pipelines[org+"/"+repo][0]
	wfID := p.ID + "-build-rerun"
	f.workflows[p.ID] = append(f.workflows[p.ID], circleciclient.Workflow{ID: wfID, Name: "build", Status: status, PipelineNumber: p.Number, CreatedAt: at})
	f.jobs[wfID] = []circleciclient.Job{{Name: "build-image-amd64", Status: status}, {Name: "build-image-arm64", Status: status}, {Name: jobPushRelease, Status: status}}
}

// workflow adds a workflow name of status to repo's newest pipeline.
func (f *fakeCircleCI) workflow(repo, name, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.pipelines[org+"/"+repo][0]
	f.workflows[p.ID] = append(f.workflows[p.ID], circleciclient.Workflow{ID: p.ID + "-" + name, Name: name, Status: status, PipelineNumber: p.Number, CreatedAt: p.CreatedAt})
}

// finish sets every workflow of repo's newest pipeline to status.
func (f *fakeCircleCI) finish(repo, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.pipelines[org+"/"+repo][0]
	for i := range f.workflows[p.ID] {
		f.workflows[p.ID][i].Status = status
	}
}

// count is how many requests the fake answered so far.
func (f *fakeCircleCI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
