package collect

import (
	"testing"
	"time"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// TestAlignFilesPullRequest: the latest pull request from align-files'
// branch counts only when align-files' App opened it, carries its state in
// the record's words with the times each state has, and the date of the last
// commit align-files pushed; a state GitHub did not have is left out.
func TestAlignFilesPullRequest(t *testing.T) {
	created := time.Date(2026, 10, 19, 8, 30, 0, 0, time.UTC)
	pushed, closed := created.Add(time.Hour), created.Add(2*time.Hour)
	node := func(state, login string, closedAt, mergedAt *time.Time) alignFilesNode {
		n := alignFilesNode{Number: 7, URL: "https://github.com/giantswarm/pagekit/pull/7", State: state, CreatedAt: created, ClosedAt: closedAt, MergedAt: mergedAt}
		n.Author = &struct {
			Login string `json:"login"`
		}{Login: login}
		n.Commits.Nodes = append(n.Commits.Nodes, struct {
			Commit struct {
				CommittedDate time.Time `json:"committedDate"`
			} `json:"commit"`
		}{})
		n.Commits.Nodes[0].Commit.CommittedDate = pushed
		return n
	}
	for _, tc := range []struct {
		name string
		node alignFilesNode
		want string
	}{
		{"open", node("OPEN", alignFilesLogin, nil, nil), inventory.PullRequestOpen},
		{"merged", node("MERGED", alignFilesLogin, &closed, &closed), inventory.PullRequestMerged},
		{"closed by a run that changed nothing", node("CLOSED", alignFilesLogin, &closed, nil), inventory.PullRequestClosed},
		{"a person's branch of the same name", node("OPEN", alice, nil, nil), ""},
		{"a state GitHub did not have", node("DRAFTED", alignFilesLogin, nil, nil), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr := alignFilesPullRequest(tc.node)
			if tc.want == "" {
				if pr != nil {
					t.Fatalf("want none, got %+v", pr)
				}
				return
			}
			if pr == nil || pr.State != tc.want || pr.Number != 7 || !pr.CreatedAt.Equal(created) || pr.LastCommitAt == nil || !pr.LastCommitAt.Equal(pushed) {
				t.Fatalf("got %+v", pr)
			}
			if (pr.ClosedAt != nil) != (tc.want != inventory.PullRequestOpen) || (pr.MergedAt != nil) != (tc.want == inventory.PullRequestMerged) {
				t.Errorf("times: closed %v merged %v", pr.ClosedAt, pr.MergedAt)
			}
		})
	}
	if pr := alignFilesPullRequest(alignFilesNode{Number: 1, State: "OPEN"}); pr != nil {
		t.Errorf("no author: %+v", pr)
	}
}
