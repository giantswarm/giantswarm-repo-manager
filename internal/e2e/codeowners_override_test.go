package e2e

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/collect"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// codeownersOverride is an align-files CODEOWNERS override: two teams, not
// the generated single-team file.
const codeownersOverride = "* @giantswarm/team-bumblebee @giantswarm/team-planeteers\n"

// A sweep lists the override directory once, at the commit it read the team
// files at, and every engine check carries its repository's CODEOWNERS
// override: the bytes when it has one, nil otherwise. Only a checked
// repository with an override costs a read; a refresh reuses the listing.
func TestEngineChecksCarryTheCodeownersOverride(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	st.ghs.files.commit(map[string][]byte{
		reposetup.CodeownersOverridePath(repoPresent): []byte(codeownersOverride),
		// An undeclared repository's override: never checked, never read.
		reposetup.CodeownersOverridePath(repoStray): []byte(codeownersOverride),
	})
	head := st.ghs.files.head()

	if _, err := st.col.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got, ok := st.checker.override(repoPresent); !ok || !bytes.Equal(got, []byte(codeownersOverride)) {
		t.Errorf("%s override: %q (checked %v), want %q", repoPresent, got, ok, codeownersOverride)
	}
	if got, ok := st.checker.override(repoLegacy); !ok || got != nil {
		t.Errorf("%s override: %q (checked %v), want nil", repoLegacy, got, ok)
	}
	for path, want := range map[string]int{
		reposetup.OverridesDir:                        1,
		reposetup.CodeownersOverridePath(repoPresent): 1,
		reposetup.CodeownersOverridePath(repoLegacy):  0,
		reposetup.CodeownersOverridePath(repoStray):   0,
	} {
		if n := st.ghs.files.readsOf(path, head); n != want {
			t.Errorf("reads of %s at %s after the sweep: %d, want %d", path, head, n, want)
		}
	}

	if _, err := st.col.Refresh(ctx, org+"/"+repoPresent, nil, inventory.SourceRefresh); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got, _ := st.checker.override(repoPresent); !bytes.Equal(got, []byte(codeownersOverride)) {
		t.Errorf("%s override after the refresh: %q, want %q", repoPresent, got, codeownersOverride)
	}
	if n := st.ghs.files.readsOf(reposetup.OverridesDir, head); n != 1 {
		t.Errorf("listings after the refresh: %d, want the sweep's one", n)
	}
	if n := st.ghs.files.readsOf(reposetup.CodeownersOverridePath(repoPresent), head); n != 2 {
		t.Errorf("override reads after the refresh: %d, want 2", n)
	}
}

// The engine's codeowners step compares the repository's CODEOWNERS with the
// request's override when it carries one, and with the generated file naming
// the team when it carries none.
func TestEngineComparesCodeownersWithTheRequestsOverride(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	repo := st.ghs.repos.add(repoPresent, alice)
	entry := st.acceptedEntry(t, repoPresent)
	engine := collect.NewEngine(st.app.Reader(), 0)

	for _, tc := range []struct {
		name, codeowners string
		override         []byte
		summary          string
	}{
		{name: "override", codeowners: codeownersOverride, override: []byte(codeownersOverride),
			summary: "matches the override " + reposetup.CodeownersOverridePath(repoPresent) + " of " + org + "/github"},
		{name: "generated", codeowners: reposetup.Codeowners(team), summary: "names @" + org + "/" + team},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st.ghs.repos.mu.Lock()
			repo.files["CODEOWNERS"] = tc.codeowners
			st.ghs.repos.mu.Unlock()
			res, err := engine.Check(ctx, reconcile.Request{Owner: org, Team: team, Entry: entry, Steps: []reconcile.Step{reconcile.StepCodeowners}, CodeownersOverride: tc.override})
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			s := res.Step(reconcile.StepCodeowners)
			if s == nil || s.Verdict != reconcile.VerdictOK || s.Summary != tc.summary || res.Mode != reconcile.ModeCheck {
				t.Errorf("codeowners step: %+v (mode %q), want ok %q", s, res.Mode, tc.summary)
			}
		})
	}
}

// acceptedEntry is the engine's verdict on the stack's team file entry name,
// as the sweep validates it.
func (st *stack) acceptedEntry(t *testing.T, name string) reposetup.Entry {
	t.Helper()
	tf, err := reposetup.ParseTeamFile(team, strings.NewReader(teamFile))
	if err != nil {
		t.Fatal(err)
	}
	res, err := reposetup.Validator{Schema: st.schema, Owner: org}.Validate(context.Background(), reposetup.Request{TeamFile: tf, Mode: reposetup.ModeExisting})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Entries {
		if e.Name == name && e.Accepted {
			return e
		}
	}
	t.Fatalf("%s is not an accepted entry of %s", name, team)
	return reposetup.Entry{}
}
