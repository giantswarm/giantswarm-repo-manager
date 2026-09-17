package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/mark3labs/mcp-go/client"

	"github.com/giantswarm/giantswarm-repo-manager/internal/collect"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/tools"
)

// TestSweepFillsOneRecordPerRepository: the full sweep over the fake org —
// sources in one query, repositories in one page, histories in one aliased
// batch — leaves one record per repository (declared and present, declared
// but gone, refused declaration, undeclared, archived), the seeded stranger
// removed, with the findings, the set-up state and the orphan scores.
func TestSweepFillsOneRecordPerRepository(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	sum, err := st.col.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if sum.Repositories != 5 || sum.Declared != 2 || sum.Undeclared != 2 || sum.Gone != 1 || sum.Archived != 1 || sum.EngineChecks != 2 || sum.Removed != 1 {
		t.Errorf("summary: %+v", sum)
	}
	if sum.GraphQL.Calls != 3 || sum.GraphQL.Cost != 3*fakeGraphQLCost || sum.GraphQL.Remaining == 0 || sum.Duration == "" {
		t.Errorf("graphql budget: %+v duration %q", sum.GraphQL, sum.Duration)
	}
	if n, _ := st.store.Count(ctx); n != 5 {
		t.Errorf("records: %d, want 5", n)
	}
	if st.checker.calls.Load() != 2 {
		t.Errorf("engine checks %d (want 2: present and legacy, both accepted by the schema)", st.checker.calls.Load())
	}

	present := st.record(t, repoPresent)
	if present.Declaration == nil || present.Declaration.Team != team || !present.Declaration.Accepted || present.Declaration.Language != "go" || present.Reality == nil {
		t.Errorf("present declaration: %+v", present.Declaration)
	}
	if present.Setup.Checks == nil || !present.Setup.Checks.Converged || present.Setup.CheckedAt == nil || len(present.Findings) != 1 || present.Findings[0].Kind != string(reconcile.FindingDefaultIcon) || present.Findings[0].Source != inventory.FindingSourceEngine {
		t.Errorf("present set-up state: %+v findings %+v", present.Setup, present.Findings)
	}
	// CircleCI without a token: the head's ci/circleci: statuses say it builds
	// the repository (the other systems' contexts are ignored, the worst
	// state counts), setup workflows are unknown until a reconciler run tells.
	if ci := present.CircleCI; ci == nil || !ci.Followed || ci.Source != inventory.CircleCISourceStatuses || ci.SetupWorkflows != nil || ci.Error != "" ||
		ci.Head == nil || ci.Head.State != "pending" || strings.Join(ci.Head.Contexts, ",") != circleBuild+","+circlePush || ci.Head.At.IsZero() ||
		strings.Join(ci.Unknown, ",") != inventory.CircleCIFactSetupWorkflows {
		t.Errorf("present circleci: %+v head=%+v", ci, ci.Head)
	}
	if !present.Catalog.Present || present.Mapping.Team != "bumblebee" {
		t.Errorf("present catalog/mapping: %+v %+v", present.Catalog, present.Mapping)
	}
	if r := present.Renovate; !r.Configured || !r.Enabled || !r.Preset || r.DashboardIssue == nil || r.DashboardIssue.Number != 3 || r.LastPullRequest == nil || r.LastPullRequest.Number != 10 || r.LastCommit == nil {
		t.Errorf("present renovate: %+v", r)
	}
	if rl := present.Reality; rl.LastPersonCommit == nil || rl.LastPersonCommit.Author != alice || rl.BotCommits != 1 || rl.OpenPullRequests.People != 1 || rl.OpenPullRequests.Bots != 1 || rl.OpenPullRequests.Renovate != 1 || rl.LatestRelease == nil || !rl.Has.CircleCI || !rl.Has.Helm || len(rl.CodeownersTeams) != 1 || len(rl.UnknownCodeownersTeams) != 0 {
		t.Errorf("present reality: %+v", rl)
	}
	if present.Orphan.Score != 0 || present.Source != inventory.SourceSweep || present.RefreshedAt.IsZero() {
		t.Errorf("present orphan/source: %+v %s", present.Orphan, present.Source)
	}

	gone := st.record(t, repoGone)
	if gone.Reality != nil || gone.Declaration == nil || len(gone.Findings) != 1 || gone.Findings[0].Kind != inventory.FindingDeclaredButGone || gone.Setup.Checks != nil {
		t.Errorf("gone: reality %v findings %+v", gone.Reality, gone.Findings)
	}
	legacy := st.record(t, repoLegacy)
	// An existing declaration is held to the schema alone: the legacy -app
	// name is a creation rule, not a finding, and the checks run for it.
	if legacy.Declaration == nil || !legacy.Declaration.Accepted || hasKind(legacy, string(reconcile.FindingEntryRefused)) || legacy.Setup.CheckError != "" {
		t.Errorf("legacy: %+v setup %+v findings %+v", legacy.Declaration, legacy.Setup, legacy.Findings)
	}
	// No ci/circleci: status on the head and no run: CircleCI does not build
	// it, setup workflows unknown — nothing guessed.
	if ci := legacy.CircleCI; ci == nil || ci.Followed || ci.Head != nil || ci.Source != inventory.CircleCISourceStatuses || strings.Join(ci.Unknown, ",") != inventory.CircleCIFactSetupWorkflows {
		t.Errorf("legacy circleci: %+v", ci)
	}
	stray := st.record(t, repoStray)
	if stray.Declaration != nil || !hasKind(stray, inventory.FindingUndeclaredOnGitHub) || stray.Orphan.Score != 100 || stray.Renovate.Enabled || len(stray.Reality.UnknownCodeownersTeams) != 1 || len(stray.Reality.OpenPullRequests.Onboarding) != 1 {
		t.Errorf("stray: %+v orphan %+v", stray.Findings, stray.Orphan)
	}
	for _, w := range []string{"no team file", "older than", "Renovate is not configured or disabled", "no release", "no CI", dissolvedTeam, "onboarding", "oldest open bot"} {
		if !strings.Contains(strings.Join(stray.Orphan.Reasons, "\n"), w) {
			t.Errorf("stray reason %q missing in %v", w, stray.Orphan.Reasons)
		}
	}
	archived := st.record(t, repoArchived)
	if !archived.Reality.IsArchived || archived.Orphan.Score != 0 || !hasKind(archived, inventory.FindingUndeclaredOnGitHub) {
		t.Errorf("archived: %+v %+v", archived.Orphan, archived.Findings)
	}
	if _, err := st.store.Get(ctx, org+"/giantswarm-repo-manager"); !errors.Is(err, inventory.ErrNotFound) {
		t.Errorf("the seeded stranger survived the sweep: %v", err)
	}
}

// TestInventoryToolsAndReconcilerRefresh: the tools over the store as alice,
// the on-demand refresh, the decision note, and the reconciler's trigger on
// the internal endpoint with its run stored — decision and run survive the
// next refresh.
func TestInventoryToolsAndReconcilerRefresh(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	if _, err := st.col.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	c := st.as(t, aliceToken)

	var all tools.Listing
	st.callJSON(t, c, tools.ToolListRepositories, nil, &all)
	if all.Total != 5 || all.Shown != 5 || all.Sweep == nil || all.Sweep.Repositories != 5 || all.Repositories[0].Repository != org+"/"+repoStray || all.Repositories[0].Orphan.Score != 100 || all.Repositories[0].Age == "" {
		t.Errorf("list: total %d shown %d first %+v", all.Total, all.Shown, all.Repositories[0])
	}
	var gone tools.Listing
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{"finding": inventory.FindingDeclaredButGone}, &gone)
	if gone.Matched != 1 || gone.Repositories[0].Repository != org+"/"+repoGone || !gone.Repositories[0].Gone {
		t.Errorf("list gone: %+v", gone.Repositories)
	}
	var undeclared tools.Listing
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{"undeclared": true}, &undeclared)
	if undeclared.Matched != 2 {
		t.Errorf("list undeclared: %+v", undeclared.Repositories)
	}
	var mine tools.Listing
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{argTeam: team, "limit": 2}, &mine)
	if mine.Matched != 3 || mine.Shown != 2 {
		t.Errorf("list team: matched %d shown %d", mine.Matched, mine.Shown)
	}

	var stray inventory.Record
	st.callJSON(t, c, tools.ToolGetRepository, map[string]any{kRepository: repoStray, "stalePeriodDays": 3650}, &stray)
	if stray.Orphan.Score >= 100 || stray.Orphan.StalePeriod != (3650*24*time.Hour).String() || stray.Age == "" {
		t.Errorf("get with a ten-year stale period: %+v age %q", stray.Orphan, stray.Age)
	}
	for _, r := range stray.Orphan.Reasons {
		if strings.Contains(r, "older than") {
			t.Errorf("stale reason kept at ten years: %s", r)
		}
	}

	var refreshed inventory.Record
	st.callJSON(t, c, tools.ToolRefreshRepository, map[string]any{kRepository: org + "/" + repoPresent}, &refreshed)
	if refreshed.Source != inventory.SourceRefresh || refreshed.Setup.Checks == nil || refreshed.Age == "" {
		t.Errorf("refresh: source %q setup %+v", refreshed.Source, refreshed.Setup)
	}
	var decided inventory.Record
	st.callJSON(t, c, tools.ToolDecideRepository, map[string]any{kRepository: repoPresent, "verdict": tools.DecisionKeep, "note": "the service is ours"}, &decided)
	if decided.Decision == nil || decided.Decision.Verdict != tools.DecisionKeep || !strings.Contains(decided.Decision.By, alice) || decided.Decision.Note != "the service is ours" {
		t.Errorf("decide: %+v", decided.Decision)
	}
	if text, isErr := call(t, c, tools.ToolDecideRepository, map[string]any{kRepository: repoPresent, "verdict": "drop"}); !isErr || !strings.Contains(text, "not defined") {
		t.Errorf("decide drop: isError=%v %s", isErr, text)
	}

	// The reconciler's run: the poller reads its reconcile-<name> artifact
	// from GitHub as the inventory App, the run lands in setup.lastRun, the
	// decision survives; the blob is fetched without the App's token.
	finished := time.Now().UTC().Truncate(time.Second)
	run := st.ghs.actions.addRun(t, runStatusCompleted, finished.Add(-time.Minute), artifactReport{name: repoPresent, finishedAt: finished,
		result: reconcile.Result{Repository: org + "/" + repoPresent, Mode: reconcile.ModeRepair, Converged: true,
			Steps: []reconcile.StepResult{{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictOK, Summary: "followed, setup workflows on, checkout key present"}}}})
	if p := st.poll(t); p.Runs != 1 || p.Artifacts != 1 || p.Skipped != 0 || len(p.Errors) != 0 {
		t.Fatalf("poll: %+v", p)
	}
	if n := st.ghs.actions.blobDownloads.Load(); n != 1 || st.ghs.actions.blobAuthorized.Load() {
		t.Errorf("blob downloads %d, authorized %v", n, st.ghs.actions.blobAuthorized.Load())
	}
	rec := st.record(t, repoPresent)
	// The run's circleci step is the second source of the CircleCI state:
	// setup workflows are known now, nothing is left unknown.
	if ci := rec.CircleCI; ci == nil || !ci.Followed || ci.Source != inventory.CircleCISourceBoth || ci.SetupWorkflows == nil || !*ci.SetupWorkflows || len(ci.Unknown) != 0 || ci.Head == nil {
		t.Errorf("circleci after the reconciler's run: %+v", ci)
	}
	if lr := rec.Setup.LastRun; lr == nil || lr.RunURL != runURL(run.ID) || lr.RunID != run.ID || lr.Attempt != 1 || !lr.Timestamp.Equal(finished) || lr.Result.Mode != reconcile.ModeRepair ||
		rec.Source != inventory.SourceReconciler || rec.Decision == nil {
		t.Errorf("reconciler refresh: setup %+v source %q decision %+v", rec.Setup, rec.Source, rec.Decision)
	}
	var again inventory.Record
	st.callJSON(t, c, tools.ToolRefreshRepository, map[string]any{kRepository: repoPresent}, &again)
	if again.Setup.LastRun == nil || again.Setup.LastRun.RunURL != runURL(run.ID) || again.Decision == nil {
		t.Errorf("run and decision did not survive the next refresh: %+v %+v", again.Setup.LastRun, again.Decision)
	}
	// A second poll, and a fresh collector over the same store (a restart),
	// read no artifact again: the cursor names the run attempt.
	if p := st.poll(t); p.Runs != 0 || p.Artifacts != 0 {
		t.Errorf("second poll: %+v", p)
	}
	if p, err := st.newCollector(0).PollReconciler(ctx); err != nil || p.Runs != 0 || p.Artifacts != 0 {
		t.Errorf("poll after a restart: %+v %v", p, err)
	}
	if n := st.ghs.actions.blobDownloads.Load(); n != 1 {
		t.Errorf("blob downloads after re-polls: %d", n)
	}
	// A re-run of the run is a new attempt with a new artifact.
	st.ghs.actions.rerun(t, run, artifactReport{name: repoPresent, finishedAt: finished.Add(time.Hour),
		result: reconcile.Result{Repository: org + "/" + repoPresent, Mode: reconcile.ModeRepair, Converged: true}})
	if p := st.poll(t); p.Runs != 1 || p.Artifacts != 1 {
		t.Errorf("poll after the re-run: %+v", p)
	}
	if lr := st.record(t, repoPresent).Setup.LastRun; lr == nil || lr.RunID != run.ID || lr.Attempt != 2 || st.ghs.actions.blobDownloads.Load() != 2 {
		t.Errorf("re-run not stored: %+v downloads %d", lr, st.ghs.actions.blobDownloads.Load())
	}

	if status, _ := st.internal(t, http.MethodGet, "/internal/sweep", "", nil); status != http.StatusUnauthorized {
		t.Errorf("internal without a token: %d", status)
	}
	if status, _ := st.internal(t, http.MethodPost, "/internal/refresh", internalToken, map[string]any{kRepository: repoPresent}); status != http.StatusNotFound {
		t.Errorf("the removed refresh endpoint answers %d, not 404", status)
	}
	status, body := st.internal(t, http.MethodGet, "/internal/sweep", internalToken, nil)
	var sw collect.SweepStatus
	if err := json.Unmarshal(body, &sw); status != http.StatusOK || err != nil || sw.Last == nil || sw.Last.Repositories != 5 || sw.Running {
		t.Errorf("internal sweep status: %d %s", status, body)
	}
}

// TestReconcileNowPendingUntilTheArtifactOrTheWindow: reconcile_repository
// leaves setup.pendingRun on the record and the poller polls fast; the run's
// artifact answers it; a run that does not report within the window is given
// up with the finding reconcile-run-missing naming the workflow's Actions
// page, which the next dispatch clears.
func TestReconcileNowPendingUntilTheArtifactOrTheWindow(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	if _, err := st.col.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	c := st.as(t, aliceToken)
	var d tools.Dispatch
	st.callJSON(t, c, tools.ToolReconcileRepository, map[string]any{argMode: modeCommit, kRepository: repoPresent}, &d)
	if !d.Dispatched || d.PendingRun == nil || d.PendingRun.By != alice || !strings.Contains(d.Then, "pendingRun") {
		t.Fatalf("dispatch: %+v", d)
	}
	if p := st.record(t, repoPresent).Setup.PendingRun; p == nil || p.By != alice {
		t.Fatalf("pendingRun on the record: %+v", p)
	}
	var listing tools.Listing
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{argTeam: team}, &listing)
	var shown bool
	for _, row := range listing.Repositories {
		shown = shown || (row.Repository == org+"/"+repoPresent && row.Setup.PendingRun != nil && row.Setup.PendingRun.By == alice)
	}
	if !shown {
		t.Errorf("list_repositories does not show the pending run: %+v", listing.Repositories)
	}
	// No run yet: the poll keeps it pending, and says so (the fast interval).
	if p := st.poll(t); p.Pending != 1 || p.Missing != 0 {
		t.Errorf("poll while pending: %+v", p)
	}
	// The run completes: its artifact answers the pending run.
	finished := time.Now().UTC().Truncate(time.Second)
	st.ghs.actions.addRun(t, runStatusCompleted, finished, artifactReport{name: repoPresent, finishedAt: finished, result: reconcile.Result{Converged: true}})
	if p := st.poll(t); p.Pending != 0 || p.Artifacts != 1 {
		t.Errorf("poll with the run: %+v", p)
	}
	if rec := st.record(t, repoPresent); rec.Setup.PendingRun != nil || rec.Setup.LastRun == nil || hasKind(rec, inventory.FindingReconcileRunMissing) {
		t.Errorf("after the run: %+v findings %+v", rec.Setup, rec.Findings)
	}
	// Dispatched again and no run within the window: given up with the finding.
	st.callJSON(t, c, tools.ToolReconcileRepository, map[string]any{argMode: modeCommit, kRepository: repoPresent}, &d)
	st.advance(14 * time.Minute)
	if p := st.poll(t); p.Pending != 1 || p.Missing != 0 {
		t.Errorf("poll inside the window: %+v", p)
	}
	st.advance(2 * time.Minute)
	if p := st.poll(t); p.Pending != 0 || p.Missing != 1 {
		t.Errorf("poll past the window: %+v", p)
	}
	rec := st.record(t, repoPresent)
	if rec.Setup.PendingRun != nil || rec.Setup.MissingRun == nil || rec.Setup.MissingRun.By != alice || !hasKind(rec, inventory.FindingReconcileRunMissing) {
		t.Fatalf("missing run: %+v findings %+v", rec.Setup, rec.Findings)
	}
	for _, f := range rec.Findings {
		if f.Kind == inventory.FindingReconcileRunMissing && (f.Source != inventory.FindingSourceInventory || !strings.Contains(f.Fix, "https://github.com/"+org+"/github/actions/workflows/"+reconcilerWorkflow)) {
			t.Errorf("finding: %+v", f)
		}
	}
	var got inventory.Record
	st.callJSON(t, c, tools.ToolGetRepository, map[string]any{kRepository: repoPresent}, &got)
	if !hasKind(&got, inventory.FindingReconcileRunMissing) {
		t.Errorf("get_repository does not carry the finding: %+v", got.Findings)
	}
	// The next dispatch forgets the missing run.
	st.callJSON(t, c, tools.ToolReconcileRepository, map[string]any{argMode: modeCommit, kRepository: repoPresent}, &d)
	if rec := st.record(t, repoPresent); rec.Setup.PendingRun == nil || rec.Setup.MissingRun != nil || hasKind(rec, inventory.FindingReconcileRunMissing) {
		t.Errorf("after the next dispatch: %+v findings %+v", rec.Setup, rec.Findings)
	}
}

// TestReconcilerPollReadsTheLastSevenDaysAndWaitsForOpenRuns: a first poll
// (no cursor) reads the runs of the last seven days only; a run still in
// progress holds the cursor where it stands, so its artifact is read when it
// completes even though newer runs completed before it.
func TestReconcilerPollReadsTheLastSevenDaysAndWaitsForOpenRuns(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	if _, err := st.col.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	report := func(finished time.Time) artifactReport {
		return artifactReport{name: repoPresent, finishedAt: finished, result: reconcile.Result{Converged: true}}
	}
	st.ghs.actions.addRun(t, runStatusCompleted, now.Add(-8*24*time.Hour), report(now.Add(-8*24*time.Hour)))
	recent := st.ghs.actions.addRun(t, runStatusCompleted, now.Add(-24*time.Hour), report(now.Add(-24*time.Hour)))
	if p := st.poll(t); p.Runs != 1 || p.Artifacts != 1 || st.ghs.actions.blobDownloads.Load() != 1 {
		t.Fatalf("first poll: %+v downloads %d", p, st.ghs.actions.blobDownloads.Load())
	}
	if lr := st.record(t, repoPresent).Setup.LastRun; lr == nil || lr.RunID != recent.ID {
		t.Errorf("the recent run is not the stored one: %+v", lr)
	}
	// A run created before the recent one, still running: the cursor waits
	// for it while newer runs come and go.
	running := st.ghs.actions.addRun(t, runStatusInProgress, now.Add(-2*time.Hour), report(now.Add(-time.Hour)))
	newer := st.ghs.actions.addRun(t, runStatusCompleted, now.Add(-time.Hour), report(now.Add(-30*time.Minute)))
	if p := st.poll(t); p.Runs != 1 || !p.Watermark.Equal(running.CreatedAt) {
		t.Errorf("poll with an open run: %+v (open run created %s)", p, running.CreatedAt)
	}
	cur, err := st.store.ReconcilerCursor(ctx)
	if err != nil || cur == nil || !cur.Watermark.Equal(running.CreatedAt) || len(cur.Consumed) != 1 {
		t.Errorf("cursor: %+v %v", cur, err)
	}
	if lr := st.record(t, repoPresent).Setup.LastRun; lr == nil || lr.RunID != newer.ID {
		t.Errorf("the newer run is not the stored one: %+v", lr)
	}
	// It completes: read, but its older result does not replace the newer run.
	st.ghs.actions.complete(running)
	if p := st.poll(t); p.Runs != 1 || p.Skipped != 1 || p.Artifacts != 0 || !p.Watermark.Equal(newer.CreatedAt) {
		t.Errorf("poll after the open run completed: %+v", p)
	}
	if lr := st.record(t, repoPresent).Setup.LastRun; lr == nil || lr.RunID != newer.ID {
		t.Errorf("an older run replaced the newer one: %+v", lr)
	}
	// Its artifact was downloaded — the finish time is inside the zip — and
	// then skipped.
	if n := st.ghs.actions.blobDownloads.Load(); n != 3 {
		t.Errorf("blob downloads: %d", n)
	}
}

// TestSweepStopsAtTheBudgetFloor: with the budget below the floor the sweep
// stops after the first query, keeps every existing record and reports how
// far it got.
func TestSweepStopsAtTheBudgetFloor(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	st.ghs.org.remaining = 1400
	col := st.newCollector(1500)
	sum, err := col.Sweep(ctx)
	if !errors.Is(err, collect.ErrBudget) || sum == nil {
		t.Fatalf("sweep: %v %+v", err, sum)
	}
	if sum.Repositories != 0 || sum.Removed != 0 || sum.GraphQL.Calls != 1 || len(sum.Errors) == 0 || !strings.Contains(sum.Errors[len(sum.Errors)-1], "budget floor") {
		t.Errorf("summary: %+v", sum)
	}
	if n, _ := st.store.Count(ctx); n != 1 {
		t.Errorf("records after the stop: %d, want the seeded 1", n)
	}
	last, _ := st.store.Sweep(ctx)
	if last == nil || last.Repositories != 0 {
		t.Errorf("summary not stored: %+v", last)
	}
}

func hasKind(r *inventory.Record, kind string) bool {
	for _, f := range r.Findings {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

func (st *stack) record(t *testing.T, name string) *inventory.Record {
	t.Helper()
	r, err := st.store.Get(context.Background(), org+"/"+name)
	if err != nil {
		t.Fatalf("record %s: %v", name, err)
	}
	return r
}

// callJSON calls a tool and decodes its JSON text; a tool error fails the test.
func (st *stack) callJSON(t *testing.T, c *client.Client, tool string, args map[string]any, out any) {
	t.Helper()
	text, isErr := call(t, c, tool, args)
	if isErr {
		t.Fatalf("%s: %s", tool, text)
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		t.Fatalf("%s: %v\n%s", tool, err, text)
	}
}

// internal calls an internal endpoint with the bearer token.
func (st *stack) internal(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, st.srv.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp.StatusCode, out.Bytes()
}
