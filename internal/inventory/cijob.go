package inventory

import (
	"regexp"
	"strings"
)

// CircleCIContext is the prefix of the commit statuses CircleCI posts, one
// per job: `ci/circleci: <job>`.
const CircleCIContext = "ci/circleci:"

// JobOf is the job a `ci/circleci: <job>` status context names.
func JobOf(context string) string {
	return strings.TrimSpace(strings.TrimPrefix(context, CircleCIContext))
}

// CIJob is one job of the pipeline's workflows, under the name CircleCI
// posts its status with (`ci/circleci: <name>`: the job's `name` parameter,
// else the job's key), and the filters that decide which refs run it.
// CircleCI runs a job on every branch and on no tag unless the filters say
// otherwise: TagsOnly opens tags, BranchesOnly and BranchesIgnore narrow
// branches, TagsIgnore tags. A pattern is a ref name, or a regular
// expression between slashes as the configuration writes it (`/^v.*/`).
type CIJob struct {
	Name           string   `json:"name"`
	BranchesOnly   []string `json:"branchesOnly,omitempty"`
	BranchesIgnore []string `json:"branchesIgnore,omitempty"`
	TagsOnly       []string `json:"tagsOnly,omitempty"`
	TagsIgnore     []string `json:"tagsIgnore,omitempty"`
}

// RunsOnBranch says whether a pipeline of the branch runs the job.
func (j CIJob) RunsOnBranch(branch string) bool {
	if len(j.BranchesOnly) > 0 && !matchesAny(j.BranchesOnly, branch) {
		return false
	}
	return !matchesAny(j.BranchesIgnore, branch)
}

// RunsOnTag says whether a pipeline of the tag runs the job: no job runs on
// a tag without a `tags.only` filter.
func (j CIJob) RunsOnTag(tag string) bool {
	if len(j.TagsOnly) == 0 || !matchesAny(j.TagsOnly, tag) {
		return false
	}
	return !matchesAny(j.TagsIgnore, tag)
}

// RunsOnBranches says whether some branch pipeline runs the job: every job
// does, unless its `branches.ignore` is the catch-all the generated pipeline
// writes on a tag-only job (`/.*/`) and no `branches.only` opens some.
func (j CIJob) RunsOnBranches() bool {
	if len(j.BranchesOnly) > 0 {
		return true
	}
	for _, p := range j.BranchesIgnore {
		switch strings.TrimSpace(p) {
		case "/.*/", "/.+/", "/^.*$/", "/^.+$/":
			return false
		}
	}
	return true
}

// TagOnly are the names of the jobs the pipeline runs on tag and not on
// branch: a status of one of them on a commit both name — a release cut on
// the default branch head — can only be the tag pipeline's. Nil without a
// CI block or when every tag job runs on branch too.
func (ci *CI) TagOnly(tag, branch string) []string {
	if ci == nil {
		return nil
	}
	var out []string
	for _, j := range ci.Jobs {
		if j.RunsOnTag(tag) && !j.RunsOnBranch(branch) {
			out = append(out, j.Name)
		}
	}
	return out
}

// Reported says whether one of the `ci/circleci:` contexts names one of the
// jobs.
func Reported(contexts, jobs []string) bool {
	for _, c := range contexts {
		for _, j := range jobs {
			if JobOf(c) == j {
				return true
			}
		}
	}
	return false
}

// matchesAny says whether ref matches one of the patterns: a regular
// expression between slashes, else the ref's name. A pattern that does not
// compile matches nothing.
func matchesAny(patterns []string, ref string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && strings.HasPrefix(p, "/") && strings.HasSuffix(p, "/") {
			re, err := regexp.Compile(p[1 : len(p)-1])
			if err == nil && re.MatchString(ref) {
				return true
			}
			continue
		}
		if p == ref {
			return true
		}
	}
	return false
}
