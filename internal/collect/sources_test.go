package collect

import (
	"reflect"
	"testing"
)

func TestRenovateStateReadsOnlyTheTopLevelEnabled(t *testing.T) {
	generated := "{\n  // Generated\n  \"extends\": [\"github>giantswarm/renovate-presets:default.json5\"],\n  packageRules: [\n    { matchPackageNames: [\"x\"], enabled: false, },\n  ],\n}\n"
	if r := renovateState("renovate.json5", generated); !r.Configured || !r.Enabled || !r.Preset || r.Path != "renovate.json5" {
		t.Errorf("generated config: %+v", r)
	}
	if r := renovateState("renovate.json", `{"enabled": false, "extends": ["config:base"]}`); r.Enabled || r.Preset {
		t.Errorf("disabled: %+v", r)
	}
	if r := renovateState(".renovaterc", "enabled: false\nnot json at all {"); r.Enabled {
		t.Errorf("unparsable disabled: %+v", r)
	}
	if r := renovateState(".renovaterc", "{ broken json\n  packageRules: [{ enabled: false }]"); !r.Enabled {
		t.Errorf("unparsable with a nested enabled: false: %+v", r)
	}
}

func TestCodeownersTeamsAndBots(t *testing.T) {
	got := codeownersTeams("giantswarm", "* @giantswarm/team-bumblebee @giantswarm/Team-Rocket\n", "/docs @other-org/docs\n")
	if want := []string{"team-bumblebee", "team-rocket"}; !reflect.DeepEqual(got, want) {
		t.Errorf("codeowners: %v", got)
	}
	for _, tc := range []struct {
		login, name, email string
		bot, renovate      bool
	}{
		{"renovate", "", "", true, true},
		{"dependabot[bot]", "", "", true, false},
		{"", "Renovate Bot", "renovate@whitesourcesoftware.com", true, true},
		{"", "Timo", "timo@giantswarm.io", false, false},
		{"teemow", "", "", false, false},
		{"", "taylorbot", "taylorbot@giantswarm.io", true, false},
	} {
		if got := isBot(tc.login, tc.name, tc.email); got != tc.bot {
			t.Errorf("isBot(%q,%q,%q)=%v", tc.login, tc.name, tc.email, got)
		}
		if got := isRenovate(tc.login, tc.name, tc.email); got != tc.renovate {
			t.Errorf("isRenovate(%q,%q,%q)=%v", tc.login, tc.name, tc.email, got)
		}
	}
	if !isOnboarding("reposetup/codeowners", "chore: CODEOWNERS", "giantswarm-align-files") || isOnboarding("feat/x", "feat: x", alice) {
		t.Error("onboarding detection")
	}
}

const alice = "alice"
