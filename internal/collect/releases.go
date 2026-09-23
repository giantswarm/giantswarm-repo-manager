package collect

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/circleciclient"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// ReleaseOptions tune the release watch: the latest release of every
// declared repository, followed from its tag to the end of the tag's own
// CircleCI pipeline, so the team hears once when nothing was published —
// within one interval of the pipeline settling, whoever merged and however.
type ReleaseOptions struct {
	// Interval is how often the watch reads; 0 turns it off.
	Interval time.Duration
	// Grace is how long after its creation a release may have no pipeline
	// before it is a missed build; 0 is DefaultReleaseGrace.
	Grace time.Duration
	// CircleCI reads the tag's pipeline, its workflows and their jobs; nil
	// turns the watch off.
	CircleCI *circleciclient.Client
	// Anonymous says the client holds no token: it reads public projects,
	// and a private project's 404 is the missing token, not a project
	// CircleCI does not know.
	Anonymous bool
}

// The watch's defaults and bounds.
const (
	DefaultReleaseInterval = 5 * time.Minute
	DefaultReleaseGrace    = 10 * time.Minute
	// releaseLookback bounds what the watch picks up: a release created
	// longer ago than this when the watch first sees it — before the watch
	// existed, or while the manager was down — is the sweep's to read, not
	// news.
	releaseLookback = 6 * time.Hour
	// releasePage is how many of the most recently pushed repositories one
	// pass reads: a tag follows the push of its commit within minutes, so
	// every repository with a fresh release is among them. One GraphQL
	// query per pass, whatever the org's size.
	releasePage = 100
)

func (o *ReleaseOptions) defaults() {
	if o.Grace <= 0 {
		o.Grace = DefaultReleaseGrace
	}
}

// ReleasePoll is what one pass of the watch did.
type ReleasePoll struct {
	// Seen is how many repositories the page carried, Started the releases
	// the pass began to follow, Settled the ones whose state became final
	// this pass, Watching the ones the watch still follows after it (running,
	// and red until a rerun or the next tag), Told the notices posted.
	Seen, Started, Watching, Settled, Told int
}

// releasesQuery is the pass's one GraphQL query: the most recently pushed
// repositories with their latest release, its tag commit and the pull
// request behind it. Ordered by push because a tag is cut minutes after
// its commit was pushed; archived repositories are left out at the source.
const releasesQuery = `
query($org: String!, $first: Int!) {
  organization(login: $org) {
    repositories(first: $first, isArchived: false, orderBy: {field: PUSHED_AT, direction: DESC}) {
      nodes {
        name visibility pushedAt
        latestRelease { tagName createdAt tagCommit { oid associatedPullRequests(first: 1) { nodes { number url } } } }
      }
    }
  }
  rateLimit { cost remaining limit resetAt }
}`

// releaseNode is one repository of the pass's page.
type releaseNode struct {
	Name          string    `json:"name"`
	Visibility    string    `json:"visibility"`
	PushedAt      time.Time `json:"pushedAt"`
	LatestRelease *struct {
		TagName   string    `json:"tagName"`
		CreatedAt time.Time `json:"createdAt"`
		TagCommit *struct {
			OID                    string `json:"oid"`
			AssociatedPullRequests struct {
				Nodes []struct {
					Number int    `json:"number"`
					URL    string `json:"url"`
				} `json:"nodes"`
			} `json:"associatedPullRequests"`
		} `json:"tagCommit"`
	} `json:"latestRelease"`
}

// pullRequest is the pull request behind the release's commit, nil without one.
func (n *releaseNode) pullRequest() *inventory.ChangePullRequest {
	if n.LatestRelease == nil || n.LatestRelease.TagCommit == nil || len(n.LatestRelease.TagCommit.AssociatedPullRequests.Nodes) == 0 {
		return nil
	}
	pr := n.LatestRelease.TagCommit.AssociatedPullRequests.Nodes[0]
	return &inventory.ChangePullRequest{Number: pr.Number, URL: pr.URL}
}

// OnReleased registers the hook the watch calls when it settles a release
// nothing was published for — red, or unbuilt within the grace period — with
// the record as it will be stored. The hook tells the team and returns the
// sentence it posted, empty when nothing reached the channel; the watch
// stores the sentence as the release's Told, so the team hears it once.
func (c *Collector) OnReleased(fn func(context.Context, *inventory.Record) string) { c.released = fn }

// RunReleaseWatch passes every Interval until ctx is done; off without an
// interval or a client. A failed pass is logged and the next one runs on
// time.
func (c *Collector) RunReleaseWatch(ctx context.Context) {
	o := c.opts.Releases
	if o.Interval <= 0 || o.CircleCI == nil {
		c.log.Info("release watch off", "interval", o.Interval, "circleci", o.CircleCI != nil)
		return
	}
	c.log.Info("release watch on", "interval", o.Interval, "grace", o.Grace, "anonymous", o.Anonymous)
	pollLoop(ctx, o.Interval, o.Interval, nil, func(ctx context.Context) bool {
		poll, err := c.WatchReleases(ctx)
		if err != nil {
			c.log.Error("release watch pass failed", "error", err)
			return false
		}
		if poll.Started+poll.Settled+poll.Told > 0 || poll.Watching > 0 {
			c.log.Info("release watch pass", "seen", poll.Seen, "started", poll.Started, "watching", poll.Watching, "settled", poll.Settled, "told", poll.Told)
		}
		return false
	})
}

// WatchReleases is one pass: the page of recently pushed repositories, a
// release not settled yet on a declared repository followed on CircleCI by
// the tag's own pipeline, the record written with what the watch knows, the
// team told once when nothing was published. The budget of a pass: one
// GraphQL query, one store read per candidate, and for every release still
// running one pipeline listing, one workflow listing and the jobs of a
// failed workflow on CircleCI.
func (c *Collector) WatchReleases(ctx context.Context) (*ReleasePoll, error) {
	o := c.opts.Releases
	if o.CircleCI == nil {
		return nil, errors.New("release watch: no CircleCI client")
	}
	if err := c.seedWatching(ctx); err != nil {
		return nil, err
	}
	nodes, err := c.fetchReleases(ctx)
	if err != nil {
		return nil, err
	}
	now := c.now()
	poll := &ReleasePoll{Seen: len(nodes)}
	// The candidates: the page's repositories with a release young enough,
	// plus every repository the watch is following whatever the page says.
	cand := map[string]*releaseNode{}
	for i := range nodes {
		n := &nodes[i]
		if n.LatestRelease == nil || now.Sub(n.LatestRelease.CreatedAt) > releaseLookback {
			continue
		}
		cand[n.Name] = n
	}
	c.watchMu.Lock()
	for name := range c.watching {
		if _, ok := cand[name]; !ok {
			cand[name] = nil
		}
	}
	c.watchMu.Unlock()
	names := make([]string, 0, len(cand))
	for n := range cand {
		names = append(names, n)
	}
	sort.Strings(names)
	var mu sync.Mutex
	parallel(c.opts.Concurrency, names, func(name string) {
		out := c.watchRelease(ctx, name, cand[name], now)
		mu.Lock()
		defer mu.Unlock()
		if out.started {
			poll.Started++
		}
		if out.told {
			poll.Told++
		}
		if out.settled {
			poll.Settled++
		}
		if out.watching {
			poll.Watching++
		}
	})
	return poll, nil
}

// watched is what one repository's pass did.
type watched struct{ started, watching, settled, told bool }

// watchRelease follows one repository's release: node is the page's
// repository (nil when the page did not carry it: the watch followed it
// already), now the pass's clock.
func (c *Collector) watchRelease(ctx context.Context, name string, node *releaseNode, now time.Time) watched {
	key := c.opts.Org + "/" + name
	rec, err := c.store.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, inventory.ErrNotFound) {
			c.log.Warn("release watch: record not read", "repository", key, "error", err)
		}
		c.follow(name, false)
		return watched{}
	}
	if rec.Declaration == nil || rec.Reality == nil || rec.Reality.IsArchived {
		// Nobody to tell, or nothing that releases.
		c.follow(name, false)
		return watched{}
	}
	w := rec.Setup.Release
	var out watched
	switch {
	case node != nil && node.LatestRelease != nil && (w == nil || w.Tag != node.LatestRelease.TagName):
		// A new release: the watch starts from its tag.
		w = &inventory.ReleaseWatch{Tag: node.LatestRelease.TagName, CreatedAt: node.LatestRelease.CreatedAt, PullRequest: node.pullRequest(), State: inventory.ReleaseWatching, CheckedAt: now}
		if reason := outsideReason(w.Tag, rec); reason != "" {
			w.Settle(now, inventory.ReleaseUnchecked)
			w.Reason = reason
		}
		out.started = true
	case !w.Following():
		c.follow(name, false)
		return watched{}
	}
	before := w.State
	if w.Following() {
		c.readRelease(ctx, rec, w, now)
	}
	rec.Setup.Release = w
	rewriteReleaseStep(rec)
	rec.Finalize()
	if w.Untold() {
		if before := toldByTheRun(rec, w.Tag); before != "" {
			// The completion path told the team about this release from a
			// reconciler run's finding before the watch read the tag: once
			// is enough.
			w.Told, w.ToldAt = before, ptr(now)
		} else if c.released != nil {
			if told := c.released(ctx, rec); told != "" {
				w.Told, w.ToldAt = told, ptr(now)
				out.told = true
			}
		}
	}
	if err := c.store.Put(ctx, rec); err != nil {
		c.log.Warn("release watch: record not stored", "repository", key, "error", err)
	}
	out.settled, out.watching = w.Settled() && w.State != before, w.Following()
	c.follow(name, out.watching)
	if out.settled {
		c.log.Info("release settled", "repository", key, "tag", w.Tag, "state", w.State, "was", before, "failedJobs", w.FailedJobs, "reason", w.Reason, "told", w.Told != "")
	}
	return out
}

// toldByTheRun is the sentence a reconciler run's release finding told the
// team about tag, when the completion path posted one (setup.told) before
// the watch settled the release: the engine's and the record's release
// findings both open with "release <tag> of <owner/name>". Empty when none.
func toldByTheRun(rec *inventory.Record, tag string) string {
	prefix := rec.Name + ": release " + tag + " of " + rec.Repository
	for _, s := range rec.Setup.Told {
		if strings.HasPrefix(s, prefix) {
			return s
		}
	}
	return ""
}

// outsideReason says why a release is outside the watch's reach before any
// read: a tag outside the vX.Y.Z flow auto-release cuts and the generated
// pipeline builds, or a repository whose default branch carries no CircleCI
// pipeline (released by GitHub Actions, or a configuration repository).
// Empty when the watch reads it.
func outsideReason(tag string, rec *inventory.Record) string {
	if !isReleaseTag(tag) {
		return "not a vX.Y.Z tag: outside the flow the tag pipeline builds"
	}
	if !rec.Reality.Has.CircleCI {
		return "no CircleCI pipeline on the default branch: nothing builds the tag"
	}
	return ""
}

// releaseTagRe is vMAJOR.MINOR.PATCH with an optional pre-release suffix
// (v1.2.3-rc.1) and build metadata: the strict semver shape the engine's
// release step verifies.
var releaseTagRe = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// isReleaseTag says whether tag is a release of the flow the watch follows:
// what auto-release cuts and the generated pipeline's tag filter builds; the
// engine's release step verifies the same tags.
func isReleaseTag(tag string) bool {
	return releaseTagRe.MatchString(tag)
}

// readRelease reads the tag's pipeline on CircleCI into w: the pipeline by
// its vcs.tag, of every workflow name the newest run (a rerun from failed
// is a second run of the same name; the one it replaced keeps its failed
// status), the failed jobs of a failed run. A failed run settles the
// release red at once; the watch keeps reading a red release, and a rerun
// that goes green turns it built on a later pass, silently — the team was
// told once, and hears nothing more about the tag. No pipeline within the
// grace period settles it unbuilt. A 404 settles it unchecked: without a
// token, the project is private and the token is what is missing; with
// one, CircleCI does not know the project. Any other error keeps the watch
// for the next pass.
func (c *Collector) readRelease(ctx context.Context, rec *inventory.Record, w *inventory.ReleaseWatch, now time.Time) {
	o := c.opts.Releases
	org, name := c.opts.Org, rec.Name
	w.CheckedAt = now
	p, err := o.CircleCI.FindPipelineByTag(ctx, org, name, w.Tag)
	switch {
	case circleciclient.IsNotFound(err):
		w.Settle(now, inventory.ReleaseUnchecked)
		w.Reason = "CircleCI does not know the project"
		if o.Anonymous {
			w.Reason = "the repository is private and no CircleCI token is configured (circleci.existingSecret): the tag pipeline is out of the inventory's reach"
		}
		return
	case err != nil:
		c.log.Warn("release watch: pipelines not read", "repository", rec.Repository, "tag", w.Tag, "error", err)
		return
	case p == nil:
		if now.Sub(w.CreatedAt) >= o.Grace {
			w.Settle(now, inventory.ReleaseUnbuilt)
		}
		return
	}
	w.Pipeline = &inventory.ReleasePipeline{Number: p.Number, URL: circleciclient.PipelineURL(org, name, p.Number)}
	runs, err := o.CircleCI.ListPipelineWorkflows(ctx, p.ID)
	if err != nil {
		c.log.Warn("release watch: workflows not read", "repository", rec.Repository, "tag", w.Tag, "pipeline", p.Number, "error", err)
		return
	}
	var failed []string
	running, succeeded := false, 0
	w.Pipeline.Workflow = ""
	for _, run := range circleciclient.NewestWorkflows(runs) {
		switch {
		case circleciclient.WorkflowFailed(run.Status):
			jobs, err := o.CircleCI.ListWorkflowJobs(ctx, run.ID)
			if err != nil {
				c.log.Warn("release watch: jobs not read", "repository", rec.Repository, "tag", w.Tag, "workflow", run.Name, "error", err)
				return
			}
			names := failedJobs(jobs)
			if len(names) == 0 {
				names = []string{run.Name + " (" + failureWord(run.Status) + ")"}
			}
			failed = append(failed, names...)
			if w.Pipeline.Workflow == "" {
				w.Pipeline.Workflow = circleciclient.WorkflowURL(org, name, p.Number, run.ID)
			}
		case circleciclient.WorkflowSucceeded(run.Status):
			succeeded++
		case run.Status == workflowNotRun:
		default:
			running = true
		}
	}
	switch {
	case len(failed) > 0:
		w.FailedJobs = failed
		w.Settle(now, inventory.ReleaseRed)
	case running, succeeded == 0:
		// Still moving, or no workflow has started yet: a red release
		// being rerun stays red until the rerun ends.
	default:
		w.FailedJobs, w.Pipeline.Workflow = nil, ""
		w.Settle(now, inventory.ReleaseBuilt)
	}
}

// workflowNotRun is a workflow the pipeline's filters left out: neither
// green nor red.
const workflowNotRun = "not_run"

// failedJobStatuses are the CircleCI job statuses that are failures, as
// devctl's release wait counts them.
var failedJobStatuses = map[string]string{
	"failed": "failed", "error": "error", "canceled": "canceled", "timedout": "timed out",
	"infrastructure_fail": "infrastructure failure", "unauthorized": "unauthorized",
}

// failedJobs names the failed jobs, `<job> (<how>)`, in the order CircleCI
// lists them.
func failedJobs(jobs []circleciclient.Job) []string {
	var out []string
	for _, j := range jobs {
		if how, ok := failedJobStatuses[j.Status]; ok {
			out = append(out, j.Name+" ("+how+")")
		}
	}
	return out
}

// failureWord is a status as a sentence says it.
func failureWord(status string) string {
	if how, ok := failedJobStatuses[status]; ok {
		return how
	}
	return status
}

// fetchReleases reads the pass's page.
func (c *Collector) fetchReleases(ctx context.Context) ([]releaseNode, error) {
	var data struct {
		Organization *struct {
			Repositories struct {
				Nodes []releaseNode `json:"nodes"`
			} `json:"repositories"`
		} `json:"organization"`
	}
	if _, err := c.gql.do(ctx, releasesQuery, map[string]any{varOrg: c.opts.Org, "first": releasePage}, &data); err != nil {
		return nil, fmt.Errorf("release watch: %w", err)
	}
	if data.Organization == nil {
		return nil, fmt.Errorf("release watch: organization %s not resolved", c.opts.Org)
	}
	return data.Organization.Repositories.Nodes, nil
}

// seedWatching fills the set of repositories the watch follows from the
// store once, at the first pass: the releases still watching when the
// manager last ran keep being followed after a restart.
func (c *Collector) seedWatching(ctx context.Context) error {
	c.watchMu.Lock()
	defer c.watchMu.Unlock()
	if c.watching != nil {
		return nil
	}
	list, err := c.store.List(ctx)
	if err != nil {
		return fmt.Errorf("release watch: %w", err)
	}
	c.watching = map[string]bool{}
	for i := range list {
		if list[i].Setup.Release.Following() {
			c.watching[list[i].Name] = true
		}
	}
	return nil
}

// follow adds name to the set of repositories the watch follows, or drops it.
func (c *Collector) follow(name string, on bool) {
	c.watchMu.Lock()
	defer c.watchMu.Unlock()
	if c.watching == nil {
		c.watching = map[string]bool{}
	}
	if on {
		c.watching[name] = true
		return
	}
	delete(c.watching, name)
}

// keepRelease carries the release watch's state from the stored record into
// r before a sweep writes it: the sweep read its old records minutes ago,
// and a release the watch settled in between — and what the team was told
// about it — must not be written over, or the team hears it twice.
func (c *Collector) keepRelease(ctx context.Context, r *inventory.Record) {
	cur, err := c.store.Get(ctx, r.Repository)
	if err != nil || cur.Setup.Release == nil {
		return
	}
	if r.Setup.Release == nil || cur.Setup.Release.CheckedAt.After(r.Setup.Release.CheckedAt) {
		r.Setup.Release = cur.Setup.Release
		rewriteReleaseStep(r)
		r.Finalize()
	}
}
