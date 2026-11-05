package collect

import (
	"sort"
	"strings"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// statusRank orders GitHub's status states worst first, for the head's
// summary state.
var statusRank = map[string]int{"FAILURE": 0, "ERROR": 1, "PENDING": 2, "EXPECTED": 3, "SUCCESS": 4}

// circleCI derives the record's CircleCI state without a CircleCI token: the
// `ci/circleci:` statuses on the default branch head (read with the
// repository, no extra call) say whether CircleCI builds the repository; the
// reconciler's last run — its circleci step, in the run artifact the poller
// read — says which pipeline builds it, read from who posted the CircleCI
// statuses of the recent commits (devctl pkg/reposetup/reconcile,
// circleCIFromStatuses): the CircleCI GitHub App, an OAuth follow, both, or
// none. What neither yields is named in Unknown, never guessed.
func circleCI(n *repoNode, run *inventory.LastRun) *inventory.CircleCI {
	out := &inventory.CircleCI{Source: inventory.CircleCISourceStatuses}
	head, truncated := headStatus(n)
	out.Head = head
	out.Followed = head != nil
	if run != nil {
		if sr := run.Result.Step(reconcile.StepCircleCI); sr != nil && sr.Verdict != reconcile.VerdictSkipped {
			out.Source = inventory.CircleCISourceBoth
			applyStep(out, sr)
		}
	}
	if out.Pipeline == "" {
		out.Unknown = append(out.Unknown, inventory.CircleCIFactPipeline)
	}
	if head == nil && truncated && out.Pipeline == "" {
		out.Unknown = append(out.Unknown, inventory.CircleCIFactFollowed)
	}
	return out
}

// applyStep reads the reconciler's circleci step into out: the pipeline its
// findings name — circleci-pipeline-missing none, circleci-oauth-follow an
// OAuth follow, beside the GitHub App pipeline unless the engine says there
// is none — a step without either the GitHub App pipeline, and the error of
// a failed step. A step that could not read the statuses (finding
// unchecked) leaves the pipeline unknown.
func applyStep(out *inventory.CircleCI, sr *reconcile.StepResult) {
	if sr.Verdict == reconcile.VerdictFailed {
		out.Error = "the reconciler's circleci step failed: " + sr.Summary
		return
	}
	pipeline := inventory.CircleCIPipelineApp
	for _, f := range sr.Findings {
		switch f.Kind {
		case reconcile.FindingUnchecked:
			return
		case reconcile.FindingCircleCIPipelineMissing:
			pipeline = inventory.CircleCIPipelineNone
		case reconcile.FindingCircleCIOAuthFollow:
			pipeline = inventory.CircleCIPipelineAppAndOAuth
			if strings.HasPrefix(sr.Summary, oauthOnly) {
				pipeline = inventory.CircleCIPipelineOAuth
			}
		}
	}
	out.Pipeline = pipeline
	if pipeline != inventory.CircleCIPipelineNone {
		out.Followed = true
	}
}

// refineCircleCI re-derives the record's pipeline from the live check's
// circleci step: the engine ran it in this refresh or sweep, reading the
// same statuses the reconciler's run read, but now — after a person created
// the GitHub App pipeline, the record says so before the next nightly run.
// A step the check skipped, failed or could not read (finding unchecked)
// leaves what the run said.
func refineCircleCI(cc *inventory.CircleCI, checks *reconcile.Result) {
	if cc == nil || checks == nil {
		return
	}
	sr := checks.Step(reconcile.StepCircleCI)
	if sr == nil || sr.Verdict == reconcile.VerdictSkipped || sr.Verdict == reconcile.VerdictFailed {
		return
	}
	live := &inventory.CircleCI{}
	applyStep(live, sr)
	if live.Pipeline == "" {
		return
	}
	cc.Pipeline = live.Pipeline
	cc.Followed = cc.Head != nil || live.Followed
	unknown := cc.Unknown[:0]
	for _, u := range cc.Unknown {
		if u != inventory.CircleCIFactPipeline && u != inventory.CircleCIFactFollowed {
			unknown = append(unknown, u)
		}
	}
	cc.Unknown = unknown
	if len(cc.Unknown) == 0 {
		cc.Unknown = nil
	}
}

// oauthOnly is how the engine's circleci step starts its summary when an
// OAuth follow builds the repository and no GitHub App pipeline does (devctl
// pkg/reposetup/reconcile, step_circleci.go).
const oauthOnly = "OAuth follow, no GitHub App pipeline"

// headStatus is the default branch head's `ci/circleci:` statuses, nil when it
// has none; truncated says the status connection had more contexts than were
// read.
func headStatus(n *repoNode) (head *inventory.HeadStatus, truncated bool) {
	if n == nil || n.DefaultBranchRef == nil || n.DefaultBranchRef.Target == nil {
		return nil, false
	}
	return rollupStatus(n.DefaultBranchRef.Target.StatusCheckRollup)
}

// releaseBuild is a release's tag commit's `ci/circleci:` statuses — whether
// CircleCI built the tag — nil when the commit carries none; truncated says
// the contexts were more than were read and none of the read ones was
// CircleCI's.
func releaseBuild(tagCommit *tagCommitNode) (*inventory.HeadStatus, bool) {
	if tagCommit == nil {
		return nil, false
	}
	return rollupStatus(tagCommit.StatusCheckRollup)
}

// rollupStatus is a commit's `ci/circleci:` statuses out of its status
// rollup — the worst state, the contexts sorted, the newest time — nil when
// it carries none; truncated says the connection had more contexts than were
// read.
func rollupStatus(rollup *statusRollup) (*inventory.HeadStatus, bool) {
	if rollup == nil {
		return nil, false
	}
	worst, at := "", time.Time{}
	var contexts, failed, pending []string
	for _, c := range rollup.Contexts.Nodes {
		if !strings.HasPrefix(c.Context, inventory.CircleCIContext) {
			continue
		}
		contexts = append(contexts, c.Context)
		switch c.State {
		case "FAILURE", "ERROR":
			failed = append(failed, c.Context)
		case "PENDING", "EXPECTED":
			pending = append(pending, c.Context)
		}
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
	sort.Strings(failed)
	sort.Strings(pending)
	return &inventory.HeadStatus{State: strings.ToLower(worst), Contexts: contexts, Failed: failed, Pending: pending, At: at}, rollup.Contexts.PageInfo.HasNextPage
}

func ptr[T any](v T) *T { return &v }
