package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/circleciclient"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// TestReleaseWatchTellsTheTeamOnce: the release watch against the fakes —
// the page of recently pushed repositories, the tag's own pipeline on
// CircleCI, the team's standup channel through the gateway. A green tag
// pipeline beside a red branch pipeline at the same commit is built and
// silent; a red one is one notice naming the tag, the pull request, the
// failed jobs and the rerun, linking the failed workflow, and nothing on the
// next pass; a rerun from failed that goes green turns the record built,
// silently; a tag without a pipeline is a missed build after the grace
// period, told once; a private repository read without a token is unchecked
// and silent; a refresh keeps what the watch knows.
func TestReleaseWatchTellsTheTeamOnce(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	if _, err := st.col.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	o := st.ghs.org
	sha := circleciclient.PipelineVCS{Revision: "a80db8ff"}
	tag := func(v string) circleciclient.PipelineVCS {
		return circleciclient.PipelineVCS{Tag: v, Revision: sha.Revision}
	}
	branch := func(b string) circleciclient.PipelineVCS {
		return circleciclient.PipelineVCS{Branch: b, Revision: sha.Revision}
	}
	key := org + "/" + repoPresent
	record := func() *inventory.Record {
		t.Helper()
		rec, err := st.store.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	notices := func() []map[string]any {
		_, n := st.gw.posted()
		return n
	}

	// A green tag pipeline beside a red branch pipeline at the same commit
	// (backstage v2.58.8): built, nothing told, the record's release step
	// and findings from the tag's pipeline.
	now := st.now()
	o.release(repoPresent, "v1.1.0", now.Add(-20*time.Minute), 41)
	st.cc.pipeline(repoPresent, tag("v1.1.0"), now.Add(-19*time.Minute), "success", circleciclient.Job{Name: jobPushRelease, Status: "success"})
	st.cc.pipeline(repoPresent, branch("changesets-ghcommit-temp/changeset-release/main"), now.Add(-18*time.Minute), "failed")
	if p := st.watchReleases(t); p.Started != 1 || p.Settled != 1 || p.Told != 0 || p.Watching != 0 {
		t.Fatalf("green beside red: %+v", p)
	}
	rec := record()
	if w := rec.Setup.Release; w == nil || w.Tag != "v1.1.0" || w.State != inventory.ReleaseBuilt || w.Pipeline == nil || w.Pipeline.Number != 11501 || w.PullRequest == nil || w.PullRequest.Number != 41 {
		t.Errorf("built release: %+v", rec.Setup.Release)
	}
	if sr := rec.Setup.Checks.Step(reconcile.StepRelease); sr == nil || sr.Verdict != reconcile.VerdictOK || !strings.Contains(sr.Summary, "release v1.1.0 built: the tag's pipeline 11501 succeeded") || len(rec.Findings) != 1 {
		t.Errorf("release step after the watch: %+v findings=%+v", sr, rec.Findings)
	}
	if n := notices(); len(n) != 0 {
		t.Fatalf("a built release posts nothing: %v", n)
	}

	// A red tag pipeline: the notice, once.
	o.release(repoPresent, "v1.2.0", now.Add(-10*time.Minute), 42)
	wf := st.cc.pipeline(repoPresent, tag("v1.2.0"), now.Add(-9*time.Minute), "failed")
	reads := st.cc.count()
	if p := st.watchReleases(t); p.Started != 1 || p.Settled != 1 || p.Told != 1 || p.Watching != 1 {
		t.Fatalf("red: %+v", p)
	}
	if got := st.cc.count() - reads; got != 3 {
		t.Errorf("CircleCI reads for one red release: %d, want the pipelines, the workflows and the failed workflow's jobs", got)
	}
	want := repoPresent + ": release v1.2.0 (pull request #42) is red, the tag pipeline 11503 failed in build-image-amd64 (timed out), build-image-arm64 (timed out) — rerun the workflow from failed on CircleCI, then devctl release wait " + key + " v1.2.0 confirms it"
	n := notices()
	if len(n) != 1 || n[0][kChannel] != bumblebeeStandup || n[0]["team"] != team || n[0]["text"] != want || n[0]["link"] != "https://app.circleci.com/pipelines/github/"+key+"/11503/workflows/"+wf {
		t.Fatalf("the red release's notice: %v\nwant text %q", n, want)
	}
	rec = record()
	if w := rec.Setup.Release; w.State != inventory.ReleaseRed || w.Told != want || w.ToldAt == nil || len(w.FailedJobs) != 2 {
		t.Errorf("red release: %+v", w)
	}
	if f := rec.Findings; len(f) != 2 || f[1].Kind != string(reconcile.FindingRedRelease) || !strings.Contains(f[1].Message, "the tag pipeline 11503 of "+key+" v1.2.0 failed in build-image-amd64 (timed out)") {
		t.Errorf("findings of the red release: %+v", f)
	}
	// The next pass reads the red release again and says nothing more.
	if p := st.watchReleases(t); p.Started != 0 || p.Settled != 0 || p.Told != 0 || p.Watching != 1 {
		t.Errorf("second pass over the red release: %+v", p)
	}
	if n := notices(); len(n) != 1 {
		t.Fatalf("told once: %v", n)
	}
	// A refresh keeps what the watch knows, and its release step comes from
	// the tag's pipeline, not the commit's statuses.
	if _, err := st.col.Refresh(ctx, key, nil, inventory.SourceRefresh); err != nil {
		t.Fatal(err)
	}
	rec = record()
	if w := rec.Setup.Release; w == nil || w.State != inventory.ReleaseRed || w.Told != want {
		t.Errorf("after a refresh: %+v", w)
	}
	if sr := rec.Setup.Checks.Step(reconcile.StepRelease); sr == nil || !strings.Contains(sr.Summary, "the tag's pipeline 11503 failed") {
		t.Errorf("release step after a refresh: %+v", sr)
	}

	// The rerun from failed goes green: built, silently.
	st.cc.rerun(repoPresent, "success", st.now())
	if p := st.watchReleases(t); p.Settled != 1 || p.Told != 0 || p.Watching != 0 {
		t.Errorf("rerun green: %+v", p)
	}
	rec = record()
	if w := rec.Setup.Release; w.State != inventory.ReleaseBuilt || len(w.FailedJobs) != 0 || w.Told != want {
		t.Errorf("recovered release: %+v", w)
	}
	if len(rec.Findings) != 1 || len(notices()) != 1 {
		t.Errorf("a recovered release clears its finding and posts nothing: findings=%+v notices=%d", rec.Findings, len(notices()))
	}

	// A tag without a pipeline: watched within the grace period, a missed
	// build after it, told once.
	o.release(repoPresent, "v1.3.0", st.now().Add(-2*time.Minute), 43)
	if p := st.watchReleases(t); p.Started != 1 || p.Settled != 0 || p.Watching != 1 || p.Told != 0 {
		t.Errorf("no pipeline yet: %+v", p)
	}
	st.advance(10 * time.Minute)
	if p := st.watchReleases(t); p.Settled != 1 || p.Told != 1 || p.Watching != 0 {
		t.Errorf("missed build: %+v", p)
	}
	n = notices()
	if len(n) != 2 || !strings.HasPrefix(n[1]["text"].(string), repoPresent+": release v1.3.0 (pull request #43) has no CircleCI pipeline 12 minutes after the tag, nothing was built or published — trigger the tag's pipeline by hand on CircleCI, then devctl release wait "+key+" v1.3.0 confirms it") || n[1]["link"] != "https://app.circleci.com/pipelines/github/"+key {
		t.Fatalf("the missed build's notice: %v", n)
	}
	if p := st.watchReleases(t); p.Told != 0 || p.Watching != 0 {
		t.Errorf("a missed build is told once: %+v", p)
	}
	if f := record().Findings; len(f) != 2 || f[1].Kind != string(reconcile.FindingMissedTagBuild) {
		t.Errorf("findings of the missed build: %+v", f)
	}

	// A team without a policy file is not messaged, and the watch stops
	// asking: the sentence is recorded as told, no notice reaches the
	// gateway, the next pass reads the red release without a second try.
	policyPath := "repository-setup/" + team + ".yaml"
	st.ghs.files.mu.Lock()
	policy := st.ghs.files.files[policyPath]
	delete(st.ghs.files.files, policyPath)
	st.ghs.files.mu.Unlock()
	o.release(repoPresent, "v1.4.0", st.now().Add(-5*time.Minute), 44)
	st.cc.pipeline(repoPresent, tag("v1.4.0"), st.now().Add(-4*time.Minute), "failed")
	if p := st.watchReleases(t); p.Started != 1 || p.Settled != 1 || p.Told != 0 {
		t.Errorf("no policy file: %+v", p)
	}
	rec = record()
	if w := rec.Setup.Release; w.State != inventory.ReleaseRed || !strings.HasPrefix(w.Told, repoPresent+": release v1.4.0 (pull request #44) is red") {
		t.Errorf("a team without a policy file: the sentence is recorded, not posted: %+v", w)
	}
	if len(notices()) != 2 {
		t.Errorf("a team without a policy file hears nothing: %v", notices())
	}
	st.ghs.files.mu.Lock()
	st.ghs.files.files[policyPath] = policy
	st.ghs.files.mu.Unlock()

	// A private repository read without a token: CircleCI answers 404, the
	// release is unchecked, nothing is told.
	o.private = map[string]bool{repoLegacy: true}
	if _, err := st.col.Refresh(ctx, org+"/"+repoLegacy, nil, inventory.SourceRefresh); err != nil {
		t.Fatal(err)
	}
	o.release(repoLegacy, "v2.0.0", st.now().Add(-time.Minute), 7)
	if p := st.watchReleases(t); p.Started != 1 || p.Settled != 1 || p.Told != 0 {
		t.Errorf("private: %+v", p)
	}
	legacy, err := st.store.Get(ctx, org+"/"+repoLegacy)
	if err != nil {
		t.Fatal(err)
	}
	if w := legacy.Setup.Release; w == nil || w.State != inventory.ReleaseUnchecked || !strings.Contains(w.Reason, "no CircleCI token") {
		t.Errorf("private release: %+v", legacy.Setup.Release)
	}
	if len(notices()) != 2 {
		t.Errorf("a private repository without a token posts nothing: %v", notices())
	}
	// An undeclared repository's release is nobody's to tell.
	o.release(repoStray, "v0.1.0", st.now().Add(-time.Minute), 0)
	if p := st.watchReleases(t); p.Started != 0 || p.Told != 0 {
		t.Errorf("undeclared: %+v", p)
	}
}
