package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/collect"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/tools"
)

// The nine tools against the fakes: GitHub (org GraphQL, the team-files
// repository with its git data, pull requests, reviews and dispatches), the
// broker, Dex, klaus-gateway's team-review endpoint and a seeded store.

var newEntry = map[string]any{
	kName: "shiny-service", kComponentType: kService,
	"gen": map[string]any{"language": "go", kFlavours: []any{kApp}, "ci": map[string]any{kChartName: "shiny-service"}},
}

// TestValidateRepositoryRendersAndRefuses: the dry run renders an accepted
// entry with its template, refuses a bad one as data, and carries the guard
// notices — team-review for an outsider, batch-review above three entries.
func TestValidateRepositoryRendersAndRefuses(t *testing.T) {
	st := newStack(t)
	asAlice := st.as(t, st.idp.mint(t, alice, aliceEmail))
	var v tools.Validation
	st.callJSON(t, asAlice, tools.ToolValidateRepository, map[string]any{argTeam: team, argEntry: newEntry}, &v)
	if !v.Accepted || len(v.Entries) != 1 || v.Entries[0].Template != "giantswarm/template" || v.Entries[0].NameCheck.Verdict != "free" ||
		len(v.Notices) != 0 || !v.MachineApproved || v.AuthorLogin != alice || v.TeamsSource != kGitHub {
		t.Errorf("member's valid entry: %+v notices=%v teams=%v/%s", v.Result, v.Notices, v.AuthorTeams, v.TeamsSource)
	}

	bad := map[string]any{kName: "shiny-app", kComponentType: kService, "gen": map[string]any{"language": "go", kFlavours: []any{kApp}}}
	st.callJSON(t, asAlice, tools.ToolValidateRepository, map[string]any{argTeam: team, argEntry: bad}, &v)
	if v.Accepted || len(v.Entries) != 1 || len(v.Entries[0].Problems) == 0 {
		t.Errorf("refusal should be data: %+v", v.Result)
	}

	asCarol := st.as(t, st.idp.mint(t, carol, "carol@example.com"))
	st.callJSON(t, asCarol, tools.ToolValidateRepository, map[string]any{argTeam: team, argEntry: newEntry}, &v)
	if !v.Accepted || v.MachineApproved || len(v.Notices) != 1 || string(v.Notices[0].Kind) != "team-review" {
		t.Errorf("outsider should see the team-review notice: %+v", v.Notices)
	}

	four := []any{}
	for _, n := range []string{"a-one", "a-two", "a-three", "a-four"} {
		e := map[string]any{kName: n, kComponentType: kService, "gen": map[string]any{"language": "go", kFlavours: []any{"generic"}}}
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
	c := st.as(t, st.idp.mint(t, alice, aliceEmail))
	text, isErr := call(t, c, tools.ToolCreateRepository, map[string]any{argDryRun: true, argTeam: team, argEntry: newEntry})
	if isErr || !strings.Contains(text, `"accepted": true`) {
		t.Errorf("dry run: isError=%v %s", isErr, text)
	}
	if n := len(st.ghs.files.pullRequests()); n != 0 {
		t.Errorf("dry run opened %d pull requests", n)
	}
	for _, tool := range tools.WriteToolNames() {
		text, isErr := call(t, c, tool, map[string]any{argMode: "apply", argTeam: team, argEntry: newEntry, kRepository: repoPresent, argToTeam: teamPlaneteers, argLifecycle: "archived", argPullRequest: 1})
		if !isErr || !strings.Contains(text, `mode "apply" is refused`) {
			t.Errorf("%s: apply should be refused: isError=%v %s", tool, isErr, text)
		}
	}
	if n := len(st.ghs.files.pullRequests()); n != 0 {
		t.Errorf("apply attempts opened %d pull requests", n)
	}
}

// TestCreateCommitOpensThePullRequestAsThePerson: alice's commit is a
// creation-only pull request under her name; a person without a grant is
// told to connect GitHub and nothing opens.
func TestCreateCommitOpensThePullRequestAsThePerson(t *testing.T) {
	st := newStack(t)
	bob := st.as(t, st.idp.mint(t, "bob", "bob@example.com"))
	text, isErr := call(t, bob, tools.ToolCreateRepository, map[string]any{argMode: modeCommit, argTeam: team, argEntry: newEntry})
	if !isErr || !strings.Contains(text, "connect GitHub") {
		t.Errorf("bob without a grant: isError=%v %s", isErr, text)
	}
	var out tools.Committed
	st.callJSON(t, st.as(t, st.idp.mint(t, alice, aliceEmail)), tools.ToolCreateRepository, map[string]any{argMode: modeCommit, argTeam: team, argEntry: newEntry, "reason": "the shiny thing"}, &out)
	prs := st.ghs.files.pullRequests()
	if len(prs) != 1 || out.PullRequest == nil || out.PullRequest.Number != prs[0].Number || prs[0].Author != alice || out.PullRequest.Author != alice {
		t.Fatalf("pull request as alice: %+v %+v", out.PullRequest, prs)
	}
	pr := prs[0]
	file := string(pr.Files["repositories/"+team+".yaml"])
	if !strings.Contains(pr.Title, "declare shiny-service for "+team) || !strings.Contains(file, "- name: shiny-service") || !strings.Contains(file, "- name: "+repoPresent) ||
		!strings.Contains(pr.Body, "the shiny thing") || !strings.Contains(pr.Body, "team="+team) {
		t.Errorf("pull request: %q\n%s\n%s", pr.Title, pr.Body, file)
	}
}

// TestTransferNamesBothTeams: the entry leaves one file and enters the
// other in one pull request; the receiving team gets the ask, the giving
// team the notice.
func TestTransferNamesBothTeams(t *testing.T) {
	st := newStack(t)
	c := st.as(t, st.idp.mint(t, alice, aliceEmail))
	var plan tools.Plan
	st.callJSON(t, c, tools.ToolTransferRepository, map[string]any{argDryRun: true, kRepository: repoPresent, argToTeam: teamPlaneteers}, &plan)
	if plan.Team != teamPlaneteers || plan.FromTeam != team || len(plan.PullRequest.Files) != 2 || plan.Ask == nil || !plan.Ask.Deliverable || plan.Ask.Channel != planeteersChannel ||
		plan.Notice == nil || plan.Notice.Channel != bumblebeeChannel || plan.PullRequest.As != alice {
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
	if len(asks) != 1 || asks[0]["channel"] != planeteersChannel || asks[0]["team"] != teamPlaneteers || len(notices) != 1 || notices[0]["channel"] != bumblebeeChannel ||
		out.Ask == nil || !out.Ask.Delivered || out.Notice == nil || !out.Notice.Delivered {
		t.Errorf("asks=%v notices=%v out=%+v", asks, notices, out)
	}
}

// TestSetLifecycleArchivedOpensThePullRequestAndPostsTheAsk, then
// approve_change refuses the outsider and lands the member's review.
func TestSetLifecycleArchivedAndApproveChange(t *testing.T) {
	st := newStack(t)
	c := st.as(t, st.idp.mint(t, alice, aliceEmail))
	var out tools.Committed
	st.callJSON(t, c, tools.ToolSetLifecycle, map[string]any{argMode: modeCommit, kRepository: repoPresent, argLifecycle: "archived", "reason": "superseded"}, &out)
	pr := st.ghs.files.pullRequests()[0]
	file := string(pr.Files["repositories/"+team+".yaml"])
	if !strings.Contains(file, "lifecycle: archived") || !strings.Contains(file, "- name: "+repoLegacy) || !strings.Contains(pr.Title, "archive "+repoPresent) {
		t.Errorf("archive pull request %q\n%s", pr.Title, file)
	}
	asks, _ := st.gw.posted()
	if len(asks) != 1 || asks[0]["channel"] != bumblebeeChannel || !strings.Contains(asks[0]["text"].(string), "Archive") || !strings.Contains(asks[0]["text"].(string), "superseded") {
		t.Fatalf("ask: %v", asks)
	}
	approve := asks[0]["approve"].(map[string]any)
	if approve["tool"] != "x_giantswarm-repo-manager_approve_change" || approve["arguments"].(map[string]any)["pullRequest"] != float64(pr.Number) {
		t.Errorf("approve: %v", approve)
	}
	if out.Ask == nil || !out.Ask.Delivered || out.Ask.ReviewID == "" {
		t.Errorf("delivery: %+v", out.Ask)
	}

	// The clicking member is carol — not in team-bumblebee: refused, no review.
	text, isErr := call(t, st.as(t, st.idp.mint(t, carol, "carol@example.com")), tools.ToolApproveChange, map[string]any{argMode: modeCommit, argPullRequest: pr.Number})
	if !isErr || !strings.Contains(text, "not a member") || len(pr.Reviews) != 0 {
		t.Errorf("carol: isError=%v %s reviews=%v", isErr, text, pr.Reviews)
	}
	var a tools.Approval
	st.callJSON(t, c, tools.ToolApproveChange, map[string]any{argMode: modeCommit, argPullRequest: pr.Number}, &a)
	if !a.Member || a.Team != team || a.ReviewURL == "" || len(pr.Reviews) != 1 || pr.Reviews[0].User != alice || pr.Reviews[0].Event != "APPROVE" {
		t.Errorf("alice's approval: %+v reviews=%v", a, pr.Reviews)
	}
}

// TestUpdateRepositoryReplacesOneEntry: the changed entry is rewritten in
// place, the rest of the file is byte-identical, the schema judges it.
func TestUpdateRepositoryReplacesOneEntry(t *testing.T) {
	st := newStack(t)
	c := st.as(t, st.idp.mint(t, alice, aliceEmail))
	entry := map[string]any{"name": repoPresent, kComponentType: kService, "description": "now described",
		"gen": map[string]any{"language": "go", kFlavours: []any{kApp}, "ci": map[string]any{kChartName: repoPresent}}}
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
		{Repository: org + "/planet-service", Name: "planet-service", Declaration: &inventory.Declaration{Team: teamPlaneteers, Lifecycle: "deprecated"}, Reality: &inventory.Reality{Visibility: "private", IsFork: true, LastPersonCommit: &inventory.Commit{Date: old}}},
		{Repository: org + "/" + repoStray, Name: repoStray, Reality: &inventory.Reality{Visibility: kPublic, Description: "a stray thing"}},
	}
	for _, r := range seed {
		r.RefreshedAt = now
		if err := st.store.Put(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	c := st.as(t, st.idp.mint(t, alice, aliceEmail))
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
		{map[string]any{"visibility": "private"}, org + "/planet-service"},
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
	// bob has no grant: his teams come from the IdP groups (the fake Dex
	// puts everyone in team-bumblebee).
	var l tools.Listing
	st.callJSON(t, st.as(t, st.idp.mint(t, "bob", "bob@example.com")), tools.ToolListRepositories, map[string]any{argScope: "mine"}, &l)
	if l.TeamsSource != "idp-groups" || strings.Join(l.Teams, ",") != team || l.Matched != 2 {
		t.Errorf("bob's scope mine: teams=%v/%s matched=%d", l.Teams, l.TeamsSource, l.Matched)
	}
}

// TestReconcileDispatchesAsThePersonAndTheCompletionMessageFollows.
func TestReconcileDispatchesAsThePersonAndTheCompletionMessageFollows(t *testing.T) {
	st := newStack(t)
	c := st.as(t, st.idp.mint(t, alice, aliceEmail))
	var d tools.Dispatch
	st.callJSON(t, c, tools.ToolReconcileRepository, map[string]any{argDryRun: true, kRepository: repoPresent}, &d)
	if d.Dispatched || d.As != alice || d.Workflow != "reconcile-repositories.yaml" || len(st.ghs.files.dispatches) != 0 {
		t.Fatalf("dry run: %+v dispatches=%v", d, st.ghs.files.dispatches)
	}
	st.callJSON(t, c, tools.ToolReconcileRepository, map[string]any{argMode: modeCommit, kRepository: repoPresent, argTeam: team}, &d)
	ds := st.ghs.files.dispatches
	if !d.Dispatched || len(ds) != 1 || ds[0]["workflow"] != "reconcile-repositories.yaml" || ds[0]["as"] != alice || ds[0]["ref"] != mainBranch ||
		ds[0]["inputs"].(map[string]any)["repository"] != repoPresent || ds[0]["inputs"].(map[string]any)["team"] != team {
		t.Errorf("dispatch: %+v %v", d, ds)
	}

	// The reconciler reports back: the record carries the run, the team's
	// channel gets the completion message.
	run := inventory.LastRun{RunURL: "https://github.com/giantswarm/github/actions/runs/1", Timestamp: time.Now().UTC(),
		Result: reconcile.Result{Converged: true, Steps: []reconcile.StepResult{{Step: "release", Verdict: "ok", Summary: "v0.1.0 built"}}}}
	status, body := st.internal(t, http.MethodPost, "/internal/refresh", internalToken, collect.RefreshRequest{Repository: org + "/" + repoPresent, LastRun: &run})
	if status != http.StatusOK {
		t.Fatalf("refresh: %d %s", status, body)
	}
	var rec inventory.Record
	if err := json.Unmarshal(body, &rec); err != nil || rec.Setup.LastRun == nil || rec.Setup.LastRun.RunURL != run.RunURL {
		t.Fatalf("record after the run: %v %s", err, body)
	}
	_, notices := st.gw.posted()
	if len(notices) != 1 || notices[0]["channel"] != bumblebeeChannel || !strings.Contains(notices[0]["text"].(string), "*Reconciled* `"+org+"/"+repoPresent+"`") ||
		!strings.Contains(notices[0]["text"].(string), "catalog entity: present") || notices[0]["link"] != run.RunURL {
		t.Errorf("completion message: %v", notices)
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
