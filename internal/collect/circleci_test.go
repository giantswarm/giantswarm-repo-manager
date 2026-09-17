package collect

import (
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// TestCircleCIDerivation: the record's CircleCI state from the head's
// statuses and the reconciler's circleci step — every combination names what
// it knows and what stays unknown, nothing is guessed.
func TestCircleCIDerivation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	head := func(truncated bool, contexts ...string) *repoNode {
		n := &repoNode{}
		n.DefaultBranchRef = &struct {
			Name   string        `json:"name"`
			Target *branchTarget `json:"target"`
		}{Name: "main", Target: &branchTarget{StatusCheckRollup: &statusRollup{State: "SUCCESS"}}}
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
	run := func(mode reconcile.Mode, verdict reconcile.Verdict, changes ...string) *inventory.LastRun {
		return &inventory.LastRun{Result: reconcile.Result{Mode: mode, Steps: []reconcile.StepResult{{Step: reconcile.StepCircleCI, Verdict: verdict, Summary: "the step's summary", Changes: changes}}}}
	}
	yes, no := true, false
	cases := []struct {
		name string
		node *repoNode
		run  *inventory.LastRun
		want inventory.CircleCI
	}{
		{"no statuses, no run", head(false, "sonar"), nil,
			inventory.CircleCI{Source: "statuses", Unknown: []string{"setupWorkflows"}}},
		{"statuses: the worst state, CircleCI's contexts only, the newest time", head(false, "ci/circleci: b!", "ci/circleci: a", "sonar!"), nil,
			inventory.CircleCI{Followed: true, Source: "statuses", Unknown: []string{"setupWorkflows"},
				Head: &inventory.HeadStatus{State: "failure", Contexts: []string{"ci/circleci: a", "ci/circleci: b"}, At: now.Add(time.Minute)}}},
		{"truncated without CircleCI: followed unknown", head(true, "sonar"), nil,
			inventory.CircleCI{Source: "statuses", Unknown: []string{"setupWorkflows", "followed"}}},
		{"run converged: followed, setup workflows on", head(false), run(reconcile.ModeCheck, reconcile.VerdictOK),
			inventory.CircleCI{Followed: true, SetupWorkflows: &yes, Source: "statuses+artifact"}},
		{"check run: follow planned, settings unread", head(false), run(reconcile.ModeCheck, reconcile.VerdictDrift, "follow giantswarm/x", "enable setup workflows", "create a deploy key"),
			inventory.CircleCI{Source: "statuses+artifact", Unknown: []string{"setupWorkflows"}}},
		{"check run: followed, setup workflows off", head(false), run(reconcile.ModeCheck, reconcile.VerdictDrift, "enable setup workflows"),
			inventory.CircleCI{Followed: true, SetupWorkflows: &no, Source: "statuses+artifact"}},
		{"check run: followed, only a key missing", head(false), run(reconcile.ModeCheck, reconcile.VerdictDrift, "create a deploy key"),
			inventory.CircleCI{Followed: true, SetupWorkflows: &yes, Source: "statuses+artifact"}},
		{"repair run: everything holds after it", head(false), run(reconcile.ModeRepair, reconcile.VerdictRepaired, "follow giantswarm/x", "enable setup workflows"),
			inventory.CircleCI{Followed: true, SetupWorkflows: &yes, Source: "statuses+artifact"}},
		{"failed step: the error, nothing known", head(false), run(reconcile.ModeRepair, reconcile.VerdictFailed),
			inventory.CircleCI{Source: "statuses+artifact", Unknown: []string{"setupWorkflows"}, Error: "the reconciler's circleci step failed: the step's summary"}},
		{"skipped step (no client in that run) counts as no run", head(false, "ci/circleci: a"), run(reconcile.ModeCheck, reconcile.VerdictSkipped),
			inventory.CircleCI{Followed: true, Source: "statuses", Unknown: []string{"setupWorkflows"},
				Head: &inventory.HeadStatus{State: "success", Contexts: []string{"ci/circleci: a"}, At: now}}},
		{"statuses beat a check run that planned the follow", head(false, "ci/circleci: a"), run(reconcile.ModeCheck, reconcile.VerdictDrift, "follow giantswarm/x"),
			inventory.CircleCI{Followed: true, Source: "statuses+artifact", Unknown: []string{"setupWorkflows"},
				Head: &inventory.HeadStatus{State: "success", Contexts: []string{"ci/circleci: a"}, At: now}}},
		{"no default branch", &repoNode{}, nil,
			inventory.CircleCI{Source: "statuses", Unknown: []string{"setupWorkflows"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := circleCI(tc.node, tc.run)
			if got.Followed != tc.want.Followed || got.Source != tc.want.Source || got.Error != tc.want.Error ||
				strings.Join(got.Unknown, ",") != strings.Join(tc.want.Unknown, ",") || !sameBool(got.SetupWorkflows, tc.want.SetupWorkflows) || !sameHead(got.Head, tc.want.Head) {
				t.Errorf("got %+v head=%+v setup=%v\nwant %+v head=%+v setup=%v", got, got.Head, deref(got.SetupWorkflows), tc.want, tc.want.Head, deref(tc.want.SetupWorkflows))
			}
		})
	}
}

func sameBool(a, b *bool) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func sameHead(a, b *inventory.HeadStatus) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || (a.State == b.State && a.At.Equal(b.At) && strings.Join(a.Contexts, ",") == strings.Join(b.Contexts, ","))
}

func deref(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}
