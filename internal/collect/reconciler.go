package collect

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/google/go-github/v92/github"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

// ReconcilerOptions say where the reconciler's runs are and how the poller
// reads them. The reconciler is a GitHub Actions workflow that uploads one
// artifact reconcile-<name> per repository it ran over; nothing reaches this
// server from it — the inventory pulls the artifacts as the inventory App.
type ReconcilerOptions struct {
	// Repository is owner/name of the repository the workflow runs in: the
	// team files repository.
	Repository string
	// Workflow is the workflow file name in that repository.
	Workflow string
	// PollInterval is how often completed runs are read; 0 turns the poller
	// off.
	PollInterval time.Duration
	// PendingInterval is the poll interval while an Align now is pending.
	PendingInterval time.Duration
	// PendingWindow is how long an expected run may take to report — from
	// the dispatch of an Align now, from the merge of a pull request — before
	// it is given up as missing.
	PendingWindow time.Duration
	// Lookback bounds the first poll and a cursor that fell behind: runs
	// created earlier are not read.
	Lookback time.Duration
	// MaxArtifact bounds one artifact download in bytes.
	MaxArtifact int64
}

// The poller's defaults.
const (
	DefaultReconcilerPoll  = 5 * time.Minute
	defaultPendingInterval = 30 * time.Second
	defaultPendingWindow   = 15 * time.Minute
	defaultLookback        = 7 * 24 * time.Hour
	defaultMaxArtifact     = 4 << 20
	// ArtifactPrefix is what the reconciler names its per-repository
	// artifact: reconcile-<name>, the name without the org.
	ArtifactPrefix = "reconcile-"
	// runCompleted is the Actions run status the artifacts are final at,
	// runSuccess the conclusion of a run whose every job succeeded.
	runCompleted = "completed"
	runSuccess   = "success"
	// The events of the runs a record expects: an Align now's dispatch, the
	// push of a merged team-file pull request.
	eventWorkflowDispatch = "workflow_dispatch"
	eventPush             = "push"
	// reconcileJobPrefix starts the name of the reconciler's job for one
	// repository: "Reconcile <name>", listed under its team's shard as
	// "<team> / Reconcile <name>".
	reconcileJobPrefix = "Reconcile "
	// dispatchSlack is how much earlier than its mark a run may have been
	// created and still be the mark's own: GitHub truncates created_at to
	// the second, and the manager's clock is not GitHub's.
	dispatchSlack = time.Minute
)

func (o *ReconcilerOptions) defaults() {
	if o.Repository == "" {
		o.Repository = teamfiles.DefaultRepository
	}
	if o.Workflow == "" {
		o.Workflow = teamfiles.ReconcilerWorkflow
	}
	if o.PendingInterval <= 0 {
		o.PendingInterval = defaultPendingInterval
	}
	if o.PendingWindow <= 0 {
		o.PendingWindow = defaultPendingWindow
	}
	if o.Lookback <= 0 {
		o.Lookback = defaultLookback
	}
	if o.MaxArtifact <= 0 {
		o.MaxArtifact = defaultMaxArtifact
	}
}

// repo is the workflow's repository as owner and name.
func (o ReconcilerOptions) repo() (owner, name string) {
	owner, name, _ = strings.Cut(o.Repository, "/")
	return owner, name
}

// RunsURL is the workflow's Actions page.
func (o ReconcilerOptions) RunsURL() string {
	owner, name := o.repo()
	return teamfiles.Repo{Owner: owner, Name: name}.WorkflowURL(o.Workflow)
}

// ReconcilerPoll is what one poll did.
type ReconcilerPoll struct {
	// Runs is the completed runs consumed, Artifacts the artifacts stored as
	// a repository's setup.lastRun, Skipped the ones a record already named.
	Runs, Artifacts, Skipped int
	// Pending is the expected runs whose window is running — an Align now,
	// a merged pull request — and Missing the ones given up this time: the
	// window ran out, or the run completed without a report. A pull request
	// still open is neither: no run is due yet.
	Pending, Missing int
	// Conflicting is the open pull requests GitHub reports conflicting with
	// their base this time (pendingRun.conflictsSince on their records).
	Conflicting int
	// Watermark is where the cursor stands after the poll.
	Watermark time.Time
	Errors    []string
}

// Artifact outcomes consumeRun tells apart.
var (
	errArtifactSkipped   = errors.New("artifact already stored")
	errArtifactMalformed = errors.New("artifact malformed")
)

// RunReconcilerPoll polls until ctx is done: at start, then every
// PollInterval, every PendingInterval while a run is pending, and at once
// when WakeReconciler is called. A failed poll is logged and retried at the
// next interval.
func (c *Collector) RunReconcilerPoll(ctx context.Context) {
	o := c.opts.Reconciler
	if o.PollInterval <= 0 {
		return
	}
	c.log.Info("reconciler poller on", "repository", o.Repository, "workflow", o.Workflow, "interval", o.PollInterval, "pendingInterval", o.PendingInterval)
	pollLoop(ctx, o.PollInterval, o.PendingInterval, c.wake, func(ctx context.Context) bool {
		poll, err := c.PollReconciler(ctx)
		if err != nil {
			c.log.Error("reconciler poll failed", "error", err)
			return false
		}
		return poll.Pending > 0
	})
}

// OnConflict registers the hook the poller calls when it first finds an open
// pull request of this server conflicting with its base (mergeable: false —
// a neighbouring entry of the team file changed first), with the record
// whose pending run the pull request is and the pull request as read. The
// poller reads as the inventory App and cannot push; what follows — an ask
// for the approve_change that re-renders the pull request — is the hook's.
func (c *Collector) OnConflict(fn func(context.Context, *inventory.Record, *github.PullRequest)) {
	c.conflicted = fn
}

// WakeReconciler makes the poller read the reconciler's runs at once rather
// than at its next tick: a record whose run was just marked pending is read
// within PendingInterval from the mark on. It never blocks; wakes that
// arrive while one is already waiting fold into it.
func (c *Collector) WakeReconciler() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// pollLoop calls poll at once, then every interval — every pendingInterval
// while poll reports a pending run — and at once when wake receives; it
// returns when ctx is done. One loop, one timer: a wake between two ticks
// replaces the tick, and the interval after it is chosen from that poll.
func pollLoop(ctx context.Context, interval, pendingInterval time.Duration, wake <-chan struct{}, poll func(context.Context) (pending bool)) {
	wait := time.Duration(0)
	for {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-wake:
			timer.Stop()
		}
		wait = interval
		if poll(ctx) {
			wait = pendingInterval
		}
	}
}

// PollReconciler reads the reconciler's runs since the cursor once: every
// completed run's reconcile-<name> artifacts become the repositories'
// setup.lastRun, the cursor moves past the runs that are consumed, and the
// pending Align nows older than the window are given up. It returns what
// it did; a cursor or listing that could not be read is the error.
func (c *Collector) PollReconciler(ctx context.Context) (*ReconcilerPoll, error) {
	now := c.now()
	cur, err := c.store.ReconcilerCursor(ctx)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		cur = &inventory.ReconcilerCursor{}
	}
	if floor := now.Add(-c.opts.Reconciler.Lookback); cur.Watermark.Before(floor) {
		cur.Watermark = floor
	}
	if cur.Consumed == nil {
		cur.Consumed = map[string]time.Time{}
	}
	runs, err := c.listRuns(ctx, cur.Watermark)
	if err != nil {
		return nil, err
	}
	poll := &ReconcilerPoll{}
	// The watermark moves to the oldest run still open — not completed, or
	// not read through this time — else to the newest run seen; a run created
	// in the watermark's second is listed again and Consumed skips it.
	var newest, hold time.Time
	for _, run := range runs {
		created := run.GetCreatedAt().Time
		if created.After(newest) {
			newest = created
		}
		if _, done := cur.Consumed[runKey(run)]; done {
			continue
		}
		open := run.GetStatus() != runCompleted
		if !open {
			if open = !c.consumeRun(ctx, run, poll); !open {
				cur.Consumed[runKey(run)] = created
				poll.Runs++
			}
		}
		if open && (hold.IsZero() || created.Before(hold)) {
			hold = created
		}
	}
	switch {
	case !hold.IsZero():
		cur.Watermark = hold
	case newest.After(cur.Watermark):
		cur.Watermark = newest
	}
	for id, created := range cur.Consumed {
		if created.Before(cur.Watermark) {
			delete(cur.Consumed, id)
		}
	}
	cur.PolledAt = now
	poll.Watermark = cur.Watermark
	if err := c.store.PutReconcilerCursor(ctx, cur); err != nil {
		return poll, err
	}
	c.expirePending(ctx, now, poll)
	return poll, nil
}

// runKey names a run attempt in the cursor: a re-run of a run keeps its id
// and is a new attempt with new artifacts.
func runKey(run *github.WorkflowRun) string {
	return fmt.Sprintf("%d/%d", run.GetID(), run.GetRunAttempt())
}

// listRuns lists the workflow's runs created at or after since, every
// status, newest first.
func (c *Collector) listRuns(ctx context.Context, since time.Time) ([]*github.WorkflowRun, error) {
	owner, repo := c.opts.Reconciler.repo()
	opts := &github.ListWorkflowRunsOptions{Created: ">=" + since.UTC().Format("2006-01-02T15:04:05+00:00"), ListOptions: github.ListOptions{PerPage: 100}}
	var out []*github.WorkflowRun
	for {
		runs, resp, err := c.reader.REST().Actions.ListWorkflowRunsByFileName(ctx, owner, repo, c.opts.Reconciler.Workflow, opts)
		if err != nil {
			return nil, fmt.Errorf("list runs of %s in %s/%s: %w", c.opts.Reconciler.Workflow, owner, repo, err)
		}
		out = append(out, runs.WorkflowRuns...)
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// consumeRun stores every reconcile-<name> artifact of a completed run, the
// artifacts read Concurrency at a time; false when one could not be read
// now, which keeps the run open for the next poll. A run without artifacts
// (cancelled, failed before its report step) or with a malformed one is
// consumed with a log line, and the records expecting a run this run
// handled and did not report on hear of its failure at once (runWithoutReport).
func (c *Collector) consumeRun(ctx context.Context, run *github.WorkflowRun, poll *ReconcilerPoll) bool {
	log := c.log.With("run", run.GetID(), "attempt", run.GetRunAttempt(), "url", run.GetHTMLURL())
	artifacts, err := c.listArtifacts(ctx, run.GetID())
	if err != nil {
		poll.Errors = append(poll.Errors, err.Error())
		log.Error("reconciler run: artifacts not listed", "error", err)
		return false
	}
	type report struct {
		artifact *github.Artifact
		name     string
	}
	var reports []report
	reported := map[string]bool{}
	for _, a := range artifacts {
		name, is := strings.CutPrefix(a.GetName(), ArtifactPrefix)
		if !is || a.GetExpired() {
			continue
		}
		reports = append(reports, report{a, name})
		reported[name] = true
	}
	var mu sync.Mutex
	ok := true
	parallel(c.opts.Concurrency, reports, func(r report) {
		err := c.consumeArtifact(ctx, run, r.artifact, r.name)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err == nil:
			poll.Artifacts++
			log.Info("reconciler run stored", "repository", r.name)
		case errors.Is(err, errArtifactSkipped):
			poll.Skipped++
		case errors.Is(err, errArtifactMalformed), errors.Is(err, inventory.ErrNotFound):
			poll.Errors = append(poll.Errors, err.Error())
			log.Warn("reconciler run: artifact not stored", "repository", r.name, "error", err)
		default:
			poll.Errors = append(poll.Errors, err.Error())
			log.Error("reconciler run: artifact not read", "repository", r.name, "error", err)
			ok = false
		}
	})
	if len(reports) == 0 {
		log.Info("reconciler run without a report", "conclusion", run.GetConclusion())
	}
	if len(reports) == 0 || run.GetConclusion() != runSuccess {
		c.runWithoutReport(ctx, run, reported, poll, log)
	}
	return ok
}

// runWithoutReport gives up the pending runs a completed run leaves
// unanswered: the run handled their repository — failed or cancelled before
// its report step — and uploaded no report for it. The record gets the
// finding reconcile-run-missing at once, naming the run and its conclusion,
// rather than at the pending window's end. The run's payload does not say
// which repositories it handled (the REST API omits a dispatch's inputs);
// its jobs do, one "Reconcile <name>" per repository. Only the records whose
// pending run this run is are given up (expects); a run whose jobs cannot be
// read now is logged, and the window says so later.
func (c *Collector) runWithoutReport(ctx context.Context, run *github.WorkflowRun, reported map[string]bool, poll *ReconcilerPoll, log *slog.Logger) {
	names, err := c.listRunRepositories(ctx, run.GetID())
	if err != nil {
		poll.Errors = append(poll.Errors, err.Error())
		log.Warn("reconciler run: jobs not listed", "conclusion", run.GetConclusion(), "error", err)
		return
	}
	now := c.now()
	for _, name := range names {
		if reported[name] {
			continue
		}
		rec, err := c.store.Get(ctx, c.opts.Org+"/"+name)
		if err != nil {
			if !errors.Is(err, inventory.ErrNotFound) {
				log.Error("reconciler run without a report: record not read", "repository", name, "error", err)
			}
			continue
		}
		p := rec.Setup.PendingRun
		if p == nil {
			continue
		}
		mergeSHA := ""
		if p.PullRequest != nil && run.GetEvent() == eventPush {
			mergeSHA = c.readMergeCommit(ctx, rec, log)
		}
		if !expects(p, run, mergeSHA) {
			continue
		}
		rec.RunFailed(now, c.opts.Reconciler.RunsURL(), run.GetHTMLURL(), run.GetConclusion())
		if err := c.store.Put(ctx, rec); err != nil {
			log.Error("reconciler run without a report: failure not stored", "repository", rec.Repository, "error", err)
			continue
		}
		poll.Missing++
		log.Warn("reconciler run failed without a report", "repository", rec.Repository, "conclusion", run.GetConclusion(), "by", p.By, "kind", p.Kind)
	}
}

// expects says whether run is the one pending run p awaits. An Align now's
// is a workflow_dispatch run created at or after the mark (dispatchSlack
// earlier at most); a team-file pull request's is the push run whose head
// is the pull request's merge commit, mergeSHA ("" while the pull request is
// unmerged). A run of another event over the same repository — the
// schedule, somebody else's dispatch — is not the mark's: its own run may
// still report.
func expects(p *inventory.PendingRun, run *github.WorkflowRun, mergeSHA string) bool {
	if p.PullRequest == nil {
		return run.GetEvent() == eventWorkflowDispatch && !p.DispatchedAt.After(run.GetCreatedAt().Add(dispatchSlack))
	}
	return run.GetEvent() == eventPush && mergeSHA != "" && mergeSHA == run.GetHeadSHA()
}

// readMergeCommit reads the pull request of rec's pending run as the
// inventory App and returns its merge commit, "" while it is unmerged or not
// readable now; a merge the mark does not carry yet is noted on it.
func (c *Collector) readMergeCommit(ctx context.Context, rec *inventory.Record, log *slog.Logger) string {
	owner, repo := c.opts.Reconciler.repo()
	p := rec.Setup.PendingRun
	pr, _, err := c.reader.REST().PullRequests.Get(ctx, owner, repo, p.PullRequest.Number)
	if err != nil {
		log.Error("reconciler run without a report: pull request not read", "repository", rec.Repository, "pullRequest", p.PullRequest.URL, "error", err)
		return ""
	}
	if !pr.GetMerged() {
		return ""
	}
	if p.MergedAt == nil {
		rec.Merged(pr.GetMergedAt().Time)
	}
	return pr.GetMergeCommitSHA()
}

// listRunRepositories is the repositories a run handled, read from the
// names of its latest attempt's jobs: the reconciler runs one job
// "Reconcile <name>" per repository, under its team's shard; the Plan job
// and the summaries name none.
func (c *Collector) listRunRepositories(ctx context.Context, runID int64) ([]string, error) {
	owner, repo := c.opts.Reconciler.repo()
	opts := &github.ListWorkflowJobsOptions{Filter: "latest", ListOptions: github.ListOptions{PerPage: 100}}
	var out []string
	for {
		jobs, resp, err := c.reader.REST().Actions.ListWorkflowJobs(ctx, owner, repo, runID, opts)
		if err != nil {
			return nil, fmt.Errorf("list jobs of run %d: %w", runID, err)
		}
		for _, j := range jobs.Jobs {
			if name := repositoryOfJob(j.GetName()); name != "" {
				out = append(out, name)
			}
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// repositoryOfJob is the repository a reconciler job's name says it handled:
// "Reconcile <name>", shown under the reusable workflow's caller as
// "<team> / Reconcile <name>"; "" for any other job.
func repositoryOfJob(job string) string {
	if i := strings.LastIndex(job, " / "); i >= 0 {
		job = job[i+len(" / "):]
	}
	name, is := strings.CutPrefix(job, reconcileJobPrefix)
	if !is || name == "" {
		return ""
	}
	return name
}

// listArtifacts lists a run's artifacts.
func (c *Collector) listArtifacts(ctx context.Context, runID int64) ([]*github.Artifact, error) {
	owner, repo := c.opts.Reconciler.repo()
	opts := &github.ListOptions{PerPage: 100}
	var out []*github.Artifact
	for {
		list, resp, err := c.reader.REST().Actions.ListWorkflowRunArtifacts(ctx, owner, repo, runID, opts)
		if err != nil {
			return nil, fmt.Errorf("list artifacts of run %d: %w", runID, err)
		}
		out = append(out, list.Artifacts...)
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// consumeArtifact stores one artifact as its repository's setup.lastRun
// through Refresh, unless the record already names the run and attempt or
// carries a later run.
func (c *Collector) consumeArtifact(ctx context.Context, run *github.WorkflowRun, a *github.Artifact, name string) error {
	key := c.opts.Org + "/" + name
	old, err := c.store.Get(ctx, key)
	if err != nil && !errors.Is(err, inventory.ErrNotFound) {
		return err
	}
	var stored *inventory.LastRun
	if old != nil {
		stored = old.Setup.LastRun
	}
	if stored.Names(run.GetID(), run.GetRunAttempt()) {
		return errArtifactSkipped
	}
	if a.GetSizeInBytes() > c.opts.Reconciler.MaxArtifact {
		return fmt.Errorf("%w: %s is %d bytes, over %d", errArtifactMalformed, a.GetName(), a.GetSizeInBytes(), c.opts.Reconciler.MaxArtifact)
	}
	report, err := c.downloadArtifact(ctx, a.GetID())
	if err != nil {
		return fmt.Errorf("%s: %w", a.GetName(), err)
	}
	lr := &inventory.LastRun{Result: report.Result, RunID: run.GetID(), Attempt: run.GetRunAttempt(),
		RunURL:    firstNonEmpty(report.WorkflowRun.URL, run.GetHTMLURL()),
		Timestamp: firstTime(report.FinishedAt, report.Result.FinishedAt, run.GetUpdatedAt().Time),
		Change:    report.Change}
	if stored != nil && stored.Timestamp.After(lr.Timestamp) {
		return errArtifactSkipped
	}
	_, err = c.Refresh(ctx, key, lr, inventory.SourceReconciler)
	return err
}

// artifactReport is the JSON in a reconcile-<name> artifact: the engine's
// result as devctl repo reconcile prints it, with the run that produced it,
// when it finished, and the change block — the team-file change the run
// followed (kind, who, the pull request), kept on the record as it is.
type artifactReport struct {
	Result      reconcile.Result  `json:"-"`
	WorkflowRun artifactRun       `json:"workflowRun"`
	FinishedAt  time.Time         `json:"finishedAt"`
	Change      *inventory.Change `json:"change"`
}

// artifactRun is the run in an artifact. Its id and attempt are a JSON
// number or a numeric string: the reconciler workflow writes both from
// GITHUB_RUN_ID and GITHUB_RUN_ATTEMPT, which GitHub Actions hands to the
// step as strings.
type artifactRun struct {
	ID      int64
	URL     string
	Attempt int
	Event   string
	Trigger string
	Devctl  string
}

func (r *artifactRun) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID      json.RawMessage `json:"id"`
		URL     string          `json:"url"`
		Attempt json.RawMessage `json:"attempt"`
		Event   string          `json:"event"`
		Trigger string          `json:"trigger"`
		Devctl  string          `json:"devctl"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	id, err := runNumber(raw.ID, "workflowRun.id")
	if err != nil {
		return err
	}
	attempt, err := runNumber(raw.Attempt, "workflowRun.attempt")
	if err != nil {
		return err
	}
	*r = artifactRun{ID: id, URL: raw.URL, Attempt: int(attempt), Event: raw.Event, Trigger: raw.Trigger, Devctl: raw.Devctl}
	return nil
}

// runNumber reads a run id or attempt: a JSON number, a numeric string, or
// nothing; anything else is refused naming the field.
func runNumber(raw json.RawMessage, field string) (int64, error) {
	s := string(raw)
	if s == "" || s == "null" {
		return 0, nil
	}
	if unquoted, err := strconv.Unquote(s); err == nil {
		s = unquoted
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %s is not a run number", field, raw)
	}
	return n, nil
}

// downloadArtifact reads one artifact: the API answers the download with a
// redirect to a signed blob URL, which is fetched without the installation
// token — the token must not reach the blob store.
func (c *Collector) downloadArtifact(ctx context.Context, id int64) (*artifactReport, error) {
	owner, repo := c.opts.Reconciler.repo()
	u, _, err := c.reader.REST().Actions.DownloadArtifact(ctx, owner, repo, id, 0)
	if err != nil {
		return nil, fmt.Errorf("download artifact %d: %w", id, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.blobs.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download artifact %d: %w", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download artifact %d: %s", id, resp.Status)
	}
	max := c.opts.Reconciler.MaxArtifact
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("download artifact %d: %w", id, err)
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("%w: over %d bytes", errArtifactMalformed, max)
	}
	return decodeArtifact(body, max)
}

// decodeArtifact reads the one JSON file of the artifact zip.
func decodeArtifact(zipped []byte, max int64) (*artifactReport, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errArtifactMalformed, err)
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".json") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errArtifactMalformed, err)
		}
		b, err := io.ReadAll(io.LimitReader(rc, max+1))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errArtifactMalformed, err)
		}
		if int64(len(b)) > max {
			return nil, fmt.Errorf("%w: %s is over %d bytes", errArtifactMalformed, f.Name, max)
		}
		var r artifactReport
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", errArtifactMalformed, f.Name, err)
		}
		if err := json.Unmarshal(b, &r.Result); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", errArtifactMalformed, f.Name, err)
		}
		return &r, nil
	}
	return nil, fmt.Errorf("%w: no JSON file in the zip", errArtifactMalformed)
}

// expirePending gives up the expected runs whose pending window ran out with
// the finding reconcile-run-missing and counts the ones still waiting. An
// Align now's window counts from the dispatch; a pull request's from its
// merge, read from the pull request until the mark carries it — an open pull
// request waits without a deadline, one closed without a merge is followed
// by no run.
func (c *Collector) expirePending(ctx context.Context, now time.Time, poll *ReconcilerPoll) {
	recs, err := c.store.List(ctx)
	if err != nil {
		c.log.Error("reconciler poll: pending runs not read", "error", err)
		return
	}
	for i := range recs {
		rec := &recs[i]
		p := rec.Setup.PendingRun
		if p == nil {
			continue
		}
		from := p.AwaitedFrom()
		if from.IsZero() {
			if from = c.readMerge(ctx, rec, poll); from.IsZero() {
				continue
			}
		}
		if now.Sub(from) < c.opts.Reconciler.PendingWindow {
			poll.Pending++
			continue
		}
		rec.RunMissing(now, c.opts.Reconciler.RunsURL())
		if err := c.store.Put(ctx, rec); err != nil {
			c.log.Error("reconciler poll: missing run not stored", "repository", rec.Repository, "error", err)
			continue
		}
		poll.Missing++
		c.log.Warn("reconciler run missing", "repository", rec.Repository, "awaitedFrom", from, "by", p.By, slog.Duration("window", c.opts.Reconciler.PendingWindow))
	}
}

// readMerge reads the pull request of rec's pending run as the inventory App
// and stores what it learns: a merge starts the window (the mark carries the
// merge time from then on), a close without a merge drops the mark. It
// returns the window's start — zero while the pull request is open, closed
// or not readable now.
func (c *Collector) readMerge(ctx context.Context, rec *inventory.Record, poll *ReconcilerPoll) time.Time {
	owner, repo := c.opts.Reconciler.repo()
	p := rec.Setup.PendingRun
	log := c.log.With("repository", rec.Repository, "pullRequest", p.PullRequest.URL)
	pr, _, err := c.reader.REST().PullRequests.Get(ctx, owner, repo, p.PullRequest.Number)
	if err != nil {
		log.Error("reconciler poll: pull request not read", "error", err)
		return time.Time{}
	}
	var from time.Time
	switch {
	case pr.GetMerged():
		from = pr.GetMergedAt().Time
		rec.Merged(from)
		log.Info("reconciler run awaited from the merge", "mergedAt", from)
	case pr.GetState() == "closed":
		rec.Closed()
		log.Info("reconciler run no longer expected: pull request closed without a merge")
	default:
		c.readConflict(ctx, rec, pr, poll, log)
		return time.Time{}
	}
	if err := c.store.Put(ctx, rec); err != nil {
		log.Error("reconciler poll: pull request's state not stored", "error", err)
		return time.Time{}
	}
	return from
}

// readConflict notes on the mark whether GitHub reports the open pull
// request conflicting with its base — mergeable: false, a neighbouring entry
// changed first (pendingRun.conflictsSince) — and hands a conflict found for
// the first time to the conflict hook: this identity reads only; a member's
// approve_change re-renders the pull request. A pull request mergeable again
// — re-rendered, or rebased by hand — drops the note. While GitHub has not
// computed the mergeability (null) nothing changes; the next poll reads it.
func (c *Collector) readConflict(ctx context.Context, rec *inventory.Record, pr *github.PullRequest, poll *ReconcilerPoll, log *slog.Logger) {
	if pr.Mergeable == nil {
		return
	}
	p := rec.Setup.PendingRun
	conflicts := !pr.GetMergeable()
	if conflicts {
		poll.Conflicting++
	}
	switch {
	case conflicts && !p.Conflicting():
		now := c.now()
		rec.Conflicts(now)
		if err := c.store.Put(ctx, rec); err != nil {
			log.Error("reconciler poll: conflict not stored", "error", err)
			return
		}
		log.Warn("pull request conflicts with its base: a neighbouring entry changed first", "since", now, "by", p.By, "kind", p.Kind)
		if c.conflicted != nil {
			c.conflicted(ctx, rec, pr)
		}
	case !conflicts && p.Conflicting():
		rec.Mergeable()
		if err := c.store.Put(ctx, rec); err != nil {
			log.Error("reconciler poll: mergeable state not stored", "error", err)
			return
		}
		log.Info("pull request mergeable again")
	}
}

// firstTime is the first non-zero time.
func firstTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}
