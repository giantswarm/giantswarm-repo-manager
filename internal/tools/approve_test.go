package tools

import (
	"errors"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

// The approval's sentence for the channel says what became of the pull
// request: merged, left to auto-merge, or neither and why.
func TestApprovalOutcomeIsOneSentence(t *testing.T) {
	d := decision{repo: teamfiles.Repo{Owner: "giantswarm", Name: "github", Ref: "main"}, number: 6119, team: testTeam, author: testAuthor}
	for want, l := range map[string]teamfiles.Landing{
		"Approved as carol and merged: giantswarm/github#6119.":                                                                              {Merged: true},
		"Approved as carol; giantswarm/github#6119 merges by itself once its checks pass.":                                                   {AutoMerge: true},
		"Approved as carol; giantswarm/github#6119 is not merged: merging giantswarm/github#6119 failed: 403 Forbidden. Merge it on GitHub.": {Reason: "merging giantswarm/github#6119 failed: 403 Forbidden."},
	} {
		if got := d.outcome("carol", l, nil, ""); got != want {
			t.Errorf("outcome(%+v):\n got %q\nwant %q", l, got, want)
		}
	}
	// A pull request its base moved under says it was re-rendered first; one
	// that could not be says why it waits.
	rr := &teamfiles.Rerendered{Number: 6119, Branch: "reposetup/archived-x"}
	for want, l := range map[string]teamfiles.Landing{
		"Approved as carol and merged: giantswarm/github#6119, re-rendered on main first (a neighbouring entry had changed).":                             {Merged: true},
		"Approved as carol; giantswarm/github#6119, re-rendered on main first (a neighbouring entry had changed), merges by itself once its checks pass.": {AutoMerge: true},
	} {
		if got := d.outcome("carol", l, rr, ""); got != want {
			t.Errorf("outcome(%+v, re-rendered):\n got %q\nwant %q", l, got, want)
		}
	}
	want := "Approved as carol; giantswarm/github#6119 is not merged: it conflicts with main (a neighbouring entry changed first) and re-rendering it failed: the pull request changes a file that is not a team file: giantswarm/github#6119 changes README.md. Rebase it on GitHub."
	if got := d.outcome("carol", teamfiles.Landing{Reason: "merging failed: 405"}, nil, "the pull request changes a file that is not a team file: giantswarm/github#6119 changes README.md"); got != want {
		t.Errorf("outcome(re-render failed):\n got %q\nwant %q", got, want)
	}
}

// The deciding team is read from the marker every pull request of this server
// carries; a body without one names none.
func TestTeamFromMarker(t *testing.T) {
	if got := teamFromMarker("## Problem\n\nx\n\n<!-- giantswarm-repo-manager: team=team-bumblebee -->\n"); got != testTeam {
		t.Errorf("marker: %q", got)
	}
	if got := teamFromMarker("a body by hand"); got != "" {
		t.Errorf("no marker: %q", got)
	}
}

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
