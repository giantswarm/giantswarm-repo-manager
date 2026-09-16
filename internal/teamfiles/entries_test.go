package teamfiles

import (
	"strings"
	"testing"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
)

const fixture = `# yaml-language-server: $schema=../.github/repositories.schema.json
- name: alpha
  componentType: service
  gen:
    language: go
    flavours: [app]
# beta is special.
- name: beta
  componentType: service
  # a comment inside beta
  gen:
    language: generic
    flavours: [app]

- name: gamma
  componentType: configuration
`

func TestReplaceEntryKeepsEveryOtherByte(t *testing.T) {
	d, err := ParseEntry([]byte("- name: beta\n  componentType: service\n  lifecycle: archived\n"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ReplaceEntry([]byte(fixture), "beta", d)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	want := `# yaml-language-server: $schema=../.github/repositories.schema.json
- name: alpha
  componentType: service
  gen:
    language: go
    flavours: [app]
# beta is special.
- name: beta
  componentType: service
  lifecycle: archived
- name: gamma
  componentType: configuration
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if _, err := ReplaceEntry([]byte(fixture), "delta", d); err == nil || !strings.Contains(err.Error(), "delta") {
		t.Errorf("unknown entry: %v", err)
	}
}

func TestRemoveEntryAndSetField(t *testing.T) {
	out, err := RemoveEntry([]byte(fixture), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "# yaml-language-server: $schema=../.github/repositories.schema.json\n# beta is special.\n- name: beta\n") || strings.Contains(string(out), "alpha") {
		t.Errorf("remove alpha:\n%s", out)
	}
	out, err = RemoveEntry([]byte(fixture), "gamma")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "gamma") || !strings.HasSuffix(string(out), "flavours: [app]\n\n") {
		t.Errorf("remove gamma:\n%s", out)
	}

	tf, err := reposetup.ParseTeamFile("team-x", strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	beta, _ := tf.Entry("beta")
	changed, err := SetField(beta, FieldLifecycle, LifecycleDeprecated)
	if err != nil {
		t.Fatal(err)
	}
	f, err := changed.Fields()
	if err != nil || f.Lifecycle != LifecycleDeprecated || f.ComponentType != "service" || f.Gen == nil || f.Gen.Language != "generic" {
		t.Errorf("set lifecycle: %+v %v", f, err)
	}
	y, _ := changed.YAML()
	if !strings.HasPrefix(y, "- name: beta\n") || !strings.Contains(y, "lifecycle: deprecated") {
		t.Errorf("rendered:\n%s", y)
	}
}
