package collect

import (
	"fmt"
	"strings"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// The states a commit status carries, as the record keeps them.
const (
	statusFailure  = "failure"
	statusError    = "error"
	statusPending  = "pending"
	statusExpected = "expected"
)

// A commit's `ci/circleci:` statuses are per commit, not per pipeline: a
// branch pipeline that builds the same commit as the tag's posts its jobs'
// statuses beside the tag pipeline's, and the default branch head carries
// the tag pipeline's beside the branch's own. The declaration (CI.Jobs)
// tells them apart as far as a status can be told: a job the declaration
// never runs on the ref is another pipeline's. A job it runs on the ref and
// on other refs too could be either's — a status names the job, not the
// pipeline — which the release step reports rather than guesses.

// statusesReleaseStep is the release step from the tag commit's statuses,
// the stand-in for a repository whose tag pipeline the release watch cannot
// read (private, no CircleCI token). Contexts of jobs the declaration does
// not run on the tag are a branch pipeline's at the same commit and are
// ignored (backstage v2.58.8: build-image-amd64 and build-image-arm64 red
// beside a green tag pipeline). Of the rest, a failed context of a job that
// runs on the tag alone is the tag pipeline's failure; a failed context of
// a job that runs on branches too cannot be attributed while a branch
// pipeline's statuses are on the commit, and the release reads unchecked
// (tunnelport v1.6.7: go-build and go-test in error from a canceled branch
// pipeline six days after the tag). Without a branch pipeline's statuses
// every failure is the tag pipeline's, as before.
func statusesReleaseStep(slug, tag string, b *inventory.HeadStatus, ci *inventory.CI) reconcile.StepResult {
	sr := reconcile.StepResult{Step: reconcile.StepRelease}
	own, foreign := splitStatuses(b, ci, func(j inventory.CIJob) bool { return j.RunsOnTag(tag) })
	failed, pending := statesOf(b, own)
	count := fmt.Sprintf("%s, %s", plural(len(own), "job"), b.At.UTC().Format(time.RFC3339))
	ignored := ""
	if len(foreign) > 0 {
		ignored = fmt.Sprintf("; %s of a branch pipeline at the commit ignored", plural(len(foreign), "status"))
	}
	switch {
	case len(failed) > 0 && len(foreign) > 0 && !anyTagOnly(failed, ci, tag):
		sr.Verdict = reconcile.VerdictReported
		sr.Summary = fmt.Sprintf("release %s: two pipelines' statuses on its commit, %s failed (%s)", tag, jobNames(failed), count)
		sr.Findings = []reconcile.Finding{finding(reconcile.FindingUnchecked,
			fmt.Sprintf("whether CircleCI built the tag %s of %s is out of the inventory's reach: %s failed on the release commit, but a branch pipeline built the commit too and a commit status does not say whose job it was", tag, slug, jobNames(failed)),
			fmt.Sprintf("configure a CircleCI token (`circleci.existingSecret`) so the release watch reads the tag's own pipeline; `devctl release wait %s %s` tells now", slug, tag))}
	case len(failed) > 0:
		sr.Verdict = reconcile.VerdictReported
		sr.Summary = fmt.Sprintf("release %s: CircleCI failure in %s (%s)%s", tag, jobNames(failed), count, ignored)
		sr.Findings = []reconcile.Finding{finding(reconcile.FindingRedRelease,
			fmt.Sprintf("the tag pipeline of %s %s failed in %s", slug, tag, jobNames(failed)),
			fmt.Sprintf("nothing was published for %s: rerun the failed workflow from failed on CircleCI, then `devctl release wait %s %s` confirms the images and chart; when the cause is in the code, the next tag is the release", tag, slug, tag))}
	case len(pending) > 0:
		sr.Verdict = reconcile.VerdictOK
		sr.Summary = fmt.Sprintf("release %s: CircleCI pipeline running (%s)%s", tag, count, ignored)
	default:
		sr.Verdict = reconcile.VerdictOK
		sr.Summary = fmt.Sprintf("release %s built: CircleCI success (%s)%s", tag, count, ignored)
	}
	return sr
}

// splitStatuses divides a commit's statuses by the declaration: own are the
// contexts of jobs the declaration runs on the ref (runs) and of jobs it
// does not name, foreign the contexts of jobs it never runs on the ref.
// Without a declaration, or when it names none of the contexts' jobs as the
// ref's, every context is own: the declaration is no evidence then.
func splitStatuses(s *inventory.HeadStatus, ci *inventory.CI, runs func(inventory.CIJob) bool) (own, foreign []string) {
	jobs := jobIndex(ci)
	for _, c := range s.Contexts {
		if js, ok := jobs[inventory.JobOf(c)]; ok && !runsAny(js, runs) {
			foreign = append(foreign, c)
			continue
		}
		own = append(own, c)
	}
	if len(own) == 0 {
		return s.Contexts, nil
	}
	return own, foreign
}

// anyTagOnly says whether one of the contexts is a job's that the
// declaration runs on the tag and on no branch: its status is the tag
// pipeline's for certain.
func anyTagOnly(contexts []string, ci *inventory.CI, tag string) bool {
	jobs := jobIndex(ci)
	for _, c := range contexts {
		js, ok := jobs[inventory.JobOf(c)]
		if ok && runsAny(js, func(j inventory.CIJob) bool { return j.RunsOnTag(tag) }) && !runsAny(js, inventory.CIJob.RunsOnBranches) {
			return true
		}
	}
	return false
}

// statesOf are the failed and the pending contexts among own. A record read
// before the states were kept (Failed and Pending absent while State is not
// success) reads its State onto every own context, as the step did then.
func statesOf(s *inventory.HeadStatus, own []string) (failed, pending []string) {
	if len(s.Failed) == 0 && len(s.Pending) == 0 {
		switch s.State {
		case statusFailure, statusError:
			return own, nil
		case statusPending, statusExpected:
			return nil, own
		}
		return nil, nil
	}
	return among(s.Failed, own), among(s.Pending, own)
}

// jobIndex is the declaration's jobs by name; a name used by several
// workflows keeps every use.
func jobIndex(ci *inventory.CI) map[string][]inventory.CIJob {
	idx := map[string][]inventory.CIJob{}
	if ci == nil {
		return idx
	}
	for _, j := range ci.Jobs {
		idx[j.Name] = append(idx[j.Name], j)
	}
	return idx
}

func runsAny(jobs []inventory.CIJob, runs func(inventory.CIJob) bool) bool {
	for _, j := range jobs {
		if runs(j) {
			return true
		}
	}
	return false
}

// jobNames lists the contexts' jobs.
func jobNames(contexts []string) string {
	names := make([]string, 0, len(contexts))
	for _, c := range contexts {
		names = append(names, inventory.JobOf(c))
	}
	return strings.Join(names, ", ")
}

// among keeps the items of list that are in set, in list's order.
func among(list, set []string) []string {
	in := map[string]bool{}
	for _, s := range set {
		in[s] = true
	}
	var out []string
	for _, item := range list {
		if in[item] {
			out = append(out, item)
		}
	}
	return out
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if noun == "status" {
		return fmt.Sprintf("%d statuses", n)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
