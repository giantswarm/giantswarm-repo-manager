package collect

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// ciFiles are the .circleci files read, in the order they are stored: the
// entry point (a dynamic-config setup workflow on devctl-generated CI, the
// whole pipeline on a legacy one), the generated workflows and the
// repository's own additions.
var ciFiles = []string{"config.yml", "workflows.yml", "custom.yml"}

const (
	architectOrb = "giantswarm/architect@"
	// generatedMarker is in the header devctl writes on the files it generates.
	generatedMarker = "devctl gen circleci"

	// The orb versions the facts hinge on.
	orbSigning = "8.2.0" // `sign: true` on push-to-registries and push-to-app-catalog
	orbBuildx  = "9.0.0" // every image push through buildx; platforms from go-build's `.platforms`

	// defaultPlatforms is go-build's `platforms` default, which push-to-registries
	// derives its list from when it is given none.
	defaultPlatforms = "linux/amd64,linux/arm64"
	platformARM64    = "linux/arm64"
	platformAMD64    = "linux/amd64"

	jobPush          = "architect/push-to-registries"
	jobPushMultiarch = "architect/push-to-registries-multiarch"
	jobBuildImage    = "architect/build-image"
	jobGoBuild       = "architect/go-build"
	jobChart         = "architect/push-to-app-catalog"
	jobSyncChina     = "architect/sync-china-registry"
)

// ciFacts reads the repository's CircleCI configuration for the record's CI
// block: the questions a person asks about a repository's CI before aligning
// it — which architect orb, arm64 images or not, how the images reach China,
// whether images and charts are signed. nil when the default branch carries
// no .circleci file. public says the repository is public: cosign signs
// public images and charts only.
func ciFacts(n *repoNode, reality *inventory.Reality) *inventory.CI {
	if n == nil {
		return nil
	}
	texts := map[string]string{}
	for i, b := range []*blob{n.CIConfig, n.CIWorkflows, n.CICustom} {
		if b != nil {
			texts[ciFiles[i]] = b.Text
		}
	}
	if len(texts) == 0 {
		return nil
	}
	return parseCI(texts, reality != nil && strings.EqualFold(reality.Visibility, "public"))
}

// jobUse is one job of a workflow: an orb job or a job of the file, with the
// parameters the workflow passes it.
type jobUse struct {
	name   string
	params map[string]any
}

// parseCI reads the files' texts (by name, ciFiles' names) into the CI facts.
func parseCI(texts map[string]string, public bool) *inventory.CI {
	ci := &inventory.CI{ChinaPush: inventory.ChinaPushNone, Signing: inventory.SigningNone}
	var jobs []jobUse
	var errs []string
	for _, file := range ciFiles {
		text, ok := texts[file]
		if !ok {
			continue
		}
		ci.Files = append(ci.Files, file)
		if file == ciFiles[0] && strings.Contains(text, generatedMarker) {
			ci.Generated = true
		}
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", file, yamlError(err)))
			continue
		}
		if ci.Orb == "" {
			ci.Orb = architectVersion(doc)
		}
		jobs = append(jobs, workflowJobs(doc)...)
	}
	ci.Error = strings.Join(errs, "; ")
	ci.Jobs = ciJobs(jobs)

	var pushes, builds, goBuilds, charts []jobUse
	sync := false
	for _, j := range jobs {
		switch j.name {
		case jobPush, jobPushMultiarch:
			pushes = append(pushes, j)
		case jobBuildImage:
			builds = append(builds, j)
		case jobGoBuild:
			goBuilds = append(goBuilds, j)
		case jobChart:
			charts = append(charts, j)
		case jobSyncChina:
			sync = true
		}
	}
	ci.ImagePush, ci.ChartPush = len(pushes) > 0, len(charts) > 0
	ci.Platforms, ci.ARM64 = imagePlatforms(ci.Orb, pushes, builds, goBuilds)
	ci.ChinaPush = chinaPush(pushes, sync)
	ci.Signing, ci.SigningReason = signing(ci.Orb, pushes, charts, public)
	return ci
}

// yamlError is the error's first line: yaml.v3 wraps several.
func yamlError(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "yaml: ")
}

// architectVersion is the giantswarm/architect orb version the document pins,
// "" without the orb.
func architectVersion(doc map[string]any) string {
	orbs, _ := doc["orbs"].(map[string]any)
	for _, v := range orbs {
		if s, ok := v.(string); ok && strings.HasPrefix(s, architectOrb) {
			return strings.TrimPrefix(s, architectOrb)
		}
	}
	return ""
}

// workflowJobs lists the jobs every workflow of the document runs, with the
// parameters passed to each; a job given as a bare name has none.
func workflowJobs(doc map[string]any) []jobUse {
	workflows, _ := doc["workflows"].(map[string]any)
	var out []jobUse
	for name, wf := range workflows {
		if name == "version" {
			continue
		}
		w, _ := wf.(map[string]any)
		list, _ := w["jobs"].([]any)
		for _, item := range list {
			switch v := item.(type) {
			case string:
				out = append(out, jobUse{name: v})
			case map[string]any:
				for k, p := range v {
					params, _ := p.(map[string]any)
					out = append(out, jobUse{name: k, params: params})
				}
			}
		}
	}
	return out
}

// imagePlatforms resolves the platforms the image push jobs build, the way the
// orb does: the job's `platforms`, else the per-architecture build-image jobs
// it merges (`merge-digests`), else — from orb 9, every push through buildx —
// go-build's `platforms` when a go-build job wrote `.platforms` (its default
// covers amd64 and arm64) and the orb's own default, the same two, without
// one; before orb 9 the legacy rule (`multiarch: true` or the multiarch job
// builds both, a plain push amd64). The union over the push jobs; nil, nil
// when no image is pushed or the configuration says nothing for any.
func imagePlatforms(orb string, pushes, builds, goBuilds []jobUse) ([]string, *bool) {
	set := map[string]bool{}
	for _, p := range pushes {
		switch {
		case stringParam(p, "platforms") != "":
			addPlatforms(set, stringParam(p, "platforms"))
		case boolParam(p, "merge-digests"):
			for _, b := range builds {
				if pl := stringParam(b, "platform"); pl != "" {
					set[pl] = true
				}
			}
		case p.name == jobPushMultiarch || boolParam(p, "multiarch"):
			addPlatforms(set, defaultPlatforms)
		case atLeast(orb, orbBuildx):
			// buildx takes go-build's `.platforms` when a go-build job wrote
			// it, else the orb's built-in default — the same list.
			list := defaultPlatforms
			for _, g := range goBuilds {
				if pl := stringParam(g, "platforms"); pl != "" {
					list = pl
				}
			}
			addPlatforms(set, list)
		case orb != "":
			set[platformAMD64] = true
		}
	}
	if len(set) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(set))
	for pl := range set {
		out = append(out, pl)
	}
	sort.Strings(out)
	arm := set[platformARM64]
	return out, &arm
}

func addPlatforms(set map[string]bool, list string) {
	for _, pl := range strings.Split(list, ",") {
		if pl = strings.TrimSpace(pl); pl != "" {
			set[pl] = true
		}
	}
}

// chinaPush is how the pushed images reach the China registry: split when a
// push job asks for `split-china-push` or the sync-china-registry job mirrors
// them from the in-China runner, custom when a push job overrides the
// registry list (`registries-data`, whose hosts the configuration does not
// name), inline when the push job itself pushes to every registry, none
// without an image push.
func chinaPush(pushes []jobUse, sync bool) string {
	if len(pushes) == 0 {
		return inventory.ChinaPushNone
	}
	if sync {
		return inventory.ChinaPushSplit
	}
	custom := false
	for _, p := range pushes {
		if boolParam(p, "split-china-push") {
			return inventory.ChinaPushSplit
		}
		if stringParam(p, "registries-data") != "" {
			custom = true
		}
	}
	if custom {
		return inventory.ChinaPushCustom
	}
	return inventory.ChinaPushInline
}

// signing says whether the pushed images and charts are signed with cosign:
// the orb signs both by default (`sign: true`) since orbSigning, public
// artifacts only. The reason names what leaves them unsigned.
func signing(orb string, pushes, charts []jobUse, public bool) (string, string) {
	if len(pushes) == 0 && len(charts) == 0 {
		return inventory.SigningNone, ""
	}
	if orb == "" {
		return inventory.SigningUnknown, "no architect orb: the pipeline pushes without the orb's signing steps"
	}
	if _, ok := semver(orb); !ok {
		return inventory.SigningUnknown, fmt.Sprintf("orb version %s is not a release", orb)
	}
	if !atLeast(orb, orbSigning) {
		return inventory.SigningUnsigned, fmt.Sprintf("orb %s predates signing (%s)", orb, orbSigning)
	}
	var off []string
	for _, j := range append(append([]jobUse{}, pushes...), charts...) {
		if v, ok := j.params["sign"].(bool); ok && !v {
			off = append(off, jobName(j))
		}
	}
	if len(off) > 0 {
		sort.Strings(off)
		return inventory.SigningUnsigned, "sign: false on " + strings.Join(off, ", ")
	}
	if !public {
		return inventory.SigningUnsigned, "private repository: cosign signs public images and charts only"
	}
	return inventory.SigningSigned, ""
}

// ciJobs are the workflows' jobs with their filters, as the record keeps
// them to tell whose a commit status is.
func ciJobs(jobs []jobUse) []inventory.CIJob {
	out := make([]inventory.CIJob, 0, len(jobs))
	for _, j := range jobs {
		cj := inventory.CIJob{Name: jobName(j)}
		if filters, _ := j.params["filters"].(map[string]any); filters != nil {
			if b, _ := filters["branches"].(map[string]any); b != nil {
				cj.BranchesOnly, cj.BranchesIgnore = patterns(b["only"]), patterns(b["ignore"])
			}
			if t, _ := filters["tags"].(map[string]any); t != nil {
				cj.TagsOnly, cj.TagsIgnore = patterns(t["only"]), patterns(t["ignore"])
			}
		}
		out = append(out, cj)
	}
	return out
}

// patterns is a filter's value as the configuration writes it: one pattern
// or a list of them.
func patterns(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// jobName is the job's `name` parameter, else the orb job's name.
func jobName(j jobUse) string {
	if n := stringParam(j, "name"); n != "" {
		return n
	}
	return j.name
}

func stringParam(j jobUse, key string) string {
	s, _ := j.params[key].(string)
	return strings.TrimSpace(s)
}

func boolParam(j jobUse, key string) bool {
	b, _ := j.params[key].(bool)
	return b
}

// semver parses X.Y.Z; ok is false for anything else (a dev orb, empty).
func semver(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// atLeast says whether orb version v is min or newer; false when v is not a
// release version.
func atLeast(v, min string) bool {
	a, ok := semver(v)
	if !ok {
		return false
	}
	b, _ := semver(min)
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}
