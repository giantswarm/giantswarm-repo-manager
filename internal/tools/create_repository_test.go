package tools

import (
	"strings"
	"testing"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
)

// TestFirstRelease: v0.1.0 follows on its own when the entry's release
// workflow is auto-release — gen.ci.releaseWorkflow when set, else
// auto-release with gen.ci.generate: true — and from nothing else.
func TestFirstRelease(t *testing.T) {
	for _, tc := range []struct {
		name, ci string
		want     string
	}{
		{"generated CI", "    ci:\n      generate: true\n", FirstRelease},
		{"without CI", "    ci:\n      generate: false\n", NoFirstRelease},
		{"no gen.ci", "", NoFirstRelease},
		{"generated CI on legacy", "    ci:\n      generate: true\n      releaseWorkflow: legacy\n", NoFirstRelease},
		{"auto-release without CI", "    ci:\n      generate: false\n      releaseWorkflow: auto-release\n", FirstRelease},
		{"auto-release, generate unset", "    ci:\n      releaseWorkflow: auto-release\n", FirstRelease},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tf, err := reposetup.ParseTeamFile("team-a", strings.NewReader("- name: x\n  gen:\n    language: go\n    flavours: [generic]\n"+tc.ci))
			if err != nil {
				t.Fatal(err)
			}
			if got := firstRelease(tf.Entries[0]); got != tc.want {
				t.Errorf("firstRelease = %q, want %q", got, tc.want)
			}
		})
	}
}
