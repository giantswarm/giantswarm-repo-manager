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
	if sum.Repositories != 5 || sum.Declared != 2 || sum.Undeclared != 2 || sum.Gone != 1 || sum.Archived != 1 || sum.EngineChecks != 1 || sum.Removed != 1 {
		t.Errorf("summary: %+v", sum)
	}
	if sum.GraphQL.Calls != 3 || sum.GraphQL.Cost != 3*fakeGraphQLCost || sum.GraphQL.Remaining == 0 || sum.Duration == "" {
		t.Errorf("graphql budget: %+v duration %q", sum.GraphQL, sum.Duration)
	}
	if n, _ := st.store.Count(ctx); n != 5 {
		t.Errorf("records: %d, want 5", n)
	}
	if st.checker.calls.Load() != 1 || st.circle.Calls() != 4 {
		t.Errorf("engine checks %d (want 1), circleci calls %d (want 4)", st.checker.calls.Load(), st.circle.Calls())
	}

	present := st.record(t, repoPresent)
	if present.Declaration == nil || present.Declaration.Team != team || !present.Declaration.Accepted || present.Declaration.Language != "go" || present.Reality == nil {
		t.Errorf("present declaration: %+v", present.Declaration)
	}
	if present.Setup.Checks == nil || !present.Setup.Checks.Converged || present.Setup.CheckedAt == nil || len(present.Findings) != 1 || present.Findings[0].Kind != string(reconcile.FindingDefaultIcon) || present.Findings[0].Source != inventory.FindingSourceEngine {
		t.Errorf("present set-up state: %+v findings %+v", present.Setup, present.Findings)
	}
	if present.CircleCI == nil || !present.CircleCI.Followed || present.CircleCI.LastPipeline == nil || !present.Catalog.Present || present.Mapping.Team != "bumblebee" {
		t.Errorf("present circleci/catalog/mapping: %+v %+v %+v", present.CircleCI, present.Catalog, present.Mapping)
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
	if legacy.Declaration == nil || legacy.Declaration.Accepted || !hasKind(legacy, inventory.FindingDeclarationRefused) || !strings.Contains(legacy.Setup.CheckError, "creation rules") {
		t.Errorf("legacy: %+v setup %+v findings %+v", legacy.Declaration, legacy.Setup, legacy.Findings)
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
	c := st.as(t, st.idp.mint(t, alice, aliceEmail))

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
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{"team": team, "limit": 2}, &mine)
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

	// The reconciler's trigger: its run lands in setup.lastRun, the decision survives.
	run := inventory.LastRun{RunURL: fakeRunURL, Timestamp: time.Now().UTC(), Result: reconcile.Result{Repository: org + "/" + repoPresent, Mode: reconcile.ModeRepair, Converged: true}}
	status, body := st.internal(t, http.MethodPost, "/internal/refresh", internalToken, collect.RefreshRequest{Repository: org + "/" + repoPresent, LastRun: &run})
	var rec inventory.Record
	if err := json.Unmarshal(body, &rec); status != http.StatusOK || err != nil {
		t.Fatalf("internal refresh: %d %s", status, body)
	}
	if rec.Setup.LastRun == nil || rec.Setup.LastRun.RunURL != fakeRunURL || rec.Setup.LastRun.Result.Mode != reconcile.ModeRepair || rec.Source != inventory.SourceReconciler || rec.Decision == nil || rec.Age == "" {
		t.Errorf("reconciler refresh: setup %+v source %q decision %+v", rec.Setup, rec.Source, rec.Decision)
	}
	var again inventory.Record
	st.callJSON(t, c, tools.ToolRefreshRepository, map[string]any{kRepository: repoPresent}, &again)
	if again.Setup.LastRun == nil || again.Setup.LastRun.RunURL != fakeRunURL || again.Decision == nil {
		t.Errorf("run and decision did not survive the next refresh: %+v %+v", again.Setup.LastRun, again.Decision)
	}
	if status, _ := st.internal(t, http.MethodPost, "/internal/refresh", "", collect.RefreshRequest{Repository: repoPresent}); status != http.StatusUnauthorized {
		t.Errorf("internal without a token: %d", status)
	}
	if status, _ := st.internal(t, http.MethodPost, "/internal/refresh", "wrong", collect.RefreshRequest{Repository: repoPresent}); status != http.StatusUnauthorized {
		t.Errorf("internal with a wrong token: %d", status)
	}
	if status, _ := st.internal(t, http.MethodPost, "/internal/refresh", internalToken, collect.RefreshRequest{Repository: "nobody-knows"}); status != http.StatusNotFound {
		t.Errorf("internal refresh of an unknown repository: %d", status)
	}
	status, body = st.internal(t, http.MethodGet, "/internal/sweep", internalToken, nil)
	var sw collect.SweepStatus
	if err := json.Unmarshal(body, &sw); status != http.StatusOK || err != nil || sw.Last == nil || sw.Last.Repositories != 5 || sw.Running {
		t.Errorf("internal sweep status: %d %s", status, body)
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
