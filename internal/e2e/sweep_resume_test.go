package e2e

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/google/go-github/v92/github"

	"github.com/giantswarm/giantswarm-repo-manager/internal/collect"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// interruptingChecker is the engine a pod's shutdown cuts short at its at-th
// check (0: never): the sweep's context ends there and the check fails with
// it.
type interruptingChecker struct {
	*fakeChecker
	at     int32
	calls  atomic.Int32
	cancel context.CancelFunc
}

func (c *interruptingChecker) Check(ctx context.Context, req reconcile.Request) (*reconcile.Result, error) {
	if c.calls.Add(1) == c.at {
		c.cancel()
		return nil, ctx.Err()
	}
	return c.fakeChecker.Check(ctx, req)
}

// restReadingChecker is the engine reading GitHub over REST as the App:
// every check makes a read whose answer carries the REST budget.
type restReadingChecker struct {
	*fakeChecker
	rest *github.Client
}

func (c *restReadingChecker) Check(ctx context.Context, req reconcile.Request) (*reconcile.Result, error) {
	// The answer's rate-limit headers are what counts, not the repository.
	_, _, _ = c.rest.Repositories.Get(ctx, org, req.Entry.Name)
	return c.fakeChecker.Check(ctx, req)
}

// logBuffer is a sweep's log at Info, for the lines an operator reads.
func logBuffer() (*bytes.Buffer, *slog.Logger) {
	var b bytes.Buffer
	return &b, slog.New(slog.NewTextHandler(&b, nil))
}

// sweptSince are the repositories whose record a sweep wrote at or after
// start, sorted.
func (st *stack) sweptSince(t *testing.T, start time.Time) []string {
	t.Helper()
	list, err := st.store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range list {
		if r.Source == inventory.SourceSweep && !r.RefreshedAt.Before(start) {
			out = append(out, r.Name)
		}
	}
	sort.Strings(out)
	return out
}

// TestAnInterruptedSweepIsTakenUpByTheNextPod: three pods sweep the org in
// name order, one check at a time; the first two are stopped mid-sweep. Each
// leaves the records it finished written and fresh, the cursor stays, no
// summary is written and nothing is removed; the next pod takes the sweep up
// where the records end, checks only what is left and writes the summary of
// the whole sweep once.
func TestAnInterruptedSweepIsTakenUpByTheNextPod(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	logs, log := logBuffer()
	checks := 0
	pod := func(at int32) (*inventory.SweepSummary, error) {
		pctx, cancel := context.WithCancel(ctx)
		defer cancel()
		chk := &interruptingChecker{fakeChecker: st.checker, at: at, cancel: cancel}
		defer func() { checks += int(chk.calls.Load()) }()
		return st.collectorWith(collect.Options{Concurrency: 1, Interval: 24 * time.Hour}, chk, log).Sweep(pctx)
	}

	// Pod 1 stops at its first check, legacy-app's.
	if _, err := pod(1); !errors.Is(err, context.Canceled) {
		t.Fatalf("pod 1: %v, want the shutdown's cancellation", err)
	}
	cur, err := st.store.SweepCursor(ctx)
	if err != nil || cur == nil || cur.Resumes != 0 {
		t.Fatalf("cursor after pod 1: %+v %v", cur, err)
	}
	if got := strings.Join(st.sweptSince(t, cur.StartedAt), ","); got != repoArchived+","+repoGone {
		t.Errorf("records pod 1 wrote: %s", got)
	}
	if n, _ := st.store.Count(ctx); n != 3 {
		t.Errorf("records after pod 1: %d, want its 2 and the seeded one (nothing removed before a complete pass)", n)
	}
	if last, _ := st.store.Sweep(ctx); last != nil {
		t.Errorf("an interrupted sweep wrote a summary: %+v", last)
	}

	// Pod 2 checks legacy-app and stops at present-service's check.
	if _, err := pod(2); !errors.Is(err, context.Canceled) {
		t.Fatalf("pod 2: %v, want the shutdown's cancellation", err)
	}
	if got := strings.Join(st.sweptSince(t, cur.StartedAt), ","); got != repoArchived+","+repoGone+","+repoLegacy {
		t.Errorf("records after pod 2: %s", got)
	}
	legacy := st.record(t, repoLegacy)
	if legacy.Setup.Checks == nil || legacy.Setup.CheckError != "" {
		t.Errorf("legacy-app's check: %+v", legacy.Setup)
	}
	if _, err := st.store.Get(ctx, org+"/"+repoPresent); !errors.Is(err, inventory.ErrNotFound) {
		t.Errorf("present-service's cut-short check was written: %v", err)
	}

	// Pod 3 finishes the sweep: present-service and stray-repo.
	sum, err := pod(0)
	if err != nil {
		t.Fatalf("pod 3: %v", err)
	}
	if sum.Repositories != 5 || sum.Declared != 2 || sum.Undeclared != 2 || sum.Gone != 1 || sum.Archived != 1 || sum.Removed != 1 ||
		sum.Resumes != 2 || sum.Carried != 3 || sum.EngineChecks != 1 || !sum.StartedAt.Equal(cur.StartedAt) {
		t.Errorf("summary: %+v", sum)
	}
	if last, _ := st.store.Sweep(ctx); last == nil || last.Resumes != 2 || !last.StartedAt.Equal(cur.StartedAt) {
		t.Errorf("stored summary: %+v", last)
	}
	if cur, _ := st.store.SweepCursor(ctx); cur != nil {
		t.Errorf("cursor after the complete sweep: %+v", cur)
	}
	if got := strings.Join(st.sweptSince(t, cur.StartedAt), ","); got != repoArchived+","+repoGone+","+repoLegacy+","+repoPresent+","+repoStray {
		t.Errorf("records after pod 3: %s", got)
	}
	if n, _ := st.store.Count(ctx); n != 5 {
		t.Errorf("records: %d, want 5 (the seeded one removed)", n)
	}
	if r := st.record(t, repoLegacy); !r.RefreshedAt.Equal(legacy.RefreshedAt) {
		t.Errorf("pod 3 wrote legacy-app again: %s, pod 2 at %s", r.RefreshedAt, legacy.RefreshedAt)
	}
	// Four engine calls in all: legacy-app cut short, legacy-app, present-service
	// cut short, present-service.
	if checks != 4 {
		t.Errorf("engine calls: %d, want 4", checks)
	}
	for _, line := range []string{`msg="sweep interrupted" done=2 of=5`, `msg="sweep resuming"`, `resumes=2`, `msg="sweep progress" done=5 of=5`, `carried=3`} {
		if !strings.Contains(logs.String(), line) {
			t.Errorf("log has no %q:\n%s", line, logs)
		}
	}
}

// TestTheScheduleTakesAnUnfinishedSweepUpAtOnce: a pod starting after a
// sweep stopped short (its summary is minutes old) takes the sweep up at
// once instead of waiting out the interval; without a cursor it waits.
func TestTheScheduleTakesAnUnfinishedSweepUpAtOnce(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	const interval = 24 * time.Hour
	now := st.now()
	stopped := &inventory.SweepSummary{StartedAt: now.Add(-time.Hour), FinishedAt: now.Add(-10 * time.Minute), Errors: []string{"stopped at the budget floor"}}
	if err := st.store.PutSweep(ctx, stopped); err != nil {
		t.Fatal(err)
	}
	// schedule runs the schedule until its first wait that is not zero and
	// returns the waits.
	schedule := func() []time.Duration {
		sctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var waits []time.Duration
		col := st.collectorWith(collect.Options{Concurrency: 2, Interval: interval, Sleep: func(ctx context.Context, d time.Duration) error {
			waits = append(waits, d)
			if d > 0 {
				cancel()
				return ctx.Err()
			}
			return nil
		}}, st.checker, st.log)
		col.RunSchedule(sctx)
		return waits
	}

	if waits := schedule(); len(waits) != 1 || waits[0] < interval-11*time.Minute || waits[0] > interval-10*time.Minute {
		t.Errorf("without a cursor the schedule waits out the interval: %v", waits)
	}
	if err := st.store.PutSweepCursor(ctx, &inventory.SweepCursor{StartedAt: stopped.StartedAt}); err != nil {
		t.Fatal(err)
	}
	if waits := schedule(); len(waits) != 2 || waits[0] != 0 || waits[1] != interval {
		t.Errorf("with a cursor the schedule sweeps at once, then waits the interval: %v", waits)
	}
	last, _ := st.store.Sweep(ctx)
	if last == nil || !last.StartedAt.Equal(stopped.StartedAt) || last.Resumes != 1 || last.Repositories != 5 || len(last.Errors) != 0 {
		t.Errorf("summary of the sweep taken up: %+v", last)
	}
	if cur, _ := st.store.SweepCursor(ctx); cur != nil {
		t.Errorf("cursor after the complete sweep: %+v", cur)
	}
}

// TestASweepPausesAtTheRESTBudgetFloor: with the REST budget below the floor
// the sweep's checks wait for the reset — one wait, the other check queued
// behind it — and then run, instead of failing with GitHub's refusal.
func TestASweepPausesAtTheRESTBudgetFloor(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	logs, log := logBuffer()
	reset := st.now().Add(time.Hour).Truncate(time.Second)
	st.ghs.rest.Store(&fakeRESTBudget{remaining: 3, reset: reset})
	rest := st.app.Reader().REST()
	// A read before the sweep reports the budget's end.
	_, _, _ = rest.Repositories.Get(ctx, org, repoPresent)

	// The waits are serialized by the pause itself.
	var waits []time.Duration
	col := st.collectorWith(collect.Options{Concurrency: 2, RESTFloor: 100, Sleep: func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		// GitHub resets the budget; the clock moves past the reset.
		st.ghs.rest.Store(&fakeRESTBudget{remaining: 5000, reset: reset.Add(time.Hour)})
		st.advance(d)
		return nil
	}}, &restReadingChecker{fakeChecker: st.checker, rest: rest}, log)
	sum, err := col.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(waits) != 1 || waits[0] < 59*time.Minute || waits[0] > time.Hour+time.Second {
		t.Errorf("waits: %v, want one until the reset", waits)
	}
	if sum.EngineChecks != 2 || len(sum.Errors) != 0 {
		t.Errorf("summary: %+v", sum)
	}
	for _, name := range []string{repoLegacy, repoPresent} {
		if r := st.record(t, name); r.Setup.Checks == nil || r.Setup.CheckError != "" {
			t.Errorf("%s check: %+v", name, r.Setup)
		}
	}
	for _, line := range []string{`msg="sweep paused at the REST budget floor" restRemaining=3 floor=100`, `msg="sweep continues after the REST budget reset"`} {
		if !strings.Contains(logs.String(), line) {
			t.Errorf("log has no %q:\n%s", line, logs)
		}
	}
}
