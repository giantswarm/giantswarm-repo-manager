package collect

import (
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
)

// engineSchema is a repositories schema as an engine embeds it: name,
// componentType and visibility, nothing else.
const engineSchema = `{
  "type": "array",
  "items": {
    "type": "object",
    "required": ["name", "componentType"],
    "additionalProperties": false,
    "properties": {
      "name": {"type": "string"},
      "componentType": {"enum": ["service", "library"]},
      "visibility": {"enum": ["public", "private"]}
    }
  }
}`

func compileTestSchema(t *testing.T, doc string) *reposetup.Schema {
	t.Helper()
	s, err := reposetup.CompileSchema([]byte(doc), reposetup.SchemaOriginFile)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func parseTestEntry(t *testing.T, entry string) reposetup.Declaration {
	t.Helper()
	tf, err := reposetup.ParseTeamFile("team-test", strings.NewReader(entry))
	if err != nil {
		t.Fatal(err)
	}
	if len(tf.Entries) != 1 {
		t.Fatalf("want one entry, got %d", len(tf.Entries))
	}
	return tf.Entries[0]
}

// TestUnknownFieldsNamesWhatTheEngineDoesNotKnow: of an entry the declared
// schema accepted, the fields the engine's own schema refuses are named once
// each — a field it has no property for, a value outside its enumeration —
// and an entry the engine knows whole names none.
func TestUnknownFieldsNamesWhatTheEngineDoesNotKnow(t *testing.T) {
	engine := compileTestSchema(t, engineSchema)
	for _, tc := range []struct {
		name  string
		entry string
		want  []string
	}{
		{name: "known whole", entry: "- name: a\n  componentType: service\n  visibility: public\n"},
		{name: "a newer field", entry: "- name: a\n  componentType: service\n  pruneRulesets: true\n", want: []string{"pruneRulesets"}},
		{name: "a newer value", entry: "- name: a\n  componentType: tool\n", want: []string{"componentType"}},
		{name: "several, each once", entry: "- name: a\n  componentType: tool\n  pruneRulesets: true\n  gen:\n    language: go\n", want: []string{"componentType", "gen", "pruneRulesets"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := unknownFields(engine, parseTestEntry(t, tc.entry))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("unknownFields = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDeclaredSchemaCompilesOncePerDocument: the same document compiles
// once, a changed one again, a document that is no schema is the error.
func TestDeclaredSchemaCompilesOncePerDocument(t *testing.T) {
	c := &Collector{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	first, err := c.declaredSchema(engineSchema, "aaa")
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.declaredSchema(engineSchema, "bbb")
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Error("the same document compiled twice")
	}
	newer := strings.Replace(engineSchema, `"visibility"`, `"pruneRulesets": {"type": "boolean"},
      "visibility"`, 1)
	changed, err := c.declaredSchema(newer, "ccc")
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Error("a changed document kept the old schema")
	}
	if _, err := c.declaredSchema("{", "ddd"); err == nil {
		t.Error("a document that is no schema compiled")
	}
}
