package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/google/go-github/v92/github"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// ToolWatchRepository follows a new repository to readiness.
const ToolWatchRepository = "watch_repository"

const argTimeout = "timeout"

// The watch's bounds: how often GitHub is read, how long the release's
// statuses must stay unchanged before they count — CircleCI posts one status
// per job as the job starts, 20–60 s apart, so a green set between two jobs
// is not the pipeline's verdict — how long one call waits without being
// told, and the most it may wait — under the MCPServer's 180 s.
const (
	DefaultWatchInterval = 5 * time.Second
	DefaultWatchSettle   = 60 * time.Second
	DefaultWatchTimeout  = 120
	MaxWatchTimeout      = 150
)

// contextImagePush is the CircleCI job that pushes the image, reported on a
// repository whose default branch carries a Dockerfile; a chart's jobs name
// chart (build-chart, push-chart).
const contextImagePush = "push-to-registries"

// The phases of a new repository, in order: the repository exists, its
// default branch carries the scaffold, the declaration pull request is open,
// merged, the reconciler run of that pull request has reported, and the
// first release exists with its CircleCI statuses green, complete for the
// declaration and settled.
const (
	PhaseCreated    = "created"
	PhaseScaffolded = "scaffolded"
	PhaseDeclared   = "declared"
	PhaseMerged     = "merged"
	PhaseSetUp      = "setUp"
	PhaseReleased   = "released"
)

// Watch is watch_repository's result: the phases done so far with when each
// was reached, and whether the repository is ready, still pending or failed.
type Watch struct {
	// Repository and PullRequest are the URLs on GitHub.
	Repository  string  `json:"repository"`
	PullRequest string  `json:"pullRequest,omitempty"`
	Phases      []Phase `json:"phases"`
	// Ready says every phase is done without a failure.
	Ready bool `json:"ready"`
	// Pending names the phase still waited for when the call's timeout ran
	// out; empty when ready or failed.
	Pending string `json:"pending,omitempty"`
	// PendingReason says why the pending phase could not be decided on the
	// last read — a read GitHub refused, the statuses no identity reads, or,
	// once CircleCI reports on the release, the statuses reported and what
	// is still awaited; empty when the phase is simply not reached yet.
	PendingReason string `json:"pendingReason,omitempty"`
	// Failure names the phase that failed and why; the phases before it are
	// done.
	Failure *Failure `json:"failure,omitempty"`
	// Release is the first release once it exists.
	Release *WatchRelease `json:"release,omitempty"`
	// Findings are the reconciler run's findings for a person, once it has
	// reported (a default icon, an unchecked step, …); they do not fail the
	// setUp phase unless they refuse the entry.
	Findings []reconcile.Finding `json:"findings,omitempty"`
	// Waited is how many seconds this call blocked.
	Waited int `json:"waited"`
}

// Phase is one phase done, with when it was reached and how many seconds
// after the phase before it.
type Phase struct {
	Name    string    `json:"name"`
	At      time.Time `json:"at"`
	Seconds int       `json:"seconds"`
}

// Failure is the phase that failed and why.
type Failure struct {
	Phase  string `json:"phase"`
	Reason string `json:"reason"`
}

// WatchRelease is the first release.
type WatchRelease struct {
	Tag string `json:"tag"`
	URL string `json:"url"`
}

func (t *tools) registerWatch(s *mcpserver.MCPServer) {
	s.AddTool(mcp.NewTool(ToolWatchRepository,
		mcp.WithDescription("Read-only. Follow a new repository to readiness after create_repository, and return when it is ready, when a phase fails, "+
			"or when timeout runs out — with the phases reached either way, each with its timestamp and the seconds since the phase before: "+
			"created (the repository exists), scaffolded (its default branch carries the scaffold commit), declared (the declaration pull request is open), "+
			"merged, setUp (the reconciler run of that pull request has reported: a failed step or a refused entry fails the phase, the run's other findings are "+
			"carried as findings), released (the first release exists and the CircleCI statuses on its commit are green, complete and settled: CircleCI posts one status "+
			"per job as the job starts, so a green set counts only once it holds the jobs the declaration implies — a chart job when the flavours produce a chart, "+
			fmt.Sprintf("push-to-registries when the default branch carries a Dockerfile — and has not changed for %d s; a failing status fails the phase at once; ", int(DefaultWatchSettle.Seconds()))+
			"while the statuses are pending, incomplete or settling, pendingReason lists the ones reported and what is awaited; while CircleCI has not reported "+
			"on the release's commit at all — a repository the reconciler has only just followed — the reconciler run's release step decides: a failed step or a "+
			"red-release finding fails the phase, anything else keeps waiting for the statuses). ready is true when every phase is done; "+
			"pending names the phase still waited for when the timeout ran out — call again to keep following. Who reads what: the repository, its commits, "+
			"the pull request and the release are read as you every few seconds; the commit statuses and the Dockerfile as the inventory App giantswarm-repo-manager-inventory, "+
			"the identity of the unattended reads (your token through the App giantswarm-repo-manager cannot read them) — without that App the reconciler "+
			"run's release step alone decides the released phase and pendingReason says so. A read GitHub refuses (403) keeps its phase pending with the "+
			"refusal in pendingReason; the call errors only on wrong arguments. Takes the repository and the pull request number create_repository's answer carries."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString(argRepository, mcp.Required(), mcp.Description("Repository name, with or without the org.")),
		mcp.WithNumber(argPullRequest, mcp.Required(), mcp.Description("The declaration pull request's number in the team files repository (create_repository's pullRequest.number).")),
		mcp.WithNumber(argTimeout, mcp.Description(fmt.Sprintf("Seconds to wait before answering with what is pending (default %d, at most %d — under the MCPServer's 180 s).", DefaultWatchTimeout, MaxWatchTimeout)),
			mcp.DefaultNumber(DefaultWatchTimeout), mcp.Max(MaxWatchTimeout)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return result(t.watch(ctx, req.GetArguments()))
	})
}

// watchInterval is how often the watch reads GitHub.
func (d Deps) watchInterval() time.Duration {
	if d.WatchInterval > 0 {
		return d.WatchInterval
	}
	return DefaultWatchInterval
}

// watchSettle is how long the release's statuses must stay unchanged before
// the released phase is done.
func (d Deps) watchSettle() time.Duration {
	if d.WatchSettle > 0 {
		return d.WatchSettle
	}
	return DefaultWatchSettle
}

// watch reads the phases until the repository is ready, a phase fails or
// the timeout runs out.
func (t *tools) watch(ctx context.Context, args map[string]any) (*Watch, error) {
	if t.d.Inventory == nil {
		return nil, errors.New("inventory store not configured (VALKEY_ADDR): the setUp phase is read from the record")
	}
	name, err := t.repositoryName(args)
	if err != nil {
		return nil, err
	}
	pr := int(number(args, argPullRequest, 0))
	if pr <= 0 {
		return nil, fmt.Errorf("%s is required: the declaration pull request's number", argPullRequest)
	}
	timeout := number(args, argTimeout, DefaultWatchTimeout)
	if timeout <= 0 || timeout > MaxWatchTimeout {
		return nil, fmt.Errorf("%s must be above 0 and at most %d seconds (the MCPServer's 180 s bound the call)", argTimeout, MaxWatchTimeout)
	}
	p, err := t.caller(ctx)
	if err != nil {
		return nil, err
	}
	w := &watcher{t: t, p: p, name: name, number: pr, out: &Watch{Phases: []Phase{}}}
	start := time.Now()
	deadline := start.Add(time.Duration(timeout * float64(time.Second)))
	for {
		if err := w.advance(ctx); err != nil {
			return nil, err
		}
		remaining := time.Until(deadline)
		if w.out.Ready || w.out.Failure != nil || remaining <= 0 {
			break
		}
		if err := sleep(ctx, min(t.d.watchInterval(), remaining)); err != nil {
			return nil, err
		}
	}
	w.out.Waited = int(time.Since(start).Round(time.Second) / time.Second)
	return w.out, nil
}

// sleep waits d unless ctx ends first.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// watcher is one watch: the caller, the repository and the pull request,
// and the phases reached so far.
type watcher struct {
	t      *tools
	p      *person
	name   string
	number int
	out    *Watch
	// branch is the default branch, from the created phase.
	branch string
	// flavours are the declaration's, from the record read in the setUp
	// phase: they say whether a chart job is expected on the release.
	flavours []string
	// run is the reconciler run, from the setUp phase.
	run *inventory.LastRun
	// dockerfile says whether the default branch carries a Dockerfile — the
	// image push job's condition — once read; nil before.
	dockerfile *bool
}

// outcome is what one read of a phase found: done at a time, failed for a
// reason, or neither — pending, with the reason it could not be decided
// when a read was refused.
type outcome struct {
	done   bool
	failed bool
	at     time.Time
	reason string
}

func done(at time.Time) outcome    { return outcome{done: true, at: at} }
func failed(reason string) outcome { return outcome{failed: true, reason: reason} }

// undecided is a phase left pending with why it could not be decided: a read
// refused, no identity that makes it, or the release's statuses still
// awaited — a fact about the phase, never the tool's error.
func undecided(reason string) outcome { return outcome{reason: reason} }

var pending = outcome{}

// advance reads the phases after the ones done, in order, until one is
// pending or failed, or all are done. An error is the tool's own: a wrong
// argument or the inventory store, never a read GitHub refused.
func (w *watcher) advance(ctx context.Context) error {
	checks := []struct {
		name  string
		check func(context.Context) (outcome, error)
	}{
		{PhaseCreated, w.created}, {PhaseScaffolded, w.scaffolded}, {PhaseDeclared, w.declared},
		{PhaseMerged, w.merged}, {PhaseSetUp, w.setUp}, {PhaseReleased, w.released},
	}
	for _, c := range checks[len(w.out.Phases):] {
		o, err := c.check(ctx)
		if err != nil {
			return err
		}
		switch {
		case o.failed:
			w.out.Failure = &Failure{Phase: c.name, Reason: o.reason}
			return nil
		case !o.done:
			w.out.Pending, w.out.PendingReason = c.name, o.reason
			return nil
		}
		ph := Phase{Name: c.name, At: o.at.UTC()}
		if n := len(w.out.Phases); n > 0 {
			ph.Seconds = int(ph.At.Sub(w.out.Phases[n-1].At).Round(time.Second) / time.Second)
		}
		w.out.Phases = append(w.out.Phases, ph)
	}
	w.out.Ready, w.out.Pending, w.out.PendingReason = true, "", ""
	return nil
}

func (w *watcher) org() string { return w.t.org() }

// created: the repository exists.
func (w *watcher) created(ctx context.Context) (outcome, error) {
	repo, resp, err := w.p.gh.Repositories.Get(ctx, w.org(), w.name)
	switch {
	case notFound(resp, err):
		return pending, nil
	case err != nil:
		return undecided(fmt.Sprintf("read %s/%s as you: %v", w.org(), w.name, err)), nil
	}
	w.out.Repository, w.branch = repo.GetHTMLURL(), repo.GetDefaultBranch()
	return done(repo.GetCreatedAt().Time), nil
}

// scaffolded: the default branch has a head commit — the scaffold, pushed
// as one commit.
func (w *watcher) scaffolded(ctx context.Context) (outcome, error) {
	commits, resp, err := w.p.gh.Repositories.ListCommits(ctx, w.org(), w.name, &github.CommitsListOptions{SHA: w.branch, ListOptions: github.ListOptions{PerPage: 1}})
	switch {
	case resp != nil && resp.StatusCode == http.StatusConflict, notFound(resp, err):
		// An empty repository answers 409; a branch not yet pushed 404.
		return pending, nil
	case err != nil:
		return undecided(fmt.Sprintf("read the commits of %s/%s as you: %v", w.org(), w.name, err)), nil
	case len(commits) == 0:
		return pending, nil
	}
	return done(commits[0].GetCommit().GetCommitter().GetDate().Time), nil
}

// pull reads the declaration pull request as the caller. Without the pull
// request the outcome stands: a refused read leaves the phase pending; a
// number that does not exist is the tool's error.
func (w *watcher) pull(ctx context.Context) (*github.PullRequest, outcome, error) {
	r := w.p.repo
	pr, resp, err := r.Client.PullRequests.Get(ctx, r.Owner, r.Name, w.number)
	switch {
	case notFound(resp, err):
		return nil, pending, fmt.Errorf("%s/%s#%d does not exist: pass the pull request number create_repository answered", r.Owner, r.Name, w.number)
	case err != nil:
		return nil, undecided(fmt.Sprintf("read %s/%s#%d as you: %v", r.Owner, r.Name, w.number, err)), nil
	}
	return pr, pending, nil
}

// declared: the pull request is open and declares this repository.
func (w *watcher) declared(ctx context.Context) (outcome, error) {
	pr, o, err := w.pull(ctx)
	if pr == nil {
		return o, err
	}
	if !strings.Contains(pr.GetTitle()+"\n"+pr.GetBody(), w.name) {
		return failed(fmt.Sprintf("%s does not declare %s: it names neither in its title nor in its body", pr.GetHTMLURL(), w.name)), nil
	}
	w.out.PullRequest = pr.GetHTMLURL()
	return done(pr.GetCreatedAt().Time), nil
}

// merged: the pull request is merged; closed without a merge fails.
func (w *watcher) merged(ctx context.Context) (outcome, error) {
	pr, o, err := w.pull(ctx)
	if pr == nil {
		return o, err
	}
	switch {
	case pr.GetMerged():
		return done(pr.GetMergedAt().Time), nil
	case pr.GetState() == "closed":
		return failed(fmt.Sprintf("%s was closed without being merged", pr.GetHTMLURL())), nil
	}
	return pending, nil
}

// setUp: the record's last run is the one of the pull request. Its failed
// steps fail the phase, so does a refused entry (the entry step's
// findings); the other findings are carried for the person.
func (w *watcher) setUp(ctx context.Context) (outcome, error) {
	rec, err := w.t.d.Inventory.Get(ctx, w.org()+"/"+w.name)
	switch {
	case errors.Is(err, inventory.ErrNotFound):
		return pending, nil
	case err != nil:
		return pending, err
	}
	if rec.Declaration != nil {
		w.flavours = rec.Declaration.Flavours
	}
	if m := rec.Setup.MissingRun; m != nil && m.Follows(w.number) {
		return failed(rec.MissingRunFinding().Message), nil
	}
	run := rec.Setup.LastRun
	if run == nil || run.Change == nil || run.Change.PullRequest == nil || run.Change.PullRequest.Number != w.number {
		return pending, nil
	}
	w.run = run
	w.out.Findings = run.Result.Findings()
	if fs := run.Result.Failed(); len(fs) > 0 {
		reasons := make([]string, 0, len(fs))
		for _, f := range fs {
			reasons = append(reasons, fmt.Sprintf("the %s step failed: %s", f.Step, f.Summary))
		}
		return failed(strings.Join(reasons, "; ") + " (" + run.RunURL + ")"), nil
	}
	if s := run.Result.Step(reconcile.StepEntry); s != nil {
		return failed(fmt.Sprintf("the reconciler refused the entry and ran no step: %s (%s)", findingsText(s.Findings), run.RunURL)), nil
	}
	return done(run.Timestamp), nil
}

// released: the first release exists (read as the caller) and the CircleCI
// statuses on its commit are green, complete and settled — read as the
// inventory App, the identity of the unattended reads: the caller's token
// through the App giantswarm-repo-manager has no statuses permission. A
// failing status fails the phase at once. Without any status yet, the
// reconciler run's release step decides: failed, or a red-release finding,
// fails the phase; anything else waits for the statuses. Without the
// inventory App the release step alone decides, and the pending phase says
// so.
func (w *watcher) released(ctx context.Context) (outcome, error) {
	rel, resp, err := w.p.gh.Repositories.GetLatestRelease(ctx, w.org(), w.name)
	switch {
	case notFound(resp, err):
		return pending, nil
	case err != nil:
		return undecided(fmt.Sprintf("read the releases of %s/%s as you: %v", w.org(), w.name, err)), nil
	}
	tag := rel.GetTagName()
	w.out.Release = &WatchRelease{Tag: tag, URL: rel.GetHTMLURL()}
	if w.t.d.App == nil {
		if o := w.releaseStepVerdict(); o.failed {
			return o, nil
		}
		return undecided(fmt.Sprintf("the CircleCI statuses on %s are not read — %v; the reconciler run's release step alone decides", tag, ErrNoApp)), nil
	}
	st, _, err := w.t.d.App.Installation().Repositories.GetCombinedStatus(ctx, w.org(), w.name, tag, &github.ListOptions{PerPage: 100})
	if err != nil {
		return undecided(fmt.Sprintf("read the statuses of %s/%s@%s as the inventory App: %v", w.org(), w.name, tag, err)), nil
	}
	if st.GetTotalCount() == 0 {
		return w.releaseStepVerdict(), nil
	}
	return w.statusesVerdict(ctx, tag, st)
}

// statusesVerdict is the released phase once CircleCI has reported on the
// release: a red status fails it at once; a pending status, a job the
// declaration implies that has not reported, or a set younger than the
// settle window keep it pending, with the statuses reported and what is
// awaited as the reason.
func (w *watcher) statusesVerdict(ctx context.Context, tag string, st *github.CombinedStatus) (outcome, error) {
	var red, waiting, reported []string
	for _, s := range st.Statuses {
		reported = append(reported, s.GetContext()+" ("+s.GetState()+")")
		switch s.GetState() {
		case "success":
		case "pending":
			waiting = append(waiting, s.GetContext())
		default:
			red = append(red, s.GetContext()+" ("+s.GetState()+")")
		}
	}
	if len(red) > 0 {
		return failed(fmt.Sprintf("the CircleCI statuses on %s are %s: %s", tag, st.GetState(), strings.Join(red, ", "))), nil
	}
	line := fmt.Sprintf("the CircleCI statuses on %s: %s", tag, strings.Join(reported, ", "))
	if len(waiting) > 0 {
		return undecided(line + " — waiting for the pending ones"), nil
	}
	awaited, err := w.awaited(ctx, st)
	switch {
	case err != nil:
		return undecided(fmt.Sprintf("%s — %v", line, err)), nil
	case len(awaited) > 0:
		return undecided(fmt.Sprintf("%s — awaiting %s", line, strings.Join(awaited, ", "))), nil
	}
	newest := newestStatus(st)
	if age := time.Since(newest); age < w.t.d.watchSettle() {
		return undecided(fmt.Sprintf("%s — green for %s, released once unchanged for %s (CircleCI posts a job's status as the job starts)",
			line, age.Round(time.Second), w.t.d.watchSettle())), nil
	}
	return done(newest), nil
}

// awaited names the jobs the declaration implies that have not reported on
// the release: a chart job (a context naming chart) when the flavours
// produce a chart, the image push when the default branch carries a
// Dockerfile.
func (w *watcher) awaited(ctx context.Context, st *github.CombinedStatus) ([]string, error) {
	has := func(sub string) bool {
		for _, s := range st.Statuses {
			if strings.Contains(s.GetContext(), sub) {
				return true
			}
		}
		return false
	}
	var out []string
	if reposetup.HasChart(w.flavours) && !has("chart") {
		out = append(out, "a chart job")
	}
	image, err := w.hasDockerfile(ctx)
	if err != nil {
		return nil, err
	}
	if image && !has(contextImagePush) {
		out = append(out, contextImagePush)
	}
	return out, nil
}

// hasDockerfile says whether the default branch carries a root Dockerfile,
// read once as the inventory App.
func (w *watcher) hasDockerfile(ctx context.Context) (bool, error) {
	if w.dockerfile == nil {
		_, _, resp, err := w.t.d.App.Installation().Repositories.GetContents(ctx, w.org(), w.name, "Dockerfile", &github.RepositoryContentGetOptions{Ref: w.branch})
		if err != nil && !notFound(resp, err) {
			return false, fmt.Errorf("read the Dockerfile of %s/%s as the inventory App: %w", w.org(), w.name, err)
		}
		has := err == nil
		w.dockerfile = &has
	}
	return *w.dockerfile, nil
}

// releaseStepVerdict is the released phase while CircleCI has not reported:
// the reconciler run's release step decides.
func (w *watcher) releaseStepVerdict() outcome {
	s := w.run.Result.Step(reconcile.StepRelease)
	if s == nil {
		return pending
	}
	if s.Verdict == reconcile.VerdictFailed {
		return failed(fmt.Sprintf("the reconciler's release step failed: %s (%s)", s.Summary, w.run.RunURL))
	}
	for _, f := range s.Findings {
		if f.Kind == reconcile.FindingRedRelease {
			return failed(f.Message)
		}
	}
	return pending
}

// newestStatus is when the newest of the combined status' statuses was
// posted.
func newestStatus(st *github.CombinedStatus) time.Time {
	var at time.Time
	for _, s := range st.Statuses {
		if u := s.GetUpdatedAt().Time; u.After(at) {
			at = u
		}
	}
	return at
}

func notFound(resp *github.Response, err error) bool {
	return err != nil && resp != nil && resp.StatusCode == http.StatusNotFound
}

func findingsText(fs []reconcile.Finding) string {
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		parts = append(parts, f.Message)
	}
	return strings.Join(parts, "; ")
}
