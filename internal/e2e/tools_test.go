package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/tools"
)

// The nine tools against the fakes: GitHub (the person behind their user
// token, org GraphQL, the team-files repository with its git data, pull
// requests, reviews and dispatches), klaus-gateway's team-review endpoint and
// a seeded store.

// shinyService is the repository the creation tests create.
const shinyService = "shiny-service"

var newEntry = map[string]any{
	kName: shinyService, kComponentType: kService,
	kGen: map[string]any{kLanguage: kGo, kFlavours: []any{kApp}, kCI: map[string]any{kChartName: shinyService}},
}

// TestValidateRepositoryRendersAndRefuses: the dry run renders an accepted
// entry with its template, refuses a bad one as data, and carries the guard
// notices — team-review for an outsider, batch-review above three entries.
func TestValidateRepositoryRendersAndRefuses(t *testing.T) {
	st := newStack(t)
	asAlice := st.as(t, aliceToken)
	var v tools.Validation
	st.callJSON(t, asAlice, tools.ToolValidateRepository, map[string]any{argTeam: team, argEntry: newEntry}, &v)
	if !v.Accepted || len(v.Entries) != 1 || v.Entries[0].Template != "giantswarm/template" || v.Entries[0].NameCheck.Verdict != "free" ||
		len(v.Notices) != 0 || !v.MachineApproved || v.AuthorLogin != alice || v.TeamsSource != kGitHub {
		t.Errorf("member's valid entry: %+v notices=%v teams=%v/%s", v.Result, v.Notices, v.AuthorTeams, v.TeamsSource)
	}

	bad := map[string]any{kName: "shiny-app", kComponentType: kService, kGen: map[string]any{kLanguage: kGo, kFlavours: []any{kApp}}}
	st.callJSON(t, asAlice, tools.ToolValidateRepository, map[string]any{argTeam: team, argEntry: bad}, &v)
	if v.Accepted || len(v.Entries) != 1 || len(v.Entries[0].Problems) == 0 {
		t.Errorf("refusal should be data: %+v", v.Result)
	}

	asCarol := st.as(t, carolToken)
	st.callJSON(t, asCarol, tools.ToolValidateRepository, map[string]any{argTeam: team, argEntry: newEntry}, &v)
	if !v.Accepted || v.MachineApproved || len(v.Notices) != 1 || string(v.Notices[0].Kind) != "team-review" {
		t.Errorf("outsider should see the team-review notice: %+v", v.Notices)
	}

	four := []any{}
	for _, n := range []string{"a-one", "a-two", "a-three", "a-four"} {
		e := map[string]any{kName: n, kComponentType: kService, kGen: map[string]any{kLanguage: kGo, kFlavours: []any{"generic"}}}
		four = append(four, e)
	}
	st.callJSON(t, asAlice, tools.ToolValidateRepository, map[string]any{argTeam: team, "entries": four}, &v)
	if len(v.Entries) != 4 || !hasNotice(v.Notices, "batch-review") {
		t.Errorf("four entries should carry batch-review: %d entries, %+v", len(v.Entries), v.Notices)
	}
}

// TestCreateDryRunOpensNothingAndApplyIsRefusedEverywhere.
func TestCreateDryRunOpensNothingAndApplyIsRefusedEverywhere(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	text, isErr := call(t, c, tools.ToolCreateRepository, map[string]any{argDryRun: true, argTeam: team, argEntry: newEntry})
	if isErr || !strings.Contains(text, `"accepted": true`) {
		t.Errorf("dry run: isError=%v %s", isErr, text)
	}
	if n := len(st.ghs.files.pullRequests()); n != 0 {
		t.Errorf("dry run opened %d pull requests", n)
	}
	for _, tool := range tools.WriteToolNames() {
		text, isErr := call(t, c, tool, map[string]any{argMode: modeApply, argTeam: team, argEntry: newEntry, kRepository: repoPresent, argToTeam: teamPlaneteers, argLifecycle: lifecycleArchived, argPullRequest: 1})
		if !isErr || !strings.Contains(text, `mode "apply" is refused`) {
			t.Errorf("%s: apply should be refused: isError=%v %s", tool, isErr, text)
		}
	}
	if n := len(st.ghs.files.pullRequests()); n != 0 {
		t.Errorf("apply attempts opened %d pull requests", n)
	}
}

// The writes of a creation, in order: the repository, its scaffold (the
// branch moved to the scaffold commit), the declaration pull request.
var (
	writeCreate   = "POST /api/v3/orgs/" + org + "/repos"
	writeScaffold = "POST /api/v3/repos/" + org + "/shiny-service/git/commits"
	writePull     = "POST /api/v3/repos/" + org + "/github/pulls"
)

// index is where s first appears in list, -1 when it does not.
func index(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

// assertNoCreationWrites fails when any write of a creation happened.
func assertNoCreationWrites(t *testing.T, st *stack) {
	t.Helper()
	for _, w := range st.ghs.written() {
		if w == writeCreate || strings.HasPrefix(w, "POST /api/v3/repos/"+org+"/shiny-service/") || w == writePull {
			t.Errorf("written: %s", w)
		}
	}
	if st.ghs.repos.get(shinyService) != nil && len(st.ghs.repos.get(shinyService).files) != 1 {
		t.Errorf("shiny-service changed: %v", st.ghs.repos.get(shinyService).files)
	}
}

// TestCreateCommitCreatesScaffoldsThenOpensThePullRequestAsThePerson: alice
// (an owner of the org) gets the repository created as her — she administers
// it — its scaffold pushed as one commit on main, and then the creation-only
// pull request under her name, in that order; the result names all three.
func TestCreateCommitCreatesScaffoldsThenOpensThePullRequestAsThePerson(t *testing.T) {
	st := newStack(t)
	var out tools.Created
	st.callJSON(t, st.as(t, aliceToken), tools.ToolCreateRepository, map[string]any{argMode: modeCommit, argTeam: team, argEntry: newEntry, argReason: "the shiny thing"}, &out)

	repo := st.ghs.repos.get(shinyService)
	if repo == nil || !repo.admins[alice] {
		t.Fatalf("shiny-service created as alice: %+v", repo)
	}
	if _, ok := repo.files["CODEOWNERS"]; !ok || repo.files[readmeFile] != scaffoldFiles[readmeFile] {
		t.Errorf("the scaffold replaced the initial README: %v", repo.files)
	}
	if len(out.Repositories) != 1 || !out.Repositories[0].Created || out.Repositories[0].Repository != "https://github.com/"+org+"/shiny-service" ||
		out.Repositories[0].ScaffoldCommit == "" || out.Repositories[0].ScaffoldCommit != repo.head || out.FirstRelease != tools.FirstRelease {
		t.Errorf("result: %+v (head %s)", out.Repositories, repo.head)
	}
	if steps := out.Repositories[0].Steps; len(steps) != 2 || steps[0].Step != reconcile.StepCreate || steps[0].Verdict != reconcile.VerdictRepaired ||
		steps[1].Step != reconcile.StepScaffold || steps[1].Verdict != reconcile.VerdictRepaired {
		t.Errorf("steps: %+v", steps)
	}
	prs := st.ghs.files.pullRequests()
	if len(prs) != 1 || out.PullRequest == nil || out.PullRequest.Number != prs[0].Number || prs[0].Author != alice || out.PullRequest.Author != alice || out.PullRequest.Existing {
		t.Fatalf("pull request as alice: %+v %+v", out.PullRequest, prs)
	}
	pr := prs[0]
	file := string(pr.Files["repositories/"+team+".yaml"])
	if !strings.Contains(pr.Title, "declare shiny-service for "+team) || !strings.Contains(file, "- name: shiny-service") || !strings.Contains(file, "- name: "+repoPresent) ||
		!strings.Contains(pr.Body, "the shiny thing") || !strings.Contains(pr.Body, "team="+team) || !strings.Contains(pr.Body, "created and scaffolded as @"+alice) || !strings.Contains(pr.Body, "never creates") {
		t.Errorf("pull request: %q\n%s\n%s", pr.Title, pr.Body, file)
	}
	w := st.ghs.written()
	if c, s, p := index(w, writeCreate), index(w, writeScaffold), index(w, writePull); c < 0 || s < c || p < s {
		t.Errorf("order create → scaffold → pull request: %v", w)
	}
}

// TestCreateDryRunPlansTheThreeWrites: the dry run names the create and
// scaffold steps the engine would run as alice and the pull request with
// the reason in its body, and writes nothing; validate_repository takes the
// same arguments and carries the same plan, without a reason as before.
func TestCreateDryRunPlansTheThreeWrites(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	for _, tool := range []string{tools.ToolCreateRepository, tools.ToolValidateRepository} {
		var plain tools.Validation
		st.callJSON(t, c, tool, map[string]any{argDryRun: true, argTeam: team, argEntry: newEntry}, &plain)
		if plain.Creation == nil || plain.Creation.PullRequest == nil || strings.Contains(plain.Creation.PullRequest.Body, "Reason:") {
			t.Errorf("%s: without a reason: %+v", tool, plain.Creation)
		}
		var v tools.Validation
		st.callJSON(t, c, tool, map[string]any{argDryRun: true, argTeam: team, argEntry: newEntry, argReason: "the shiny thing"}, &v)
		if !v.Accepted || v.Creation == nil || v.Creation.Refusal != "" || len(v.Creation.Repositories) != 1 || v.Creation.PullRequest == nil {
			t.Fatalf("%s: creation plan: %+v", tool, v.Creation)
		}
		steps := v.Creation.Repositories[0].Steps
		if len(steps) != 2 || steps[0].Step != reconcile.StepCreate || steps[0].Verdict != reconcile.VerdictDrift || len(steps[0].Changes) != 1 || !strings.Contains(steps[0].Changes[0], "create "+org+"/shiny-service") ||
			steps[1].Step != reconcile.StepScaffold || steps[1].Verdict != reconcile.VerdictDrift || len(steps[1].Changes) != 1 || !strings.Contains(steps[1].Changes[0], "push it as the first commit") {
			t.Errorf("%s: steps: %+v", tool, steps)
		}
		if pr := v.Creation.PullRequest; pr.Repository != org+"/github" || pr.Branch != "reposetup/create-shiny-service" || pr.As != alice || len(pr.Files) != 1 || !strings.Contains(pr.Body, "\n\nReason: the shiny thing\n\n") {
			t.Errorf("%s: pull request plan: %+v", tool, pr)
		}
	}
	assertNoCreationWrites(t, st)
	if st.ghs.repos.get(shinyService) != nil || len(st.ghs.files.pullRequests()) != 0 {
		t.Error("the dry run wrote")
	}
}

// TestCreateRefusesANonOwnerBeforeAnyWrite: dave is a member, not an owner,
// of the org; the dry run says so with the engine's text, the commit is
// refused with the same text and nothing is written.
func TestCreateRefusesANonOwnerBeforeAnyWrite(t *testing.T) {
	st := newStack(t)
	c := st.as(t, daveToken)
	want := reconcile.NotOwnerRefusal(org)
	var v tools.Validation
	st.callJSON(t, c, tools.ToolValidateRepository, map[string]any{argTeam: team, argEntry: newEntry}, &v)
	if v.Creation == nil || v.Creation.Refusal != want || len(v.Creation.Repositories) != 0 {
		t.Errorf("dry run for a non-owner: %+v", v.Creation)
	}
	text, isErr := call(t, c, tools.ToolCreateRepository, map[string]any{argMode: modeCommit, argTeam: team, argEntry: newEntry})
	if !isErr || !strings.Contains(text, want) {
		t.Errorf("commit for a non-owner: isError=%v %s", isErr, text)
	}
	assertNoCreationWrites(t, st)
	if st.ghs.repos.get(shinyService) != nil || len(st.ghs.files.pullRequests()) != 0 {
		t.Error("a non-owner's creation wrote")
	}
}

// TestCreateRefusesATakenNameBeforeAnyWrite: shiny-service exists and is
// someone else's; the name check refuses the entry and nothing is written.
func TestCreateRefusesATakenNameBeforeAnyWrite(t *testing.T) {
	st := newStack(t)
	st.ghs.repos.add(shinyService, "mallory")
	text, isErr := call(t, st.as(t, aliceToken), tools.ToolCreateRepository, map[string]any{argMode: modeCommit, argTeam: team, argEntry: newEntry})
	if !isErr || !strings.Contains(text, "the engine refuses the declaration") || !strings.Contains(text, "shiny-service: name:") || !strings.Contains(text, "nothing was created") {
		t.Errorf("taken name: isError=%v %s", isErr, text)
	}
	assertNoCreationWrites(t, st)
	if len(st.ghs.files.pullRequests()) != 0 {
		t.Error("a refused creation opened a pull request")
	}
}

// TestCreateResumesAfterAPartialFailure: shiny-service exists, alice
// administers it and only the README of its creation is on main — a creation
// that stopped after the create step. The dry run resumes it (validated for
// an existing repository); the commit skips the create step, pushes the
// scaffold and opens the pull request; a second commit reports the open pull
// request and writes nothing.
func TestCreateResumesAfterAPartialFailure(t *testing.T) {
	st := newStack(t)
	st.ghs.repos.add(shinyService, alice)
	c := st.as(t, aliceToken)

	var v tools.Validation
	st.callJSON(t, c, tools.ToolCreateRepository, map[string]any{argDryRun: true, argTeam: team, argEntry: newEntry}, &v)
	if !v.Accepted || v.Creation == nil || len(v.Creation.Resumed) != 1 || v.Creation.Resumed[0] != shinyService || v.Creation.Repositories[0].Repository == "" {
		t.Fatalf("resumed dry run: accepted=%v %+v", v.Accepted, v.Creation)
	}
	if steps := v.Creation.Repositories[0].Steps; steps[0].Summary != "exists" || steps[1].Verdict != reconcile.VerdictDrift {
		t.Errorf("resumed plan: %+v", steps)
	}

	var out tools.Created
	st.callJSON(t, c, tools.ToolCreateRepository, map[string]any{argMode: modeCommit, argTeam: team, argEntry: newEntry}, &out)
	repo := st.ghs.repos.get(shinyService)
	if len(out.Repositories) != 1 || out.Repositories[0].Created || out.Repositories[0].ScaffoldCommit != repo.head || out.Repositories[0].Steps[0].Summary != "exists" ||
		out.Repositories[0].Steps[1].Verdict != reconcile.VerdictRepaired {
		t.Errorf("resumed commit: %+v (head %s)", out.Repositories, repo.head)
	}
	if _, ok := repo.files["CODEOWNERS"]; !ok {
		t.Errorf("the scaffold was pushed: %v", repo.files)
	}
	if w := st.ghs.written(); index(w, writeCreate) >= 0 || index(w, writeScaffold) < 0 || index(w, writePull) < index(w, writeScaffold) {
		t.Errorf("resume: no create, scaffold then pull request: %v", w)
	}
	prs := st.ghs.files.pullRequests()
	if len(prs) != 1 || out.PullRequest == nil || out.PullRequest.Existing {
		t.Fatalf("pull request: %+v %+v", out.PullRequest, prs)
	}

	before := len(st.ghs.written())
	var again tools.Created
	st.callJSON(t, c, tools.ToolCreateRepository, map[string]any{argMode: modeCommit, argTeam: team, argEntry: newEntry}, &again)
	if again.Repositories[0].Created || again.Repositories[0].Steps[1].Summary != "present" || again.Repositories[0].ScaffoldCommit != repo.head ||
		again.PullRequest == nil || !again.PullRequest.Existing || again.PullRequest.Number != prs[0].Number || again.PullRequest.Author != alice {
		t.Errorf("second run: %+v %+v", again.Repositories, again.PullRequest)
	}
	if len(st.ghs.files.pullRequests()) != 1 || len(st.ghs.written()) != before {
		t.Errorf("second run wrote: %v", st.ghs.written()[before:])
	}
}

// TestTransferNamesBothTeams: the entry leaves one file and enters the
// other in one pull request; the receiving team gets the ask, the giving
// team the notice.
func TestTransferNamesBothTeams(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	var plan tools.Plan
	st.callJSON(t, c, tools.ToolTransferRepository, map[string]any{argDryRun: true, kRepository: repoPresent, argToTeam: teamPlaneteers}, &plan)
	if plan.Team != teamPlaneteers || plan.FromTeam != team || len(plan.PullRequest.Files) != 2 || plan.Ask == nil || !plan.Ask.Deliverable || plan.Ask.Channel != planeteersChannel ||
		plan.Notice == nil || plan.Notice.Channel != bumblebeeStandup || plan.PullRequest.As != alice || !strings.HasPrefix(plan.Ask.Text, alice+" asks to transfer") {
		t.Fatalf("plan: %+v ask=%+v notice=%+v", plan, plan.Ask, plan.Notice)
	}
	var out tools.Committed
	st.callJSON(t, c, tools.ToolTransferRepository, map[string]any{argMode: modeCommit, kRepository: repoPresent, argToTeam: teamPlaneteers}, &out)
	pr := st.ghs.files.pullRequests()[0]
	from, to := string(pr.Files["repositories/"+team+".yaml"]), string(pr.Files["repositories/"+teamPlaneteers+".yaml"])
	if strings.Contains(from, repoPresent) || !strings.Contains(from, repoLegacy) || !strings.Contains(to, "- name: "+repoPresent) || !strings.Contains(to, "planet-service") ||
		!strings.Contains(pr.Title, "from "+team+" to "+teamPlaneteers) || !strings.Contains(pr.Body, "giving team: **"+team+"**") || !strings.Contains(pr.Body, "receiving team: **"+teamPlaneteers+"**") {
		t.Errorf("transfer pull request %q\n%s\n--- from ---\n%s\n--- to ---\n%s", pr.Title, pr.Body, from, to)
	}
	asks, notices := st.gw.posted()
	if len(asks) != 1 || asks[0][kChannel] != planeteersChannel || asks[0]["team"] != teamPlaneteers || len(notices) != 1 || notices[0][kChannel] != bumblebeeStandup ||
		out.Ask == nil || !out.Ask.Delivered || out.Ask.Channel != planeteersChannel || out.Ask.IntendedChannel != "" ||
		out.Notice == nil || !out.Notice.Delivered || out.Notice.Channel != bumblebeeStandup || out.Notice.IntendedChannel != "" {
		t.Errorf("asks=%v notices=%v out=%+v", asks, notices, out)
	}
}

// TestDebugChannelReceivesEveryAskAndNotice: with reviews.debugChannel set,
// the transfer's ask and notice both land in the debug channel, each closing
// with the channel the policy file chose — the name reviews.channels maps,
// or the ID as the file carries it; the pull request and the tools' answers
// read as without it; get_info shows the channel.
func TestDebugChannelReceivesEveryAskAndNotice(t *testing.T) {
	st := newStackWith(t, debugChannelName)
	c := st.as(t, aliceToken)
	if info := getInfo(t, c); !info.Reviews.Configured || info.Reviews.DebugChannel != debugChannelName {
		t.Errorf("get_info reviews: %+v", info.Reviews)
	}
	var out tools.Committed
	st.callJSON(t, c, tools.ToolTransferRepository, map[string]any{argMode: modeCommit, kRepository: repoPresent, argToTeam: teamPlaneteers}, &out)
	asks, notices := st.gw.posted()
	if len(asks) != 1 || len(notices) != 1 {
		t.Fatalf("asks=%v notices=%v", asks, notices)
	}
	ask, notice := asks[0], notices[0]
	askText, noticeText := ask["text"].(string), notice["text"].(string)
	pr := st.ghs.files.pullRequests()[0]
	if ask[kChannel] != debugChannelID || ask["team"] != teamPlaneteers || !strings.HasPrefix(askText, alice+" asks to transfer") || !strings.HasSuffix(askText, " (for #"+teamPlaneteers+")") ||
		!strings.HasSuffix(ask["link"].(string), fmt.Sprintf("/pull/%d", pr.Number)) {
		t.Errorf("ask: %v", ask)
	}
	if notice[kChannel] != debugChannelID || notice["team"] != team || !strings.HasSuffix(noticeText, " (for "+bumblebeeStandup+")") {
		t.Errorf("notice: %v", notice)
	}
	if out.Ask == nil || !out.Ask.Delivered || out.Ask.Channel != debugChannelID || out.Ask.IntendedChannel != planeteersChannel ||
		out.Notice == nil || !out.Notice.Delivered || out.Notice.Channel != debugChannelID || out.Notice.IntendedChannel != bumblebeeStandup {
		t.Errorf("answer: ask=%+v notice=%+v", out.Ask, out.Notice)
	}
}

// TestSetLifecycleArchivedOpensThePullRequestAndPostsTheAsk, then
// approve_change refuses the outsider and the asker, and lands another
// member's review.
func TestSetLifecycleArchivedAndApproveChange(t *testing.T) {
	st := newStack(t)
	// dave is the other member of team-bumblebee here: the one who did not
	// open the pull request and may approve it.
	st.ghs.teams[dave] = []string{team}
	c := st.as(t, aliceToken)
	var out tools.Committed
	st.callJSON(t, c, tools.ToolSetLifecycle, map[string]any{argMode: modeCommit, kRepository: repoPresent, argLifecycle: lifecycleArchived, argReason: "superseded"}, &out)
	pr := st.ghs.files.pullRequests()[0]
	file := string(pr.Files["repositories/"+team+".yaml"])
	if !strings.Contains(file, "lifecycle: archived") || !strings.Contains(file, "- name: "+repoLegacy) || !strings.Contains(pr.Title, "archive "+repoPresent) {
		t.Errorf("archive pull request %q\n%s", pr.Title, file)
	}
	asks, _ := st.gw.posted()
	if len(asks) != 1 || asks[0][kChannel] != bumblebeeChannel || !strings.Contains(asks[0]["text"].(string), alice+" asks to archive") || !strings.Contains(asks[0]["text"].(string), "Reason: superseded. A member of") ||
		!strings.Contains(asks[0]["text"].(string), "A member of "+team+" other than "+alice+" approves.") || strings.Contains(asks[0]["text"].(string), "https://") || !strings.HasSuffix(asks[0]["link"].(string), fmt.Sprintf("/pull/%d", pr.Number)) {
		t.Fatalf("ask: %v", asks)
	}
	approve := asks[0]["approve"].(map[string]any)
	if approve["tool"] != "x_giantswarm-repo-manager_approve_change" || approve["arguments"].(map[string]any)["pullRequest"] != float64(pr.Number) {
		t.Errorf("approve: %v", approve)
	}
	if out.Ask == nil || !out.Ask.Delivered || out.Ask.ReviewID == "" {
		t.Errorf("delivery: %+v", out.Ask)
	}
	if !out.PullRequest.AutoMerge || !pr.AutoMerge {
		t.Errorf("auto-merge is armed as the author at opening: %+v fake=%v", out.PullRequest, pr.AutoMerge)
	}

	// The clicking member is carol — not in team-bumblebee: refused, no review.
	text, isErr := call(t, st.as(t, carolToken), tools.ToolApproveChange, map[string]any{argMode: modeCommit, argPullRequest: pr.Number})
	if !isErr || !strings.Contains(text, "not a member") || len(pr.Reviews) != 0 {
		t.Errorf("carol: isError=%v %s reviews=%v", isErr, text, pr.Reviews)
	}
	// alice opened the pull request: her own click is refused with why, before
	// GitHub would refuse it, whatever her membership.
	text, isErr = call(t, c, tools.ToolApproveChange, map[string]any{argMode: modeCommit, argPullRequest: pr.Number})
	if !isErr || !strings.Contains(text, alice+" opened "+org+"/github#") || !strings.Contains(text, "another member of "+team+" has to approve") || len(pr.Reviews) != 0 {
		t.Errorf("alice's own approval: isError=%v %s reviews=%v", isErr, text, pr.Reviews)
	}
	// dave, a member who did not open it, lands the review; GitHub's
	// auto-merge, armed at opening, merges the pull request on it.
	var a tools.Approval
	st.callJSON(t, st.as(t, daveToken), tools.ToolApproveChange, map[string]any{argMode: modeCommit, argPullRequest: pr.Number}, &a)
	if !a.Member || a.Team != team || a.Author != alice || a.ReviewURL == "" || len(pr.Reviews) != 1 || pr.Reviews[0].User != dave || pr.Reviews[0].Event != "APPROVE" {
		t.Errorf("dave's approval: %+v reviews=%v", a, pr.Reviews)
	}
	if !a.Merged || !pr.Merged || a.Message != fmt.Sprintf("Approved as %s and merged: %s/github#%d.", dave, org, pr.Number) {
		t.Errorf("landing: %+v fake merged=%v", a, pr.Merged)
	}
}

// TestApproveLandsAPullRequestWithoutAutoMerge: a pull request opened before
// auto-merge was armed at opening is merged by the approver, as them, with
// the squash titled after it; one whose checks still run is left to
// auto-merge, armed by the approver; the answer says which in one sentence.
func TestApproveLandsAPullRequestWithoutAutoMerge(t *testing.T) {
	st := newStack(t)
	st.ghs.teams[dave] = []string{team}
	var out tools.Committed
	st.callJSON(t, st.as(t, aliceToken), tools.ToolSetLifecycle, map[string]any{argMode: modeCommit, kRepository: repoPresent, argLifecycle: lifecycleArchived}, &out)
	pr := st.ghs.files.pullRequests()[0]
	st.ghs.files.disarmAutoMerge(pr.Number)

	// Checks still running: not mergeable, so auto-merge is armed instead.
	st.ghs.files.setChecksPending(pr.Number, true)
	var a tools.Approval
	st.callJSON(t, st.as(t, daveToken), tools.ToolApproveChange, map[string]any{argMode: modeCommit, argPullRequest: pr.Number}, &a)
	if a.Merged || !a.AutoMerge || pr.Merged || !pr.AutoMerge || a.Message != fmt.Sprintf("Approved as %s; %s/github#%d merges by itself once its checks pass.", dave, org, pr.Number) {
		t.Errorf("pending checks: %+v fake merged=%v autoMerge=%v", a, pr.Merged, pr.AutoMerge)
	}

	// A second pull request, checks done, no auto-merge: merged by the approver.
	st.ghs.files.disarmAutoMerge(pr.Number)
	st.ghs.files.setChecksPending(pr.Number, false)
	st.callJSON(t, st.as(t, daveToken), tools.ToolApproveChange, map[string]any{argMode: modeCommit, argPullRequest: pr.Number}, &a)
	if !a.Merged || a.AutoMerge || !pr.Merged || pr.MergeTitle != fmt.Sprintf("%s (#%d)", pr.Title, pr.Number) || a.Message != fmt.Sprintf("Approved as %s and merged: %s/github#%d.", dave, org, pr.Number) {
		t.Errorf("merge by the approver: %+v fake merged=%v title=%q", a, pr.Merged, pr.MergeTitle)
	}
}

// TestUpdateRepositoryReplacesOneEntry: the changed entry is rewritten in
// place, the rest of the file is byte-identical, the schema judges it.
func TestUpdateRepositoryReplacesOneEntry(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	entry := map[string]any{"name": repoPresent, kComponentType: kService, kDescription: "now described",
		kGen: map[string]any{kLanguage: kGo, kFlavours: []any{kApp}, kCI: map[string]any{kChartName: repoPresent}}}
	var plan tools.Plan
	st.callJSON(t, c, tools.ToolUpdateRepository, map[string]any{argDryRun: true, kRepository: repoPresent, argEntry: entry}, &plan)
	if !plan.Accepted || !strings.Contains(plan.Entry, "now described") || strings.Contains(plan.Before, "described") || plan.Team != team {
		t.Fatalf("plan: %+v", plan)
	}
	var out tools.Committed
	st.callJSON(t, c, tools.ToolUpdateRepository, map[string]any{argMode: modeCommit, kRepository: repoPresent, argEntry: entry}, &out)
	file := string(st.ghs.files.pullRequests()[0].Files["repositories/"+team+".yaml"])
	tail := teamFile[strings.Index(teamFile, "- name: "+repoGone):]
	if !strings.HasPrefix(file, "# yaml-language-server") || !strings.Contains(file, "description: now described") || !strings.HasSuffix(file, tail) {
		t.Errorf("file:\n%s", file)
	}
	// A schema refusal is data in the dry run and an error in commit.
	entry["visibility"] = "secret"
	st.callJSON(t, c, tools.ToolUpdateRepository, map[string]any{argDryRun: true, kRepository: repoPresent, argEntry: entry}, &plan)
	if plan.Accepted || len(plan.Problems) == 0 {
		t.Errorf("visibility secret should be refused: %+v", plan)
	}
}

// TestListScopesPerCaller: mine follows alice's GitHub teams, unassigned the
// undeclared repositories, team the argument; the page's filters apply.
func TestListScopesPerCaller(t *testing.T) {
	st := newStack(t)
	now := time.Now()
	old := now.Add(-400 * 24 * time.Hour)
	seed := []*inventory.Record{
		{Repository: org + "/" + repoPresent, Name: repoPresent, Declaration: &inventory.Declaration{Team: team}, Reality: &inventory.Reality{Visibility: kPublic, LastPersonCommit: &inventory.Commit{Date: now}}, Renovate: inventory.Renovate{Configured: true}},
		{Repository: org + "/planet-service", Name: "planet-service", Declaration: &inventory.Declaration{Team: teamPlaneteers, Lifecycle: "deprecated"}, Reality: &inventory.Reality{Visibility: kPrivate, IsFork: true, LastPersonCommit: &inventory.Commit{Date: old}}},
		{Repository: org + "/" + repoStray, Name: repoStray, Reality: &inventory.Reality{Visibility: kPublic, Description: "a stray thing"}},
	}
	for _, r := range seed {
		r.RefreshedAt = now
		if err := st.store.Put(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	c := st.as(t, aliceToken)
	names := func(args map[string]any) []string {
		var l tools.Listing
		st.callJSON(t, c, tools.ToolListRepositories, args, &l)
		var out []string
		for _, r := range l.Repositories {
			out = append(out, r.Repository)
		}
		return out
	}
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{argScope: "mine"}, org + "/giantswarm-repo-manager," + org + "/" + repoPresent},
		{map[string]any{argScope: "unassigned"}, org + "/" + repoStray},
		{map[string]any{argScope: "team", argTeam: teamPlaneteers}, org + "/planet-service"},
		{map[string]any{argTeam: "none"}, org + "/" + repoStray},
		{map[string]any{"search": "stray thing"}, org + "/" + repoStray},
		{map[string]any{kVisibility: kPrivate}, org + "/planet-service"},
		{map[string]any{"fork": true}, org + "/planet-service"},
		{map[string]any{argLifecycle: "deprecated"}, org + "/planet-service"},
		{map[string]any{"inactiveDays": 365, argScope: "team", argTeam: teamPlaneteers}, org + "/planet-service"},
		{map[string]any{"renovate": "configured"}, org + "/" + repoPresent},
	}
	for _, tc := range cases {
		if got := strings.Join(names(tc.args), ","); got != tc.want {
			t.Errorf("%v: got %q want %q", tc.args, got, tc.want)
		}
	}
	// dave is in no team: scope mine has nothing to scope to and says so.
	text, isErr := call(t, st.as(t, daveToken), tools.ToolListRepositories, map[string]any{argScope: "mine"})
	if !isErr || !strings.Contains(text, "no team known for you (none)") {
		t.Errorf("dave's scope mine: isError=%v %s", isErr, text)
	}
}

// TestReconcileDispatchesAsThePersonAndTheCompletionMessageFollows.
func TestReconcileDispatchesAsThePersonAndTheCompletionMessageFollows(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	var d tools.Dispatch
	st.callJSON(t, c, tools.ToolAlignRepository, map[string]any{argDryRun: true, kRepository: repoPresent}, &d)
	if d.Dispatched || d.As != alice || d.Workflow != reconcilerWorkflow || len(st.ghs.files.dispatches) != 0 {
		t.Fatalf("dry run: %+v dispatches=%v", d, st.ghs.files.dispatches)
	}
	// The answer says what the run does: the declaring team has opted in, so
	// the run aligns, and the warning names what an alignment changes.
	if d.Team != team || !d.OptedIn || d.Mode != tools.DispatchModeAlign || !strings.Contains(d.Warning, "has opted in") ||
		!strings.Contains(d.Warning, "enforce_admins") {
		t.Errorf("dry run answer for an opted-in team: team=%q optedIn=%v mode=%q warning=%q", d.Team, d.OptedIn, d.Mode, d.Warning)
	}
	// A team without the opt-in: the run is a check, and the warning says nothing changes.
	var check tools.Dispatch
	st.callJSON(t, c, tools.ToolAlignRepository, map[string]any{argDryRun: true, kRepository: repoPresent, argTeam: teamPlaneteers}, &check)
	if check.Team != teamPlaneteers || check.OptedIn || check.Mode != tools.DispatchModeCheck || !strings.Contains(check.Warning, "has not opted in") {
		t.Errorf("dry run answer for a team without opt-in: team=%q optedIn=%v mode=%q warning=%q", check.Team, check.OptedIn, check.Mode, check.Warning)
	}
	st.callJSON(t, c, tools.ToolAlignRepository, map[string]any{argMode: modeCommit, kRepository: repoPresent, argTeam: team}, &d)
	ds := st.ghs.files.dispatches
	if !d.Dispatched || len(ds) != 1 || ds[0]["workflow"] != reconcilerWorkflow || ds[0]["as"] != alice || ds[0]["ref"] != mainBranch ||
		ds[0]["inputs"].(map[string]any)["repository"] != repoPresent || ds[0]["inputs"].(map[string]any)["team"] != team {
		t.Errorf("dispatch: %+v %v", d, ds)
	}

	// The dispatched run completes and uploads its artifact; the poller
	// reads it: the record carries the run and its change block, and the
	// team hears nothing — an Align now with nothing to fix is not news.
	finished := time.Now().UTC()
	converged := reconcile.Result{Converged: true, Steps: []reconcile.StepResult{{Step: "release", Verdict: "ok", Summary: "v0.1.0 built"}}}
	run := st.ghs.actions.addRun(t, runStatusCompleted, finished, artifactReport{name: repoPresent, finishedAt: finished, result: converged,
		change: &inventory.Change{Kind: inventory.ChangeDispatched, By: alice}})
	if p := st.poll(t); p.Artifacts != 1 {
		t.Fatalf("poll: %+v", p)
	}
	rec := st.record(t, repoPresent)
	if rec.Setup.LastRun == nil || rec.Setup.LastRun.RunURL != runURL(run.ID) || rec.Setup.PendingRun != nil ||
		rec.Setup.LastRun.Change == nil || rec.Setup.LastRun.Change.Kind != inventory.ChangeDispatched || rec.Setup.LastRun.Change.By != alice {
		t.Fatalf("record after the run: %+v change=%+v", rec.Setup, rec.Setup.LastRun.Change)
	}
	if _, notices := st.gw.posted(); len(notices) != 0 {
		t.Errorf("a converged Align now should post nothing: %v", notices)
	}

	// The run that followed alice's merged pull request creating the
	// repository: one sentence about it in the team's standup channel,
	// linking the pull request — and a finding as a second sentence linking
	// the run.
	prURL := "https://github.com/" + org + "/github/pull/4711"
	created := reconcile.Result{Converged: true, Steps: []reconcile.StepResult{{Step: "release", Verdict: "ok", Summary: "v0.1.0 built"},
		{Step: "metadata", Verdict: "reported", Findings: []reconcile.Finding{{Kind: "default-icon", Message: "the repository has the default icon", Fix: "upload one under Settings"}}}}}
	later := finished.Add(time.Minute)
	pushed := st.ghs.actions.addRun(t, runStatusCompleted, later, artifactReport{name: repoPresent, finishedAt: later, result: created,
		change: &inventory.Change{Kind: inventory.ChangeCreated, By: alice, PullRequest: &inventory.ChangePullRequest{Number: 4711, URL: prURL}}})
	if p := st.poll(t); p.Artifacts != 1 {
		t.Fatalf("second poll: %+v", p)
	}
	_, notices := st.gw.posted()
	if len(notices) != 2 || notices[0][kChannel] != bumblebeeStandup || notices[0]["team"] != team ||
		notices[0]["text"] != alice+" created a new repo: "+repoPresent+" (app, go)" || notices[0]["link"] != prURL ||
		notices[1][kChannel] != bumblebeeStandup || notices[1]["text"] != repoPresent+": the repository has the default icon — upload one under Settings" || notices[1]["link"] != runURL(pushed.ID) {
		t.Errorf("messages after the creating run: %v", notices)
	}
}

// TestCreationNoticeForARepositoryTheInventoryHasNotSwept: the run behind
// the pull request declaring a repository reaches the poller minutes after
// the merge, before any sweep read the entry — the inventory's read of the
// team files predates it. The poller reads the team files again for such a
// run: the record carries the declaration and the team hears the one
// sentence about the creation.
func TestCreationNoticeForARepositoryTheInventoryHasNotSwept(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	if _, err := st.col.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if rec := st.record(t, repoStray); rec.Declaration != nil {
		t.Fatalf("%s should be undeclared after the sweep: %+v", repoStray, rec.Declaration)
	}
	// alice's pull request declaring the repository merges after the sweep
	// read the team files; the reconciler run it triggered reports.
	st.ghs.org.declare("- name: " + repoStray + "\n  componentType: service\n  gen:\n    language: python\n    flavours: [app]\n")
	prURL := "https://github.com/" + org + "/github/pull/4712"
	// The artifact carries the finish at second precision: the run finishes
	// the second after the sweep read the team files.
	finished := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	run := st.ghs.actions.addRun(t, runStatusCompleted, finished, artifactReport{name: repoStray, finishedAt: finished,
		result: reconcile.Result{Repository: org + "/" + repoStray, Declared: org + "/" + repoStray, Team: team, Converged: true},
		change: &inventory.Change{Kind: inventory.ChangeCreated, By: alice, PullRequest: &inventory.ChangePullRequest{Number: 4712, URL: prURL}}})
	if p := st.poll(t); p.Artifacts != 1 || len(p.Errors) != 0 {
		t.Fatalf("poll: %+v", p)
	}
	rec := st.record(t, repoStray)
	if rec.Declaration == nil || rec.Declaration.Team != team || rec.Setup.LastRun == nil || rec.Setup.LastRun.RunURL != runURL(run.ID) {
		t.Fatalf("record after the run: declaration=%+v setup=%+v", rec.Declaration, rec.Setup)
	}
	_, notices := st.gw.posted()
	if len(notices) != 1 || notices[0][kChannel] != bumblebeeStandup || notices[0]["team"] != team ||
		notices[0]["text"] != alice+" created a new repo: "+repoStray+" (app, python)" || notices[0]["link"] != prURL {
		t.Errorf("notices after the creating run: %v", notices)
	}
}

func hasNotice(ns []reposetup.Notice, kind string) bool {
	for _, n := range ns {
		if string(n.Kind) == kind {
			return true
		}
	}
	return false
}
