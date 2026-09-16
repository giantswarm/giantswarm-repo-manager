package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/google/go-github/v92/github"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"time"

	"github.com/giantswarm/giantswarm-repo-manager/internal/identity"
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

const (
	ToolValidateRepository = "validate_repository"

	argTeam    = "team"
	argEntry   = "entry"
	argEntries = "entries"
)

// Validation is validate_repository's result: the engine's dry run plus who
// the author is on GitHub, which decides the guard notices (PRD D4).
type Validation struct {
	*reposetup.Result
	Author      string   `json:"author,omitempty"`
	AuthorLogin string   `json:"authorLogin,omitempty"`
	AuthorTeams []string `json:"authorTeams,omitempty"`
	// TeamsSource says where the author's teams were read: github (as the
	// person), or none with the reason — the team-review notice then stands.
	TeamsSource string `json:"teamsSource"`
	// MachineApproved says whether the creation-only pull request would be
	// approved by the machine (no notice, every entry accepted).
	MachineApproved bool `json:"machineApproved"`
	// Findings are the engine's findings for the refused entries — the
	// entry-refused and gen-circleci-refused kinds the inventory and the
	// reconciler report, one per problem naming the field to fix.
	Findings []reconcile.Finding `json:"findings,omitempty"`
}

func (t *tools) registerValidate(s *mcpserver.MCPServer) {
	s.AddTool(mcp.NewTool(ToolValidateRepository,
		mcp.WithDescription("Read-only. The dry run of declaring one or more new repositories for a team, exactly what create_repository would put in "+
			"the pull request: each entry rendered with the schema's defaults, the implied template (giantswarm/template for Go, template-app for a chart, "+
			"the minimal scaffold otherwise) and its options, whether the name is free on GitHub, and the refusals of the creation rules as data "+
			"(entries[].problems, and as the engine's findings entry-refused / gen-circleci-refused). Plus the guard notices a person sees before any pull request exists: team-review when the author is outside the "+
			"owning team and team-planeteers, batch-review above three entries, names-unchecked without the App. Writes nothing. "+
			"Use it before create_repository; for an existing repository's state use get_repository."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString(argTeam, mcp.Required(), mcp.Description("The owning team's file, as its GitHub team slug: team-bumblebee, team-planeteers, …")),
		mcp.WithObject(argEntry, mcp.Description("One declaration as it goes into the team file: name, componentType, gen: {language, flavours}, description, visibility and the other fields of the repositories schema."), mcp.AdditionalProperties(true)),
		mcp.WithArray(argEntries, mcp.Description("Several declarations at once (a batch above three entries gets a person's review)."), mcp.Items(map[string]any{"type": "object", "additionalProperties": true})),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return result(t.validate(ctx, req.GetArguments()))
	})
}

// createRepository declares a new repository: the dry run is the engine's
// validation (rendered entry with defaults, implied template, name check on
// GitHub through the App, guard notices), the commit the creation-only pull
// request to repositories/<team>.yaml as the caller — which the Validate
// workflow of giantswarm/github approves by machine when no notice stands.
func (t *tools) createRepository() WriteTool {
	return WriteTool{
		Name: ToolCreateRepository,
		Description: "Declare one or more new repositories of the giantswarm org: entries added to the team's file (repositories/<team>.yaml in " +
			"giantswarm/github); the reconciler creates and scaffolds the repositories once the pull request merges. The dry run is validate_repository's " +
			"result; refusals are data (entries[].problems), an error means the validation could not run. mode commit opens the pull request as you — " +
			"machine-approved when you are in the team (or team-planeteers) and at most three entries are added, else your team reviews it.",
		Options: []mcp.ToolOption{
			mcp.WithString(argTeam, mcp.Required(), mcp.Description("The owning team's file, as its GitHub team slug: team-bumblebee, team-planeteers, …")),
			mcp.WithObject(argEntry, mcp.Description("The declaration as it goes into the team file: name, componentType, gen: {language, flavours}, and the other fields of the repositories schema."), mcp.AdditionalProperties(true)),
			mcp.WithArray(argEntries, mcp.Description("Several declarations at once."), mcp.Items(map[string]any{"type": "object", "additionalProperties": true})),
			mcp.WithString(argReason, mcp.Description("Why, for the pull request body.")),
		},
		DryRun: func(ctx context.Context, args map[string]any) (any, error) { return t.validate(ctx, args) },
		Commit: t.commitCreate,
	}
}

// entriesArg reads entry and entries into one list.
func entriesArg(args map[string]any) ([]any, error) {
	var out []any
	if e, ok := args[argEntry].(map[string]any); ok && e != nil {
		out = append(out, e)
	}
	if es, ok := args[argEntries].([]any); ok {
		out = append(out, es...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s or %s is required", argEntry, argEntries)
	}
	return out, nil
}

// validate runs the engine's dry run for the entries, with the author's
// GitHub teams when their grant allows reading them.
func (t *tools) validate(ctx context.Context, args map[string]any) (*Validation, error) {
	team, _ := args[argTeam].(string)
	team = strings.TrimSpace(team)
	if team == "" {
		return nil, fmt.Errorf("%s is required", argTeam)
	}
	entries, err := entriesArg(args)
	if err != nil {
		return nil, err
	}
	tf, err := parseEntries(team, entries)
	if err != nil {
		return nil, err
	}
	v := Validation{TeamsSource: None}
	req := reposetup.Request{TeamFile: tf}
	if id, ok := identity.FromContext(ctx); ok {
		req.Author = id.String()
		v.Author = req.Author
		p, err := t.person(ctx)
		switch {
		case err != nil:
			v.TeamsSource = None + ": " + err.Error()
		case len(p.teams) == 0:
			v.AuthorLogin = p.login
			v.TeamsSource = None + ": your grant lists no teams (read:org)"
		default:
			v.AuthorLogin, v.AuthorTeams, v.TeamsSource = p.login, p.teams, "github"
			req.AuthorTeams = p.teams
		}
	}
	res, err := t.validator().Validate(ctx, req)
	if err != nil {
		return nil, err
	}
	v.Result = res
	v.MachineApproved = res.Accepted && len(res.Notices) == 0
	now := time.Now()
	for _, e := range res.Entries {
		if !e.Accepted {
			v.Findings = append(v.Findings, reconcile.Refused(reconcile.Request{Owner: t.org(), Team: team, Entry: e, Added: true, Mode: reconcile.ModeCheck}, now).Findings()...)
		}
	}
	return &v, nil
}

// parseEntries renders the tool's entries as a team file of the team.
func parseEntries(team string, entries []any) (*reposetup.TeamFile, error) {
	var b strings.Builder
	for _, e := range entries {
		d, err := teamfiles.EntryFromValue(e)
		if err != nil {
			return nil, fmt.Errorf("parse entry as a team-file entry: %w", err)
		}
		y, err := d.YAML()
		if err != nil {
			return nil, err
		}
		b.WriteString(y)
		if !strings.HasSuffix(y, "\n") {
			b.WriteByte('\n')
		}
	}
	return reposetup.ParseTeamFile(team, strings.NewReader(b.String()))
}

// validator is the engine's validator with the App answering the name checks
// from its own budget.
func (t *tools) validator() reposetup.Validator {
	schema, err := reposetup.EmbeddedSchema()
	if err != nil {
		panic("embedded repositories schema does not compile: " + err.Error())
	}
	v := reposetup.Validator{Schema: schema, Owner: t.org()}
	if t.d.App != nil {
		v.Names = reposetup.GitHubNameChecker{Repositories: repositoryGetter{t.d.App.Installation()}}
	}
	return v
}

// commitCreate opens the creation-only pull request as the person.
func (t *tools) commitCreate(ctx context.Context, args map[string]any) (any, error) {
	p, err := t.person(ctx)
	if err != nil {
		return nil, err
	}
	v, err := t.validate(ctx, args)
	if err != nil {
		return nil, err
	}
	if !v.Accepted {
		refused := []string{}
		for _, e := range v.Entries {
			if !e.Accepted {
				refused = append(refused, e.Name+": "+problemsText(e.Problems))
			}
		}
		return nil, fmt.Errorf("the engine refuses the declaration: %s — fix it and run again (dryRun: true shows the rendered entries)", strings.Join(refused, "; "))
	}
	team := v.Team
	tf, err := p.repo.TeamFile(ctx, team)
	if err != nil {
		return nil, err
	}
	content := tf.Content
	names := make([]string, 0, len(v.Entries))
	entries, _ := entriesArg(args)
	parsed, err := parseEntries(team, entries)
	if err != nil {
		return nil, err
	}
	for _, d := range parsed.Entries {
		if _, dup := tf.Entries.Entry(d.Name); dup {
			return nil, fmt.Errorf("%s declares %s already: use update_repository", tf.Path, d.Name)
		}
		if content, err = reposetup.InsertEntry(team, content, d); err != nil {
			return nil, fmt.Errorf("insert %s: %w", d.Name, err)
		}
		names = append(names, d.Name)
	}
	reason, _ := args[argReason].(string)
	pl := &Plan{Repository: t.org() + "/" + strings.Join(names, ","), Team: team, Accepted: true, change: teamfiles.Change{Files: map[string][]byte{tf.Path: content}}}
	notices := ""
	for _, n := range v.Notices {
		notices += "\n- " + string(n.Kind) + ": " + n.Message
	}
	if notices != "" {
		notices = "\n\nNotices from the dry run:" + notices
	}
	pl.finish(p.repo, p.login, "reposetup/create-"+strings.Join(names, "-"), fmt.Sprintf("feat(repositories): declare %s for %s", strings.Join(names, ", "), team),
		fmt.Sprintf("## Problem\n\nNew repositories of %s: `%s`.\n\n## Solution\n\nEntries added to `%s`; the reconciler creates, scaffolds and sets them up once this merges, and their first release follows.%s\n\n%s\n\nOpened by giantswarm-repo-manager (`%s`) as the caller.",
			team, strings.Join(names, "`, `"), tf.Path, notices, reasonLine(reason), ToolCreateRepository))
	pl.Accepted = true
	return t.commit(ctx, p, pl)
}

// repositoryGetter adapts the App installation client to the engine's
// RepositoryGetter — the App's own rate budget answers the name checks.
type repositoryGetter struct{ c *github.Client }

func (g repositoryGetter) GetRepository(ctx context.Context, owner, repo string) (*github.Repository, error) {
	r, _, err := g.c.Repositories.Get(ctx, owner, repo)
	return r, err
}
