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
	kTotalCount      = "totalCount"
	kText            = "text"
	kTitle           = "title"
	kHeadRefName     = "headRefName"
	kMessageHeadline = "messageHeadline"
	mainBranch       = "main"
	kUser            = "user"
	renovateLogin    = "renovate"
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
	circleBuild     = "ci/circleci: go-build"
	circlePush      = "ci/circleci: push-to-registries"
)

var teamFile = `# yaml-language-server: $schema=../repositories.schema.json
- name: ` + repoPresent + `
  componentType: service
  gen:
    language: go
    flavours: [app]
    ci:
      chartName: ` + repoPresent + `
- name: ` + repoGone + `
  componentType: service
  gen:
    language: go
    flavours: [generic]
- name: ` + repoLegacy + `
  componentType: service
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
}

var (
	historyAlias = regexp.MustCompile(`r(\d+): repository\(owner: \$org, name: "([^"]+)"\)`)
)

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
	o.mu.Unlock()

	data := map[string]any{"rateLimit": rl}
	var errs []map[string]any
	switch {
	case contains(req.Query, "teamFiles:"):
		data["organization"] = map[string]any{"teams": map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, kNodes: []map[string]any{{kSlug: team}, {kSlug: teamPlaneteers}}}}
		data[kGitHub] = map[string]any{
			"teamFiles": map[string]any{"entries": []map[string]any{{kName: "team-bumblebee.yaml", kType: "blob", "object": map[string]any{kText: teamFile}}}},
			"catalog":   map[string]any{kText: catalogFile},
		}
		data["mcb"] = map[string]any{"mapping": map[string]any{kText: mappingFile}}
	case contains(req.Query, "repositories(first:"):
		data["organization"] = map[string]any{"repositories": map[string]any{
			kTotalCount: 4, "pageInfo": map[string]any{"hasNextPage": false},
			kNodes: []map[string]any{o.node(repoPresent), o.node(repoLegacy), o.node(repoStray), o.node(repoArchived)},
		}}
	case contains(req.Query, "repository(owner: $org, name: $name)"):
		name, _ := req.Variables["name"].(string)
		if n := o.node(name); n != nil {
			// The history joins the head's status rollup the node carries.
			target := map[string]any{"history": o.history(name)}
			if ref, ok := n["defaultBranchRef"].(map[string]any); ok {
				if t, ok := ref["target"].(map[string]any); ok {
					target["statusCheckRollup"] = t["statusCheckRollup"]
				}
			}
			n["defaultBranchRef"] = map[string]any{kName: mainBranch, "target": target}
			data["repository"] = n
		} else {
			data["repository"] = nil
			errs = append(errs, map[string]any{"message": fmt.Sprintf("Could not resolve to a Repository with the name '%s/%s'.", org, name), kType: "NOT_FOUND", kPath: []string{kRepository}})
		}
	default:
		for _, m := range historyAlias.FindAllStringSubmatch(req.Query, -1) {
			if n := o.node(m[2]); n != nil {
				data["r"+m[1]] = map[string]any{kName: m[2], "defaultBranchRef": map[string]any{"target": map[string]any{"history": o.history(m[2])}}}
			} else {
				data["r"+m[1]] = nil
			}
		}
	}
	resp := map[string]any{"data": data}
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
			kName: name, "url": "https://github.com/" + org + "/" + name, "description": "", "visibility": "PUBLIC",
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
		n["latestRelease"] = map[string]any{"tagName": "v1.0.0", "publishedAt": o.now.AddDate(0, -1, 0).Format(time.RFC3339)}
		n["oldestIssues"] = map[string]any{kNodes: []map[string]any{{kNumber: 3, kTitle: "Dependency Dashboard"}}}
		n["openPRs"] = map[string]any{kTotalCount: 2, kNodes: []map[string]any{
			{kNumber: 10, kTitle: "fix(deps): update module x", kCreatedAt: o.now.AddDate(0, 0, -2).Format(time.RFC3339), kHeadRefName: "renovate/x", kAuthor: map[string]any{kLogin: renovateLogin}},
			{kNumber: 11, kTitle: "feat: something", kCreatedAt: o.now.AddDate(0, 0, -1).Format(time.RFC3339), kHeadRefName: "feat", kAuthor: map[string]any{kLogin: alice}},
		}}
		n["codeowners"] = map[string]any{kText: "* @giantswarm/" + team + "\n"}
		n["renovate0"] = map[string]any{kText: "{\n  // generated\n  \"extends\": [\"github>giantswarm/renovate-presets:default.json5\"],\n  packageRules: [{ enabled: false, matchPackageNames: [\"x\"] },],\n}\n"}
		n["circleci"] = map[string]any{"id": "2"}
		n["defaultBranchRef"] = map[string]any{kName: mainBranch, "target": map[string]any{"statusCheckRollup": o.rollup()}}
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
		return base(false)
	case repoArchived:
		return base(true)
	}
	return nil
}

// rollup is the present repository's head status rollup: CircleCI's two job
// statuses (one still pending) among a GitHub Actions check run and another
// system's status.
func (o *fakeOrg) rollup() map[string]any {
	return map[string]any{"state": "PENDING", "contexts": map[string]any{
		"pageInfo": map[string]any{"hasNextPage": false},
		kNodes: []map[string]any{
			{kName: "lint"}, // a CheckRun: no context
			{"context": circleBuild, "state": "SUCCESS", kCreatedAt: o.now.Add(-2 * time.Hour).Format(time.RFC3339)},
			{"context": circlePush, "state": "PENDING", kCreatedAt: o.now.Add(-time.Hour).Format(time.RFC3339)},
			{"context": "sonar", "state": "FAILURE", kCreatedAt: o.now.Format(time.RFC3339)},
		}}}
}

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
type fakeChecker struct{ calls atomic.Int32 }

func (c *fakeChecker) Check(_ context.Context, teamSlug string, entry reposetup.Entry) (*reconcile.Result, error) {
	c.calls.Add(1)
	now := time.Now()
	return &reconcile.Result{
		Repository: org + "/" + entry.Name, Declared: org + "/" + entry.Name, Team: teamSlug, Mode: reconcile.ModeCheck, StartedAt: now, FinishedAt: now, Converged: true,
		Steps: []reconcile.StepResult{{Step: reconcile.StepScaffold, Verdict: reconcile.VerdictOK, Summary: "scaffold present",
			Findings: []reconcile.Finding{{Kind: reconcile.FindingDefaultIcon, Message: "the chart carries the template's icon", Fix: "replace helm/<chart>/icon.svg"}}}},
	}, nil
}
