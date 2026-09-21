package tools

import (
	"errors"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

// strayTool is the undeclared repository the tests adopt; unspecified is the
// catalog type an adoption starts from.
const (
	strayTool   = "stray-tool"
	unspecified = "unspecified"
)

// TestAdoptedEntryShapesTheDeclaration: the repository is the entry's name,
// another name is refused, a lifecycle that ends the repository's life
// brings the opt-in with it, and a deletion is refused.
func TestAdoptedEntryShapesTheDeclaration(t *testing.T) {
	d, optedIn, err := adoptedEntry(strayTool, map[string]any{teamfiles.FieldComponentType: unspecified, "description": "a stray"})
	if err != nil {
		t.Fatal(err)
	}
	y, _ := d.YAML()
	if d.Name != strayTool || optedIn || !strings.Contains(y, "name: stray-tool") || strings.Contains(y, "align") {
		t.Errorf("plain adoption: optedIn=%v\n%s", optedIn, y)
	}

	if _, _, err := adoptedEntry(strayTool, map[string]any{teamfiles.FieldName: "other"}); err == nil || !strings.Contains(err.Error(), `"other" is not stray-tool`) {
		t.Errorf("another name: %v", err)
	}

	d, optedIn, err = adoptedEntry(strayTool, map[string]any{teamfiles.FieldComponentType: unspecified, teamfiles.FieldLifecycle: teamfiles.LifecycleArchived})
	if err != nil {
		t.Fatal(err)
	}
	y, _ = d.YAML()
	if !optedIn || !strings.Contains(y, "lifecycle: archived") || !strings.Contains(y, "align: true") {
		t.Errorf("archived brings the opt-in: optedIn=%v\n%s", optedIn, y)
	}

	d, optedIn, err = adoptedEntry(strayTool, map[string]any{teamfiles.FieldComponentType: unspecified, teamfiles.FieldLifecycle: teamfiles.LifecycleDeprecated, teamfiles.FieldAlign: true})
	if err != nil {
		t.Fatal(err)
	}
	if y, _ = d.YAML(); optedIn || !strings.Contains(y, "align: true") {
		t.Errorf("an opted-in entry keeps its field and adds none: optedIn=%v\n%s", optedIn, y)
	}

	if _, _, err := adoptedEntry(strayTool, map[string]any{teamfiles.FieldLifecycle: teamfiles.LifecycleDeleted}); !errors.Is(err, ErrAdoptDeleted) {
		t.Errorf("deleted: %v", err)
	}
}

// TestAdoptVerbAndEffect: what the pull request and the ask call the
// adoption and what the reconciler does once it merges.
func TestAdoptVerbAndEffect(t *testing.T) {
	for lifecycle, want := range map[string]string{"": "adopt", "production": "adopt", teamfiles.LifecycleArchived: "adopt and archive", teamfiles.LifecycleDeprecated: "adopt and deprecate"} {
		if got := adoptVerb(lifecycle); got != want {
			t.Errorf("adoptVerb(%q) = %q, want %q", lifecycle, got, want)
		}
	}
	entry := func(fields map[string]any) string {
		d, _, err := adoptedEntry(strayTool, fields)
		if err != nil {
			t.Fatal(err)
		}
		f, err := d.Fields()
		if err != nil {
			t.Fatal(err)
		}
		return adoptEffect(f)
	}
	if got := entry(map[string]any{teamfiles.FieldComponentType: unspecified}); !strings.Contains(got, "checks the repository") || !strings.Contains(got, "nothing on GitHub or CircleCI changes") {
		t.Errorf("not opted in: %s", got)
	}
	if got := entry(map[string]any{teamfiles.FieldComponentType: unspecified, teamfiles.FieldAlign: true}); !strings.Contains(got, "aligns the repository") {
		t.Errorf("opted in: %s", got)
	}
	if got := entry(map[string]any{teamfiles.FieldComponentType: unspecified, teamfiles.FieldLifecycle: teamfiles.LifecycleArchived}); !strings.Contains(got, "archives the repository on GitHub") {
		t.Errorf("archived: %s", got)
	}
}

// TestAdoptRepositoryNeedsAReader: without the caller's token and without
// the inventory App nothing can read the team files, and the dry run says so
// instead of planning.
func TestAdoptRepositoryNeedsAReader(t *testing.T) {
	c := newTestClient(t, NewMCPServer(Deps{Version: testVersion}))
	text, isErr := call(t, c, ToolAdoptRepository, map[string]any{ArgDryRun: true, argRepository: strayTool, argTeam: testTeam, argEntry: map[string]any{teamfiles.FieldComponentType: unspecified}})
	if !isErr || !strings.Contains(text, "no GitHub token") || !strings.Contains(text, "no unattended read identity") {
		t.Errorf("dry run without a reader: isError=%v %s", isErr, text)
	}
	text, isErr = call(t, c, ToolAdoptRepository, map[string]any{ArgMode: string(ModeCommit), argRepository: strayTool, argTeam: testTeam, argEntry: map[string]any{}})
	if !isErr || !strings.Contains(text, "needs a caller") {
		t.Errorf("commit without a caller: isError=%v %s", isErr, text)
	}
}
