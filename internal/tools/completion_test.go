package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/review"
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
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

// testNotices is the team's notices channel the completion tests post to.
var testNotices = teamfiles.Channel{ID: "C0STANDUP001", Name: "standup-t"}

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
	if err := ts.Reconciled(context.Background(), undeclared); err != nil {
		t.Errorf("nothing to tell is no error: %v", err)
	}
	if logged := buf.String(); !strings.Contains(logged, "nothing to tell the team") || !strings.Contains(logged, "do not declare") ||
		!strings.Contains(logged, "team="+testTeam) || !strings.Contains(logged, "change=created") {
		t.Errorf("undeclared record: %q", logged)
	}
	buf.Reset()
	if err := ts.Reconciled(context.Background(), record(change(inventory.ChangeNightly), findingStep)); err != nil {
		t.Errorf("nothing to tell is no error: %v", err)
	}
	if logged := buf.String(); !strings.Contains(logged, "nothing to tell the team") || !strings.Contains(logged, "change=nightly") || !strings.Contains(logged, "reason=") {
		t.Errorf("nightly run: %q", logged)
	}
	buf.Reset()
	if err := ts.Reconciled(context.Background(), &inventory.Record{Repository: testRepository}); err != nil {
		t.Errorf("nothing to tell is no error: %v", err)
	}
	if logged := buf.String(); !strings.Contains(logged, "without a run") {
		t.Errorf("record without a run: %q", logged)
	}
}

const (
	testForeignRuleset = "foreign-ruleset"
	testMissedTagBuild = "missed-tag-build"
	testRulesetFix     = "declare what it enforces in the entry and delete it, or keep it knowingly"
	testMissedTagText  = "bumblebee-repo: release v0.2.9 has no pipeline — cut the next tag"
)

var (
	// pendingPRStep is the codeowners step while the pull request the engine
	// opened in this very run awaits its merge.
	pendingPRStep = reconcile.StepResult{Step: reconcile.StepCodeowners, Verdict: reconcile.VerdictDrift,
		Summary: "CODEOWNERS differs; pull request #7 awaits its merge",
		Findings: []reconcile.Finding{{Kind: reconcile.FindingPendingPullRequest,
			Message: "pull request #7 sets CODEOWNERS to @giantswarm/team-bumblebee", Fix: "merge the pull request"}}}
	// releaseFindingStep is a release whose tag was never built.
	releaseFindingStep = reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictReported,
		Findings: []reconcile.Finding{{Kind: testMissedTagBuild, Message: "release v0.2.9 has no pipeline", Fix: "cut the next tag"}}}
)

// ruleset is the protection step reporting one advisory finding per foreign
// ruleset it left alone.
func ruleset(names ...string) reconcile.StepResult {
	st := reconcile.StepResult{Step: reconcile.StepProtection, Verdict: reconcile.VerdictReported}
	for _, n := range names {
		st.Findings = append(st.Findings, reconcile.Finding{Kind: testForeignRuleset, Advisory: true,
			Message: "ruleset " + strconv.Quote(n) + " is not the engine's and is left alone", Fix: testRulesetFix})
	}
	return st
}

// checked gives the record the live read-mode check the collector runs in
// the same refresh, with the steps as the check found them.
func checked(rec *inventory.Record, steps ...reconcile.StepResult) *inventory.Record {
	rec.Setup.Checks = &reconcile.Result{Repository: testRepository, Team: testTeam, Steps: steps}
	return rec
}

// TestCompletionsSkipTheEnginesOwnPullRequest: the codeowners step opens the
// pull request it reports, in the run that reports it, and the bot-PR sweep
// merges it — telling the team to merge it asks for work nobody has to do.
func TestCompletionsSkipTheEnginesOwnPullRequest(t *testing.T) {
	if got := CompletionText(record(change(inventory.ChangeChanged), okStep, pendingPRStep)); got != "" {
		t.Errorf("the finding alone should be silent, got %q", got)
	}
	want := "alice created a new repo: bumblebee-repo (app, go)\nbumblebee-repo: the repository has the default icon — upload one under Settings"
	if got := CompletionText(record(change(inventory.ChangeCreated), pendingPRStep, findingStep)); got != want {
		t.Errorf("beside another finding:\ngot  %q\nwant %q", got, want)
	}
}

// TestCompletionsVerifyAgainstTheLiveCheck: the poller reads a run's
// artifact minutes after the run finished. The live check the collector ran
// in the same refresh is the state now, and decides what is still true.
func TestCompletionsVerifyAgainstTheLiveCheck(t *testing.T) {
	protectionOK := reconcile.StepResult{Step: reconcile.StepProtection, Verdict: reconcile.VerdictOK}
	cases := []struct {
		name string
		rec  *inventory.Record
		want string
	}{
		{name: "the live check no longer reports it: fixed between the run and the poll",
			rec: checked(record(change(inventory.ChangeChanged), ruleset("protect-main")), protectionOK)},
		{name: "the live check still reports it: told in the live check's words",
			rec:  checked(record(change(inventory.ChangeChanged), ruleset("protect-main")), ruleset("protect-giantswarm")),
			want: `bumblebee-repo: ruleset "protect-giantswarm" is not the engine's and is left alone — ` + testRulesetFix},
		{name: "two reported, one gone: the one that stands",
			rec:  checked(record(change(inventory.ChangeChanged), ruleset("protect-main", "protect-giantswarm")), ruleset("protect-main")),
			want: `bumblebee-repo: ruleset "protect-main" is not the engine's and is left alone — ` + testRulesetFix},
		{name: "the live check skipped the step: the run's finding stands",
			rec: checked(record(change(inventory.ChangeChanged), releaseFindingStep),
				reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictSkipped, Summary: "no CircleCI client to verify the pipeline"}),
			want: testMissedTagText},
		{name: "the live check failed the step: the run's finding stands",
			rec: checked(record(change(inventory.ChangeChanged), releaseFindingStep),
				reconcile.StepResult{Step: reconcile.StepRelease, Verdict: reconcile.VerdictFailed, Summary: "CircleCI answered 502"}),
			want: testMissedTagText},
		{name: "the live check has no result for the step: the run's finding stands",
			rec:  checked(record(change(inventory.ChangeChanged), releaseFindingStep), protectionOK),
			want: testMissedTagText},
		{name: "no live check at all: the run's finding stands",
			rec:  record(change(inventory.ChangeChanged), releaseFindingStep),
			want: testMissedTagText},
		{name: "a failed step is the run's own and is not verified away",
			rec:  checked(record(change(inventory.ChangeChanged), failedStep), reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictOK}),
			want: "bumblebee-repo: the circleci step failed (CircleCI answered 502) — look at the run, fix the cause and reconcile again"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompletionText(tc.rec); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestCompletionsMarkFindings: the change sentence and a failed step are
// about this run and are always told; a finding is told once.
func TestCompletionsMarkFindings(t *testing.T) {
	msgs := Completions(record(change(inventory.ChangeCreated), failedStep, findingStep))
	if len(msgs) != 3 || msgs[0].Finding || msgs[1].Finding || !msgs[2].Finding {
		t.Errorf("finding flags: %+v", msgs)
	}
}

// notices is klaus-gateway's notice endpoint: it records the texts posted,
// and refuses the ones named in refuse so a failed delivery can be told from
// a suppressed one.
func notices(t *testing.T, refuse map[string]bool) (*review.Client, *[]string) {
	t.Helper()
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n review.Notice
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if refuse[n.Text] {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		got = append(got, n.Text)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(review.Posted{ID: "notice-1", Channel: n.Channel, TS: "1.2"})
	}))
	t.Cleanup(srv.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("sa-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := review.New(review.Config{BaseURL: srv.URL, TokenFile: tokenFile})
	if err != nil {
		t.Fatal(err)
	}
	return c, &got
}

// runs replays consecutive reconciler runs over one repository, each with
// its steps, carrying Setup.Told from one to the next as the record does,
// and returns what reached the channel on each run.
func runs(t *testing.T, client *review.Client, posted *[]string, steps ...[]reconcile.StepResult) [][]string {
	t.Helper()
	ts := &Tools{t: &tools{d: Deps{Log: slog.New(slog.NewTextHandler(new(bytes.Buffer), nil)), Review: client}}}
	var told []string
	var out [][]string
	for _, st := range steps {
		rec := record(change(inventory.ChangeChanged), st...)
		rec.Setup.Told = told
		before := len(*posted)
		found, err := ts.tell(context.Background(), rec, testTeam, testNotices, Completions(rec))
		if err != nil {
			t.Fatalf("tell: %v", err)
		}
		ts.remember(context.Background(), rec, found)
		told = rec.Setup.Told
		out = append(out, append([]string(nil), (*posted)[before:]...))
	}
	return out
}

// TestFindingsAreToldOnce: a standing finding asks for a decision or a
// chore, which is on the record and on the page until someone does it. Every
// merged change of the entry re-reports it; the team hears it once, and
// again only after it has gone away and come back.
func TestFindingsAreToldOnce(t *testing.T) {
	client, posted := notices(t, nil)
	main := `bumblebee-repo: ruleset "protect-main" is not the engine's and is left alone — ` + testRulesetFix
	giantswarm := `bumblebee-repo: ruleset "protect-giantswarm" is not the engine's and is left alone — ` + testRulesetFix
	got := runs(t, client, posted,
		[]reconcile.StepResult{ruleset("protect-main")},                       // told
		[]reconcile.StepResult{ruleset("protect-main")},                       // already told
		[]reconcile.StepResult{ruleset("protect-main", "protect-giantswarm")}, // the new one alone
		[]reconcile.StepResult{okStep},                                        // both gone
		[]reconcile.StepResult{ruleset("protect-main")},                       // back: told again
	)
	want := [][]string{{main}, nil, {giantswarm}, nil, {main}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// TestAFindingThatDoesNotReachTheChannelStaysUntold: a finding is told when
// it was posted, so a gateway that refuses it is retried by the next run.
func TestAFindingThatDoesNotReachTheChannelStaysUntold(t *testing.T) {
	main := `bumblebee-repo: ruleset "protect-main" is not the engine's and is left alone — ` + testRulesetFix
	client, posted := notices(t, map[string]bool{main: true})
	rec := record(change(inventory.ChangeChanged), ruleset("protect-main"))
	ts := &Tools{t: &tools{d: Deps{Log: slog.New(slog.NewTextHandler(new(bytes.Buffer), nil)), Review: client}}}
	told, err := ts.tell(context.Background(), rec, testTeam, testNotices, Completions(rec))
	if len(*posted) != 0 || len(told) != 0 || err != nil || !rec.Setup.LastRun.Told {
		t.Errorf("a refused finding: posted %q, told %q, err %v, run told %v", *posted, told, err, rec.Setup.LastRun.Told)
	}
}

// TestARefusedChangeSentenceIsToldOnceLater: the change sentence is told
// once per run. A gateway that refuses it leaves the run untold and says so
// (the poller tells the run again); the next try posts it, and a try after
// that posts nothing.
func TestARefusedChangeSentenceIsToldOnceLater(t *testing.T) {
	rec := record(change(inventory.ChangeCreated))
	sentence := changeSentence(rec)
	if sentence == "" {
		t.Fatal("no change sentence for a creation")
	}
	refusing, _ := notices(t, map[string]bool{sentence: true})
	accepting, posted := notices(t, nil)
	tell := func(client *review.Client) error {
		ts := &Tools{t: &tools{d: Deps{Log: slog.New(slog.NewTextHandler(new(bytes.Buffer), nil)), Review: client}}}
		_, err := ts.tell(context.Background(), rec, testTeam, testNotices, Completions(rec))
		return err
	}
	if err := tell(refusing); err == nil || rec.Setup.LastRun.Told {
		t.Fatalf("a refused change sentence: err %v, run told %v", err, rec.Setup.LastRun.Told)
	}
	if err := tell(accepting); err != nil || !rec.Setup.LastRun.Told || len(*posted) != 1 || (*posted)[0] != sentence {
		t.Fatalf("the retry: err %v, run told %v, posted %q", err, rec.Setup.LastRun.Told, *posted)
	}
	if err := tell(accepting); err != nil || len(*posted) != 1 {
		t.Errorf("a told run is told again: err %v, posted %q", err, *posted)
	}
}

// TestCompletionsLeaveReleaseFindingsToTheWatch: the release watch tells the
// team about the latest release from the tag's own pipeline, once; a run's
// release finding on a repository the watch reads is not told a second time,
// and stays the run's news where the watch cannot read the pipeline — a
// private repository without a CircleCI token, unchecked.
func TestCompletionsLeaveReleaseFindingsToTheWatch(t *testing.T) {
	watched := record(change(inventory.ChangeChanged), releaseFindingStep)
	watched.Setup.Release = &inventory.ReleaseWatch{Tag: "v0.1.0", State: inventory.ReleaseUnbuilt}
	if got := CompletionText(watched); got != "" {
		t.Errorf("a watched repository's release finding is the watch's to tell, got %q", got)
	}
	unchecked := record(change(inventory.ChangeChanged), releaseFindingStep)
	unchecked.Setup.Release = &inventory.ReleaseWatch{Tag: "v0.1.0", State: inventory.ReleaseUnchecked, Reason: "private"}
	if got := CompletionText(unchecked); got != testMissedTagText {
		t.Errorf("an unchecked release stays the run's news:\ngot  %q\nwant %q", got, testMissedTagText)
	}
}
