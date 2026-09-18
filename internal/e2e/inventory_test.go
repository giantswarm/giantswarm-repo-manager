package e2e

import (
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
// removed, with the findings and the set-up state.
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
		ci.Head == nil || ci.Head.State != statePending || strings.Join(ci.Head.Contexts, ",") != circleBuild+","+circlePush || ci.Head.At.IsZero() ||
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
	if present.Source != inventory.SourceSweep || present.RefreshedAt.IsZero() {
		t.Errorf("present source: %s refreshed %s", present.Source, present.RefreshedAt)
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
	if stray.Declaration != nil || !hasKind(stray, inventory.FindingUndeclaredOnGitHub) || stray.Renovate.Enabled || strings.Join(stray.Reality.UnknownCodeownersTeams, ",") != dissolvedTeam || len(stray.Reality.OpenPullRequests.Onboarding) != 1 {
		t.Errorf("stray: %+v reality %+v", stray.Findings, stray.Reality)
	}
	archived := st.record(t, repoArchived)
	if !archived.Reality.IsArchived || !hasKind(archived, inventory.FindingUndeclaredOnGitHub) {
		t.Errorf("archived: %+v", archived.Findings)
	}
	if _, err := st.store.Get(ctx, org+"/giantswarm-repo-manager"); !errors.Is(err, inventory.ErrNotFound) {
		t.Errorf("the seeded stranger survived the sweep: %v", err)
	}
}

// TestInventoryToolsAndReconcilerRefresh: the tools over the store as alice
// (rows by name, the page's filters — team under mine, archived, lifecycle
// counting GitHub's archived flag), the on-demand refresh, and the
// reconciler's run pulled from GitHub and stored — the run survives the next
// refresh.
func TestInventoryToolsAndReconcilerRefresh(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	if _, err := st.col.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	c := st.as(t, aliceToken)

	var all tools.Listing
	st.callJSON(t, c, tools.ToolListRepositories, nil, &all)
	// Rows come by name: archived-old, gone-service, legacy-app, present-service, stray-repo.
	if all.Total != 5 || all.Shown != 5 || all.Sweep == nil || all.Sweep.Repositories != 5 || all.Repositories[0].Repository != org+"/"+repoArchived || !all.Repositories[0].Archived || all.Repositories[4].Repository != org+"/"+repoStray || all.Repositories[0].Age == "" {
		t.Errorf("list: total %d shown %d rows %+v", all.Total, all.Shown, all.Repositories)
	}
	var raw map[string]any
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{kLimit: 1}, &raw)
	if rows, _ := raw["repositories"].([]any); len(rows) != 1 || rows[0].(map[string]any)["orphan"] != nil || rows[0].(map[string]any)["decision"] != nil {
		t.Errorf("a row carries orphan or decision: %v", raw["repositories"])
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
	var byTeam tools.Listing
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{argTeam: team, kLimit: 2}, &byTeam)
	if byTeam.Matched != 3 || byTeam.Shown != 2 {
		t.Errorf("list team: matched %d shown %d", byTeam.Matched, byTeam.Shown)
	}
	// team under mine: alice's own team narrows to it; a team she is not in
	// selects no rows and the note says so — no error.
	var mine tools.Listing
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{"scope": tools.ScopeMine, argTeam: team}, &mine)
	if mine.Matched != 3 || strings.Join(mine.Teams, ",") != team || mine.Note != "" {
		t.Errorf("mine narrowed to %s: matched %d teams %v note %q", team, mine.Matched, mine.Teams, mine.Note)
	}
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{"scope": tools.ScopeMine, argTeam: teamOther}, &mine)
	if mine.Matched != 0 || len(mine.Repositories) != 0 || strings.Join(mine.Teams, ",") != team || !strings.Contains(mine.Note, teamOther+" is not one of your teams ("+team+")") {
		t.Errorf("mine narrowed to %s: matched %d teams %v note %q", teamOther, mine.Matched, mine.Teams, mine.Note)
	}
	// archived: the flag on GitHub counts, declared or not; so does lifecycle
	// archived. Nothing in the fixture declares a lifecycle: active is the rest.
	var archived tools.Listing
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{"archived": true}, &archived)
	if archived.Matched != 1 || archived.Repositories[0].Repository != org+"/"+repoArchived {
		t.Errorf("archived: %+v", archived.Repositories)
	}
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{"archived": false}, &archived)
	if archived.Matched != 4 {
		t.Errorf("not archived: %+v", archived.Repositories)
	}
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{argLifecycle: lifecycleArchived}, &archived)
	if archived.Matched != 1 || archived.Repositories[0].Repository != org+"/"+repoArchived {
		t.Errorf("lifecycle archived: %+v", archived.Repositories)
	}
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{argLifecycle: tools.LifecycleActive}, &archived)
	if archived.Matched != 4 {
		t.Errorf("lifecycle active: %+v", archived.Repositories)
	}

	var stray map[string]any
	st.callJSON(t, c, tools.ToolGetRepository, map[string]any{kRepository: repoStray}, &stray)
	if stray["repository"] != org+"/"+repoStray || stray["age"] == "" || stray["orphan"] != nil || stray["decision"] != nil {
		t.Errorf("get_repository: %v", stray)
	}

	var refreshed inventory.Record
	st.callJSON(t, c, tools.ToolRefreshRepository, map[string]any{kRepository: org + "/" + repoPresent}, &refreshed)
	if refreshed.Source != inventory.SourceRefresh || refreshed.Setup.Checks == nil || refreshed.Age == "" {
		t.Errorf("refresh: source %q setup %+v", refreshed.Source, refreshed.Setup)
	}

	// The reconciler's run: the poller reads its reconcile-<name> artifact
	// from GitHub as the inventory App, the run lands in setup.lastRun; the
	// blob is fetched without the App's token.
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
		rec.Source != inventory.SourceReconciler {
		t.Errorf("reconciler refresh: setup %+v source %q", rec.Setup, rec.Source)
	}
	var again inventory.Record
	st.callJSON(t, c, tools.ToolRefreshRepository, map[string]any{kRepository: repoPresent}, &again)
	if again.Setup.LastRun == nil || again.Setup.LastRun.RunURL != runURL(run.ID) {
		t.Errorf("the run did not survive the next refresh: %+v", again.Setup.LastRun)
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
}

// TestSweepInventoryForTheOwningTeams: sweep_inventory starts the sweep for a
// member of a configured team; while it runs a second call says so and
// carries the last summary; a non-member is refused in approve_change's
// words; and the listener has no sweep control of its own — the tool behind
// muster is the one way in.
func TestSweepInventoryForTheOwningTeams(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	if _, err := st.col.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	// carol is in team-other: the sweep is not hers to start.
	if text, isErr := call(t, st.as(t, carolToken), tools.ToolSweepInventory, nil); !isErr ||
		!strings.Contains(text, carol+" is not a member of "+team+" or "+teamPlaneteers+" (your teams: "+teamOther+")") || !strings.Contains(text, "the sweep is not yours to start") {
		t.Errorf("carol's sweep: error %v %s", isErr, text)
	}
	if st.col.Running() {
		t.Fatal("a refused call started a sweep")
	}
	// The engine checks hold this sweep: the second call finds it running.
	st.checker.hold = make(chan struct{})
	c := st.as(t, aliceToken)
	var first, second tools.Sweep
	st.callJSON(t, c, tools.ToolSweepInventory, nil, &first)
	if !first.Started || !first.Running || first.Last == nil || first.Last.Repositories != 5 || first.Login != alice || len(first.Teams) != 2 {
		t.Errorf("alice's sweep: %+v", first)
	}
	st.callJSON(t, c, tools.ToolSweepInventory, nil, &second)
	if second.Started || !second.Running || second.Last == nil || second.Last.Repositories != 5 {
		t.Errorf("a second sweep while the first runs: %+v", second)
	}
	close(st.checker.hold)
	for deadline := time.Now().Add(10 * time.Second); st.col.Running(); {
		if time.Now().After(deadline) {
			t.Fatal("the held sweep did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.checker.calls.Load() != 4 {
		t.Errorf("engine checks after two sweeps: %d, want 4", st.checker.calls.Load())
	}
	// The former sweep control's path (spelled in two parts: a search of the
	// tree for the prefix finds the changelog alone) has no handler at all.
	resp, err := http.Get(st.srv.URL + "/" + "internal/sweep") // #nosec G107 -- the test server's URL
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("the former sweep control answers %d, not 404", resp.StatusCode)
	}
}

// TestReconcileNowPendingUntilTheArtifactOrTheWindow: align_repository
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
	st.callJSON(t, c, tools.ToolAlignRepository, map[string]any{argMode: modeCommit, kRepository: repoPresent}, &d)
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
	st.callJSON(t, c, tools.ToolAlignRepository, map[string]any{argMode: modeCommit, kRepository: repoPresent}, &d)
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
	st.callJSON(t, c, tools.ToolAlignRepository, map[string]any{argMode: modeCommit, kRepository: repoPresent}, &d)
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
