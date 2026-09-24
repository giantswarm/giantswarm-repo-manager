package e2e

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/collect"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// fakeActions is the Actions API of the fake team-files repository, as the
// reconciler poller reads it: the workflow's runs (filtered by created),
// each run's artifacts, and the artifact download — a redirect to a blob
// path on the same fake, which must be fetched without the App's token.
type fakeActions struct {
	mu   sync.Mutex
	runs []*fakeRun
	// blobDownloads counts the blob fetches: the proof that a second poll
	// reads nothing again. blobAuthorized records a blob fetch that carried
	// an Authorization header — the installation token must never reach the
	// blob store.
	blobDownloads  atomic.Int64
	blobAuthorized atomic.Bool
	nextID         atomic.Int64
	// gate, while set, holds every blob fetch until gateN of them have
	// arrived or a second passed: two pollers racing over one artifact.
	gateMu  sync.Mutex
	gate    chan struct{}
	gateN   int
	arrived int
}

// holdBlobs makes the next n blob fetches wait for each other (a second at
// most), so pollers that read one artifact at once all read it before any
// stores it.
func (a *fakeActions) holdBlobs(n int) {
	a.gateMu.Lock()
	defer a.gateMu.Unlock()
	a.gate, a.gateN, a.arrived = make(chan struct{}), n, 0
}

// atGate waits at the blob gate, when one is set.
func (a *fakeActions) atGate() {
	a.gateMu.Lock()
	gate := a.gate
	if gate != nil {
		a.arrived++
		if a.arrived == a.gateN {
			close(gate)
			a.gate = nil
		}
	}
	a.gateMu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-time.After(time.Second):
		}
	}
}

type fakeRun struct {
	ID        int64
	Attempt   int
	Status    string
	Event     string
	CreatedAt time.Time
	Artifacts []*fakeArtifact
	// Conclusion is the completed run's: success, or failure when a job
	// failed before its report step. HeadSHA is the run's head commit — a
	// push run's merge commit. Jobs are the run's job names as GitHub lists
	// them: Plan, one "<team> / Reconcile <name>" per repository handled,
	// the team's summary.
	Conclusion string
	HeadSHA    string
	Jobs       []string
}

type fakeArtifact struct {
	ID      int64
	Name    string
	Zip     []byte
	Expired bool
}

const (
	runStatusCompleted  = "completed"
	runStatusInProgress = "in_progress"
	eventDispatch       = "workflow_dispatch"
	eventPush           = "push"
	conclusionSuccess   = "success"
	conclusionFailure   = "failure"
	kConclusion         = "conclusion"
	// reconcilerWorkflow is the reconciler's workflow file: the one the
	// tools dispatch and the poller reads the runs of.
	reconcilerWorkflow = "reconcile-repositories.yaml"
)

// runURL is the fake run's page.
func runURL(id int64) string {
	return fmt.Sprintf("https://github.com/%s/github/actions/runs/%d", org, id)
}

// addRun adds a run of the reconciler workflow created at createdAt with one
// reconcile-<name> artifact per report; the report's workflowRun names the
// run. It returns the run for later changes (status, attempt).
func (a *fakeActions) addRun(t *testing.T, status string, createdAt time.Time, reports ...artifactReport) *fakeRun {
	t.Helper()
	run := &fakeRun{ID: a.nextID.Add(1), Attempt: 1, Status: status, Event: eventDispatch, CreatedAt: createdAt.UTC(), Jobs: []string{"Plan"}}
	if status == runStatusCompleted {
		run.Conclusion = conclusionSuccess
	}
	for _, r := range reports {
		run.Artifacts = append(run.Artifacts, &fakeArtifact{ID: a.nextID.Add(1), Name: collect.ArtifactPrefix + r.name, Zip: r.zip(t, run)})
		run.Jobs = append(run.Jobs, reconcileJob(r.name))
	}
	run.Jobs = append(run.Jobs, team+" / Summary "+team)
	a.mu.Lock()
	a.runs = append(a.runs, run)
	a.mu.Unlock()
	return run
}

// addRunWithoutReport adds a completed run of event over the repositories
// named that uploaded no artifact for them — its Reconcile jobs failed
// before their report step — with head as its head commit (a push run's
// merge commit) and conclusion failure.
func (a *fakeActions) addRunWithoutReport(t *testing.T, event string, createdAt time.Time, head string, repositories ...string) *fakeRun {
	t.Helper()
	run := a.addRun(t, runStatusCompleted, createdAt)
	a.mu.Lock()
	defer a.mu.Unlock()
	run.Event, run.HeadSHA, run.Conclusion = event, head, conclusionFailure
	run.Jobs = []string{"Plan"}
	for _, name := range repositories {
		run.Jobs = append(run.Jobs, reconcileJob(name))
	}
	run.Jobs = append(run.Jobs, team+" / Summary "+team)
	return run
}

// failed marks the run's Reconcile jobs for the repositories named failed
// without an artifact, beside the artifacts the run did upload: the run's
// conclusion is failure.
func (a *fakeActions) failed(run *fakeRun, repositories ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run.Conclusion = conclusionFailure
	for _, name := range repositories {
		run.Jobs = append(run.Jobs, reconcileJob(name))
	}
}

// reconcileJob is the name GitHub lists the reconciler's job for one
// repository under: the reusable workflow's job "Reconcile <name>" behind
// its caller, the team's shard.
func reconcileJob(name string) string {
	return team + " / Reconcile " + name
}

// complete marks a run completed with success.
func (a *fakeActions) complete(run *fakeRun) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run.Status, run.Conclusion = runStatusCompleted, conclusionSuccess
}

// rerun is the run's next attempt with fresh artifacts: the same run id.
func (a *fakeActions) rerun(t *testing.T, run *fakeRun, reports ...artifactReport) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	run.Attempt++
	run.Artifacts = nil
	for _, r := range reports {
		run.Artifacts = append(run.Artifacts, &fakeArtifact{ID: a.nextID.Add(1), Name: collect.ArtifactPrefix + r.name, Zip: r.zip(t, run)})
	}
}

// artifactReport is what the reconciler uploads for one repository: the
// engine's result with the run, the finish time and the change block (the
// team-file change the run followed; nil leaves it out).
type artifactReport struct {
	name       string
	result     reconcile.Result
	finishedAt time.Time
	change     *inventory.Change
}

func (r artifactReport) zip(t *testing.T, run *fakeRun) []byte {
	t.Helper()
	body := map[string]any{}
	b, err := json.Marshal(r.result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	// The id and the attempt are strings, as the workflow writes them from
	// GITHUB_RUN_ID and GITHUB_RUN_ATTEMPT.
	body["workflowRun"] = map[string]any{kID: strconv.FormatInt(run.ID, 10), kURL: runURL(run.ID), "attempt": strconv.Itoa(run.Attempt), "event": run.Event, "trigger": "align_repository", "devctl": "v8.65.0"}
	body["finishedAt"] = r.finishedAt.UTC().Format(time.RFC3339)
	if r.change != nil {
		body["change"] = r.change
	}
	b, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return zipOf(t, collect.ArtifactPrefix+r.name+".json", b)
}

// zipOf is a zip with one file.
func zipOf(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (a *fakeActions) register(mux *http.ServeMux, g *fakeGitHub) {
	base := "/api/v3/repos/" + org + "/github/actions"
	mux.HandleFunc("GET "+base+"/workflows/{file}/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue(kFile) != reconcilerWorkflow {
			ghMessage(w, http.StatusNotFound, "Not Found")
			return
		}
		var since time.Time
		if created := r.URL.Query().Get("created"); created != "" {
			ts, err := time.Parse(time.RFC3339, strings.TrimPrefix(created, ">="))
			if err != nil {
				ghMessage(w, http.StatusUnprocessableEntity, "created: "+err.Error())
				return
			}
			since = ts
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		out := []map[string]any{}
		for i := len(a.runs) - 1; i >= 0; i-- { // newest first, as GitHub lists
			run := a.runs[i]
			if run.CreatedAt.Before(since) {
				continue
			}
			out = append(out, map[string]any{kID: run.ID, "run_attempt": run.Attempt, "status": run.Status, kConclusion: run.Conclusion, "event": run.Event, "head_sha": run.HeadSHA,
				kCreatedAtREST: run.CreatedAt.Format(time.RFC3339), "updated_at": run.CreatedAt.Add(time.Minute).Format(time.RFC3339), kHTMLURL: runURL(run.ID)})
		}
		writeJSON(w, http.StatusOK, map[string]any{kTotalCount: len(out), "workflow_runs": out})
	})
	mux.HandleFunc("GET "+base+"/runs/{id}/jobs", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue(kID), 10, 64)
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, run := range a.runs {
			if run.ID != id {
				continue
			}
			out := []map[string]any{}
			for i, name := range run.Jobs {
				out = append(out, map[string]any{kID: run.ID*1000 + int64(i), kName: name, "status": run.Status, kConclusion: run.Conclusion, "run_id": run.ID, "run_attempt": run.Attempt})
			}
			writeJSON(w, http.StatusOK, map[string]any{kTotalCount: len(out), "jobs": out})
			return
		}
		ghMessage(w, http.StatusNotFound, "Not Found")
	})
	mux.HandleFunc("GET "+base+"/runs/{id}/artifacts", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue(kID), 10, 64)
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, run := range a.runs {
			if run.ID != id {
				continue
			}
			out := []map[string]any{}
			for _, art := range run.Artifacts {
				out = append(out, map[string]any{kID: art.ID, kName: art.Name, "size_in_bytes": len(art.Zip), "expired": art.Expired,
					"archive_download_url": g.URL + base + "/artifacts/" + strconv.FormatInt(art.ID, 10) + "/zip"})
			}
			writeJSON(w, http.StatusOK, map[string]any{kTotalCount: len(out), "artifacts": out})
			return
		}
		ghMessage(w, http.StatusNotFound, "Not Found")
	})
	mux.HandleFunc("GET "+base+"/artifacts/{id}/zip", func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) == "" {
			ghMessage(w, http.StatusUnauthorized, "Requires authentication")
			return
		}
		id, _ := strconv.ParseInt(r.PathValue(kID), 10, 64)
		w.Header().Set("Location", g.URL+"/blobs/"+strconv.FormatInt(id, 10)+"?sig=signed")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("GET /blobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			a.blobAuthorized.Store(true)
		}
		a.blobDownloads.Add(1)
		a.atGate()
		id, _ := strconv.ParseInt(r.PathValue(kID), 10, 64)
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, run := range a.runs {
			for _, art := range run.Artifacts {
				if art.ID == id {
					w.Header().Set("Content-Type", "application/zip")
					_, _ = w.Write(art.Zip)
					return
				}
			}
		}
		w.WriteHeader(http.StatusNotFound)
	})
}
