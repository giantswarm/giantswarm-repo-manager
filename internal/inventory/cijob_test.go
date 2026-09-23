package inventory

import "testing"

const (
	vTags      = "/^v.*/"
	mainBranch = "main"
	release    = "v1.0.0"
)

// TestCIJobRuns: the refs a job runs on, from its filters as the generated
// pipeline writes them — a job without filters on every branch and no tag, a
// tag job on the tags its pattern names and no branch, a branch job on every
// branch but the ones it ignores.
func TestCIJobRuns(t *testing.T) {
	plain := CIJob{Name: "go-build"}
	shared := CIJob{Name: "node-build", TagsOnly: []string{vTags}}
	tagOnly := CIJob{Name: "push-to-registries-release", TagsOnly: []string{vTags}, BranchesIgnore: []string{"/.*/"}}
	branchOnly := CIJob{Name: "build-image-amd64", BranchesIgnore: []string{mainBranch}}
	mainOnly := CIJob{Name: "deploy", BranchesOnly: []string{mainBranch, "/^release-.*/"}}
	rcFree := CIJob{Name: "publish", TagsOnly: []string{vTags}, TagsIgnore: []string{"/-rc\\./"}}
	broken := CIJob{Name: "odd", TagsOnly: []string{"/(/"}}

	cases := []struct {
		name          string
		job           CIJob
		branch, tag   string
		onBranch      bool
		onTag         bool
		onAnyBranches bool
	}{
		{"no filters: every branch, no tag", plain, "feature/x", release, true, false, true},
		{"tags.only opens the tag, branches stay", shared, mainBranch, release, true, true, true},
		{"tags.only misses a foreign tag", shared, mainBranch, "api/v1.0.0", true, false, true},
		{"tag-only job: the tag, no branch", tagOnly, mainBranch, release, false, true, false},
		{"branch job on a feature branch", branchOnly, "changesets-ghcommit-temp/changeset-release/main", release, true, false, true},
		{"branch job ignores main", branchOnly, mainBranch, "", false, false, true},
		{"branches.only by name", mainOnly, mainBranch, "", true, false, true},
		{"branches.only by pattern", mainOnly, "release-1.x", "", true, false, true},
		{"branches.only misses the rest", mainOnly, "feature/x", "", false, false, true},
		{"tags.ignore takes the rc out", rcFree, "", "v1.0.0-rc.1", true, false, true},
		{"tags.ignore leaves the release", rcFree, "", release, true, true, true},
		{"a pattern that does not compile matches nothing", broken, mainBranch, release, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.job.RunsOnBranch(tc.branch); got != tc.onBranch {
				t.Errorf("RunsOnBranch(%q) = %v, want %v", tc.branch, got, tc.onBranch)
			}
			if got := tc.job.RunsOnTag(tc.tag); got != tc.onTag {
				t.Errorf("RunsOnTag(%q) = %v, want %v", tc.tag, got, tc.onTag)
			}
			if got := tc.job.RunsOnBranches(); got != tc.onAnyBranches {
				t.Errorf("RunsOnBranches() = %v, want %v", got, tc.onAnyBranches)
			}
		})
	}
}
