package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"

	"github.com/giantswarm/giantswarm-repo-manager/internal/tools"
)

// engineModule is the devctl module whose embedded repositories schema the
// manager validates against.
const engineModule = "github.com/giantswarm/devctl/v8"

// embeddedSchemaDocument is the repositories schema document shipped with the
// devctl version this module builds with, read from the module's source
// rather than through the engine.
func embeddedSchemaDocument(t *testing.T) []byte {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-json", engineModule).Output() // #nosec G204 -- fixed arguments
	if err != nil {
		t.Fatalf("go list -m %s: %v", engineModule, err)
	}
	var mod struct{ Dir string }
	if err := json.Unmarshal(out, &mod); err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(filepath.Join(mod.Dir, "pkg", "reposetup", "schema", "repositories.schema.json")) // #nosec G304 -- the module cache
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

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
