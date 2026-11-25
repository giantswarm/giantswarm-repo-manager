package collect

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/google/go-github/v93/github"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// TestApplyStepReadsTheEnginesCircleCIStep: the record's pipeline is read
// from devctl's own circleci step, run here in check mode without a
// CircleCI client against a GitHub that serves the default branch head's
// statuses and, unless first build, a parent commit's — posted by the
// CircleCI GitHub App, by a person (an OAuth follow), by both, or none.
// Both on the branch's first build (no earlier commit carries a CircleCI
// status) is the follow that created the project beside the App pipeline,
// no OAuth trigger. applyStep tells an OAuth follow alone from one beside
// the App pipeline by the engine's summary (oauthOnly); a devctl that words
// it otherwise fails here, not in the record.
func TestApplyStepReadsTheEnginesCircleCIStep(t *testing.T) {
	cases := []struct {
		name       string
		posters    []string
		firstBuild bool
		pipeline   string
	}{
		{"the GitHub App", []string{reconcile.CircleCIAppLogin}, false, inventory.CircleCIPipelineApp},
		{"a person: an OAuth follow", []string{alice}, false, inventory.CircleCIPipelineOAuth},
		{"both", []string{reconcile.CircleCIAppLogin, alice}, false, inventory.CircleCIPipelineAppAndOAuth},
		{"both on the first build: the creating follow", []string{reconcile.CircleCIAppLogin, alice}, true, inventory.CircleCIPipelineApp},
		{"none", nil, false, inventory.CircleCIPipelineNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sr := engineCircleCIStep(t, tc.posters, tc.firstBuild)
			out := &inventory.CircleCI{}
			applyStep(out, sr)
			if out.Pipeline != tc.pipeline {
				t.Errorf("pipeline %q from the engine's step %+v, want %q", out.Pipeline, sr, tc.pipeline)
			}
		})
	}
}

// TestTheEnginesOAuthReleaseSkip: rewriteReleaseStep lets the release
// watch answer a release step the engine skipped for an OAuth follow's
// build, and no other skip, by the engine's words (builtOnOAuth). devctl's
// own release step for a tag an OAuth follow built carries them.
func TestTheEnginesOAuthReleaseSkip(t *testing.T) {
	sr := engineStep(t, reconcile.StepRelease, []string{alice}, false)
	if sr.Verdict != reconcile.VerdictSkipped || !strings.Contains(sr.Summary, builtOnOAuth) {
		t.Errorf("the engine's release step of an OAuth-built tag: %+v, want skipped containing %q", sr, builtOnOAuth)
	}
}

// engineCircleCIStep runs devctl's circleci step for giantswarm/x, whose
// commits carry one CircleCI status per poster, and returns it.
func engineCircleCIStep(t *testing.T, posters []string, firstBuild bool) *reconcile.StepResult {
	t.Helper()
	sr := engineStep(t, reconcile.StepCircleCI, posters, firstBuild)
	if sr.Verdict == reconcile.VerdictSkipped {
		t.Fatalf("the engine did not read the statuses: %+v", sr)
	}
	return sr
}

// engineStep runs one of devctl's steps for giantswarm/x, whose commits and
// latest release v1.0.0 carry one CircleCI status per poster, and returns
// it. Unless firstBuild, the head has a parent commit with the same
// statuses, so the head is not the default branch's first build.
func engineStep(t *testing.T, step reconcile.Step, posters []string, firstBuild bool) *reconcile.StepResult {
	t.Helper()
	commits := []map[string]any{{"sha": "head"}}
	if !firstBuild {
		commits = append(commits, map[string]any{"sha": "parent"})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/giantswarm/x":
			_ = json.NewEncoder(w).Encode(map[string]any{kName: "x", "full_name": "giantswarm/x", "default_branch": mainBranch, "size": 10})
		case "/repos/giantswarm/x/commits":
			_ = json.NewEncoder(w).Encode(commits)
		case "/repos/giantswarm/x/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.0.0", "published_at": "2026-11-01T10:00:00Z", "created_at": "2026-11-01T10:00:00Z"})
		case "/repos/giantswarm/x/commits/" + mainBranch + "/statuses", "/repos/giantswarm/x/commits/head/statuses", "/repos/giantswarm/x/commits/parent/statuses", "/repos/giantswarm/x/commits/v1.0.0/statuses":
			statuses := []map[string]any{}
			for _, p := range posters {
				statuses = append(statuses, map[string]any{"context": "ci/circleci: go-build", "state": "success", "creator": map[string]any{"login": p},
					"created_at": "2026-11-01T10:05:00Z"})
			}
			_ = json.NewEncoder(w).Encode(statuses)
		case "/repos/giantswarm/x/pulls":
			_ = json.NewEncoder(w).Encode([]any{})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Not Found"}`)
		}
	}))
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	gh, err := github.NewClient(github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	entry := reposetup.Entry{Name: "x", Accepted: true,
		Rendered: "- name: x\n  componentType: service\n  gen:\n    language: go\n    flavours: [app]\n    ci:\n      generate: true\n"}
	runner := &reconcile.Runner{GitHub: gh, Log: io.Discard}
	res, err := runner.Run(context.Background(), reconcile.Request{Owner: "giantswarm", Team: teamBumblebee, Entry: entry,
		Mode: reconcile.ModeCheck, Steps: []reconcile.Step{step}})
	if err != nil {
		t.Fatal(err)
	}
	sr := res.Step(step)
	if sr == nil || sr.Verdict == reconcile.VerdictFailed {
		t.Fatalf("the engine's %s step: %+v", step, res.Steps)
	}
	return sr
}
