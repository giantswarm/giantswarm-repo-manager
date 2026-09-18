package collect

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The reconciler workflow writes workflowRun.id and attempt as strings
// (GITHUB_RUN_ID and GITHUB_RUN_ATTEMPT reach the step as strings); a JSON
// number decodes as well; anything else is refused naming the field.
func TestDecodeArtifactRunNumbers(t *testing.T) {
	const jsonNull = "null"
	cases := []struct {
		name, id, attempt string // raw JSON; "" leaves the key out
		wantID            int64
		wantAttempt       int
		wantErr           string
	}{
		{name: "numbers", id: `123`, attempt: `2`, wantID: 123, wantAttempt: 2},
		{name: "strings as the workflow writes them", id: `"123"`, attempt: `"2"`, wantID: 123, wantAttempt: 2},
		{name: "absent"},
		{name: "JSON null", id: jsonNull, attempt: jsonNull},
		{name: "non-numeric id", id: `"abc"`, attempt: `2`, wantErr: `workflowRun.id: "abc" is not a run number`},
		{name: "non-numeric attempt", id: `123`, attempt: `"two"`, wantErr: `workflowRun.attempt: "two" is not a run number`},
		{name: "fraction", id: `1.5`, attempt: `2`, wantErr: `workflowRun.id: 1.5 is not a run number`},
		{name: "bool", id: `true`, attempt: `2`, wantErr: `workflowRun.id: true is not a run number`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := []string{`"url": "https://github.com/giantswarm/github/actions/runs/123"`, `"event": "workflow_dispatch"`}
			if tc.id != "" {
				run = append(run, `"id": `+tc.id)
			}
			if tc.attempt != "" {
				run = append(run, `"attempt": `+tc.attempt)
			}
			body := `{"repository": "giantswarm/x", "finishedAt": "2026-09-17T17:00:00Z", "workflowRun": {` + strings.Join(run, ", ") + `}}`
			r, err := decodeArtifact(zipOf(t, ArtifactPrefix+"x.json", []byte(body)), 1<<20)
			if tc.wantErr != "" {
				if !errors.Is(err, errArtifactMalformed) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want %v with %q, got %v", errArtifactMalformed, tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.WorkflowRun.ID != tc.wantID || r.WorkflowRun.Attempt != tc.wantAttempt {
				t.Errorf("run %d attempt %d, want %d and %d", r.WorkflowRun.ID, r.WorkflowRun.Attempt, tc.wantID, tc.wantAttempt)
			}
			if r.WorkflowRun.URL == "" || r.WorkflowRun.Event != "workflow_dispatch" || r.Result.Repository != "giantswarm/x" || r.FinishedAt.IsZero() {
				t.Errorf("the rest of the report: %+v", r)
			}
		})
	}
}

// zipOf is a zip with one file, as the reconciler uploads it.
func zipOf(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestDecodeArtifactChange: the artifact's change block — the team-file
// change the run followed — is kept as it is; an artifact without one leaves
// the record's change nil.
func TestDecodeArtifactChange(t *testing.T) {
	const run = `"workflowRun": {"id": "123", "url": "https://github.com/giantswarm/github/actions/runs/123", "attempt": "1", "event": "push"}`
	body := `{"repository": "giantswarm/x", "finishedAt": "2026-09-17T17:00:00Z", ` + run + `, ` +
		`"change": {"kind": "transferred", "by": "alice", "fromTeam": "team-planeteers", "pullRequest": {"number": 4711, "url": "https://github.com/giantswarm/github/pull/4711"}}}`
	r, err := decodeArtifact(zipOf(t, ArtifactPrefix+"x.json", []byte(body)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ch := r.Change
	if ch == nil || ch.Kind != "transferred" || ch.By != "alice" || ch.FromTeam != "team-planeteers" ||
		ch.PullRequest == nil || ch.PullRequest.Number != 4711 || ch.PullRequest.URL != "https://github.com/giantswarm/github/pull/4711" {
		t.Errorf("change block: %+v", ch)
	}

	r, err = decodeArtifact(zipOf(t, ArtifactPrefix+"x.json", []byte(`{"repository": "giantswarm/x", `+run+`}`)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if r.Change != nil {
		t.Errorf("an artifact without a change block: %+v", r.Change)
	}
}

// A wake between two ticks makes the poller read at once, and the pending
// cadence applies from that read on: the interval is chosen from the poll
// the wake caused, not from the tick before the mark.
func TestPollLoopWakeReadsAtOnce(t *testing.T) {
	const interval, pendingInterval = 10 * time.Second, 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	polls := make(chan time.Time, 8)
	var pending atomic.Bool
	wake := make(chan struct{}, 1)
	go pollLoop(ctx, interval, pendingInterval, wake, func(context.Context) bool {
		polls <- time.Now()
		return pending.Load()
	})
	nextPoll(t, polls, time.Second) // the poll at start, nothing pending
	pending.Store(true)
	woken := time.Now()
	wake <- struct{}{}
	second := nextPoll(t, polls, interval)
	if d := second.Sub(woken); d >= pendingInterval {
		t.Fatalf("read %v after the wake, want at once", d)
	}
	third := nextPoll(t, polls, interval)
	if d := third.Sub(second); d < pendingInterval || d >= interval/2 {
		t.Fatalf("%v between the reads after the wake, want the pending interval %v", d, pendingInterval)
	}
}

// Without a wake or a pending run the reads keep the interval: the second
// read waits the full interval, and the loop ends with its context.
func TestPollLoopIntervalWithoutPending(t *testing.T) {
	const interval, pendingInterval = 200 * time.Millisecond, 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	polls := make(chan time.Time, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pollLoop(ctx, interval, pendingInterval, make(chan struct{}), func(context.Context) bool {
			polls <- time.Now()
			return false
		})
	}()
	first := nextPoll(t, polls, time.Second)
	second := nextPoll(t, polls, 10*time.Second)
	if d := second.Sub(first); d < interval {
		t.Fatalf("%v between the reads, want at least the interval %v", d, interval)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the loop did not end with its context")
	}
}

// WakeReconciler never blocks: wakes that arrive before the poller reads
// fold into the one waiting.
func TestWakeReconcilerNeverBlocks(t *testing.T) {
	c := &Collector{wake: make(chan struct{}, 1)}
	c.WakeReconciler()
	c.WakeReconciler()
	if len(c.wake) != 1 {
		t.Fatalf("%d wakes buffered, want 1", len(c.wake))
	}
}

func nextPoll(t *testing.T, polls <-chan time.Time, within time.Duration) time.Time {
	t.Helper()
	select {
	case at := <-polls:
		return at
	case <-time.After(within):
		t.Fatalf("no read within %v", within)
		return time.Time{}
	}
}
