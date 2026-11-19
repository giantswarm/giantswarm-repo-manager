package collect

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/giantswarm/devctl/v8/pkg/circleciclient"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// TagPipeline is where a tag's own pipeline on CircleCI stands, read from
// the newest run of every workflow name (a rerun from failed is a second run
// of the same name; the one it replaced keeps its failed status): the rule
// the release watch settles a release by and watch_repository decides its
// released phase by. A failed workflow makes it red whatever still runs,
// unless only the job that publishes the GitHub release failed (releaseOnly);
// built once none failed, none is still moving and one at least succeeded;
// a workflow the pipeline's filters left out (not_run) is neither.
type TagPipeline struct {
	// Pipeline names the pipeline; its Workflow is the first failed
	// workflow's page, where the jobs and the rerun from failed are.
	Pipeline *inventory.ReleasePipeline
	// Workflows are the newest runs, `<name> (<status>)`, sorted by name.
	Workflows []string
	// Failed are the failed jobs of the failed workflows, `<job> (<how>)` —
	// the workflow itself when none of its jobs is a failure.
	Failed []string
	// Running says a workflow is still moving; Succeeded counts the green
	// ones.
	Running   bool
	Succeeded int
	// ByTargets says the workflows were read through the tag commit's
	// status target URLs (ReadTagPipelineByTargets), and Jobs are the jobs
	// of those workflows: the statuses the read saw.
	ByTargets bool
	Jobs      []string
}

// Unseen are the jobs of contexts, the `ci/circleci:` statuses on the tag's
// commit, that the declaration runs on tag and that no workflow the read
// through the targets saw has: a pipeline that posted to the commit after
// the tag's overwrote their statuses, so the read missed a workflow of the
// tag pipeline, a red one maybe. A job that never ran posts no status and
// is not asked for. Nil after a read of the pipeline's own workflow
// listing, and without a declaration: nothing to compare.
func (p *TagPipeline) Unseen(contexts []string, ci *inventory.CI, tag string) []string {
	if !p.ByTargets || ci == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, j := range p.Jobs {
		seen[j] = true
	}
	jobs := jobIndex(ci)
	var out []string
	for _, c := range contexts {
		job := inventory.JobOf(c)
		if !seen[job] && runsAny(jobs[job], func(j inventory.CIJob) bool { return j.RunsOnTag(tag) }) {
			out = append(out, job)
		}
	}
	return out
}

// Red says a workflow of the pipeline failed.
func (p *TagPipeline) Red() bool { return len(p.Failed) > 0 }

// Built says every workflow that ran succeeded and none is still moving.
func (p *TagPipeline) Built() bool { return !p.Red() && !p.Running && p.Succeeded > 0 }

// ReadTagPipeline reads the workflows of org/name's tag pipeline p, and the
// jobs of a failed one: one workflow listing, one job listing per failed
// workflow. The pipeline comes back named even when a read fails.
func ReadTagPipeline(ctx context.Context, cc *circleciclient.Client, org, name string, p *circleciclient.Pipeline) (*TagPipeline, error) {
	tp := newTagPipeline(org, name, p.Number)
	runs, err := cc.ListPipelineWorkflows(ctx, p.ID)
	if err != nil {
		return tp, fmt.Errorf("the workflows of pipeline %d: %w", p.Number, err)
	}
	return tp, tp.read(ctx, cc, org, name, runs, nil)
}

// ReadTagPipelineByTargets reads the workflows of org/name's tag pipeline
// number that the target URLs of the tag commit's CircleCI statuses lead to,
// by the rule of ReadTagPipeline. It is the read where the pipeline's
// workflow listing is out of reach: CircleCI answers that listing 404
// without a token, but answers a workflow and a workflow's jobs of a public
// project (a GitHub App pipeline's status names the workflow,
// `app.circleci.com/workflow/<id>/job/<id>`; an OAuth follow's names its
// job, `circleci.com/gh/<org>/<repo>/<job>`, which is read for its workflow
// where CircleCI answers it: without a token it answers 404), and it
// reads a pipeline no longer among the newest ones the lookup lists. A
// status names its job, not its pipeline: a workflow of another pipeline at
// the same commit (a branch's, a candidate tag's) is left out by its
// pipeline number. A workflow none of whose jobs has posted a status yet is
// not seen; no workflow at all leaves the TagPipeline without Workflows.
func ReadTagPipelineByTargets(ctx context.Context, cc *circleciclient.Client, org, name string, number int64, targets []string) (*TagPipeline, error) {
	tp := newTagPipeline(org, name, number)
	jobsOf := map[string][]circleciclient.Job{}
	ids, err := targetWorkflows(ctx, cc, org, name, number, targets, jobsOf)
	if err != nil {
		return tp, fmt.Errorf("the workflows of pipeline %d from the tag's statuses: %w", number, err)
	}
	var runs []circleciclient.Workflow
	for _, id := range ids {
		wd, err := cc.GetWorkflow(ctx, id)
		switch {
		case circleciclient.IsNotFound(err):
			continue
		case err != nil:
			return tp, fmt.Errorf("workflow %s of pipeline %d: %w", id, number, err)
		case wd.PipelineNumber == number:
			runs = append(runs, wd.Workflow)
		}
	}
	tp.ByTargets = true
	for _, run := range runs {
		jobs, ok := jobsOf[run.ID]
		if !ok {
			if jobs, err = cc.ListWorkflowJobs(ctx, run.ID); err != nil {
				return tp, fmt.Errorf("the jobs of workflow %s of pipeline %d: %w", run.Name, number, err)
			}
			jobsOf[run.ID] = jobs
		}
		for _, j := range jobs {
			tp.Jobs = append(tp.Jobs, j.Name)
		}
	}
	return tp, tp.read(ctx, cc, org, name, runs, jobsOf)
}

// targetWorkflows are the ids of the workflows the target URLs lead to, in
// the order first met: a workflow URL names its own; a pipeline URL of
// another pipeline than number is skipped; a job URL is read for its
// workflow, whose jobs are then listed so that the statuses of its other
// jobs are not read one by one. A job CircleCI does not know is skipped.
func targetWorkflows(ctx context.Context, cc *circleciclient.Client, org, name string, number int64, targets []string, jobsOf map[string][]circleciclient.Job) ([]string, error) {
	var ids []string
	seen, known := map[string]bool{}, map[int64]bool{}
	add := func(id string) bool {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
		ids = append(ids, id)
		return true
	}
	for _, raw := range targets {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if u.Host == "app.circleci.com" && len(parts) >= 2 && parts[0] == "workflow" {
			add(parts[1])
			continue
		}
		t, ok := circleciclient.ParseStatusTarget(raw)
		if !ok || t.Org != org || t.Repo != name {
			continue
		}
		if t.Pipeline > 0 {
			if t.Pipeline == number && len(parts) >= 7 && parts[5] == "workflows" {
				add(parts[6])
			}
			continue
		}
		if known[t.Job] {
			continue
		}
		job, err := cc.GetProjectJob(ctx, org, name, t.Job)
		switch {
		case circleciclient.IsNotFound(err):
			continue
		case err != nil:
			return nil, fmt.Errorf("job %d: %w", t.Job, err)
		}
		known[t.Job] = true
		if !add(job.LatestWorkflow.ID) {
			continue
		}
		jobs, err := cc.ListWorkflowJobs(ctx, job.LatestWorkflow.ID)
		if err != nil {
			return nil, fmt.Errorf("the jobs of workflow %s: %w", job.LatestWorkflow.ID, err)
		}
		jobsOf[job.LatestWorkflow.ID] = jobs
		for _, j := range jobs {
			known[j.JobNumber] = true
		}
	}
	return ids, nil
}

func newTagPipeline(org, name string, number int64) *TagPipeline {
	return &TagPipeline{Pipeline: &inventory.ReleasePipeline{Number: number, URL: circleciclient.PipelineURL(org, name, number)}}
}

// read reads runs, the workflows of the pipeline, into tp: the newest run of
// every name, and the jobs of a failed one; jobsOf holds the job listings
// already read, by workflow id (nil: none).
func (tp *TagPipeline) read(ctx context.Context, cc *circleciclient.Client, org, name string, runs []circleciclient.Workflow, jobsOf map[string][]circleciclient.Job) error {
	p := tp.Pipeline
	for _, run := range circleciclient.NewestWorkflows(runs) {
		tp.Workflows = append(tp.Workflows, run.Name+" ("+run.Status+")")
		switch {
		case circleciclient.WorkflowFailed(run.Status):
			jobs, ok := jobsOf[run.ID]
			var err error
			if !ok {
				jobs, err = cc.ListWorkflowJobs(ctx, run.ID)
			}
			if err != nil {
				return fmt.Errorf("the jobs of workflow %s of pipeline %d: %w", run.Name, p.Number, err)
			}
			if releaseOnly(jobs) {
				tp.Succeeded++
				continue
			}
			names := failedJobs(jobs)
			if len(names) == 0 {
				names = []string{run.Name + " (" + failureWord(run.Status) + ")"}
			}
			tp.Failed = append(tp.Failed, names...)
			if tp.Pipeline.Workflow == "" {
				tp.Pipeline.Workflow = circleciclient.WorkflowURL(org, name, p.Number, run.ID)
			}
		case circleciclient.WorkflowSucceeded(run.Status):
			tp.Succeeded++
		case run.Status == workflowNotRun:
		default:
			tp.Running = true
		}
	}
	return nil
}

// releaseOnly says whether a failed workflow's failed jobs all leave nothing
// unpublished by devctl's rule (reconcile.UnpublishedJobs): the job whose
// output is the GitHub release the watch follows. Such a workflow is green.
func releaseOnly(jobs []circleciclient.Job) bool {
	var red []string
	for _, j := range jobs {
		if _, ok := failedJobStatuses[j.Status]; ok {
			red = append(red, j.Name)
		}
	}
	return len(red) > 0 && len(reconcile.UnpublishedJobs(red)) == 0
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

// runsAny says whether runs holds for one of jobs.
func runsAny(jobs []inventory.CIJob, runs func(inventory.CIJob) bool) bool {
	for _, j := range jobs {
		if runs(j) {
			return true
		}
	}
	return false
}
