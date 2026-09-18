package tools

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

const (
	testRepository  = "giantswarm/bumblebee-repo"
	testRunURL      = "https://github.com/giantswarm/github/actions/runs/123"
	testPRURL       = "https://github.com/giantswarm/github/pull/4711"
	testService     = "service"
	testFlavourApp  = "app"
	testDefaultIcon = "default-icon"
)

// record is a declared repository whose last run followed change with the
// given steps.
func record(change *inventory.Change, steps ...reconcile.StepResult) *inventory.Record {
	converged := true
	for _, s := range steps {
		if s.Verdict == reconcile.VerdictFailed || s.Verdict == reconcile.VerdictDrift {
			converged = false
		}
	}
	return &inventory.Record{
		Repository:  testRepository,
		Name:        "bumblebee-repo",
		Declaration: &inventory.Declaration{Team: testTeam, ComponentType: testService, Language: "go", Flavours: []string{testFlavourApp}},
		Setup: inventory.Setup{LastRun: &inventory.LastRun{
			Result: reconcile.Result{Repository: testRepository, Team: testTeam, Converged: converged, Steps: steps},
			RunURL: testRunURL, RunID: 123, Attempt: 1, Change: change}},
	}
}

func change(kind string) *inventory.Change {
	return &inventory.Change{Kind: kind, By: "alice", PullRequest: &inventory.ChangePullRequest{Number: 4711, URL: testPRURL}}
}

var (
	okStep      = reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictOK, Summary: "v0.1.0 built"}
	failedStep  = reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictFailed, Summary: "CircleCI answered 502"}
	findingStep = reconcile.StepResult{Step: reconcile.StepMetadata, Verdict: reconcile.VerdictReported,
		Findings: []reconcile.Finding{{Kind: testDefaultIcon, Message: "the repository has the default icon", Fix: "upload one under Settings"}}}
	uncheckedStep = reconcile.StepResult{Step: reconcile.StepRenovate, Verdict: reconcile.VerdictReported,
		Findings: []reconcile.Finding{{Kind: reconcile.FindingUnchecked, Message: "whether Renovate runs is out of this token's reach", Fix: "grant the App issues: read"}}}
)

// TestCompletionText: one sentence about the change for the kinds a person
// made, one each for a failed step and a finding of that person's run, and
// nothing for the reconciler doing its job — an Align now, the nightly, an
// artifact without a change block — findings and failures included.
func TestCompletionText(t *testing.T) {
	transfer := change(inventory.ChangeTransferred)
	transfer.FromTeam = "team-planeteers"
	transferUnknownFrom := change(inventory.ChangeTransferred)
	nightly := &inventory.Change{Kind: inventory.ChangeNightly}
	cases := []struct {
		name string
		rec  *inventory.Record
		want string
	}{
		{name: "created", rec: record(change(inventory.ChangeCreated), okStep), want: "alice created a new repo: bumblebee-repo (app, go)"},
		{name: "added", rec: record(change(inventory.ChangeAdded), okStep), want: "alice added the existing repo bumblebee-repo (app, go) to team-bumblebee"},
		{name: "transferred", rec: record(transfer, okStep), want: "alice transferred the repo bumblebee-repo (app, go) from team-planeteers to team-bumblebee"},
		{name: "transferred without fromTeam", rec: record(transferUnknownFrom, okStep), want: "alice transferred the repo bumblebee-repo (app, go) to team-bumblebee"},
		{name: "archived", rec: record(change(inventory.ChangeArchived), okStep), want: "alice archived the repo bumblebee-repo"},
		{name: "deleted", rec: record(change(inventory.ChangeDeleted), okStep), want: "alice deleted the repo bumblebee-repo"},
		{name: "deprecated", rec: record(change(inventory.ChangeDeprecated), okStep), want: "alice deprecated the repo bumblebee-repo"},
		{name: "changed converged: silent", rec: record(change(inventory.ChangeChanged), okStep)},
		{name: "dispatched converged: silent", rec: record(change(inventory.ChangeDispatched), okStep)},
		{name: "nightly converged: silent", rec: record(nightly, okStep)},
		{name: "no change block, converged: silent", rec: record(nil, okStep)},
		{name: "no steps at all: silent", rec: record(change(inventory.ChangeDispatched))},
		{name: "dispatched with a failed step: silent", rec: record(change(inventory.ChangeDispatched), okStep, failedStep)},
		{name: "nightly with a finding: silent", rec: record(nightly, findingStep)},
		{name: "no change block with a failed step: silent", rec: record(nil, failedStep)},
		{name: "changed with a failed step: the failure alone", rec: record(change(inventory.ChangeChanged), okStep, failedStep),
			want: "bumblebee-repo: the circleci step failed (CircleCI answered 502) — look at the run, fix the cause and reconcile again"},
		{name: "created with a finding: two sentences", rec: record(change(inventory.ChangeCreated), okStep, findingStep),
			want: "alice created a new repo: bumblebee-repo (app, go)\nbumblebee-repo: the repository has the default icon — upload one under Settings"},
		{name: "created with an unchecked finding: the sentence alone, the token's reach is the platform's", rec: record(change(inventory.ChangeCreated), okStep, uncheckedStep),
			want: "alice created a new repo: bumblebee-repo (app, go)"},
		{name: "changed with an unchecked finding alone: silent", rec: record(change(inventory.ChangeChanged), okStep, uncheckedStep)},
		{name: "failed step without a summary", rec: record(change(inventory.ChangeChanged), reconcile.StepResult{Step: reconcile.StepCatalog, Verdict: reconcile.VerdictFailed}),
			want: "bumblebee-repo: the catalog step failed — look at the run, fix the cause and reconcile again"},
		{name: "finding without a fix", rec: record(change(inventory.ChangeChanged), reconcile.StepResult{Step: reconcile.StepEntry, Verdict: reconcile.VerdictReported,
			Findings: []reconcile.Finding{{Kind: "entry-refused", Message: "gen.language is not a language"}}}),
			want: "bumblebee-repo: gen.language is not a language"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompletionText(tc.rec); got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestCompletionSentenceWithoutFlavourLanguageOrPerson: the parenthesis
// follows the entry, the subject a missing login.
func TestCompletionSentenceWithoutFlavourLanguageOrPerson(t *testing.T) {
	rec := record(change(inventory.ChangeCreated), okStep)
	rec.Declaration.Flavours = nil
	if got, want := CompletionText(rec), "alice created a new repo: bumblebee-repo (go)"; got != want {
		t.Errorf("language only: got %q, want %q", got, want)
	}
	rec.Declaration.Language = ""
	if got, want := CompletionText(rec), "alice created a new repo: bumblebee-repo"; got != want {
		t.Errorf("neither: got %q, want %q", got, want)
	}
	rec.Declaration.Flavours = []string{testFlavourApp, "cli"}
	rec.Setup.LastRun.Change.By = ""
	if got, want := CompletionText(rec), "someone created a new repo: bumblebee-repo (app/cli)"; got != want {
		t.Errorf("two flavours, nobody: got %q, want %q", got, want)
	}
}

// TestCompletionLinks: the change links its pull request (the run when the
// block has none), a failed step and a finding link the run.
func TestCompletionLinks(t *testing.T) {
	msgs := Completions(record(change(inventory.ChangeCreated), failedStep, findingStep))
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %+v", msgs)
	}
	if msgs[0].Link != testPRURL || msgs[1].Link != testRunURL || msgs[2].Link != testRunURL {
		t.Errorf("links: %+v", msgs)
	}
	noPR := change(inventory.ChangeArchived)
	noPR.PullRequest = nil
	if msgs := Completions(record(noPR, okStep)); len(msgs) != 1 || msgs[0].Link != testRunURL {
		t.Errorf("change without a pull request should link the run: %+v", msgs)
	}
	if msgs := Completions(record(change(inventory.ChangeDispatched), okStep)); len(msgs) != 0 {
		t.Errorf("nothing to tell should be no message: %+v", msgs)
	}
}

// TestReconciledLogsWhyItStaysSilent: the hook names its reason on every way
// out — a record the team files do not declare (the team from the artifact),
// a run with nothing to tell — and posts nothing.
func TestReconciledLogsWhyItStaysSilent(t *testing.T) {
	var buf bytes.Buffer
	ts := &Tools{t: &tools{d: Deps{Log: slog.New(slog.NewTextHandler(&buf, nil))}}}
	undeclared := record(change(inventory.ChangeCreated), okStep)
	undeclared.Declaration = nil
	ts.Reconciled(context.Background(), undeclared)
	if logged := buf.String(); !strings.Contains(logged, "nothing to tell the team") || !strings.Contains(logged, "do not declare") ||
		!strings.Contains(logged, "team="+testTeam) || !strings.Contains(logged, "change=created") {
		t.Errorf("undeclared record: %q", logged)
	}
	buf.Reset()
	ts.Reconciled(context.Background(), record(change(inventory.ChangeNightly), findingStep))
	if logged := buf.String(); !strings.Contains(logged, "nothing to tell the team") || !strings.Contains(logged, "change=nightly") || !strings.Contains(logged, "reason=") {
		t.Errorf("nightly run: %q", logged)
	}
	buf.Reset()
	ts.Reconciled(context.Background(), &inventory.Record{Repository: testRepository})
	if logged := buf.String(); !strings.Contains(logged, "without a run") {
		t.Errorf("record without a run: %q", logged)
	}
}
