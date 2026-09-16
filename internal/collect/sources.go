package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// TeamFilesRepository and TeamFilesDir are where the declarations live.
const (
	TeamFilesRepository = "github"
	TeamFilesDir        = "repositories"
)

// sources are the desired-state inputs of a sweep: the team files, the
// catalog, the apps-to-teams mapping and the org's teams — one GraphQL query.
type sources struct {
	teams        map[string]bool
	declarations map[string]*declared // repository name → declaration
	catalog      map[string]bool
	mapping      map[string]string
	problems     []string
}

// declared is one team-file entry with the engine's verdict.
type declared struct {
	inventory.Declaration
	entry reposetup.Entry
}

const sourcesQuery = `
query($org: String!, $ghRepo: String!, $teamFilesDir: String!, $catalogPath: String!, $mcbRepo: String!, $mappingPath: String!, $teamsAfter: String) {
  organization(login: $org) {
    teams(first: 100, after: $teamsAfter) { pageInfo { hasNextPage endCursor } nodes { slug } }
  }
  github: repository(owner: $org, name: $ghRepo) {
    teamFiles: object(expression: $teamFilesDir) { ... on Tree { entries { name type object { ... on Blob { text } } } } }
    catalog: object(expression: $catalogPath) { ... on Blob { text } }
  }
  mcb: repository(owner: $org, name: $mcbRepo) {
    mapping: object(expression: $mappingPath) { ... on Blob { text } }
  }
  rateLimit { cost remaining limit resetAt }
}`

type blob struct {
	Text string `json:"text"`
}

type sourcesData struct {
	Organization struct {
		Teams struct {
			PageInfo pageInfo `json:"pageInfo"`
			Nodes    []struct {
				Slug string `json:"slug"`
			} `json:"nodes"`
		} `json:"teams"`
	} `json:"organization"`
	GitHub *struct {
		TeamFiles *struct {
			Entries []struct {
				Name   string `json:"name"`
				Type   string `json:"type"`
				Object *blob  `json:"object"`
			} `json:"entries"`
		} `json:"teamFiles"`
		Catalog *blob `json:"catalog"`
	} `json:"github"`
	MCB *struct {
		Mapping *blob `json:"mapping"`
	} `json:"mcb"`
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// fetchSources reads the desired state of the org.
func (c *Collector) fetchSources(ctx context.Context) (*sources, error) {
	base := reconcile.DefaultBaseline()
	vars := map[string]any{
		varOrg: c.opts.Org, "ghRepo": TeamFilesRepository, "teamFilesDir": "HEAD:" + TeamFilesDir,
		"catalogPath": "HEAD:" + base.CatalogPath, "mcbRepo": path.Base(base.MappingRepository), "mappingPath": "HEAD:" + base.MappingPath,
	}
	src := &sources{teams: map[string]bool{}, declarations: map[string]*declared{}, catalog: map[string]bool{}, mapping: map[string]string{}}
	for {
		var data sourcesData
		partial, err := c.gql.do(ctx, sourcesQuery, vars, &data)
		if err != nil && !errors.Is(err, ErrBudget) {
			return nil, fmt.Errorf("sources: %w", err)
		}
		src.problems = append(src.problems, partial...)
		for _, t := range data.Organization.Teams.Nodes {
			src.teams[t.Slug] = true
		}
		if vars["teamsAfter"] == nil {
			if err := c.parseSources(ctx, &data, src); err != nil {
				return nil, err
			}
		}
		if err != nil {
			return src, err
		}
		if !data.Organization.Teams.PageInfo.HasNextPage {
			return src, nil
		}
		vars["teamsAfter"] = data.Organization.Teams.PageInfo.EndCursor
	}
}

func (c *Collector) parseSources(ctx context.Context, data *sourcesData, src *sources) error {
	if data.GitHub == nil || data.GitHub.TeamFiles == nil {
		return fmt.Errorf("sources: %s/%s has no %s directory", c.opts.Org, TeamFilesRepository, TeamFilesDir)
	}
	schema, err := reposetup.EmbeddedSchema()
	if err != nil {
		return fmt.Errorf("sources: engine schema: %w", err)
	}
	validator := reposetup.Validator{Schema: schema, Owner: c.opts.Org}
	for _, e := range data.GitHub.TeamFiles.Entries {
		if e.Type != "blob" || !strings.HasSuffix(e.Name, ".yaml") || e.Object == nil {
			continue
		}
		file := TeamFilesDir + "/" + e.Name
		tf, err := reposetup.ParseTeamFile(reposetup.TeamOf(file), strings.NewReader(e.Object.Text))
		if err != nil {
			src.problems = append(src.problems, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		// Every declared entry stands for a repository that exists (or is the
		// reconciler's finding when gone): the engine validates it in
		// ModeExisting — the schema, not the creation rules, which apply to an
		// added entry in validate_repository's dry run alone (PRD D1).
		res, err := validator.Validate(ctx, reposetup.Request{TeamFile: tf, Mode: reposetup.ModeExisting})
		if err != nil {
			src.problems = append(src.problems, fmt.Sprintf("%s: validate: %v", file, err))
			continue
		}
		verdicts := map[string]reposetup.Entry{}
		for _, en := range res.Entries {
			verdicts[en.Name] = en
		}
		for _, d := range tf.Entries {
			decl := &declared{Declaration: inventory.Declaration{Team: tf.Team, File: file}, entry: verdicts[d.Name]}
			decl.Entry, _ = d.YAML()
			decl.Accepted = decl.entry.Accepted
			for _, p := range decl.entry.Problems {
				decl.Problems = append(decl.Problems, p.Field+": "+p.Message)
			}
			fillDeclaration(&decl.Declaration, d)
			if prev, dup := src.declarations[d.Name]; dup {
				src.problems = append(src.problems, fmt.Sprintf("%s is declared twice: %s and %s", d.Name, prev.File, file))
			}
			src.declarations[d.Name] = decl
		}
	}
	if data.GitHub.Catalog != nil {
		src.catalog = catalogNames(data.GitHub.Catalog.Text)
	} else {
		src.problems = append(src.problems, "catalog file missing")
	}
	if data.MCB != nil && data.MCB.Mapping != nil {
		src.mapping = mappingTeams(data.MCB.Mapping.Text)
	} else {
		src.problems = append(src.problems, "apps-to-teams mapping missing")
	}
	return nil
}

// fillDeclaration copies the fields the record shows from the entry.
func fillDeclaration(d *inventory.Declaration, decl reposetup.Declaration) {
	inst, err := decl.Instance()
	if err != nil {
		return
	}
	m, _ := inst.(map[string]any)
	d.ComponentType, _ = m["componentType"].(string)
	d.Lifecycle, _ = m["lifecycle"].(string)
	if gen, ok := m["gen"].(map[string]any); ok {
		d.Language, _ = gen["language"].(string)
		if fl, ok := gen["flavours"].([]any); ok {
			for _, f := range fl {
				if s, ok := f.(string); ok {
					d.Flavours = append(d.Flavours, s)
				}
			}
		}
	}
}

// catalogNames are the Component names of the multi-document catalog file.
func catalogNames(text string) map[string]bool {
	out := map[string]bool{}
	dec := yaml.NewDecoder(strings.NewReader(text))
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			return out
		}
		if doc.Kind == "Component" && doc.Metadata.Name != "" {
			out[doc.Metadata.Name] = true
		}
	}
}

// mappingTeams is the ConfigMap's data: app name → team.
func mappingTeams(text string) map[string]string {
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(text), &cm); err != nil || cm.Data == nil {
		return map[string]string{}
	}
	return cm.Data
}

// The identities the org's automation commits and opens pull requests as.
const (
	renovateBot   = "renovate"
	dependabotBot = "dependabot"
)

var (
	botLogins = map[string]bool{
		renovateBot: true, "renovate-bot": true, dependabotBot: true, "dependabot-preview": true,
		"github-actions": true, "architectbot": true, "taylorbot": true, "heraldbot": true, "catalogbot": true,
		"giantswarm-bot": true, "giantswarm-align-files": true, "pre-commit-ci": true,
		"mend-bolt-for-github": true, "mend-for-github-com": true, "renovate-approve": true,
	}
	botNames = map[string]bool{
		renovateBot: true, "renovate bot": true, dependabotBot: true, "taylorbot": true, "taylor bot": true,
		"architectbot": true, "architect": true, "heraldbot": true, "herald": true, "catalogbot": true,
		"giantswarm-bot": true, "flux": true, "github": true, "github actions": true, "pre-commit-ci": true,
	}
	botEmailPrefixes = []string{
		renovateBot, dependabotBot, "architect@", "taylorbot@", "heraldbot@",
		"bot@giantswarm.io", "github-actions", "noreply@github.com", "action@github.com",
	}
)

func isBot(login, name, email string) bool {
	login, name, email = strings.ToLower(login), strings.ToLower(name), strings.ToLower(email)
	if login != "" {
		return botLogins[login] || strings.HasSuffix(login, "[bot]")
	}
	if strings.Contains(name, "[bot]") || botNames[name] {
		return true
	}
	for _, p := range botEmailPrefixes {
		if strings.HasPrefix(email, p) {
			return true
		}
	}
	return false
}

func isRenovate(login, name, email string) bool {
	return strings.Contains(strings.ToLower(login+" "+name+" "+email), "renovate")
}

var codeownersTeam = regexp.MustCompile(`@([A-Za-z0-9-]+)/([A-Za-z0-9_.-]+)`)

// codeownersTeams are the org's teams a CODEOWNERS file names.
func codeownersTeams(org string, texts ...string) []string {
	seen := map[string]bool{}
	for _, t := range texts {
		for _, m := range codeownersTeam.FindAllStringSubmatch(t, -1) {
			if strings.EqualFold(m[1], org) {
				seen[strings.ToLower(m[2])] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// renovatePaths are the config files Renovate reads, in its precedence.
var renovatePaths = []string{"renovate.json5", "renovate.json", ".github/renovate.json5", ".github/renovate.json", ".renovaterc", ".renovaterc.json"}

var (
	json5LineComment  = regexp.MustCompile(`(?m)//[^\n]*$`)
	json5BlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	json5TrailingComa = regexp.MustCompile(`,(\s*[}\]])`)
	json5BareKey      = regexp.MustCompile(`(?m)([{,]\s*)([A-Za-z_$][A-Za-z0-9_$]*)\s*:`)
	renovateDisabled  = regexp.MustCompile(`(?m)^\s{0,2}["']?enabled["']?\s*:\s*false`)
)

// renovateState reads the Renovate config: configured, enabled (a top-level
// `enabled: false` only — one inside packageRules is not a disabled Renovate)
// and whether it extends the giantswarm presets.
func renovateState(path, text string) inventory.Renovate {
	r := inventory.Renovate{Configured: true, Path: path, Enabled: true, Preset: strings.Contains(text, "giantswarm/renovate-presets")}
	clean := json5BlockComment.ReplaceAllString(text, "")
	clean = json5LineComment.ReplaceAllString(clean, "")
	clean = json5TrailingComa.ReplaceAllString(clean, "$1")
	clean = json5BareKey.ReplaceAllString(clean, `$1"$2":`)
	var cfg map[string]any
	if err := json.Unmarshal([]byte(clean), &cfg); err == nil {
		if v, ok := cfg["enabled"].(bool); ok {
			r.Enabled = v
		}
		return r
	}
	r.Enabled = !renovateDisabled.MatchString(text)
	return r
}
