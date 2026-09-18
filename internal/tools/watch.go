package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/google/go-github/v92/github"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// ToolWatchRepository follows a new repository to readiness.
const ToolWatchRepository = "watch_repository"

const argTimeout = "timeout"

// The watch's bounds: how often GitHub is read as the caller, how long one
// call waits without being told, and the most it may wait — under the
// MCPServer's 180 s.
const (
	DefaultWatchInterval = 5 * time.Second
	DefaultWatchTimeout  = 120
	MaxWatchTimeout      = 150
)

// The phases of a new repository, in order: the repository exists, its
// default branch carries the scaffold, the declaration pull request is open,
// merged, the reconciler run of that pull request has reported, and the
// first release exists with its CircleCI statuses green.
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
			"carried as findings), released (the first release exists and the CircleCI statuses on its commit are green; a failing status fails the phase; while "+
			"CircleCI has not reported on the release's commit — a repository the reconciler has only just followed — the reconciler run's release step decides: "+
			"a failed step or a red-release finding fails the phase, anything else keeps waiting for the statuses). ready is true when every phase is done; "+
			"pending names the phase still waited for when the timeout ran out — call again to keep following. GitHub is read as you every few seconds. "+
			"Takes the repository and the pull request number create_repository's answer carries."),
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

// watch reads the phases as the caller until the repository is ready, a
// phase fails or the timeout runs out.
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
	// run is the reconciler run, from the setUp phase.
	run *inventory.LastRun
}

// outcome is what one read of a phase found: done at a time, failed for a
// reason, or neither — pending.
type outcome struct {
	done   bool
	at     time.Time
	reason string
}

func done(at time.Time) outcome    { return outcome{done: true, at: at} }
func failed(reason string) outcome { return outcome{reason: reason} }

var pending = outcome{}

// advance reads the phases after the ones done, in order, until one is
// pending or failed, or all are done. An error is a read that could not be
// made (the tool's error), not a phase failing.
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
		case o.reason != "":
			w.out.Failure = &Failure{Phase: c.name, Reason: o.reason}
			return nil
		case !o.done:
			w.out.Pending = c.name
			return nil
		}
		ph := Phase{Name: c.name, At: o.at.UTC()}
		if n := len(w.out.Phases); n > 0 {
			ph.Seconds = int(ph.At.Sub(w.out.Phases[n-1].At).Round(time.Second) / time.Second)
		}
		w.out.Phases = append(w.out.Phases, ph)
	}
	w.out.Ready, w.out.Pending = true, ""
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
		return pending, fmt.Errorf("read %s/%s as you: %w", w.org(), w.name, err)
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
		return pending, fmt.Errorf("read the commits of %s/%s as you: %w", w.org(), w.name, err)
	case len(commits) == 0:
		return pending, nil
	}
	return done(commits[0].GetCommit().GetCommitter().GetDate().Time), nil
}

// pull reads the declaration pull request as the caller.
func (w *watcher) pull(ctx context.Context) (*github.PullRequest, error) {
	r := w.p.repo
	pr, resp, err := r.Client.PullRequests.Get(ctx, r.Owner, r.Name, w.number)
	switch {
	case notFound(resp, err):
		return nil, fmt.Errorf("%s/%s#%d does not exist: pass the pull request number create_repository answered", r.Owner, r.Name, w.number)
	case err != nil:
		return nil, fmt.Errorf("read %s/%s#%d as you: %w", r.Owner, r.Name, w.number, err)
	}
	return pr, nil
}

// declared: the pull request is open and declares this repository.
func (w *watcher) declared(ctx context.Context) (outcome, error) {
	pr, err := w.pull(ctx)
	if err != nil {
		return pending, err
	}
	if !strings.Contains(pr.GetTitle()+"\n"+pr.GetBody(), w.name) {
		return failed(fmt.Sprintf("%s does not declare %s: it names neither in its title nor in its body", pr.GetHTMLURL(), w.name)), nil
	}
	w.out.PullRequest = pr.GetHTMLURL()
	return done(pr.GetCreatedAt().Time), nil
}

// merged: the pull request is merged; closed without a merge fails.
func (w *watcher) merged(ctx context.Context) (outcome, error) {
	pr, err := w.pull(ctx)
	if err != nil {
		return pending, err
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

// released: the first release exists and the CircleCI statuses on its commit
// are green. A failing status fails the phase. Without any status yet, the
// reconciler run's release step decides: failed, or a red-release finding,
// fails the phase; anything else waits for the statuses.
func (w *watcher) released(ctx context.Context) (outcome, error) {
	rel, resp, err := w.p.gh.Repositories.GetLatestRelease(ctx, w.org(), w.name)
	switch {
	case notFound(resp, err):
		return pending, nil
	case err != nil:
		return pending, fmt.Errorf("read the releases of %s/%s as you: %w", w.org(), w.name, err)
	}
	tag := rel.GetTagName()
	w.out.Release = &WatchRelease{Tag: tag, URL: rel.GetHTMLURL()}
	st, _, err := w.p.gh.Repositories.GetCombinedStatus(ctx, w.org(), w.name, tag, &github.ListOptions{PerPage: 100})
	if err != nil {
		return pending, fmt.Errorf("read the statuses of %s/%s@%s as you: %w", w.org(), w.name, tag, err)
	}
	switch {
	case st.GetTotalCount() == 0:
		return w.releaseStepVerdict(), nil
	case st.GetState() == "success":
		return done(newestStatus(st)), nil
	case st.GetState() == "pending":
		return pending, nil
	}
	var red []string
	for _, s := range st.Statuses {
		if s.GetState() != "success" && s.GetState() != "pending" {
			red = append(red, s.GetContext()+" ("+s.GetState()+")")
		}
	}
	return failed(fmt.Sprintf("the CircleCI statuses on %s are %s: %s", tag, st.GetState(), strings.Join(red, ", "))), nil
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
