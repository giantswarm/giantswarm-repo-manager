package collect

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// Test constants of this package's CircleCI derivations.
const (
	stateSuccess         = "success"
	sourceStatusesEngine = inventory.CircleCISourceStatuses + "+" + inventory.CircleCISourceEngine
)

// TestEngineCircleCI: the engine's own circleci step — real when the runner
// has a CircleCI client — is the third source of the record's CircleCI
// state: what it says stands, the facts it yields leave Unknown, and a
// skipped step (no client) adds nothing.
func TestEngineCircleCI(t *testing.T) {
	head := &inventory.HeadStatus{State: stateSuccess, Contexts: []string{"ci/circleci: go-build"}}
	yes, no := true, false
	fromStatuses := func() *inventory.CircleCI {
		return &inventory.CircleCI{Followed: true, Head: head, Source: inventory.CircleCISourceStatuses, Unknown: []string{inventory.CircleCIFactSetupWorkflows}}
	}
	bare := func() *inventory.CircleCI {
		return &inventory.CircleCI{Source: inventory.CircleCISourceStatuses, Unknown: []string{inventory.CircleCIFactSetupWorkflows, inventory.CircleCIFactFollowed}}
	}
	res := func(verdict reconcile.Verdict, summary string, changes ...string) *reconcile.Result {
		return &reconcile.Result{Mode: reconcile.ModeCheck, Steps: []reconcile.StepResult{{Step: reconcile.StepCircleCI, Verdict: verdict, Summary: summary, Changes: changes}}}
	}
	cases := []struct {
		name string
		in   *inventory.CircleCI
		res  *reconcile.Result
		want inventory.CircleCI
	}{
		{"converged: followed, setup workflows on, nothing unknown", fromStatuses(), res(reconcile.VerdictOK, "followed, setup workflows on, checkout key present"),
			inventory.CircleCI{Followed: true, SetupWorkflows: &yes, Head: head, Source: sourceStatusesEngine}},
		{"drift: setup workflows off", fromStatuses(), res(reconcile.VerdictDrift, "", "enable setup workflows"),
			inventory.CircleCI{Followed: true, SetupWorkflows: &no, Head: head, Source: sourceStatusesEngine}},
		{"drift: not followed — the head's statuses decide followed, settings unread", bare(), res(reconcile.VerdictDrift, "", "follow giantswarm/x", "enable setup workflows", "create a deploy key"),
			inventory.CircleCI{Source: sourceStatusesEngine, Unknown: []string{inventory.CircleCIFactSetupWorkflows}}},
		{"failed: the engine's error", fromStatuses(), res(reconcile.VerdictFailed, "api error: 502"),
			inventory.CircleCI{Followed: true, Head: head, Source: sourceStatusesEngine, Error: "the engine's circleci step failed: api error: 502", Unknown: []string{inventory.CircleCIFactSetupWorkflows}}},
		{"skipped (no client): nothing added", fromStatuses(), res(reconcile.VerdictSkipped, "no CircleCI client"),
			*fromStatuses()},
		{"no circleci step: nothing added", fromStatuses(), &reconcile.Result{Steps: []reconcile.StepResult{{Step: reconcile.StepSettings, Verdict: reconcile.VerdictOK}}},
			*fromStatuses()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engineCircleCI(tc.in, tc.res)
			if !reflect.DeepEqual(*tc.in, tc.want) {
				t.Errorf("got %s\nwant %s", describe(*tc.in), describe(tc.want))
			}
		})
	}
	engineCircleCI(nil, res(reconcile.VerdictOK, "")) // a gone repository has no state to add to
	engineCircleCI(fromStatuses(), nil)
}

func describe(c inventory.CircleCI) string {
	sw := "nil"
	if c.SetupWorkflows != nil {
		sw = strconv.FormatBool(*c.SetupWorkflows)
	}
	return fmt.Sprintf("{followed %v setupWorkflows %s source %s unknown %v error %q}", c.Followed, sw, c.Source, c.Unknown, c.Error)
}
