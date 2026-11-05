package collect

import (
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// TestCircleCIDerivation: the record's CircleCI state from the head's
// statuses and the reconciler's circleci step, which reads who posted the
// CircleCI statuses — every combination names what it knows and what stays
// unknown, nothing is guessed.
func TestCircleCIDerivation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	head := func(truncated bool, contexts ...string) *repoNode {
		n := &repoNode{}
		n.DefaultBranchRef = &struct {
			Name   string        `json:"name"`
			Target *branchTarget `json:"target"`
		}{Name: mainBranch, Target: &branchTarget{StatusCheckRollup: &statusRollup{State: "SUCCESS"}}}
		r := n.DefaultBranchRef.Target.StatusCheckRollup
		r.Contexts.PageInfo.HasNextPage = truncated
		for i, c := range contexts {
			state := "SUCCESS"
			if strings.HasSuffix(c, "!") {
				c, state = strings.TrimSuffix(c, "!"), "FAILURE"
			}
			r.Contexts.Nodes = append(r.Contexts.Nodes, struct {
				Context   string    `json:"context"`
				State     string    `json:"state"`
				CreatedAt time.Time `json:"createdAt"`
			}{Context: c, State: state, CreatedAt: now.Add(time.Duration(i) * time.Minute)})
		}
		return n
	}
	// step is a run whose circleci step is the engine's, as
	// circleCIFromStatuses words it.
	step := func(verdict reconcile.Verdict, summary string, findings ...reconcile.FindingKind) *inventory.LastRun {
		sr := reconcile.StepResult{Step: reconcile.StepCircleCI, Verdict: verdict, Summary: summary}
		for _, k := range findings {
			sr.Findings = append(sr.Findings, reconcile.Finding{Kind: k, Message: "the finding", Advisory: k.Advisory()})
		}
		return &inventory.LastRun{Result: reconcile.Result{Mode: reconcile.ModeCheck, Steps: []reconcile.StepResult{sr}}}
	}
	app := step(reconcile.VerdictOK, "CircleCI statuses on main from circleci-app[bot]: GitHub App pipeline")
	both := step(reconcile.VerdictReported, "CircleCI statuses on main from circleci-app[bot] and an OAuth follow", reconcile.FindingCircleCIOAuthFollow)
	oauth := step(reconcile.VerdictReported, oauthOnly, reconcile.FindingCircleCIOAuthFollow)
	missing := step(reconcile.VerdictReported, "no CircleCI status on main", reconcile.FindingCircleCIPipelineMissing)
	refused := step(reconcile.VerdictReported, "CircleCI pipeline not checked: pull requests not readable", reconcile.FindingUnchecked)
	const circleA, circleB = "ci/circleci: a", "ci/circleci: b"
	const pipeline, followed = inventory.CircleCIFactPipeline, inventory.CircleCIFactFollowed
	cases := []struct {
		name string
		node *repoNode
		run  *inventory.LastRun
		want inventory.CircleCI
	}{
		{"no statuses, no run", head(false, "sonar"), nil,
			inventory.CircleCI{Source: inventory.CircleCISourceStatuses, Unknown: []string{pipeline}}},
		{"statuses: the worst state, CircleCI's contexts only, the newest time", head(false, "ci/circleci: b!", circleA, "sonar!"), nil,
			inventory.CircleCI{Followed: true, Source: inventory.CircleCISourceStatuses, Unknown: []string{pipeline},
				Head: &inventory.HeadStatus{State: stateFailure, Contexts: []string{circleA, circleB}, At: now.Add(time.Minute)}}},
		{"truncated without CircleCI: followed unknown", head(true, "sonar"), nil,
			inventory.CircleCI{Source: inventory.CircleCISourceStatuses, Unknown: []string{pipeline, followed}}},
		{"run: the GitHub App pipeline", head(false), app,
			inventory.CircleCI{Followed: true, Pipeline: inventory.CircleCIPipelineApp, Source: inventory.CircleCISourceBoth}},
		{"run: the GitHub App pipeline and an OAuth follow", head(false), both,
			inventory.CircleCI{Followed: true, Pipeline: inventory.CircleCIPipelineAppAndOAuth, Source: inventory.CircleCISourceBoth}},
		{"run: an OAuth follow alone", head(false), oauth,
			inventory.CircleCI{Followed: true, Pipeline: inventory.CircleCIPipelineOAuth, Source: inventory.CircleCISourceBoth}},
		{"run: no pipeline", head(false), missing,
			inventory.CircleCI{Pipeline: inventory.CircleCIPipelineNone, Source: inventory.CircleCISourceBoth}},
		{"run: no pipeline, the head built since", head(false, circleA), missing,
			inventory.CircleCI{Followed: true, Pipeline: inventory.CircleCIPipelineNone, Source: inventory.CircleCISourceBoth,
				Head: &inventory.HeadStatus{State: stateSuccess, Contexts: []string{circleA}, At: now}}},
		{"run could not read the statuses: pipeline unknown", head(false), refused,
			inventory.CircleCI{Source: inventory.CircleCISourceBoth, Unknown: []string{pipeline}}},
		{"truncated head, the run tells: followed from the run", head(true, "sonar"), app,
			inventory.CircleCI{Followed: true, Pipeline: inventory.CircleCIPipelineApp, Source: inventory.CircleCISourceBoth}},
		{"failed step: the error, nothing known", head(false), step(reconcile.VerdictFailed, "the step's summary"),
			inventory.CircleCI{Source: inventory.CircleCISourceBoth, Unknown: []string{pipeline}, Error: "the reconciler's circleci step failed: the step's summary"}},
		{"skipped step counts as no run", head(false, circleA), step(reconcile.VerdictSkipped, "no CircleCI pipeline"),
			inventory.CircleCI{Followed: true, Source: inventory.CircleCISourceStatuses, Unknown: []string{pipeline},
				Head: &inventory.HeadStatus{State: stateSuccess, Contexts: []string{circleA}, At: now}}},
		{"no default branch", &repoNode{}, nil,
			inventory.CircleCI{Source: inventory.CircleCISourceStatuses, Unknown: []string{pipeline}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := circleCI(tc.node, tc.run)
			if got.Followed != tc.want.Followed || got.Pipeline != tc.want.Pipeline || got.Source != tc.want.Source || got.Error != tc.want.Error ||
				strings.Join(got.Unknown, ",") != strings.Join(tc.want.Unknown, ",") || !sameHead(got.Head, tc.want.Head) {
				t.Errorf("got %+v head=%+v\nwant %+v head=%+v", got, got.Head, tc.want, tc.want.Head)
			}
		})
	}
}

func sameHead(a, b *inventory.HeadStatus) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || (a.State == b.State && a.At.Equal(b.At) && strings.Join(a.Contexts, ",") == strings.Join(b.Contexts, ","))
}
