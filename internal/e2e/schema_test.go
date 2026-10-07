package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/tools"
)

// engineModule is the devctl module whose embedded repositories schema the
// manager validates against.
const engineModule = "github.com/giantswarm/devctl/v8"

// embeddedSchemaDocument is the repositories schema document shipped with the
// devctl version this module builds with, read from the module's source
// rather than through the engine — once per test binary, every fake org
// serves it beside its team files.
func embeddedSchemaDocument(t *testing.T) []byte {
	t.Helper()
	doc, err := readEmbeddedSchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

var readEmbeddedSchemaDocument = sync.OnceValues(func() ([]byte, error) {
	out, err := exec.Command("go", "list", "-m", "-json", engineModule).Output() // #nosec G204 -- fixed arguments
	if err != nil {
		return nil, fmt.Errorf("go list -m %s: %w", engineModule, err)
	}
	var mod struct{ Dir string }
	if err := json.Unmarshal(out, &mod); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(mod.Dir, "pkg", "reposetup", "schema", "repositories.schema.json")) // #nosec G304 -- the module cache
})

// enum is a schema node's enumeration.
type enum struct {
	Enum []string `json:"enum"`
}

// TestGetInfoReportsTheSchemaTheValidatorUses: get_info's schema is the
// declaration vocabulary of the schema document embedded in the engine's
// devctl version — each list the document's enum, in its order, and the
// origin naming that copy and the engine version get_info reports (a test
// binary may carry no module versions) — and it is the vocabulary the validator holds a
// declaration to: a reported visibility is accepted, one the report does
// not list is refused on that field.
func TestGetInfoReportsTheSchemaTheValidatorUses(t *testing.T) {
	doc := embeddedSchemaDocument(t)
	var schema struct {
		Items struct {
			Properties struct {
				ComponentType enum `json:"componentType"`
				Gen           struct {
					Properties struct {
						Flavours struct {
							Items enum `json:"items"`
						} `json:"flavours"`
						Language enum `json:"language"`
					} `json:"properties"`
				} `json:"gen"`
				Visibility enum `json:"visibility"`
				Lifecycle  enum `json:"lifecycle"`
			} `json:"properties"`
		} `json:"items"`
	}
	if err := json.Unmarshal(doc, &schema); err != nil {
		t.Fatal(err)
	}
	p := schema.Items.Properties
	want := tools.SchemaInfo{
		ComponentTypes: p.ComponentType.Enum,
		Flavours:       p.Gen.Properties.Flavours.Items.Enum,
		Languages:      p.Gen.Properties.Language.Enum,
		Visibilities:   p.Visibility.Enum,
		Lifecycles:     p.Lifecycle.Enum,
	}
	for name, values := range map[string][]string{kComponentType: want.ComponentTypes, kGen + "." + kFlavours: want.Flavours, kGen + "." + kLanguage: want.Languages, kVisibility: want.Visibilities, "lifecycle": want.Lifecycles} {
		if len(values) == 0 {
			t.Fatalf("the embedded schema document has no enum for %s", name)
		}
	}

	st := newStack(t)
	asAlice := st.as(t, aliceToken)
	info := getInfo(t, asAlice)
	want.Origin = "embedded (" + engineModule + " " + info.Engine.Version + ")"
	got := info.Schema
	if !reflect.DeepEqual(got, want) || info.Engine.Module != engineModule {
		t.Fatalf("get_info schema:\n got %+v\nwant %+v", got, want)
	}

	var v tools.Validation
	for _, visibility := range got.Visibilities {
		st.callJSON(t, asAlice, tools.ToolValidateRepository, map[string]any{argTeam: team, argEntry: withField(newEntry, kVisibility, visibility)}, &v)
		if !v.Accepted {
			t.Errorf("reported visibility %q refused: %+v", visibility, v.Entries)
		}
	}
	st.callJSON(t, asAlice, tools.ToolValidateRepository, map[string]any{argTeam: team, argEntry: withField(newEntry, kVisibility, "unreported")}, &v)
	if v.Accepted || len(v.Entries) != 1 || !hasProblem(v.Entries[0].Problems, kVisibility) {
		t.Errorf("a visibility the report does not list should be refused on %s: %+v", kVisibility, v.Entries)
	}
}

// futureField is a field of a devctl release newer than the engine's: the
// schema beside the team files knows it, the engine's embedded copy does not.
const futureField = "futureField"

// schemaWith is the embedded schema document with one more boolean entry
// property, as a newer devctl release merges it beside the team files.
func schemaWith(t *testing.T, field string) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(embeddedSchemaDocument(t), &doc); err != nil {
		t.Fatal(err)
	}
	items, _ := doc["items"].(map[string]any)
	properties, _ := items["properties"].(map[string]any)
	if properties == nil {
		t.Fatal("the embedded schema has no items.properties")
	}
	properties[field] = map[string]any{"type": "boolean", "description": "a field of a newer devctl release"}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestDeclarationsAreHeldToTheSchemaTheTeamFileDeclares: a sweep validates
// every entry with the repositories schema beside the team files at the
// commit it read them, not the engine's embedded copy alone. An entry using
// a field of a devctl release newer than the engine's is accepted and its
// checks run — the engine's verdict is the repository's, not the entry's —
// with the advisory finding entry-field-unknown naming the field: the
// listing's row is not refused and the finding filter finds it; an entry
// the declared schema refuses — a
// malformed value of that field, a field no schema knows — stays
// entry-refused, no check run, its problems naming each field. A commit
// without the schema file fails the sweep's team-file read: nothing stands
// in for the declared schema.
func TestDeclarationsAreHeldToTheSchemaTheTeamFileDeclares(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	st.ghs.org.setSchema(schemaWith(t, futureField))
	st.ghs.org.declare("- name: " + repoStray + "\n  componentType: service\n  " + futureField + ": true\n  gen:\n    language: go\n    flavours: [generic]\n")
	st.ghs.org.declare("- name: " + repoArchived + "\n  componentType: service\n  " + futureField + ": \"yes\"\n  nobodyKnows: true\n")
	if _, err := st.col.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	newer := st.record(t, repoStray)
	if d := newer.Declaration; d == nil || !d.Accepted || len(d.Problems) != 0 || strings.Join(d.UnknownFields, ",") != futureField {
		t.Fatalf("newer declaration: %+v", newer.Declaration)
	}
	if c := newer.Setup.Checks; c == nil || c.Refused() || c.Step(reconcile.StepScaffold) == nil || newer.Setup.CheckError != "" {
		t.Errorf("newer checks should have run: %+v error %q", newer.Setup.Checks, newer.Setup.CheckError)
	}
	var unknown *inventory.Finding
	for i := range newer.Findings {
		if newer.Findings[i].Kind == inventory.FindingEntryFieldUnknown {
			unknown = &newer.Findings[i]
		}
	}
	if unknown == nil || !unknown.Advisory || unknown.Source != inventory.FindingSourceInventory || !strings.Contains(unknown.Message, "field "+futureField+" is newer than the manager's engine") || !strings.Contains(unknown.Fix, "nothing to edit") {
		t.Errorf("newer finding: %+v (all %+v)", unknown, newer.Findings)
	}
	if hasKind(newer, string(reconcile.FindingEntryRefused)) {
		t.Errorf("newer entry refused: %+v", newer.Findings)
	}

	malformed := st.record(t, repoArchived)
	if d := malformed.Declaration; d == nil || d.Accepted || len(d.UnknownFields) != 0 || len(d.Problems) != 2 ||
		!strings.HasPrefix(d.Problems[0], futureField+": ") || !strings.HasPrefix(d.Problems[1], "nobodyKnows: not a field") {
		t.Fatalf("malformed declaration: %+v", malformed.Declaration)
	}
	if c := malformed.Setup.Checks; c == nil || !c.Refused() || c.Converged {
		t.Errorf("malformed checks should be the refusal: %+v", malformed.Setup.Checks)
	}
	if !hasKind(malformed, string(reconcile.FindingEntryRefused)) || hasKind(malformed, inventory.FindingEntryFieldUnknown) {
		t.Errorf("malformed findings: %+v", malformed.Findings)
	}

	c := st.as(t, aliceToken)
	var rows tools.Listing
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{argFinding: inventory.FindingEntryFieldUnknown}, &rows)
	if rows.Matched != 1 || rows.Repositories[0].Repository != org+"/"+repoStray || rows.Repositories[0].Setup.Refused || rows.Repositories[0].Setup.Converged == nil {
		t.Errorf("list entry-field-unknown: %+v", rows.Repositories)
	}
	st.callJSON(t, c, tools.ToolListRepositories, map[string]any{argFinding: string(reconcile.FindingEntryRefused)}, &rows)
	if rows.Matched != 1 || rows.Repositories[0].Repository != org+"/"+repoArchived || !rows.Repositories[0].Setup.Refused {
		t.Errorf("list entry-refused: %+v", rows.Repositories)
	}

	// The schema file gone from the commit: the team-file read fails, the
	// records stand as they were.
	st.ghs.org.setSchema("")
	st.advance(time.Hour)
	if _, err := st.col.Sweep(ctx); err == nil || !strings.Contains(err.Error(), "has no "+reposetup.SchemaPath+" at HEAD") {
		t.Errorf("a sweep without the declared schema should fail naming the file, got %v", err)
	}
	if again := st.record(t, repoStray); again.Declaration == nil || !again.Declaration.Accepted {
		t.Errorf("the record did not survive the failed read: %+v", again.Declaration)
	}
}

// withField is a copy of the entry with one more field.
func withField(entry map[string]any, field string, value any) map[string]any {
	out := make(map[string]any, len(entry)+1)
	for k, v := range entry {
		out[k] = v
	}
	out[field] = value
	return out
}

// hasProblem says whether one of the problems is on the field.
func hasProblem(problems []reposetup.Problem, field string) bool {
	for _, p := range problems {
		if p.Field == field {
			return true
		}
	}
	return false
}
