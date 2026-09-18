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
	// The added field lands before the gen block, the entry's tail, with the
	// author's comment on gen staying with gen.
	y, _ := changed.YAML()
	if !strings.HasPrefix(y, "- name: beta\n  componentType: service\n  lifecycle: deprecated\n  # a comment inside beta\n  gen:\n") || !strings.HasSuffix(y, "flavours: [app]\n") {
		t.Errorf("rendered:\n%s", y)
	}

	// The opt-in to alignment is a boolean field the same edit writes, after
	// the other added key and before gen; Fields reads it back as the bool it
	// is.
	optedIn, err := SetField(changed, FieldAlign, "true")
	if err != nil {
		t.Fatal(err)
	}
	if f, err := optedIn.Fields(); err != nil || !f.Align || f.Lifecycle != LifecycleDeprecated {
		t.Errorf("set align: %+v %v", f, err)
	}
	if y, _ := optedIn.YAML(); !strings.Contains(y, "  lifecycle: deprecated\n  align: true\n  # a comment inside beta\n  gen:\n") {
		t.Errorf("rendered:\n%s", y)
	}

	// A key the entry has keeps its place; an entry without gen gets the
	// field after the author's keys.
	if again, err := SetField(optedIn, FieldAlign, "false"); err != nil {
		t.Fatal(err)
	} else if y, _ := again.YAML(); !strings.Contains(y, "  lifecycle: deprecated\n  align: false\n  # a comment inside beta\n  gen:\n") {
		t.Errorf("rendered:\n%s", y)
	}
	gamma, _ := tf.Entry("gamma")
	if plain, err := SetField(gamma, FieldAlign, "true"); err != nil {
		t.Fatal(err)
	} else if y, _ := plain.YAML(); !strings.HasSuffix(y, "  componentType: configuration\n  align: true\n") {
		t.Errorf("rendered:\n%s", y)
	}
}

// TestRerenderFileReappliesTheChangedEntries: a pull request's change of one
// file — the entries that differ from its merge base — is written into the
// file as it reads now, whose other entries moved on: a lifecycle change
// replaces its entry in place, a creation adds its entry at its alphabetical
// place, a transfer's removal takes its entry out; the neighbour's change
// and every other byte stay. A pull request whose change is on the base
// already applies nothing; one that removes the file is refused.
func TestRerenderFileReappliesTheChangedEntries(t *testing.T) {
	const (
		header = "# header\n"
		entryA = "- name: a\n  componentType: app\n"
		entryB = "- name: b\n  componentType: app\n"
		entryC = "- name: c\n  componentType: app\n"
		entryD = "- name: d\n  componentType: app\n"
	)
	was := header + entryA + entryC
	// main moved on: b declared between a and c, c deprecated.
	now := header + entryA + entryB + entryC + "  lifecycle: deprecated\n"
	cases := []struct {
		name, want, out string
		entries         []string
	}{
		{"a lifecycle change replaces its entry", header + entryA + "  lifecycle: archived\n" + entryC, header + entryA + "  lifecycle: archived\n" + entryB + entryC + "  lifecycle: deprecated\n", []string{"a"}},
		{"a creation adds its entry at its place", was + entryD, now + entryD, []string{"d"}},
		{"a transfer's removal takes its entry out", header + entryC, header + entryB + entryC + "  lifecycle: deprecated\n", []string{"a"}},
		{"a change on the base already applies nothing", was, now, nil},
	}
	for _, c := range cases {
		out, names, err := rerenderFile("team-x", []byte(was), []byte(c.want), []byte(now))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if string(out) != c.out {
			t.Errorf("%s: file\n%s\nwant\n%s", c.name, out, c.out)
		}
		if strings.Join(names, ",") != strings.Join(c.entries, ",") {
			t.Errorf("%s: entries %v, want %v", c.name, names, c.entries)
		}
	}
	if _, _, err := rerenderFile("team-x", []byte(was), nil, []byte(now)); err == nil {
		t.Error("a pull request that removes the file: want an error")
	}
	// A file the merge base did not have (nil) declares nothing: every entry
	// of the branch is new; a base without the file takes them all.
	out, names, err := rerenderFile("team-x", nil, []byte(header+entryA), nil)
	if err != nil || string(out) != entryA || len(names) != 1 {
		t.Errorf("a new file: %q %v %v", out, names, err)
	}
}
