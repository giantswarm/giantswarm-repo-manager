package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
)

// The fake org: one declared repository that exists, one declared but gone,
// one legacy declaration the engine's creation rules refuse, and two
// repositories nobody declares (one archived).
// JSON keys of the fake's answers and the tools' arguments.
const (
	kName            = "name"
	kLogin           = "login"
	kEmail           = "email"
	kSlug            = "slug"
	kAuthor          = "author"
	kCommittedDate   = "committedDate"
	kCreatedAt       = "createdAt"
	kNodes           = "nodes"
	kNumber          = "number"
	kRepository      = "repository"
	kLimit           = "limit"
	kTotalCount      = "totalCount"
	kText            = "text"
	kTitle           = "title"
	kHeadRefName     = "headRefName"
	kMessageHeadline = "messageHeadline"
	mainBranch       = "main"
	kUser            = "user"
	kState           = "state"
	kContext         = "context"
	kTarget          = "target"
	kHasNextPage     = "hasNextPage"
	kPageInfo        = "pageInfo"
	kPrivate         = "private"
	// lifecycleArchived and lifecycleDeprecated are set_lifecycle's values.
	lifecycleArchived   = "archived"
	lifecycleDeprecated = "deprecated"
	lifecycleDeleted    = "deleted"
	argConfirm          = "confirm"
	renovateLogin       = "renovate"
)

const (
	org             = "giantswarm"
	repoPresent     = "present-service"
	repoGone        = "gone-service"
	repoLegacy      = "legacy-app"
	repoStray       = "stray-repo"
	repoArchived    = "archived-old"
	dissolvedTeam   = "team-dissolved"
	fakeRunURL      = "https://github.com/giantswarm/github/actions/runs/1"
	fakeGraphQLCost = 7
	circleSetup     = "ci/circleci: setup"
	circleBuild     = "ci/circleci: go-build"
	circleChart     = "ci/circleci: build-chart"
	circlePushChart = "ci/circleci: push-chart"
	circlePush      = "ci/circleci: push-to-registries"
	// presentTag is the present repository's latest release.
	presentTag = "v1.0.0"
	// stateSuccessGQL is GraphQL's spelling of a green status.
	stateSuccessGQL = "SUCCESS"
)

var teamFile = `# yaml-language-server: $schema=../repositories.schema.json
- name: ` + repoPresent + `
  componentType: service
  gen:
    language: go
    flavours: [app]
    ci:
      generate: true
      chartName: ` + repoPresent + `
- name: ` + repoGone + `
  componentType: service
  gen:
    language: go
    flavours: [generic]
- name: ` + repoLegacy + `
  componentType: service
  align: true
  gen:
    language: generic
    flavours: [app]
`

const catalogFile = "---\napiVersion: backstage.io/v1alpha1\nkind: Component\nmetadata:\n    name: " + repoPresent + "\n"
const mappingFile = "apiVersion: v1\nkind: ConfigMap\ndata:\n  " + repoPresent + ": bumblebee\n"

// fakeOrg is the GitHub GraphQL of the fake org, served by fakeGitHub at
// /api/v3/graphql. remaining is the budget it reports.
type fakeOrg struct {
	mu        sync.Mutex
	remaining int
	queries   atomic.Int32
	now       time.Time
	// teamFile is the team file at main; declare appends an entry, as a
	// merged pull request does.
	teamFile string
	// repos are the repositories created through the REST surface: the
	// node of one of them is a plain repository as GraphQL would answer.
	repos *fakeRepos
	// releases are the latest releases the release watch's page carries,
	// by repository, when set; the present repository's default release
	// otherwise. private names the repositories GraphQL reports PRIVATE.
	releases map[string]fakeLatestRelease
	private  map[string]bool
}

// fakeLatestRelease is a repository's latest release as the release watch reads
// it: the tag, its creation and the pull request behind the tag's commit.
type fakeLatestRelease struct {
	tag       string
	createdAt time.Time
	pr        int
}

// release sets a repository's latest release for the release watch's page.
func (o *fakeOrg) release(name, tag string, createdAt time.Time, pr int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.releases == nil {
		o.releases = map[string]fakeLatestRelease{}
	}
	o.releases[name] = fakeLatestRelease{tag: tag, createdAt: createdAt, pr: pr}
}

// releasePage is the release watch's page: the unarchived repositories,
// most recently pushed first, with their latest release.
func (o *fakeOrg) releasePage() []map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	var nodes []map[string]any
	for _, name := range []string{repoPresent, repoLegacy, repoStray} {
		n := map[string]any{kName: name, "visibility": "PUBLIC", "pushedAt": o.now.Format(time.RFC3339)}
		if o.private[name] {
			n["visibility"] = "PRIVATE"
		}
		rel, ok := o.releases[name]
		switch {
		case ok:
			var prs []map[string]any
			if rel.pr != 0 {
				prs = append(prs, map[string]any{kNumber: rel.pr, kURL: fmt.Sprintf("https://github.com/%s/%s/pull/%d", org, name, rel.pr)})
			}
			n["latestRelease"] = map[string]any{kTagName: rel.tag, kCreatedAt: rel.createdAt.UTC().Format(time.RFC3339),
				kTagCommit: map[string]any{"oid": "a80db8ff", "associatedPullRequests": map[string]any{kNodes: prs}}}
		case name == repoPresent:
			n["latestRelease"] = map[string]any{kTagName: presentTag, kCreatedAt: o.now.AddDate(0, -1, 0).Format(time.RFC3339),
				kTagCommit: map[string]any{"oid": "0ld", "associatedPullRequests": map[string]any{kNodes: []any{}}}}
		default:
			n["latestRelease"] = nil
		}
		nodes = append(nodes, n)
	}
	return nodes
}

var (
	historyAlias = regexp.MustCompile(`r(\d+): repository\(owner: \$org, name: "([^"]+)"\)`)
)

// declare appends an entry to the team file at main: the pull request
// declaring a repository merged.
func (o *fakeOrg) declare(entry string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.teamFile += entry
}

func (o *fakeOrg) handle(w http.ResponseWriter, r *http.Request) {
	o.queries.Add(1)
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.Unmarshal(body, &req)
	o.mu.Lock()
	rl := map[string]any{"cost": fakeGraphQLCost, "remaining": o.remaining, "limit": 5000, "resetAt": o.now.Add(time.Hour).Format(time.RFC3339)}
	o.remaining -= fakeGraphQLCost
	teamFileText := o.teamFile
	o.mu.Unlock()

	data := map[string]any{"rateLimit": rl}
	var errs []map[string]any
	switch {
	case contains(req.Query, "teamFiles:"):
		data["organization"] = map[string]any{"teams": map[string]any{kPageInfo: map[string]any{kHasNextPage: false}, kNodes: []map[string]any{{kSlug: team}, {kSlug: teamPlaneteers}}}}
		data[kGitHub] = map[string]any{
			"teamFiles": map[string]any{"entries": []map[string]any{{kName: "team-bumblebee.yaml", kType: "blob", kObject: map[string]any{kText: teamFileText}}}},
			"catalog":   map[string]any{kText: catalogFile},
		}
		data["mcb"] = map[string]any{"mapping": map[string]any{kText: mappingFile}}
	case contains(req.Query, "orderBy: {field: PUSHED_AT"):
		data["organization"] = map[string]any{"repositories": map[string]any{kNodes: o.releasePage()}}
	case contains(req.Query, "repositories(first:"):
		data["organization"] = map[string]any{"repositories": map[string]any{
			kTotalCount: 4, kPageInfo: map[string]any{kHasNextPage: false},
			kNodes: []map[string]any{o.node(repoPresent), o.node(repoLegacy), o.node(repoStray), o.node(repoArchived)},
		}}
	case contains(req.Query, "repository(owner: $org, name: $name)"):
		name, _ := req.Variables["name"].(string)
		if n := o.node(name); n != nil {
			// The history joins the head's status rollup the node carries.
			target := map[string]any{"history": o.history(name)}
			if ref, ok := n["defaultBranchRef"].(map[string]any); ok {
				if t, ok := ref[kTarget].(map[string]any); ok {
					target["statusCheckRollup"] = t["statusCheckRollup"]
				}
			}
			n["defaultBranchRef"] = map[string]any{kName: mainBranch, kTarget: target}
			data["repository"] = n
		} else {
			data["repository"] = nil
			errs = append(errs, map[string]any{kMessage: fmt.Sprintf("Could not resolve to a Repository with the name '%s/%s'.", org, name), kType: "NOT_FOUND", kPath: []string{kRepository}})
		}
	default:
		for _, m := range historyAlias.FindAllStringSubmatch(req.Query, -1) {
			if n := o.node(m[2]); n != nil {
				data["r"+m[1]] = map[string]any{kName: m[2], "defaultBranchRef": map[string]any{kTarget: map[string]any{"history": o.history(m[2])}}}
			} else {
				data["r"+m[1]] = nil
			}
		}
	}
	resp := map[string]any{kData: data}
	if errs != nil {
		resp["errors"] = errs
	}
	writeJSON(w, http.StatusOK, resp)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && regexp.MustCompile(regexp.QuoteMeta(sub)).MatchString(s)
}

// node is a repository's GraphQL node, nil when the fake org has none.
func (o *fakeOrg) node(name string) map[string]any {
	base := func(archived bool) map[string]any {
		return map[string]any{
			kName: name, "url": "https://github.com/" + org + "/" + name, kDescription: "", "visibility": "PUBLIC",
			"isArchived": archived, "isFork": false, "isTemplate": false, "isEmpty": false,
			kCreatedAt: o.now.AddDate(-2, 0, 0).Format(time.RFC3339), "pushedAt": o.now.Format(time.RFC3339),
			"primaryLanguage": map[string]any{kName: "Go"}, "repositoryTopics": map[string]any{kNodes: []any{}},
			"openIssues": map[string]any{kTotalCount: 1}, "oldestIssues": map[string]any{kNodes: []any{}},
			"openPRs": map[string]any{kTotalCount: 0, kNodes: []any{}}, "defaultBranchRef": map[string]any{kName: mainBranch},
			"readme": map[string]any{"id": "1"},
		}
	}
	switch name {
	case repoPresent:
		n := base(false)
		n["latestRelease"] = map[string]any{kTagName: presentTag, "publishedAt": o.now.AddDate(0, -1, 0).Format(time.RFC3339), kTagCommit: map[string]any{"statusCheckRollup": o.releaseRollup()}}
		n["oldestIssues"] = map[string]any{kNodes: []map[string]any{{kNumber: 3, kTitle: "Dependency Dashboard"}}}
		n["openPRs"] = map[string]any{kTotalCount: 2, kNodes: []map[string]any{
			{kNumber: 10, kTitle: "fix(deps): update module x", kCreatedAt: o.now.AddDate(0, 0, -2).Format(time.RFC3339), kHeadRefName: "renovate/x", kAuthor: map[string]any{kLogin: renovateLogin}},
			{kNumber: 11, kTitle: "feat: something", kCreatedAt: o.now.AddDate(0, 0, -1).Format(time.RFC3339), kHeadRefName: "feat", kAuthor: map[string]any{kLogin: alice}},
		}}
		n["codeowners"] = map[string]any{kText: "* @giantswarm/" + team + "\n"}
		n["renovate0"] = map[string]any{kText: "{\n  // generated\n  \"extends\": [\"github>giantswarm/renovate-presets:default.json5\"],\n  packageRules: [{ enabled: false, matchPackageNames: [\"x\"] },],\n}\n"}
		n["ciConfig"] = map[string]any{kText: fakeCIConfig}
		n["ciWorkflows"] = map[string]any{kText: fakeCIWorkflows}
		n["defaultBranchRef"] = map[string]any{kName: mainBranch, kTarget: map[string]any{"statusCheckRollup": o.rollup()}}
		n["dockerfile"] = map[string]any{"id": "3"}
		n["helm"] = map[string]any{"id": "4"}
		return n
	case repoStray:
		n := base(false)
		n["codeowners"] = map[string]any{kText: "* @giantswarm/" + dissolvedTeam + "\n"}
		n["renovate1"] = map[string]any{kText: "{\"enabled\": false}"}
		n["openPRs"] = map[string]any{kTotalCount: 1, kNodes: []map[string]any{
			{kNumber: 1, kTitle: "Configure Renovate", kCreatedAt: o.now.AddDate(-1, 0, 0).Format(time.RFC3339), kHeadRefName: "renovate/configure", kAuthor: map[string]any{kLogin: renovateLogin}},
		}}
		return n
	case repoLegacy:
		n := base(false)
		n["ciConfig"] = map[string]any{kText: fakeCIConfig}
		if o.private[repoLegacy] {
			n["visibility"] = "PRIVATE"
		}
		return n
	case repoArchived:
		return base(true)
	}
	if o.repos != nil {
		if r := o.repos.get(name); r != nil {
			n := base(false)
			n[kCreatedAt], n["isEmpty"] = r.createdAt.UTC().Format(time.RFC3339), r.empty
			return n
		}
	}
	return nil
}

// rollup is the present repository's head status rollup: CircleCI's two job
// statuses (one still pending) among a GitHub Actions check run and another
// system's status.
func (o *fakeOrg) rollup() map[string]any {
	return map[string]any{kState: "PENDING", "contexts": map[string]any{
		kPageInfo: map[string]any{kHasNextPage: false},
		kNodes: []map[string]any{
			{kName: "lint"}, // a CheckRun: no context
			{kContext: circleBuild, kState: stateSuccessGQL, kCreatedAt: o.now.Add(-2 * time.Hour).Format(time.RFC3339)},
			{kContext: circlePush, kState: "PENDING", kCreatedAt: o.now.Add(-time.Hour).Format(time.RFC3339)},
			{kContext: "sonar", kState: "FAILURE", kCreatedAt: o.now.Format(time.RFC3339)},
		}}}
}

// releaseRollup is the present repository's release tag commit: built green
// by the release jobs.
func (o *fakeOrg) releaseRollup() map[string]any {
	return map[string]any{kState: stateSuccessGQL, "contexts": map[string]any{
		kPageInfo: map[string]any{kHasNextPage: false},
		kNodes: []map[string]any{
			{kContext: circleBuild, kState: stateSuccessGQL, kCreatedAt: o.now.AddDate(0, -1, 0).Format(time.RFC3339)},
			{kContext: "ci/circleci: push-to-registries-release", kState: stateSuccessGQL, kCreatedAt: o.now.AddDate(0, -1, 0).Format(time.RFC3339)},
		}}}
}

// The present repository's CircleCI configuration: devctl's setup config and
// a generated pipeline on architect 10.5.0 — amd64+arm64 images, the split
// China push, a chart — as the sweep reads them on the default branch.
const fakeCIConfig = `# DO NOT EDIT. This file is generated by ` + "`devctl gen circleci`" + ` and kept in sync
version: 2.1
setup: true
orbs:
  continuation: circleci/continuation@2.0.1
workflows:
  setup:
    jobs:
      - setup
`

const fakeCIWorkflows = `version: 2.1
orbs:
  architect: giantswarm/architect@10.5.0
workflows:
  build:
    jobs:
      - architect/go-build:
          name: go-build
      - architect/push-to-registries:
          name: push-to-registries-release
          split-china-push: true
          platforms: linux/amd64,linux/arm64
          requires: [go-build]
      - architect/sync-china-registry:
          name: sync-china-registry
          requires: [push-to-registries-release]
      - architect/push-to-app-catalog:
          name: push-chart
`

// history is the default branch's recent commits: the stray repository saw
// only bots for a year.
func (o *fakeOrg) history(name string) map[string]any {
	person := map[string]any{kCommittedDate: o.now.AddDate(0, 0, -3).Format(time.RFC3339), kMessageHeadline: "feat: by a person", kAuthor: map[string]any{kName: "Alice", kEmail: "alice@example.com", kUser: map[string]any{kLogin: alice}}}
	bot := map[string]any{kCommittedDate: o.now.AddDate(0, 0, -1).Format(time.RFC3339), kMessageHeadline: "chore(deps): bump", kAuthor: map[string]any{kName: "renovate[bot]", kEmail: "renovate@example.com", kUser: map[string]any{kLogin: renovateLogin}}}
	oldPerson := map[string]any{kCommittedDate: o.now.AddDate(-1, 0, 0).Format(time.RFC3339), kMessageHeadline: "initial", kAuthor: map[string]any{kName: "Someone", kEmail: "someone@example.com", kUser: nil}}
	switch name {
	case repoStray:
		return map[string]any{kTotalCount: 2, kNodes: []map[string]any{bot, oldPerson}}
	default:
		return map[string]any{kTotalCount: 2, kNodes: []map[string]any{bot, person}}
	}
}

// fakeChecker stands in for the engine: every accepted declaration converges
// with a default-icon finding.
// fakeChecker stands in for the engine's read-mode checks; a hold, when set,
// blocks every check until it is closed — a sweep that stays running for as
// long as a test needs it to.
type fakeChecker struct {
	calls atomic.Int32
	hold  chan struct{}
	// release answers a repository's latest release tag, as the engine's
	// release step would read it; nil is the present tag.
	release func(name string) string
}

// latestTag is the repository's latest release as the engine would read it
// on GitHub: the fake org's, when the stack wired it, else the present tag.
func (c *fakeChecker) latestTag(name string) string {
	if c.release != nil {
		if tag := c.release(name); tag != "" {
			return tag
		}
	}
	return presentTag
}

// latestTag is a repository's latest release tag as set for the release
// watch's page; empty when none was set.
func (o *fakeOrg) latestTag(name string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.releases[name].tag
}

func (c *fakeChecker) Check(_ context.Context, teamSlug string, entry reposetup.Entry) (*reconcile.Result, error) {
	c.calls.Add(1)
	if c.hold != nil {
		<-c.hold
	}
	now := time.Now()
	// The engine here has no CircleCI client, so its circleci and release
	// steps are skipped with these exact words (devctl's step_circleci.go);
	// the collector writes both from the record's sources.
	return &reconcile.Result{
		Repository: org + "/" + entry.Name, Declared: org + "/" + entry.Name, Team: teamSlug, Mode: reconcile.ModeCheck, StartedAt: now, FinishedAt: now, Converged: true,
		Steps: []reconcile.StepResult{
			{Step: reconcile.StepScaffold, Verdict: reconcile.VerdictOK, Summary: "scaffold present",
				Findings: []reconcile.Finding{{Kind: reconcile.FindingDefaultIcon, Advisory: true, Message: "the chart carries the template's icon", Fix: "replace helm/<chart>/icon.svg"}}},
			{Step: reconcile.StepCircleCI, Verdict: reconcile.VerdictSkipped, Summary: "no CircleCI client"},
			{Step: reconcile.StepRelease, Verdict: reconcile.VerdictSkipped, Summary: "release " + c.latestTag(entry.Name) + ": no CircleCI client to verify the pipeline"},
		},
	}, nil
}
