package e2e

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
)

// declaredLifecycle is a lifecycle value only the declared schema allows.
const declaredLifecycle = "experimental"

// TestGetInfoReportsTheSchemaTheTeamFileDeclares: get_info's schema is the
// one the write tools' dry runs validate with. Without a schema beside the
// team files it is the embedded copy and says so; once they declare one it
// lists that schema's vocabulary and names the team-files ref it was read at.
func TestGetInfoReportsTheSchemaTheTeamFileDeclares(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)

	absent := getInfo(t, c)
	if want := "embedded (" + engineModule + " " + absent.Engine.Version + ")"; absent.Schema.Origin != want {
		t.Errorf("without a declared schema: origin %q, want %q", absent.Schema.Origin, want)
	}
	if slices.Contains(absent.Schema.Lifecycles, declaredLifecycle) {
		t.Fatalf("the embedded schema already lists lifecycle %q", declaredLifecycle)
	}

	var doc map[string]any
	if err := json.Unmarshal(embeddedSchemaDocument(t), &doc); err != nil {
		t.Fatal(err)
	}
	properties := doc["items"].(map[string]any)["properties"].(map[string]any)
	lifecycle := properties["lifecycle"].(map[string]any)
	lifecycle["enum"] = append(lifecycle["enum"].([]any), declaredLifecycle)
	declared, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	st.ghs.files.commit(map[string][]byte{reposetup.SchemaPath: declared})

	got := getInfo(t, c)
	if want := "team files (" + got.TeamFiles.Repository + "@" + got.TeamFiles.Ref + ")"; got.Schema.Origin != want {
		t.Errorf("with a declared schema: origin %q, want %q", got.Schema.Origin, want)
	}
	if !slices.Contains(got.Schema.Lifecycles, declaredLifecycle) || got.Schema.Error != "" {
		t.Errorf("with a declared schema: lifecycles %v, error %q, want %q listed", got.Schema.Lifecycles, got.Schema.Error, declaredLifecycle)
	}
}
