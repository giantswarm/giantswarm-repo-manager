package collect

import (
	"context"
	"fmt"

	"github.com/giantswarm/devctl/v8/pkg/circleciclient"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// TagPipeline is where a tag's own pipeline on CircleCI stands, read from
// the newest run of every workflow name (a rerun from failed is a second run
// of the same name; the one it replaced keeps its failed status): the rule
// the release watch settles a release by and watch_repository decides its
// released phase by. A failed workflow makes it red whatever still runs;
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
}

// Red says a workflow of the pipeline failed.
func (p *TagPipeline) Red() bool { return len(p.Failed) > 0 }

// Built says every workflow that ran succeeded and none is still moving.
func (p *TagPipeline) Built() bool { return !p.Red() && !p.Running && p.Succeeded > 0 }

// ReadTagPipeline reads the workflows of org/name's tag pipeline p, and the
// jobs of a failed one: one workflow listing, one job listing per failed
// workflow. The pipeline comes back named even when a read fails.
func ReadTagPipeline(ctx context.Context, cc *circleciclient.Client, org, name string, p *circleciclient.Pipeline) (*TagPipeline, error) {
	tp := &TagPipeline{Pipeline: &inventory.ReleasePipeline{Number: p.Number, URL: circleciclient.PipelineURL(org, name, p.Number)}}
	runs, err := cc.ListPipelineWorkflows(ctx, p.ID)
	if err != nil {
		return tp, fmt.Errorf("the workflows of pipeline %d: %w", p.Number, err)
	}
	for _, run := range circleciclient.NewestWorkflows(runs) {
		tp.Workflows = append(tp.Workflows, run.Name+" ("+run.Status+")")
		switch {
		case circleciclient.WorkflowFailed(run.Status):
			jobs, err := cc.ListWorkflowJobs(ctx, run.ID)
			if err != nil {
				return tp, fmt.Errorf("the jobs of workflow %s of pipeline %d: %w", run.Name, p.Number, err)
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
	return tp, nil
}
