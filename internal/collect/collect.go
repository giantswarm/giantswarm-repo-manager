// Package collect fills the inventory (D8): a full sweep over the org on a
// schedule, one repository after a reconciler run and on demand. GitHub is
// read through GraphQL as the App installation the way the prototype learned
// — repository metadata 50 a page and commit history in aliased batches of
// 20, since one combined query made GitHub answer 502 — and the engine's
// checks run in read mode per accepted declaration.
package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/gh"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// Options tune a sweep.
type Options struct {
	// Org is the GitHub organization.
	Org string
	// Stale is the orphan score's stale period.
	Stale time.Duration
	// EngineChecks runs the engine's read-mode checks for every accepted
	// declaration during a sweep (REST, the read identity's budget); a refresh
	// always runs them.
	EngineChecks bool
	// Concurrency bounds the parallel CircleCI and engine reads.
	Concurrency int
	// PageSize, HistoryBatch and HistoryDepth are the GraphQL paging shape.
	PageSize, HistoryBatch, HistoryDepth int
	// BudgetFloor stops a sweep cleanly when the GraphQL budget's remaining
	// points fall below it; 0 never stops.
	BudgetFloor int
}

// DefaultStale is the default stale period: 180 days.
const DefaultStale = 180 * 24 * time.Hour

// DefaultPageSize is the repositories page a sweep starts with. The
// prototype's 50 held for the active repositories; over the whole org GitHub
// answered 50 with 502 and 25 with a truncated body (2026-09-16), 12 went
// through — a failed page is retried at half the size down to minPageSize.
const DefaultPageSize = 20

func (o *Options) defaults() {
	if o.Org == "" {
		o.Org = reposetup.DefaultOwner
	}
	if o.Stale <= 0 {
		o.Stale = DefaultStale
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	if o.PageSize <= 0 {
		o.PageSize = DefaultPageSize
	}
	if o.HistoryBatch <= 0 {
		o.HistoryBatch = 20
	}
	if o.HistoryDepth <= 0 {
		o.HistoryDepth = 30
	}
}

// Checker runs the engine's checks in read mode for one accepted declaration.
type Checker interface {
	Check(ctx context.Context, team string, entry reposetup.Entry) (*reconcile.Result, error)
}

// CircleCI reads one project's state; Calls counts the API calls made.
type CircleCI interface {
	Project(ctx context.Context, org, repo string) *inventory.CircleCI
	Calls() int
}

// ErrSweepRunning is returned when a sweep is already running.
var ErrSweepRunning = errors.New("a sweep is already running")

// Collector fills the store.
type Collector struct {
	reconciled func(context.Context, *inventory.Record)
	opts       Options
	reader     *gh.Reader
	gql        *graphQL
	store      *inventory.Store
	checker    Checker
	circle     CircleCI
	log        *slog.Logger
	now        func() time.Time
	running    atomic.Bool

	srcMu    sync.Mutex
	srcCache *sources
	srcAt    time.Time
}

// New builds a collector; checker and circle may be nil.
func New(opts Options, reader *gh.Reader, store *inventory.Store, checker Checker, circle CircleCI, log *slog.Logger) *Collector {
	opts.defaults()
	if log == nil {
		log = slog.Default()
	}
	return &Collector{opts: opts, reader: reader, gql: newGraphQL(reader.GraphQLURL(), reader.HTTP(), opts.BudgetFloor), store: store, checker: checker, circle: circle, log: log, now: time.Now}
}

// Options are the collector's options.
func (c *Collector) Options() Options { return c.opts }

// Running says whether a sweep is in progress.
func (c *Collector) Running() bool { return c.running.Load() }

// Sweep fills one record per repository of the org and stores the summary.
// A budget stop keeps what was collected and removes nothing.
func (c *Collector) Sweep(ctx context.Context) (*inventory.SweepSummary, error) {
	if !c.running.CompareAndSwap(false, true) {
		return nil, ErrSweepRunning
	}
	defer c.running.Store(false)
	start := c.now()
	sum := &inventory.SweepSummary{StartedAt: start}
	restBefore := c.reader.Usage()
	gqlBefore := c.gql.snapshot()
	circleBefore := 0
	if c.circle != nil {
		circleBefore = c.circle.Calls()
	}
	c.log.Info("sweep starting", "org", c.opts.Org, "identity", c.reader.Name(), "engineChecks", c.opts.EngineChecks && c.checker != nil)

	src, err := c.fetchSources(ctx)
	if err != nil && !errors.Is(err, ErrBudget) {
		return nil, err
	}
	c.setCachedSources(src)
	complete := err == nil
	sum.Errors = append(sum.Errors, src.problems...)

	nodes := map[string]*repoNode{}
	if complete {
		nodes, err = c.fetchRepositories(ctx, sum)
		if err != nil && !errors.Is(err, ErrBudget) {
			return nil, err
		}
		complete = err == nil
	}
	if complete {
		var names []string
		for n, node := range nodes {
			if !node.IsEmpty {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		err = c.fetchHistories(ctx, names, nodes)
		if err != nil && !errors.Is(err, ErrBudget) {
			return nil, err
		}
		complete = err == nil
	}
	if !complete {
		sum.Errors = append(sum.Errors, "stopped at the budget floor: "+err.Error())
	}

	old, err := c.existing(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(nodes))
	for n := range nodes {
		names = append(names, n)
	}
	if complete {
		for n := range src.declarations {
			if _, ok := nodes[n]; !ok {
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)

	records := make([]*inventory.Record, 0, len(names))
	for _, n := range names {
		records = append(records, c.build(n, nodes[n], src, old[c.opts.Org+"/"+n]))
	}
	c.log.Info("records assembled", "repositories", len(records), "circleci", c.circle != nil, "engineChecks", c.opts.EngineChecks && c.checker != nil)
	c.readCircleCI(ctx, records)
	if c.opts.EngineChecks {
		sum.EngineChecks = c.runChecks(ctx, records, src, true)
	}
	kept := map[string]bool{}
	for _, r := range records {
		r.Finalize(c.opts.Stale, c.now())
		r.RefreshedAt, r.Source = c.now(), inventory.SourceSweep
		if err := c.store.Put(ctx, r); err != nil {
			return nil, err
		}
		kept[r.Repository] = true
		c.count(sum, r)
	}
	if complete {
		var stale []string
		for k := range old {
			if !kept[k] {
				stale = append(stale, k)
			}
		}
		if err := c.store.Delete(ctx, stale...); err != nil {
			return nil, err
		}
		sum.Removed = len(stale)
	}

	sum.FinishedAt = c.now()
	sum.Duration = sum.FinishedAt.Sub(start).Round(time.Second).String()
	sum.GraphQL = c.gql.snapshot()
	sum.GraphQL.Calls -= gqlBefore.Calls
	sum.GraphQL.Cost -= gqlBefore.Cost
	rest := c.reader.Usage()
	sum.REST = inventory.Budget{Calls: rest.Calls - restBefore.Calls, Cost: rest.Calls - restBefore.Calls, Remaining: rest.Remaining, Limit: rest.Limit}
	if !rest.ResetAt.IsZero() {
		t := rest.ResetAt
		sum.REST.ResetAt = &t
	}
	if c.circle != nil {
		sum.CircleCI = c.circle.Calls() - circleBefore
	}
	if err := c.store.PutSweep(ctx, sum); err != nil {
		return nil, err
	}
	c.log.Info("sweep finished", "repositories", sum.Repositories, "declared", sum.Declared, "undeclared", sum.Undeclared, "gone", sum.Gone,
		"duration", sum.Duration, "graphqlCalls", sum.GraphQL.Calls, "graphqlCost", sum.GraphQL.Cost, "graphqlRemaining", sum.GraphQL.Remaining,
		"restCalls", sum.REST.Calls, "restRemaining", sum.REST.Remaining, "circleciCalls", sum.CircleCI, "complete", complete)
	if !complete {
		return sum, ErrBudget
	}
	return sum, nil
}

func (c *Collector) count(sum *inventory.SweepSummary, r *inventory.Record) {
	sum.Repositories++
	switch {
	case r.Reality == nil:
		sum.Gone++
	case r.Declaration == nil:
		sum.Undeclared++
	default:
		sum.Declared++
	}
	if r.Reality != nil && r.Reality.IsArchived {
		sum.Archived++
	}
}

// existing reads the current records by repository.
func (c *Collector) existing(ctx context.Context) (map[string]*inventory.Record, error) {
	list, err := c.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*inventory.Record, len(list))
	for i := range list {
		out[list[i].Repository] = &list[i]
	}
	return out, nil
}

// Refresh rebuilds one record: after a reconciler run (run set, source
// reconciler) or on demand. A repository neither declared nor on GitHub loses
// its record and is reported as inventory.ErrNotFound.
// OnReconciled registers the hook a refresh with a reconciler run calls after
// the record is stored: the completion message to the team's channel.
func (c *Collector) OnReconciled(fn func(context.Context, *inventory.Record)) { c.reconciled = fn }

func (c *Collector) Refresh(ctx context.Context, repository string, run *inventory.LastRun, source string) (*inventory.Record, error) {
	name := strings.TrimPrefix(repository, c.opts.Org+"/")
	if name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("refresh: %q is not a repository of %s", repository, c.opts.Org)
	}
	src, err := c.cachedSources(ctx)
	if err != nil {
		return nil, err
	}
	node, err := c.fetchOne(ctx, name)
	if err != nil {
		return nil, err
	}
	key := c.opts.Org + "/" + name
	old, err := c.store.Get(ctx, key)
	if err != nil && !errors.Is(err, inventory.ErrNotFound) {
		return nil, err
	}
	if node == nil && src.declarations[name] == nil {
		if old != nil {
			_ = c.store.Delete(ctx, key)
		}
		return nil, fmt.Errorf("%w: %s is neither declared nor on GitHub", inventory.ErrNotFound, key)
	}
	rec := c.build(name, node, src, old)
	c.readCircleCI(ctx, []*inventory.Record{rec})
	c.runChecks(ctx, []*inventory.Record{rec}, src, false)
	if run != nil {
		rec.Setup.LastRun = run
	}
	rec.Finalize(c.opts.Stale, c.now())
	rec.RefreshedAt, rec.Source = c.now(), source
	if err := c.store.Put(ctx, rec); err != nil {
		return nil, err
	}
	if run != nil && c.reconciled != nil {
		c.reconciled(ctx, rec)
	}
	return rec, nil
}

// sourcesTTL is how long a refresh reuses the last sources read.
const sourcesTTL = 5 * time.Minute

func (c *Collector) cachedSources(ctx context.Context) (*sources, error) {
	c.srcMu.Lock()
	if c.srcCache != nil && c.now().Sub(c.srcAt) < sourcesTTL {
		defer c.srcMu.Unlock()
		return c.srcCache, nil
	}
	c.srcMu.Unlock()
	src, err := c.fetchSources(ctx)
	if err != nil {
		return nil, err
	}
	c.setCachedSources(src)
	return src, nil
}

func (c *Collector) setCachedSources(src *sources) {
	c.srcMu.Lock()
	defer c.srcMu.Unlock()
	c.srcCache, c.srcAt = src, c.now()
}

// readCircleCI fills CircleCI for the records that exist on GitHub.
func (c *Collector) readCircleCI(ctx context.Context, records []*inventory.Record) {
	if c.circle == nil {
		return
	}
	c.parallel(records, func(r *inventory.Record) {
		if r.Reality != nil {
			r.CircleCI = c.circle.Project(ctx, c.opts.Org, r.Name)
		}
	})
}

// runChecks runs the engine's read-mode checks for every accepted, present
// declaration; keepOld leaves an older result in place where a check does
// not run.
func (c *Collector) runChecks(ctx context.Context, records []*inventory.Record, src *sources, keepOld bool) int {
	if c.checker == nil {
		for _, r := range records {
			if r.Declaration != nil && r.Setup.Checks == nil {
				r.Setup.CheckError = "engine checks need a GitHub read identity"
			}
		}
		return 0
	}
	var n atomic.Int32
	c.parallel(records, func(r *inventory.Record) {
		d := src.declarations[r.Name]
		switch {
		case r.Declaration == nil || d == nil:
			r.Setup.Checks, r.Setup.CheckedAt, r.Setup.CheckError = nil, nil, ""
			return
		case !d.entry.Accepted:
			// A refused entry is the engine's result without a run — the entry
			// step reported, one finding per problem, what `devctl repo
			// reconcile` prints for it. Read from the declaration alone, so a
			// gone repository keeps the refusal beside declared-but-gone.
			at := c.now()
			r.Setup.Checks = reconcile.Refused(reconcile.Request{Owner: c.opts.Org, Team: d.Team, Entry: d.entry, Mode: reconcile.ModeCheck}, at)
			r.Setup.CheckedAt, r.Setup.CheckError = &at, ""
			return
		case r.Reality == nil:
			r.Setup.Checks, r.Setup.CheckedAt, r.Setup.CheckError = nil, nil, ""
			return
		}
		res, err := c.checker.Check(ctx, d.Team, d.entry)
		n.Add(1)
		if err != nil {
			if !keepOld {
				r.Setup.Checks, r.Setup.CheckedAt = nil, nil
			}
			r.Setup.CheckError = err.Error()
			return
		}
		at := c.now()
		r.Setup.Checks, r.Setup.CheckedAt, r.Setup.CheckError = res, &at, ""
	})
	return int(n.Load())
}

func (c *Collector) parallel(records []*inventory.Record, fn func(*inventory.Record)) {
	sem := make(chan struct{}, c.opts.Concurrency)
	var wg sync.WaitGroup
	for _, r := range records {
		wg.Add(1)
		sem <- struct{}{}
		go func(r *inventory.Record) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(r)
		}(r)
	}
	wg.Wait()
}

// build assembles a record from the GitHub node (nil: gone), the sources and
// the previous record (decision and last reconciler run survive).
func (c *Collector) build(name string, node *repoNode, src *sources, old *inventory.Record) *inventory.Record {
	rec := &inventory.Record{Repository: c.opts.Org + "/" + name, Name: name}
	if d := src.declarations[name]; d != nil {
		decl := d.Declaration
		rec.Declaration = &decl
	}
	if node != nil {
		rec.Reality, rec.Renovate = c.reality(node, src)
	}
	rec.Catalog.Present = src.catalog[name]
	if team, ok := src.mapping[name]; ok {
		rec.Mapping = inventory.Mapping{Present: true, Team: team}
	}
	if old != nil {
		rec.Decision = old.Decision
		rec.Setup = old.Setup
		rec.CircleCI = old.CircleCI
	}
	return rec
}

// reality maps a GraphQL node to the record's reality and Renovate state.
func (c *Collector) reality(n *repoNode, src *sources) (*inventory.Reality, inventory.Renovate) {
	r := &inventory.Reality{
		URL: n.URL, Description: n.Description, Visibility: strings.ToLower(n.Visibility),
		IsArchived: n.IsArchived, IsFork: n.IsFork, IsTemplate: n.IsTemplate, IsEmpty: n.IsEmpty,
		CreatedAt: n.CreatedAt, PushedAt: n.PushedAt, OpenIssues: n.OpenIssues.TotalCount,
	}
	if n.PrimaryLanguage != nil {
		r.Language = n.PrimaryLanguage.Name
	}
	for _, t := range n.RepositoryTopics.Nodes {
		r.Topics = append(r.Topics, t.Topic.Name)
	}
	if n.LatestRelease != nil {
		r.LatestRelease = &inventory.Release{Tag: n.LatestRelease.TagName, PublishedAt: n.LatestRelease.PublishedAt}
	}
	if n.DefaultBranchRef != nil {
		r.DefaultBranch = n.DefaultBranchRef.Name
	}
	r.Has = inventory.Presence{
		Dependabot: n.Dependabot != nil, CircleCI: n.CircleCI != nil, Dockerfile: n.Dockerfile != nil, Helm: n.Helm != nil, Readme: n.Readme != nil,
		Workflows:  n.Workflows != nil && len(n.Workflows.Entries) > 0,
		Codeowners: n.Codeowners != nil || n.CodeownersGh != nil || n.CodeownersDocs != nil,
	}
	r.CodeownersTeams = codeownersTeams(c.opts.Org, blobText(n.Codeowners), blobText(n.CodeownersGh), blobText(n.CodeownersDocs))
	if len(src.teams) > 0 {
		for _, t := range r.CodeownersTeams {
			if !src.teams[t] {
				r.UnknownCodeownersTeams = append(r.UnknownCodeownersTeams, t)
			}
		}
	}

	var ren inventory.Renovate
	for i, b := range []*blob{n.Renovate0, n.Renovate1, n.Renovate2, n.Renovate3, n.Renovate4, n.Renovate5} {
		if b != nil {
			ren = renovateState(renovatePaths[i], b.Text)
			r.Has.Renovate = true
			break
		}
	}
	for _, is := range n.OldestIssues.Nodes {
		if strings.EqualFold(is.Title, "Dependency Dashboard") {
			ren.DashboardIssue = &inventory.Issue{Number: is.Number, Title: is.Title}
			break
		}
	}

	prs := &r.OpenPullRequests
	prs.Total = n.OpenPRs.TotalCount
	for _, p := range n.OpenPRs.Nodes {
		login := ""
		if p.Author != nil {
			login = p.Author.Login
		}
		pr := inventory.PullRequest{Number: p.Number, Title: p.Title, Author: login, CreatedAt: p.CreatedAt}
		bot := isBot(login, "", "")
		if bot {
			prs.Bots++
			if prs.OldestBotAt == nil || p.CreatedAt.Before(*prs.OldestBotAt) {
				t := p.CreatedAt
				prs.OldestBotAt = &t
			}
			if isRenovate(login, "", "") {
				prs.Renovate++
				if ren.LastPullRequest == nil || p.CreatedAt.After(ren.LastPullRequest.CreatedAt) {
					lp := pr
					ren.LastPullRequest = &lp
				}
			}
		}
		if isOnboarding(p.HeadRefName, p.Title, login) {
			prs.Onboarding = append(prs.Onboarding, pr)
		}
	}
	prs.People = prs.Total - prs.Bots
	if prs.People < 0 {
		prs.People = 0
	}

	if n.DefaultBranchRef != nil && n.DefaultBranchRef.Target != nil && n.DefaultBranchRef.Target.History != nil {
		h := n.DefaultBranchRef.Target.History
		r.HistorySampled = len(h.Nodes)
		for _, cm := range h.Nodes {
			login := ""
			if cm.Author.User != nil {
				login = cm.Author.User.Login
			}
			commit := &inventory.Commit{Date: cm.CommittedDate, Author: firstNonEmpty(login, cm.Author.Name, cm.Author.Email), Message: cm.MessageHeadline}
			if r.LastCommit == nil {
				r.LastCommit = commit
			}
			if isBot(login, cm.Author.Name, cm.Author.Email) {
				r.BotCommits++
				if ren.LastCommit == nil && isRenovate(login, cm.Author.Name, cm.Author.Email) {
					t := cm.CommittedDate
					ren.LastCommit = &t
				}
			} else if r.LastPersonCommit == nil {
				r.LastPersonCommit = commit
			}
		}
	}
	return r, ren
}

// isOnboarding recognises the set-up automation's pull requests.
func isOnboarding(head, title, login string) bool {
	title = strings.ToLower(title)
	return strings.HasPrefix(head, "reposetup/") || strings.HasPrefix(head, "renovate/configure") ||
		(login == "giantswarm-align-files" && strings.Contains(title, "align")) || strings.Contains(title, "configure renovate")
}

func blobText(b *blob) string {
	if b == nil {
		return ""
	}
	return b.Text
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// RunSchedule sweeps at start when the last sweep is older than interval (or
// none ran), then every interval; a failed sweep is retried after an hour at
// most. It returns when ctx is done.
func (c *Collector) RunSchedule(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	wait := time.Duration(0)
	if last, err := c.store.Sweep(ctx); err == nil && last != nil {
		if since := c.now().Sub(last.FinishedAt); since < interval {
			wait = interval - since
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if _, err := c.Sweep(ctx); err != nil {
			c.log.Error("scheduled sweep failed", "error", err)
			wait = min(interval, time.Hour)
			continue
		}
		wait = interval
	}
}

// StartSweep runs a sweep in the background; false when one is running.
func (c *Collector) StartSweep() bool {
	if c.running.Load() {
		return false
	}
	go func() {
		if _, err := c.Sweep(context.Background()); err != nil && !errors.Is(err, ErrSweepRunning) {
			c.log.Error("sweep failed", "error", err)
		}
	}()
	return true
}

// --- GraphQL shapes ---

const repoFields = `
fragment RepoFields on Repository {
  name url description visibility isArchived isFork isTemplate isEmpty createdAt pushedAt
  primaryLanguage { name }
  repositoryTopics(first: 20) { nodes { topic { name } } }
  latestRelease { tagName publishedAt }
  openIssues: issues(states: OPEN) { totalCount }
  oldestIssues: issues(states: OPEN, first: 5, orderBy: {field: CREATED_AT, direction: ASC}) { nodes { number title } }
  openPRs: pullRequests(states: OPEN, first: 40, orderBy: {field: CREATED_AT, direction: ASC}) {
    totalCount nodes { number title createdAt headRefName author { login } }
  }
  defaultBranchRef { name }
  codeowners: object(expression: "HEAD:CODEOWNERS") { ... on Blob { text } }
  codeownersGh: object(expression: "HEAD:.github/CODEOWNERS") { ... on Blob { text } }
  codeownersDocs: object(expression: "HEAD:docs/CODEOWNERS") { ... on Blob { text } }
  renovate0: object(expression: "HEAD:renovate.json5") { ... on Blob { text } }
  renovate1: object(expression: "HEAD:renovate.json") { ... on Blob { text } }
  renovate2: object(expression: "HEAD:.github/renovate.json5") { ... on Blob { text } }
  renovate3: object(expression: "HEAD:.github/renovate.json") { ... on Blob { text } }
  renovate4: object(expression: "HEAD:.renovaterc") { ... on Blob { text } }
  renovate5: object(expression: "HEAD:.renovaterc.json") { ... on Blob { text } }
  dependabot: object(expression: "HEAD:.github/dependabot.yml") { id }
  circleci: object(expression: "HEAD:.circleci/config.yml") { id }
  workflows: object(expression: "HEAD:.github/workflows") { ... on Tree { entries { name } } }
  readme: object(expression: "HEAD:README.md") { id }
  dockerfile: object(expression: "HEAD:Dockerfile") { id }
  helm: object(expression: "HEAD:helm") { id }
}`

const reposQuery = repoFields + `
query($org: String!, $first: Int!, $after: String) {
  organization(login: $org) {
    repositories(first: $first, after: $after, orderBy: {field: NAME, direction: ASC}) {
      totalCount pageInfo { hasNextPage endCursor } nodes { ...RepoFields }
    }
  }
  rateLimit { cost remaining limit resetAt }
}`

const historyFields = `history(first: %d) { totalCount nodes { committedDate messageHeadline author { name email user { login } } } }`

const oneQuery = repoFields + `
query($org: String!, $name: String!) {
  repository(owner: $org, name: $name) {
    ...RepoFields
    defaultBranchRef { target { ... on Commit { ` + "%s" + ` } } }
  }
  rateLimit { cost remaining limit resetAt }
}`

type repoNode struct {
	Name            string    `json:"name"`
	URL             string    `json:"url"`
	Description     string    `json:"description"`
	Visibility      string    `json:"visibility"`
	IsArchived      bool      `json:"isArchived"`
	IsFork          bool      `json:"isFork"`
	IsTemplate      bool      `json:"isTemplate"`
	IsEmpty         bool      `json:"isEmpty"`
	CreatedAt       time.Time `json:"createdAt"`
	PushedAt        time.Time `json:"pushedAt"`
	PrimaryLanguage *struct {
		Name string `json:"name"`
	} `json:"primaryLanguage"`
	RepositoryTopics struct {
		Nodes []struct {
			Topic struct {
				Name string `json:"name"`
			} `json:"topic"`
		} `json:"nodes"`
	} `json:"repositoryTopics"`
	LatestRelease *struct {
		TagName     string    `json:"tagName"`
		PublishedAt time.Time `json:"publishedAt"`
	} `json:"latestRelease"`
	OpenIssues struct {
		TotalCount int `json:"totalCount"`
	} `json:"openIssues"`
	OldestIssues struct {
		Nodes []struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
		} `json:"nodes"`
	} `json:"oldestIssues"`
	OpenPRs struct {
		TotalCount int      `json:"totalCount"`
		Nodes      []prNode `json:"nodes"`
	} `json:"openPRs"`
	DefaultBranchRef *struct {
		Name   string `json:"name"`
		Target *struct {
			History *historyConn `json:"history"`
		} `json:"target"`
	} `json:"defaultBranchRef"`
	Codeowners     *blob   `json:"codeowners"`
	CodeownersGh   *blob   `json:"codeownersGh"`
	CodeownersDocs *blob   `json:"codeownersDocs"`
	Renovate0      *blob   `json:"renovate0"`
	Renovate1      *blob   `json:"renovate1"`
	Renovate2      *blob   `json:"renovate2"`
	Renovate3      *blob   `json:"renovate3"`
	Renovate4      *blob   `json:"renovate4"`
	Renovate5      *blob   `json:"renovate5"`
	Dependabot     *idNode `json:"dependabot"`
	CircleCI       *idNode `json:"circleci"`
	Workflows      *struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	} `json:"workflows"`
	Readme     *idNode `json:"readme"`
	Dockerfile *idNode `json:"dockerfile"`
	Helm       *idNode `json:"helm"`
}

type idNode struct {
	ID string `json:"id"`
}

type prNode struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	CreatedAt   time.Time `json:"createdAt"`
	HeadRefName string    `json:"headRefName"`
	Author      *struct {
		Login string `json:"login"`
	} `json:"author"`
}

type historyConn struct {
	TotalCount int `json:"totalCount"`
	Nodes      []struct {
		CommittedDate   time.Time `json:"committedDate"`
		MessageHeadline string    `json:"messageHeadline"`
		Author          struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			User  *struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"author"`
	} `json:"nodes"`
}

// minPageSize is the smallest repositories page a sweep falls back to.
const minPageSize = 5

// fetchRepositories pages the org's repositories, PageSize a page, halving
// the page when GitHub cannot answer one.
func (c *Collector) fetchRepositories(ctx context.Context, sum *inventory.SweepSummary) (map[string]*repoNode, error) {
	nodes := map[string]*repoNode{}
	vars := map[string]any{varOrg: c.opts.Org, "first": c.opts.PageSize}
	for page := 1; ; page++ {
		var data struct {
			Organization struct {
				Repositories struct {
					TotalCount int         `json:"totalCount"`
					PageInfo   pageInfo    `json:"pageInfo"`
					Nodes      []*repoNode `json:"nodes"`
				} `json:"repositories"`
			} `json:"organization"`
		}
		partial, err := c.gql.do(ctx, reposQuery, vars, &data)
		if err != nil && !errors.Is(err, ErrBudget) {
			// A page GitHub cannot answer (502 after the retries) is asked again
			// at half the size; the prototype's 50 held for the active
			// repositories, the whole org needs less.
			if first, _ := vars["first"].(int); first > minPageSize && ctx.Err() == nil {
				vars["first"] = max(minPageSize, first/2)
				c.log.Warn("repositories page failed, halving the page size", "error", err, "pageSize", vars["first"])
				sum.Errors = append(sum.Errors, fmt.Sprintf("repositories page %d at %d a page: %v; continuing at %d", page, first, err, vars["first"]))
				continue
			}
			return nil, fmt.Errorf("repositories page %d: %w", page, err)
		}
		sum.Errors = append(sum.Errors, partial...)
		for _, n := range data.Organization.Repositories.Nodes {
			if n != nil {
				nodes[n.Name] = n
			}
		}
		u := c.gql.snapshot()
		c.log.Info("repositories page", "page", page, "pageSize", vars["first"], "repositories", len(nodes), "of", data.Organization.Repositories.TotalCount, "graphqlRemaining", u.Remaining)
		if err != nil {
			return nodes, err
		}
		if !data.Organization.Repositories.PageInfo.HasNextPage {
			return nodes, nil
		}
		vars["after"] = data.Organization.Repositories.PageInfo.EndCursor
	}
}

// fetchHistories reads the default-branch history of names in aliased
// batches of HistoryBatch and attaches it to the nodes.
func (c *Collector) fetchHistories(ctx context.Context, names []string, nodes map[string]*repoNode) error {
	batch := c.opts.HistoryBatch
	for len(names) > 0 {
		n := min(batch, len(names))
		chunk := names[:n]
		var b strings.Builder
		b.WriteString("query($org: String!) { ")
		for i, name := range chunk {
			q, _ := json.Marshal(name)
			fmt.Fprintf(&b, "r%d: repository(owner: $org, name: %s) { name defaultBranchRef { target { ... on Commit { %s } } } } ", i, q, fmt.Sprintf(historyFields, c.opts.HistoryDepth))
		}
		b.WriteString("rateLimit { cost remaining limit resetAt } }")
		var data map[string]*struct {
			Name             string `json:"name"`
			DefaultBranchRef *struct {
				Target *struct {
					History *historyConn `json:"history"`
				} `json:"target"`
			} `json:"defaultBranchRef"`
		}
		_, err := c.gql.do(ctx, b.String(), map[string]any{varOrg: c.opts.Org}, &data)
		if err != nil && !errors.Is(err, ErrBudget) {
			if batch > 3 && ctx.Err() == nil {
				batch = max(3, batch/2)
				c.log.Warn("history batch failed, halving", "error", err, "batch", batch)
				continue
			}
			return fmt.Errorf("histories: %w", err)
		}
		for _, r := range data {
			if r == nil || r.DefaultBranchRef == nil || r.DefaultBranchRef.Target == nil {
				continue
			}
			if node := nodes[r.Name]; node != nil && node.DefaultBranchRef != nil {
				node.DefaultBranchRef.Target = r.DefaultBranchRef.Target
			}
		}
		if err != nil {
			return err
		}
		names = names[n:]
		u := c.gql.snapshot()
		c.log.Info("history batch", "batch", batch, "left", len(names), "graphqlRemaining", u.Remaining)
	}
	return nil
}

// fetchOne reads one repository with its history; nil when it is gone.
func (c *Collector) fetchOne(ctx context.Context, name string) (*repoNode, error) {
	var data struct {
		Repository *repoNode `json:"repository"`
	}
	partial, err := c.gql.do(ctx, fmt.Sprintf(oneQuery, fmt.Sprintf(historyFields, c.opts.HistoryDepth)), map[string]any{varOrg: c.opts.Org, "name": name}, &data)
	if err != nil && !errors.Is(err, ErrBudget) {
		return nil, fmt.Errorf("repository %s: %w", name, err)
	}
	if data.Repository == nil {
		for _, p := range partial {
			if !strings.Contains(p, "Could not resolve to a Repository") {
				return nil, fmt.Errorf("repository %s: %s", name, p)
			}
		}
		return nil, nil
	}
	return data.Repository, nil
}
