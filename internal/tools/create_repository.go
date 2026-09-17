package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/google/go-github/v92/github"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-repo-manager/internal/identity"
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

const (
	ToolValidateRepository = "validate_repository"

	argTeam    = "team"
	argEntry   = "entry"
	argEntries = "entries"

	// FirstRelease says where a created repository's v0.1.0 comes from.
	FirstRelease = "v0.1.0 follows from the scaffold's auto-release"
)

// Validation is validate_repository's result: the engine's dry run plus who
// the author is on GitHub, which decides the guard notices (PRD D4), and the
// creation as the caller would run it.
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
	// Creation is what create_repository in mode commit writes as the
	// caller, in order: each repository created, its scaffold pushed, the
	// pull request. Nil without the caller's token or when the entries are
	// refused (the problems say why nothing would be written).
	Creation *CreationPlan `json:"creation,omitempty"`
	// resumed names the entries validated for an existing repository: the
	// caller's own creations interrupted after the create step.
	resumed []string
}

// CreationPlan is the dry run of create_repository's three writes as the
// caller: the engine's create and scaffold steps in check mode, then the
// declaration pull request.
type CreationPlan struct {
	// Refusal is why the creation would not run — the engine's text when the
	// caller is not an owner of the organization, or the team file unreadable
	// as the caller. Nothing would be written.
	Refusal string `json:"refusal,omitempty"`
	// Repositories are the create and scaffold steps planned per entry.
	Repositories []RepositoryPlan `json:"repositories,omitempty"`
	// PullRequest is the declaration pull request that follows.
	PullRequest *PlannedPullRequest `json:"pullRequest,omitempty"`
	// Resumed names the entries whose repository exists and the caller
	// administers: a creation resumed, the create step skipped.
	Resumed []string `json:"resumed,omitempty"`
}

// RepositoryPlan is one repository's create and scaffold steps as planned.
type RepositoryPlan struct {
	Name string `json:"name"`
	// Repository is the URL when it exists already (a creation resumed).
	Repository string                 `json:"repository,omitempty"`
	Steps      []reconcile.StepResult `json:"steps"`
}

// Created is create_repository's result in mode commit: the repositories
// created and scaffolded as the caller, then the declaration pull request.
type Created struct {
	Repositories []CreatedRepository `json:"repositories"`
	*Committed
	FirstRelease string `json:"firstRelease"`
}

// CreatedRepository is one repository after the create and scaffold steps.
type CreatedRepository struct {
	Name string `json:"name"`
	// Repository is the URL on GitHub.
	Repository string `json:"repository"`
	// Created says this call created it; false when it existed already (a
	// creation resumed).
	Created bool `json:"created"`
	// ScaffoldCommit is the scaffold at the head of the default branch.
	ScaffoldCommit string                 `json:"scaffoldCommit,omitempty"`
	Steps          []reconcile.StepResult `json:"steps"`
}

// creationArguments are the arguments create_repository and its dry run
// validate_repository declare, one list for both: a client checks a call
// against the tool's schema and refuses an argument it does not declare, and
// it runs the dry run with exactly the arguments it commits.
func creationArguments() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString(argTeam, mcp.Required(), mcp.Description("The owning team's file, as its GitHub team slug: team-bumblebee, team-planeteers, …")),
		mcp.WithObject(argEntry, mcp.Description("One declaration as it goes into the team file: name, componentType, gen: {language, flavours}, description, visibility and the other fields of the repositories schema."), mcp.AdditionalProperties(true)),
		mcp.WithArray(argEntries, mcp.Description("Several declarations at once (a batch above three entries gets a person's review)."), mcp.Items(map[string]any{"type": "object", "additionalProperties": true})),
		mcp.WithString(argReason, mcp.Description("Why, for the pull request body: the dry run plans it, create_repository writes it.")),
	}
}

func (t *tools) registerValidate(s *mcpserver.MCPServer) {
	opts := append([]mcp.ToolOption{
		mcp.WithDescription("Read-only. The dry run of creating one or more new repositories for a team, exactly what create_repository would do: each entry " +
			"rendered with the schema's defaults, the implied template (giantswarm/template for Go, template-app for a chart, " +
			"the minimal scaffold otherwise) and its options, whether the name is free on GitHub, and the refusals of the creation rules as data " +
			"(entries[].problems, and as the engine's findings entry-refused / gen-circleci-refused). Plus the guard notices a person sees before any pull request exists: team-review when the author is outside the " +
			"owning team and team-planeteers, batch-review above three entries, names-unchecked without the App. And the creation as you (creation): the create and scaffold steps the engine " +
			"would run with your token and the pull request that follows, its body carrying your reason — or the refusal when you are not an owner of the org (the org lets only owners create repositories). Writes nothing. " +
			"Takes the same arguments as create_repository (team, entry or entries, reason), so you run it with exactly the arguments you commit. " +
			"Use it before create_repository; for an existing repository's state use get_repository."),
		mcp.WithReadOnlyHintAnnotation(true),
	}, creationArguments()...)
	s.AddTool(mcp.NewTool(ToolValidateRepository, opts...), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return result(t.validate(ctx, req.GetArguments()))
	})
}

// createRepository creates new repositories as the caller: after the dry
// run, with the caller's own GitHub token, the engine's create step (the
// repository, the caller its admin) and scaffold step (one commit on the
// default branch), then the creation-only pull request to
// repositories/<team>.yaml — which the Validate workflow of giantswarm/github
// approves by machine when no notice stands, and whose merge sets the
// repositories up through the reconciler, which never creates.
func (t *tools) createRepository() WriteTool {
	return WriteTool{
		Name: ToolCreateRepository,
		Description: "Create one or more new repositories of the giantswarm org as you, in this order: the repository (you are its admin), one scaffold commit " +
			"on its default branch rendered by the engine from the declaration (" + FirstRelease + "), then the pull request adding the entries to the team's file " +
			"(repositories/<team>.yaml in giantswarm/github) — the reconciler sets the repositories up once it merges and never creates. An owner role in the org is " +
			"required: the org lets only owners create repositories, and the dry run tells you so before any write. dryRun: true is validate_repository's result with the " +
			"creation plan (creation: the create and scaffold steps, the pull request) and writes nothing; refusals are data (entries[].problems, creation.refusal), an error " +
			"means the validation could not run. mode commit refuses before any write — the engine's refusals, a taken name, a missing owner role — and resumes a creation " +
			"interrupted by a failure: a repository you administer is not created again, a scaffold on the default branch not pushed again, an open pull request for the " +
			"branch is reported. The pull request is machine-approved when you are in the team (or team-planeteers) and at most three entries are added, else your team reviews it.",
		Options: creationArguments(),
		DryRun:  func(ctx context.Context, args map[string]any) (any, error) { return t.validate(ctx, args) },
		Commit:  t.commitCreate,
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

// validate is the dry run with the creation as the caller planned: the
// engine's create and scaffold steps in check mode (the caller's role in the
// org read, nothing written) and the pull request.
func (t *tools) validate(ctx context.Context, args map[string]any) (*Validation, error) {
	var p *person
	var perr error
	if _, ok := identity.FromContext(ctx); ok {
		// The bearer was verified, so the person is known; without a usable
		// token nothing runs as them and the plan stays empty.
		p, perr = t.person(ctx)
	}
	v, err := t.dryRun(ctx, args, p, perr)
	if err != nil {
		return nil, err
	}
	if p != nil && v.Accepted {
		v.Creation = t.planCreation(ctx, p, v, args)
	}
	return v, nil
}

// dryRun runs the engine's validation for the entries, with the author's
// GitHub teams when they can be read as the author (p; nil with the reason
// in perr), and resumes the caller's own interrupted creations.
func (t *tools) dryRun(ctx context.Context, args map[string]any, p *person, perr error) (*Validation, error) {
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
		switch {
		case p == nil:
			v.TeamsSource = None + ": " + perr.Error()
		case len(p.teams) == 0:
			v.AuthorLogin = p.login
			v.TeamsSource = None + ": no team of yours in the org is readable as you (the App giantswarm-repo-manager's Organization members: read)"
		default:
			v.AuthorLogin, v.AuthorTeams, v.TeamsSource = p.login, p.teams, "github"
			req.AuthorTeams = p.teams
		}
	}
	res, err := t.validator().Validate(ctx, req)
	if err != nil {
		return nil, err
	}
	if p != nil {
		if res, v.resumed, err = t.resumed(ctx, p, req, res); err != nil {
			return nil, err
		}
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

// resumed validates again, for repositories that exist, the entries refused
// for their taken name alone whose repository the caller administers — the
// caller's own creations interrupted after the create step. Anyone else's
// repository stays refused. The ModeExisting verdicts replace those entries;
// the guard notices are the creation's.
func (t *tools) resumed(ctx context.Context, p *person, req reposetup.Request, res *reposetup.Result) (*reposetup.Result, []string, error) {
	var names []string
	for _, e := range res.Entries {
		if !e.RefusedForTakenName() {
			continue
		}
		repo, resp, err := p.gh.Repositories.Get(ctx, t.org(), e.Name)
		switch {
		case err != nil && resp != nil && resp.StatusCode == http.StatusNotFound:
			continue
		case err != nil:
			return nil, nil, fmt.Errorf("read %s/%s as you: %w", t.org(), e.Name, err)
		case strings.EqualFold(repo.GetFullName(), t.org()+"/"+e.Name) && repo.GetPermissions().GetAdmin():
			names = append(names, e.Name)
		}
	}
	if len(names) == 0 {
		return res, nil, nil
	}
	req.Names, req.Mode = names, reposetup.ModeExisting
	existing, err := t.validator().Validate(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	byName := make(map[string]reposetup.Entry, len(existing.Entries))
	for _, e := range existing.Entries {
		byName[e.Name] = e
	}
	res.Accepted = true
	for i, e := range res.Entries {
		if x, ok := byName[e.Name]; ok {
			res.Entries[i] = x
		}
		res.Accepted = res.Accepted && res.Entries[i].Accepted
	}
	return res, names, nil
}

// planCreation is the creation as the caller, in check mode: the engine
// reads the caller's role in the org and plans the create and scaffold steps
// of every entry; the declaration pull request is rendered. Nothing is
// written; a refusal is reported in place of the plan.
func (t *tools) planCreation(ctx context.Context, p *person, v *Validation, args map[string]any) *CreationPlan {
	plan := &CreationPlan{Resumed: v.resumed}
	runner := t.engine(ctx, p)
	for _, e := range v.Entries {
		res, err := runner.Create(ctx, reconcile.CreateRequest{Owner: t.org(), Team: v.Team, Entry: e, Mode: reconcile.ModeCheck})
		if err != nil {
			plan.Refusal = t.refusal(err).Error()
			return plan
		}
		plan.Repositories = append(plan.Repositories, RepositoryPlan{Name: e.Name, Repository: res.URL, Steps: res.Steps})
	}
	pl, err := t.declaration(ctx, p, v, args)
	if err != nil {
		plan.Refusal = err.Error()
		return plan
	}
	plan.PullRequest = &pl.PullRequest
	return plan
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

// engine is the set-up engine as the caller: their client creates the
// repository and pushes the scaffold, their token downloads the templates
// (giantswarm/template is private). The steps' lines go to the log.
func (t *tools) engine(ctx context.Context, p *person) reconcile.Runner {
	r := reconcile.Runner{GitHub: p.gh, Renderer: t.d.Scaffold, Log: stepLog{log: t.d.Log, as: p.login}}
	if r.Renderer == nil {
		tok, _ := identity.TokenFromContext(ctx)
		r.Renderer = reposetup.Renderer{Templates: reposetup.GitHubTemplates{Token: tok}}
	}
	return r
}

// refusal is the engine's refusal of the caller as the tool's error: devctl's
// text, word for word, for a caller who is not an owner of the org.
func (t *tools) refusal(err error) error {
	if reconcile.IsNotOwner(err) {
		return errors.New(reconcile.NotOwnerRefusal(t.org()))
	}
	return err
}

// stepLog forwards the engine's step lines to the server log.
type stepLog struct {
	log *slog.Logger
	as  string
}

func (l stepLog) Write(b []byte) (int, error) {
	l.log.Info(strings.TrimSpace(string(b)), "tool", ToolCreateRepository, "as", l.as)
	return len(b), nil
}

// declaration plans the creation-only pull request: the entries inserted
// into the team file read as the person, refused when one is declared
// already. It writes nothing.
func (t *tools) declaration(ctx context.Context, p *person, v *Validation, args map[string]any) (*Plan, error) {
	team := v.Team
	tf, err := p.repo.TeamFile(ctx, team)
	if err != nil {
		return nil, err
	}
	content := tf.Content
	entries, _ := entriesArg(args)
	parsed, err := parseEntries(team, entries)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(parsed.Entries))
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
	repos := ""
	for _, n := range names {
		repos += "\n- https://github.com/" + t.org() + "/" + n
	}
	notices := ""
	for _, n := range v.Notices {
		notices += "\n- " + string(n.Kind) + ": " + n.Message
	}
	if notices != "" {
		notices = "\n\nNotices from the dry run:" + notices
	}
	pl.finish(p.repo, p.login, "reposetup/create-"+strings.Join(names, "-"), fmt.Sprintf("feat(repositories): declare %s for %s", strings.Join(names, ", "), team),
		fmt.Sprintf("## Problem\n\nNew repositories of %s: `%s`.\n\n## Solution\n\nThe repositories exist — created and scaffolded as @%s before this pull request was opened, so %s:%s\n\nEntries added to `%s`; the reconciler sets the repositories up once this merges and never creates.%s\n\n%s\n\nOpened by giantswarm-repo-manager (`%s`) as the caller.",
			team, strings.Join(names, "`, `"), p.login, FirstRelease, repos, tf.Path, notices, reasonLine(reason), ToolCreateRepository))
	return pl, nil
}

// commitCreate creates the repositories as the person, in order: the dry
// run and the pull request's plan (the refusals — the engine's, a taken
// name, a declaration already there — before any write), then per entry the
// engine's create step (the caller's role in the org read first; a non-owner
// is refused with the engine's text) and scaffold step, then the pull
// request. A step that fails ends the call with its cause; the next call
// resumes where it stopped.
func (t *tools) commitCreate(ctx context.Context, args map[string]any) (any, error) {
	p, err := t.person(ctx)
	if err != nil {
		return nil, err
	}
	v, err := t.dryRun(ctx, args, p, nil)
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
		return nil, fmt.Errorf("the engine refuses the declaration: %s — fix it and run again (dryRun: true shows the rendered entries); nothing was created", strings.Join(refused, "; "))
	}
	pl, err := t.declaration(ctx, p, v, args)
	if err != nil {
		return nil, err
	}
	runner := t.engine(ctx, p)
	out := &Created{FirstRelease: FirstRelease}
	for _, e := range v.Entries {
		res, err := runner.Create(ctx, reconcile.CreateRequest{Owner: t.org(), Team: v.Team, Entry: e, Mode: reconcile.ModeRepair})
		if err != nil {
			return nil, t.refusal(err)
		}
		if failed := res.Failed(); len(failed) > 0 {
			return nil, fmt.Errorf("%s: the %s step failed: %s — fix the cause and run again; the creation resumes where it stopped (a repository you administer is not created again, a scaffold on the default branch not pushed again)", res.Repository, failed[0].Step, failed[0].Summary)
		}
		out.Repositories = append(out.Repositories, CreatedRepository{Name: e.Name, Repository: res.URL, Created: res.Created, ScaffoldCommit: res.ScaffoldCommit, Steps: res.Steps})
		t.d.Log.Info("repository created as the caller", "repository", res.Repository, "created", res.Created, "scaffoldCommit", res.ScaffoldCommit, "as", p.login)
	}
	committed, err := t.commit(ctx, p, pl)
	if err != nil {
		return nil, fmt.Errorf("the repositories stand, the pull request does not: %w — run again to open it", err)
	}
	out.Committed = committed
	return out, nil
}

// repositoryGetter adapts the App installation client to the engine's
// RepositoryGetter — the App's own rate budget answers the name checks.
type repositoryGetter struct{ c *github.Client }

func (g repositoryGetter) GetRepository(ctx context.Context, owner, repo string) (*github.Repository, error) {
	r, _, err := g.c.Repositories.Get(ctx, owner, repo)
	return r, err
}
