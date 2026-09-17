package collect

import (
	"sort"
	"strings"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// circleContext is the prefix of the commit statuses CircleCI posts, one per
// job: `ci/circleci: <job>`.
const circleContext = "ci/circleci:"

// statusRank orders GitHub's status states worst first, for the head's
// summary state.
var statusRank = map[string]int{"FAILURE": 0, "ERROR": 1, "PENDING": 2, "EXPECTED": 3, "SUCCESS": 4}

// circleCI derives the record's CircleCI state without a CircleCI token: the
// `ci/circleci:` statuses on the default branch head (read with the
// repository, no extra call) say whether CircleCI builds the repository; the
// reconciler's last run — its circleci step, in the run artifact posted to
// /internal/refresh — says whether the project is followed and setup
// workflows are on. What neither yields is named in Unknown, never guessed.
func circleCI(n *repoNode, run *inventory.LastRun) *inventory.CircleCI {
	out := &inventory.CircleCI{Source: inventory.CircleCISourceStatuses}
	head, truncated := headStatus(n)
	out.Head = head
	out.Followed = head != nil
	setup := false
	if run != nil {
		if sr := run.Result.Step(reconcile.StepCircleCI); sr != nil && sr.Verdict != reconcile.VerdictSkipped {
			out.Source = inventory.CircleCISourceBoth
			setup = applyStep(out, run.Result.Mode, sr)
		}
	}
	if !setup {
		out.Unknown = append(out.Unknown, inventory.CircleCIFactSetupWorkflows)
	}
	if head == nil && truncated && out.Source == inventory.CircleCISourceStatuses {
		out.Unknown = append(out.Unknown, inventory.CircleCIFactFollowed)
	}
	return out
}

// applyStep reads the reconciler's circleci step: the summary of a converged
// step, the changes a check run would make or a repair made, the error of a
// failed one. It returns whether the setup-workflows setting is known.
func applyStep(out *inventory.CircleCI, mode reconcile.Mode, sr *reconcile.StepResult) bool {
	switch sr.Verdict {
	case reconcile.VerdictFailed:
		out.Error = "the reconciler's circleci step failed: " + sr.Summary
		return false
	case reconcile.VerdictOK, reconcile.VerdictReported:
		out.Followed = true
		out.SetupWorkflows = ptr(true)
		return true
	}
	// Drift (a check run) or repaired: the changes name what was missing.
	follow := hasChange(sr, "follow ")
	enable := hasChange(sr, "enable setup workflows")
	if mode == reconcile.ModeRepair {
		// The step follows, then reads the settings and enables what is off:
		// after the run both hold.
		out.Followed = true
		out.SetupWorkflows = ptr(true)
		return true
	}
	if follow {
		// Not followed at the run: the settings do not exist yet, the step
		// plans them without reading anything.
		out.Followed = out.Head != nil
		return false
	}
	out.Followed = true
	out.SetupWorkflows = ptr(!enable)
	return true
}

func hasChange(sr *reconcile.StepResult, prefix string) bool {
	for _, c := range sr.Changes {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// headStatus is the head's `ci/circleci:` statuses, nil when it has none;
// truncated says the status connection had more contexts than were read.
func headStatus(n *repoNode) (head *inventory.HeadStatus, truncated bool) {
	if n == nil || n.DefaultBranchRef == nil || n.DefaultBranchRef.Target == nil || n.DefaultBranchRef.Target.StatusCheckRollup == nil {
		return nil, false
	}
	rollup := n.DefaultBranchRef.Target.StatusCheckRollup
	worst, at := "", time.Time{}
	var contexts []string
	for _, c := range rollup.Contexts.Nodes {
		if !strings.HasPrefix(c.Context, circleContext) {
			continue
		}
		contexts = append(contexts, c.Context)
		if worst == "" || statusRank[c.State] < statusRank[worst] {
			worst = c.State
		}
		if c.CreatedAt.After(at) {
			at = c.CreatedAt
		}
	}
	if len(contexts) == 0 {
		return nil, rollup.Contexts.PageInfo.HasNextPage
	}
	sort.Strings(contexts)
	return &inventory.HeadStatus{State: strings.ToLower(worst), Contexts: contexts, At: at}, rollup.Contexts.PageInfo.HasNextPage
}

func ptr[T any](v T) *T { return &v }
