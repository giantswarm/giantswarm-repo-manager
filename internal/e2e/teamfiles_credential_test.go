package e2e

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
	"github.com/giantswarm/giantswarm-repo-manager/internal/tools"
)

// TestTeamFileReadNamesTheCredential (#11): GitHub answers 404 both for a
// file that is missing and for a repository the person's credential does not
// reach. The tool's error tells them apart — the credential and the fix for
// the one, the plain not-found for the other — and get_info says beforehand
// whether the caller's credential reaches the team files.
func TestTeamFileReadNamesTheCredential(t *testing.T) {
	st := newStack(t)
	st.ghs.files.deny(dave)

	// dave's authorization of the App does not reach giantswarm/github.
	asDave := st.as(t, daveToken)
	if tf := getInfo(t, asDave).TeamFiles; tf.Readable != tools.ReadableFalse || !strings.Contains(tf.Reason, "your authorization of the App "+teamfiles.WriteApp+" does not reach "+org+"/github") {
		t.Errorf("get_info as dave: %+v", tf)
	}
	text, isErr := call(t, asDave, tools.ToolSetLifecycle, map[string]any{argDryRun: true, kRepository: repoPresent, argLifecycle: "archived"})
	want := "reading " + org + "/github as " + dave + " failed (404): the repository is not reachable with this credential — your authorization of the App " + teamfiles.WriteApp + " does not reach the repository: the App must be installed on all repositories (an org owner's setting), or your own access does not include it"
	if !isErr || !strings.Contains(text, want) {
		t.Errorf("read as dave: isErr=%v\n%s\nwant: %s", isErr, text, want)
	}
	if strings.Contains(text, "not found") {
		t.Errorf("a credential that does not reach the repository must not read as a missing file:\n%s", text)
	}

	// alice reaches the repository; a team without a file is a missing file.
	asAlice := st.as(t, aliceToken)
	if tf := getInfo(t, asAlice).TeamFiles; tf.Readable != tools.ReadableTrue {
		t.Errorf("get_info as alice: %+v", tf)
	}
	text, isErr = call(t, asAlice, tools.ToolTransferRepository, map[string]any{argDryRun: true, kRepository: repoPresent, argToTeam: "team-nobody"})
	if !isErr || !strings.Contains(text, org+"/github: repositories/team-nobody.yaml not found in "+mainBranch) || strings.Contains(text, "authorization of the App") {
		t.Errorf("missing file as alice: isErr=%v\n%s", isErr, text)
	}
}
