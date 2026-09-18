package e2e

import (
	"fmt"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-repo-manager/internal/tools"
)

// Two writes to neighbouring entries of one team file: alice's archive of
// present-service and her deprecation of legacy-app, both one commit on a
// fresh branch of main. The one that lands first moves main under the other,
// which GitHub then reports conflicting (mergeable: false).

// teamFilePath is the file both pull requests change.
var teamFilePath = "repositories/" + team + ".yaml"

// openNeighbours opens alice's two pull requests and returns them, the
// archive first; dave is the member who approves.
func openNeighbours(t *testing.T, st *stack) (archive, deprecate *fakePullRequest) {
	t.Helper()
	st.ghs.teams[dave] = []string{team}
	c := st.as(t, aliceToken)
	var out tools.Committed
	st.callJSON(t, c, tools.ToolSetLifecycle, map[string]any{argMode: modeCommit, kRepository: repoPresent, argLifecycle: lifecycleArchived}, &out)
	st.callJSON(t, c, tools.ToolSetLifecycle, map[string]any{argMode: modeCommit, kRepository: repoLegacy, argLifecycle: lifecycleDeprecated}, &out)
	prs := st.ghs.files.pullRequests()
	if len(prs) != 2 {
		t.Fatalf("%d pull requests, want 2", len(prs))
	}
	return prs[0], prs[1]
}

// mainCarriesBothChanges: the team file on main reads as the fixture plus
// the two lifecycle lines, the archive's opt-in to alignment (present-service
// had none) and nothing else.
func mainCarriesBothChanges(t *testing.T, st *stack) {
	t.Helper()
	final := string(st.ghs.files.at(mainBranch, teamFilePath))
	if !strings.Contains(final, "lifecycle: archived") || !strings.Contains(final, "lifecycle: deprecated") {
		t.Fatalf("main after both merges:\n%s", final)
	}
	for _, line := range strings.Split(strings.TrimSpace(teamFile), "\n") {
		if !strings.Contains(final, line) {
			t.Errorf("main lost the line %q:\n%s", line, final)
		}
	}
	if got, want := strings.Count(final, "\n"), strings.Count(teamFile, "\n")+3; got != want {
		t.Errorf("main has %d lines, want %d (the fixture plus two lifecycle lines and the archive's align: true):\n%s", got, want, final)
	}
	if !strings.Contains(final, "  lifecycle: archived\n  align: true\n") {
		t.Errorf("main should carry the archive's opt-in:\n%s", final)
	}
}

// TestApproveRerendersAPullRequestWhoseBaseMoved: the archive pull request
// awaits its review when the deprecation of the neighbouring entry merges;
// GitHub reports it conflicting. The poller notes that on the record and
// posts nothing — the standing ask is the click. dave's Approve re-renders
// the pull request on main first (the archive re-applied to the file as it
// reads now, the branch force-pushed as dave), then approves, and the pull
// request merges: main carries both changes, the answer names the re-render.
func TestApproveRerendersAPullRequestWhoseBaseMoved(t *testing.T) {
	st := newStack(t)
	archive, deprecate := openNeighbours(t, st)
	c := st.as(t, daveToken)
	var a tools.Approval
	st.callJSON(t, c, tools.ToolApproveChange, map[string]any{argMode: modeCommit, argPullRequest: deprecate.Number}, &a)
	if !a.Merged || a.Rerendered != nil || a.RerenderError != "" {
		t.Fatalf("the neighbour's approval: %+v", a)
	}
	if st.ghs.files.mergeable(archive.Number) {
		t.Fatal("fake: the archive pull request should conflict once its neighbour merged")
	}
	// The poller: the conflict is on the record; the pull request awaits its
	// review, so no second ask.
	if p := st.poll(t); p.Conflicting != 1 {
		t.Errorf("poll: %+v", p)
	}
	if rec := st.record(t, repoPresent); !rec.Setup.PendingRun.Follows(archive.Number) || !rec.Setup.PendingRun.Conflicting() {
		t.Fatalf("record while conflicting: %+v", rec.Setup.PendingRun)
	}
	if asks, _ := st.gw.posted(); len(asks) != 2 {
		t.Fatalf("%d asks after the poll, want the two writes' only", len(asks))
	}

	// dave clicks the standing ask.
	st.callJSON(t, c, tools.ToolApproveChange, map[string]any{argMode: modeCommit, argPullRequest: archive.Number}, &a)
	rr := a.Rerendered
	if rr == nil || rr.Number != archive.Number || rr.Branch != archive.Head || rr.Base == "" || rr.Commit == "" ||
		strings.Join(rr.Files, ",") != teamFilePath || strings.Join(rr.Entries, ",") != repoPresent || a.RerenderError != "" {
		t.Fatalf("re-render: %+v", a)
	}
	if !a.Merged || !archive.Merged || !archive.AutoMerge || len(archive.Reviews) != 1 || archive.Reviews[0].User != dave {
		t.Errorf("landing: %+v fake merged=%v autoMerge=%v reviews=%v", a, archive.Merged, archive.AutoMerge, archive.Reviews)
	}
	if want := fmt.Sprintf("Approved as %s and merged: %s/github#%d, re-rendered on %s first (a neighbouring entry had changed).", dave, org, archive.Number, mainBranch); a.Message != want {
		t.Errorf("message %q, want %q", a.Message, want)
	}
	w := st.ghs.written()
	if index(w, "PATCH /api/v3/repos/"+org+"/github/git/refs/heads/"+archive.Head) < 0 {
		t.Errorf("no force-push of %s among %v", archive.Head, w)
	}
	mainCarriesBothChanges(t, st)
	if rec := st.record(t, repoPresent); !rec.Setup.PendingRun.Follows(archive.Number) || rec.Setup.PendingRun.Conflicting() {
		t.Errorf("record after the re-render: %+v", rec.Setup.PendingRun)
	}
}

// TestPollerAsksAgainForAnApprovedPullRequestThatConflicts: dave approves the
// archive while its checks still run — the review lands, auto-merge waits —
// then the neighbouring deprecation merges under it. Auto-merge cannot fire
// and the ask is spent, so the poller, finding the pull request conflicting
// and approved, asks the team once more, once; the Approve of that ask
// re-renders the pull request and lands it.
func TestPollerAsksAgainForAnApprovedPullRequestThatConflicts(t *testing.T) {
	st := newStack(t)
	archive, deprecate := openNeighbours(t, st)
	c := st.as(t, daveToken)
	st.ghs.files.setChecksPending(archive.Number, true)
	var a tools.Approval
	st.callJSON(t, c, tools.ToolApproveChange, map[string]any{argMode: modeCommit, argPullRequest: archive.Number}, &a)
	if a.Merged || !a.AutoMerge || a.Rerendered != nil {
		t.Fatalf("approval while the checks run: %+v", a)
	}
	st.callJSON(t, c, tools.ToolApproveChange, map[string]any{argMode: modeCommit, argPullRequest: deprecate.Number}, &a)
	if !a.Merged {
		t.Fatalf("the neighbour's approval: %+v", a)
	}

	if p := st.poll(t); p.Conflicting != 1 || p.Pending != 1 {
		t.Errorf("poll: %+v", p)
	}
	asks, _ := st.gw.posted()
	if len(asks) != 3 {
		t.Fatalf("%d asks, want the two writes' and the poller's", len(asks))
	}
	ask := asks[2]
	text, _ := ask["text"].(string)
	for _, want := range []string{alice + "'s approved archive pull request for " + org + "/" + repoPresent + " conflicts with " + mainBranch,
		"a neighbouring entry changed first", "Approve re-renders it on " + mainBranch + " as you and lands it.", "A member of " + team + " other than " + alice + " approves."} {
		if !strings.Contains(text, want) {
			t.Errorf("ask %q lacks %q", text, want)
		}
	}
	approve, _ := ask["approve"].(map[string]any)
	if ask[kChannel] != bumblebeeChannel || !strings.HasSuffix(ask["link"].(string), fmt.Sprintf("/pull/%d", archive.Number)) ||
		approve["tool"] != "x_giantswarm-repo-manager_approve_change" || approve["arguments"].(map[string]any)[argPullRequest] != float64(archive.Number) {
		t.Errorf("ask: %v", ask)
	}
	// Once: the next poll finds the conflict noted already.
	if p := st.poll(t); p.Conflicting != 1 {
		t.Errorf("second poll: %+v", p)
	}
	if asks, _ := st.gw.posted(); len(asks) != 3 {
		t.Fatalf("%d asks after the second poll, want 3", len(asks))
	}

	// The click, the checks done by now: re-rendered, approved again, merged.
	st.ghs.files.setChecksPending(archive.Number, false)
	st.callJSON(t, c, tools.ToolApproveChange, map[string]any{argMode: modeCommit, argPullRequest: archive.Number}, &a)
	if a.Rerendered == nil || !a.Merged || !archive.Merged || strings.Join(a.Rerendered.Entries, ",") != repoPresent {
		t.Fatalf("the second click: %+v merged=%v", a, archive.Merged)
	}
	mainCarriesBothChanges(t, st)
	if rec := st.record(t, repoPresent); rec.Setup.PendingRun.Conflicting() {
		t.Errorf("record after the re-render: %+v", rec.Setup.PendingRun)
	}
	if p := st.poll(t); p.Conflicting != 0 || p.Pending != 2 {
		t.Errorf("poll after both merged: %+v", p)
	}
}
