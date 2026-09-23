package tools

import (
	"testing"
	"time"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// TestReleaseNotice: the one sentence about a release nothing was published
// for — red with the failed jobs, the pull request and the rerun; unbuilt
// with the missed build — linking the failed workflow or the project's
// pipelines; nothing for a built, watching or unchecked release, and nothing
// for one the team has heard about.
func TestReleaseNotice(t *testing.T) {
	const tag = "v2.58.8"
	created := time.Date(2026, 9, 23, 7, 18, 53, 0, time.UTC)
	settled := created.Add(11*time.Minute + 20*time.Second)
	pipe := &inventory.ReleasePipeline{Number: 11508, URL: "https://app.circleci.com/pipelines/github/giantswarm/backstage/11508", Workflow: "https://app.circleci.com/pipelines/github/giantswarm/backstage/11508/workflows/wf-build"}
	rec := func(w *inventory.ReleaseWatch) *inventory.Record {
		return &inventory.Record{Repository: "giantswarm/backstage", Name: "backstage", Setup: inventory.Setup{Release: w}}
	}
	pr := &inventory.ChangePullRequest{Number: 2567, URL: "https://github.com/giantswarm/backstage/pull/2567"}
	cases := []struct {
		name string
		w    *inventory.ReleaseWatch
		text string
		link string
	}{
		{"red with the pull request", &inventory.ReleaseWatch{Tag: tag, CreatedAt: created, PullRequest: pr, State: inventory.ReleaseRed, Pipeline: pipe, FailedJobs: []string{"build-image-amd64 (timed out)", "build-image-arm64 (timed out)"}},
			"backstage: release v2.58.8 (pull request #2567) is red, the tag pipeline 11508 failed in build-image-amd64 (timed out), build-image-arm64 (timed out) — rerun the workflow from failed on CircleCI, then devctl release wait giantswarm/backstage v2.58.8 confirms it",
			pipe.Workflow},
		{"red without a pull request links the pipeline when no workflow is named", &inventory.ReleaseWatch{Tag: tag, CreatedAt: created, State: inventory.ReleaseRed, Pipeline: &inventory.ReleasePipeline{Number: 11508, URL: pipe.URL}, FailedJobs: []string{"go-build (failed)"}},
			"backstage: release v2.58.8 is red, the tag pipeline 11508 failed in go-build (failed) — rerun the workflow from failed on CircleCI, then devctl release wait giantswarm/backstage v2.58.8 confirms it",
			pipe.URL},
		{"unbuilt", &inventory.ReleaseWatch{Tag: "v2.58.9", CreatedAt: created, PullRequest: pr, State: inventory.ReleaseUnbuilt, SettledAt: &settled},
			"backstage: release v2.58.9 (pull request #2567) has no CircleCI pipeline 11 minutes after the tag, nothing was built or published — trigger the tag's pipeline by hand on CircleCI, then devctl release wait giantswarm/backstage v2.58.9 confirms it",
			"https://app.circleci.com/pipelines/github/giantswarm/backstage"},
		{"built is nothing", &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseBuilt, Pipeline: pipe}, "", ""},
		{"watching is nothing", &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseWatching, Pipeline: pipe}, "", ""},
		{"unchecked is nothing", &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseUnchecked, Reason: "private"}, "", ""},
		{"told once", &inventory.ReleaseWatch{Tag: tag, State: inventory.ReleaseRed, Pipeline: pipe, FailedJobs: []string{"go-build (failed)"}, Told: "backstage: release v2.58.8 is red …"}, "", ""},
		{"no release is nothing", nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := ReleaseNotice(rec(tc.w))
			if ok != (tc.text != "") || n.Text != tc.text || n.Link != tc.link {
				t.Errorf("got ok=%v text=%q link=%q\nwant text=%q link=%q", ok, n.Text, n.Link, tc.text, tc.link)
			}
		})
	}
}
