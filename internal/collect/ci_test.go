package collect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// The .circleci file names as the fixtures use them.
var cfgFile, wfFile, customFile = ciFiles[0], ciFiles[1], ciFiles[2]

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // a fixture of this package
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestCIFactsGenerated: a devctl-generated pipeline — the setup config, the
// generated workflows and the repository's custom.yml — answers every
// question: generated, orb 10.5.0, amd64+arm64 images, the split China push,
// signed images and charts on a public repository.
func TestCIFactsGenerated(t *testing.T) {
	texts := map[string]string{
		cfgFile:    fixture(t, "generated-config.yml"),
		wfFile:     fixture(t, "generated-workflows.yml"),
		customFile: fixture(t, "generated-custom.yml"),
	}
	ci := parseCI(texts, true)
	if strings.Join(ci.Files, ",") != "config.yml,workflows.yml,custom.yml" || !ci.Generated || ci.Orb != "10.5.0" || ci.Error != "" {
		t.Errorf("files %v generated %v orb %q error %q", ci.Files, ci.Generated, ci.Orb, ci.Error)
	}
	if !ci.ImagePush || !ci.ChartPush || strings.Join(ci.Platforms, ",") != defaultPlatforms || ci.ARM64 == nil || !*ci.ARM64 {
		t.Errorf("image %v chart %v platforms %v arm64 %v", ci.ImagePush, ci.ChartPush, ci.Platforms, ci.ARM64)
	}
	if ci.ChinaPush != inventory.ChinaPushSplit || ci.Signing != inventory.SigningSigned || ci.SigningReason != "" {
		t.Errorf("china %s signing %s %q", ci.ChinaPush, ci.Signing, ci.SigningReason)
	}
	// The same pipeline on a private repository: cosign skips it at runtime.
	if ci := parseCI(texts, false); ci.Signing != inventory.SigningUnsigned || !strings.Contains(ci.SigningReason, "private repository") {
		t.Errorf("private: signing %s %q", ci.Signing, ci.SigningReason)
	}
}

// TestCIFactsLegacy: a single hand-maintained config.yml on orb 9.5.5 with an
// amd64-only push and the split China push.
func TestCIFactsLegacy(t *testing.T) {
	ci := parseCI(map[string]string{cfgFile: fixture(t, "legacy-config.yml")}, true)
	if ci.Generated || ci.Orb != "9.5.5" || ci.Error != "" || strings.Join(ci.Files, ",") != "config.yml" {
		t.Errorf("generated %v orb %q error %q files %v", ci.Generated, ci.Orb, ci.Error, ci.Files)
	}
	if !ci.ImagePush || strings.Join(ci.Platforms, ",") != "linux/amd64" || ci.ARM64 == nil || *ci.ARM64 {
		t.Errorf("image %v platforms %v arm64 %v", ci.ImagePush, ci.Platforms, ci.ARM64)
	}
	if ci.ChinaPush != inventory.ChinaPushSplit || ci.Signing != inventory.SigningSigned {
		t.Errorf("china %s signing %s %q", ci.ChinaPush, ci.Signing, ci.SigningReason)
	}
}

// TestCIFactsRules: the orb rules on small hand-written pipelines.
func TestCIFactsRules(t *testing.T) {
	const pre9 = `version: 2.1
orbs:
  architect: giantswarm/architect@7.2.0
workflows:
  build:
    jobs:
      - architect/go-build:
          name: go-build
      - architect/push-to-registries-multiarch:
          name: push
          requires: [go-build]
`
	const plainOld = `version: 2.1
orbs:
  architect: giantswarm/architect@8.3.0
workflows:
  build:
    jobs:
      - architect/push-to-registries:
          name: push
          sign: false
`
	const buildxNoGo = `version: 2.1
orbs:
  architect: giantswarm/architect@10.0.0
workflows:
  build:
    jobs:
      - architect/push-to-registries:
          name: push
          registries-data: "abc"
`
	const native = `version: 2.1
orbs:
  arch: giantswarm/architect@10.5.0
workflows:
  build:
    jobs:
      - architect/build-image:
          name: build-arm
          platform: linux/arm64
      - architect/build-image:
          name: build-amd
          platform: linux/amd64
      - architect/push-to-registries:
          name: push
          merge-digests: true
          split-china-push: true
      - architect/push-to-app-catalog:
          name: push-chart
`
	const chartOnly = `version: 2.1
orbs:
  architect: giantswarm/architect@dev:abc
workflows:
  build:
    jobs:
      - architect/push-to-app-catalog
`
	const noOrb = `version: 2.1
jobs:
  build:
    docker: [{image: alpine}]
    steps: [checkout]
workflows:
  build:
    jobs: [build]
`
	yes, no := true, false
	cases := []struct {
		name      string
		text      string
		public    bool
		platforms string
		arm       *bool
		china     string
		signing   string
		reason    string
	}{
		{"pre-9 multiarch job: both platforms, inline China, orb predates signing", pre9, true, defaultPlatforms, &yes, inventory.ChinaPushInline, inventory.SigningUnsigned, "predates signing"},
		{"pre-9 plain push with sign false: amd64 only, sign off", plainOld, true, "linux/amd64", &no, inventory.ChinaPushInline, inventory.SigningUnsigned, "sign: false on push"},
		{"buildx without go-build and a registry override: the orb's default platforms, custom China", buildxNoGo, true, defaultPlatforms, &yes, inventory.ChinaPushCustom, inventory.SigningSigned, ""},
		{"native per-architecture builds merged: both platforms, split China, signed", native, true, defaultPlatforms, &yes, inventory.ChinaPushSplit, inventory.SigningSigned, ""},
		{"chart only on a dev orb: no image, signing unknown", chartOnly, true, "", nil, inventory.ChinaPushNone, inventory.SigningUnknown, "not a release"},
		{"no architect orb, nothing pushed", noOrb, true, "", nil, inventory.ChinaPushNone, inventory.SigningNone, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ci := parseCI(map[string]string{cfgFile: tc.text}, tc.public)
			if ci.Error != "" {
				t.Fatalf("error %q", ci.Error)
			}
			if strings.Join(ci.Platforms, ",") != tc.platforms {
				t.Errorf("platforms %v, want %q", ci.Platforms, tc.platforms)
			}
			switch {
			case tc.arm == nil && ci.ARM64 != nil, tc.arm != nil && (ci.ARM64 == nil || *ci.ARM64 != *tc.arm):
				t.Errorf("arm64 %v, want %v", ci.ARM64, tc.arm)
			}
			if ci.ChinaPush != tc.china || ci.Signing != tc.signing || !strings.Contains(ci.SigningReason, tc.reason) {
				t.Errorf("china %s signing %s %q, want %s %s %q", ci.ChinaPush, ci.Signing, ci.SigningReason, tc.china, tc.signing, tc.reason)
			}
		})
	}
	if ci := parseCI(map[string]string{cfgFile: noOrb}, true); ci.Orb != "" || ci.ImagePush || ci.ChartPush || len(ci.Platforms) != 0 {
		t.Errorf("no orb: %+v", ci)
	}
}

// TestCIFactsUnparsable: a file that is not YAML is named in Error; the
// other files still answer.
func TestCIFactsUnparsable(t *testing.T) {
	ci := parseCI(map[string]string{cfgFile: "version: [", wfFile: fixture(t, "generated-workflows.yml")}, true)
	if !strings.HasPrefix(ci.Error, "config.yml: ") || ci.Orb != "10.5.0" || !ci.ImagePush {
		t.Errorf("error %q orb %q image %v", ci.Error, ci.Orb, ci.ImagePush)
	}
	if ciFacts(nil, nil) != nil || ciFacts(&repoNode{}, nil) != nil {
		t.Error("a node without .circleci files has no CI facts")
	}
}
