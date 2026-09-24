package e2e

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/collect"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// repoFresh is a repository created after the last sweep: no record of it
// exists when its creation run reports.
const repoFresh = "fresh-service"

// creationRun adds the completed reconciler run that followed alice's merged
// pull request creating name, finishing now.
func (st *stack) creationRun(t *testing.T, name string, pr int) (*fakeRun, string) {
	t.Helper()
	prURL := "https://github.com/" + org + "/github/pull/" + itoa(pr)
	finished := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	run := st.ghs.actions.addRun(t, runStatusCompleted, finished, artifactReport{name: name, finishedAt: finished,
		result: reconcile.Result{Repository: org + "/" + name, Declared: org + "/" + name, Team: team, Converged: true},
		change: &inventory.Change{Kind: inventory.ChangeCreated, By: alice, PullRequest: &inventory.ChangePullRequest{Number: pr, URL: prURL}}})
	return run, prURL
}

// creationNotices is the creation sentences about name posted so far.
func (st *stack) creationNotices(name, lang string) int {
	_, notices := st.gw.posted()
	n := 0
	for _, m := range notices {
		if m["text"] == alice+" created a new repo: "+name+" (app, "+lang+")" {
			n++
		}
	}
	return n
}

// TestCreationNoticeAfterTheFirstReadFailed: the creation run of a
// repository the inventory has no record of reports before the first
// refresh can read the repository — neither on GitHub nor in the team files
// yet. The run stays open; the next poll builds the record and posts the
// creation notice once.
func TestCreationNoticeAfterTheFirstReadFailed(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	if _, err := st.col.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	st.creationRun(t, repoFresh, 4713)
	if p := st.poll(t); p.Artifacts != 0 || len(p.Errors) != 1 || p.Runs != 0 {
		t.Fatalf("first poll should keep the run open: %+v", p)
	}
	if _, err := st.store.Get(ctx, org+"/"+repoFresh); err == nil {
		t.Fatal("a record without a repository to read")
	}
	st.ghs.repos.add(repoFresh, alice)
	st.ghs.org.declare("- name: " + repoFresh + "\n  componentType: service\n  gen:\n    language: go\n    flavours: [app]\n")
	if p := st.poll(t); p.Artifacts != 1 || p.Runs != 1 || len(p.Errors) != 0 {
		t.Fatalf("second poll: %+v", p)
	}
	if rec := st.record(t, repoFresh); rec.Declaration == nil || rec.Setup.LastRun == nil {
		t.Fatalf("record after the retry: %+v", rec)
	}
	st.poll(t)
	if n := st.creationNotices(repoFresh, "go"); n != 1 {
		t.Errorf("creation notices: %d, want 1", n)
	}
}

// TestCreationNoticeAfterARestartBetweenTheStoreAndThePost: a pod stores
// the creation run's record and is replaced before it posts (and before it
// writes the cursor). The next pod's poll finds the run stored but untold
// and posts the notice once; later polls post nothing.
func TestCreationNoticeAfterARestartBetweenTheStoreAndThePost(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	if _, err := st.col.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	st.ghs.org.declare("- name: " + repoStray + "\n  componentType: service\n  gen:\n    language: python\n    flavours: [app]\n")
	st.creationRun(t, repoStray, 4714)
	// The replaced pod: it stores the record, dies before its hook posts
	// and before its cursor is written.
	if p, err := st.newCollector(0).PollReconciler(ctx); err != nil || p.Artifacts != 1 {
		t.Fatalf("the replaced pod's poll: %+v %v", p, err)
	}
	if err := st.store.PutReconcilerCursor(ctx, &inventory.ReconcilerCursor{}); err != nil {
		t.Fatal(err)
	}
	if n := st.creationNotices(repoStray, "python"); n != 0 {
		t.Fatalf("the replaced pod posted: %d", n)
	}
	st.poll(t)
	st.poll(t)
	if p, err := st.newCollector(0).PollReconciler(ctx); err != nil || p.Artifacts != 0 {
		t.Errorf("a poll after the notice: %+v %v", p, err)
	}
	if n := st.creationNotices(repoStray, "python"); n != 1 {
		t.Errorf("creation notices: %d, want 1", n)
	}
}

// TestTwoPollersPostOneCreationNotice: during a rolling update the old and
// the new pod poll the one cursor at once. One of them consumes the
// creation run; the team hears of the creation once.
func TestTwoPollersPostOneCreationNotice(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	if _, err := st.col.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	st.ghs.org.declare("- name: " + repoStray + "\n  componentType: service\n  gen:\n    language: python\n    flavours: [app]\n")
	st.creationRun(t, repoStray, 4715)
	other := st.newCollector(0)
	other.OnReconciled(st.tools.Reconciled)
	st.ghs.actions.holdBlobs(2)
	var wg sync.WaitGroup
	for _, c := range []*collect.Collector{st.col, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.PollReconciler(ctx); err != nil {
				t.Errorf("poll: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := st.creationNotices(repoStray, "python"); n != 1 {
		t.Errorf("creation notices: %d, want 1", n)
	}
	if lr := st.record(t, repoStray).Setup.LastRun; lr == nil || !lr.Told {
		t.Errorf("the record does not say the run was told: %+v", lr)
	}
}

// TestARunWhoseRepositoryNeverAppearsIsGivenUp: a run whose repository
// cannot be read stays open for the retry window, then it is consumed.
func TestARunWhoseRepositoryNeverAppearsIsGivenUp(t *testing.T) {
	st := newStack(t)
	st.creationRun(t, repoFresh, 4716)
	if p := st.poll(t); p.Runs != 0 || len(p.Errors) != 1 {
		t.Fatalf("first poll should keep the run open: %+v", p)
	}
	st.advance(45 * time.Minute)
	if p := st.poll(t); p.Runs != 1 || len(p.Errors) != 1 {
		t.Fatalf("the poll after the window should give the run up: %+v", p)
	}
	if p := st.poll(t); p.Runs != 0 || len(p.Errors) != 0 {
		t.Errorf("a given-up run is read again: %+v", p)
	}
}
