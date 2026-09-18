package tools

import (
	"errors"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

// testAuthor opened the pull request under test.
const testAuthor = "alice"

// The approval of a team-file pull request is a member's to give — never the
// author's, whatever their teams: GitHub refuses an author's own review, and
// the refusal here says so in words the asker can act on.
func TestApprovalByRefusesTheAuthorFirst(t *testing.T) {
	d := decision{repo: teamfiles.Repo{Owner: "giantswarm", Name: "github", Ref: "main"}, number: 6119, team: testTeam, author: testAuthor}

	_, err := d.approvalBy(&person{login: "Alice", teams: []string{testTeam}})
	if !errors.Is(err, ErrOwnPullRequest) {
		t.Fatalf("author (any case): want ErrOwnPullRequest, got %v", err)
	}
	for _, want := range []string{"Alice opened giantswarm/github#6119", "another member of " + testTeam + " has to approve"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "@main") {
		t.Errorf("refusal %q names the ref, which a person does not read", err)
	}

	_, err = d.approvalBy(&person{login: "bob", teams: []string{"team-other"}})
	if !errors.Is(err, ErrNotAMember) {
		t.Fatalf("non-member: want ErrNotAMember, got %v", err)
	}
	if !strings.Contains(err.Error(), "the review of giantswarm/github#6119 is not yours to give") {
		t.Errorf("non-member refusal %q lacks the consequence", err)
	}

	a, err := d.approvalBy(&person{login: "carol", teams: []string{"team-other", testTeam}})
	if err != nil {
		t.Fatalf("member: %v", err)
	}
	if !a.Member || a.Author != testAuthor || a.Team != testTeam || a.PullRequest != 6119 || a.Login != "carol" {
		t.Errorf("member's approval: %+v", a)
	}
}

// An ask names who decides it and reads its reason as one.
func TestAskNamesWhoDecides(t *testing.T) {
	if got, want := decides(testTeam, testAuthor), " A member of team-bumblebee other than alice approves."; got != want {
		t.Errorf("decides: %q, want %q", got, want)
	}
	if got, want := reasonSuffix("  just a test repo "), " Reason: just a test repo."; got != want {
		t.Errorf("reasonSuffix: %q, want %q", got, want)
	}
	if got, want := reasonSuffix("superseded by the new service."), " Reason: superseded by the new service."; got != want {
		t.Errorf("reasonSuffix(sentence): %q, want %q", got, want)
	}
	if got := reasonSuffix("  "); got != "" {
		t.Errorf("reasonSuffix(blank): %q, want empty", got)
	}
}
