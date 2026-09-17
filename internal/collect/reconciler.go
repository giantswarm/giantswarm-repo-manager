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
	"strings"
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
	// PendingInterval is the poll interval while a Reconcile now is pending.
	PendingInterval time.Duration
	// PendingWindow is how long a dispatched run may take to report before it
	// is given up as missing.
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
	// runCompleted is the Actions run status the artifacts are final at.
	runCompleted = "completed"
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
	// Pending is the Reconcile nows still waiting for their run, Missing the
	// ones given up this time.
	Pending, Missing int
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
// PollInterval, and every PendingInterval while a Reconcile now is pending. A
// failed poll is logged and retried at the next interval.
func (c *Collector) RunReconcilerPoll(ctx context.Context) {
	o := c.opts.Reconciler
	if o.PollInterval <= 0 {
		return
	}
	c.log.Info("reconciler poller on", "repository", o.Repository, "workflow", o.Workflow, "interval", o.PollInterval, "pendingInterval", o.PendingInterval)
	wait := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = o.PollInterval
		poll, err := c.PollReconciler(ctx)
		if err != nil {
			c.log.Error("reconciler poll failed", "error", err)
			continue
		}
		if poll.Pending > 0 {
			wait = o.PendingInterval
		}
	}
}

// PollReconciler reads the reconciler's runs since the cursor once: every
// completed run's reconcile-<name> artifacts become the repositories'
// setup.lastRun, the cursor moves past the runs that are consumed, and the
// pending Reconcile nows older than the window are given up. It returns what
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

// consumeRun stores every reconcile-<name> artifact of a completed run;
// false when one could not be read now, which keeps the run open for the
// next poll. A run without artifacts (cancelled, failed before its report
// step) or with a malformed one is consumed with a log line.
func (c *Collector) consumeRun(ctx context.Context, run *github.WorkflowRun, poll *ReconcilerPoll) bool {
	log := c.log.With("run", run.GetID(), "attempt", run.GetRunAttempt(), "url", run.GetHTMLURL())
	artifacts, err := c.listArtifacts(ctx, run.GetID())
	if err != nil {
		poll.Errors = append(poll.Errors, err.Error())
		log.Error("reconciler run: artifacts not listed", "error", err)
		return false
	}
	ok, found := true, 0
	for _, a := range artifacts {
		name, is := strings.CutPrefix(a.GetName(), ArtifactPrefix)
		if !is || a.GetExpired() {
			continue
		}
		found++
		err := c.consumeArtifact(ctx, run, a, name)
		switch {
		case err == nil:
			poll.Artifacts++
			log.Info("reconciler run stored", "repository", name)
		case errors.Is(err, errArtifactSkipped):
			poll.Skipped++
		case errors.Is(err, errArtifactMalformed), errors.Is(err, inventory.ErrNotFound):
			poll.Errors = append(poll.Errors, err.Error())
			log.Warn("reconciler run: artifact not stored", "repository", name, "error", err)
		default:
			poll.Errors = append(poll.Errors, err.Error())
			log.Error("reconciler run: artifact not read", "repository", name, "error", err)
			ok = false
		}
	}
	if found == 0 {
		log.Info("reconciler run without a report", "conclusion", run.GetConclusion())
	}
	return ok
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
		Timestamp: firstTime(report.FinishedAt, report.Result.FinishedAt, run.GetUpdatedAt().Time)}
	if stored != nil && stored.Timestamp.After(lr.Timestamp) {
		return errArtifactSkipped
	}
	_, err = c.Refresh(ctx, key, lr, inventory.SourceReconciler)
	return err
}

// artifactReport is the JSON in a reconcile-<name> artifact: the engine's
// result as devctl repo reconcile prints it, with the run that produced it
// and when it finished.
type artifactReport struct {
	Result      reconcile.Result `json:"-"`
	WorkflowRun struct {
		ID      int64  `json:"id"`
		URL     string `json:"url"`
		Attempt int    `json:"attempt"`
		Event   string `json:"event"`
		Trigger string `json:"trigger"`
		Devctl  string `json:"devctl"`
	} `json:"workflowRun"`
	FinishedAt time.Time `json:"finishedAt"`
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

// expirePending gives up the Reconcile nows older than the pending window
// with the finding reconcile-run-missing and counts the ones still waiting.
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
		if now.Sub(p.DispatchedAt) < c.opts.Reconciler.PendingWindow {
			poll.Pending++
			continue
		}
		rec.RunMissing(now, c.opts.Reconciler.RunsURL())
		if err := c.store.Put(ctx, rec); err != nil {
			c.log.Error("reconciler poll: missing run not stored", "repository", rec.Repository, "error", err)
			continue
		}
		poll.Missing++
		c.log.Warn("reconciler run missing", "repository", rec.Repository, "dispatchedAt", p.DispatchedAt, "by", p.By, slog.Duration("window", c.opts.Reconciler.PendingWindow))
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
