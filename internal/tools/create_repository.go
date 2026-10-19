package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/giantswarm/mcp-toolkit/tooldesc"
	"github.com/google/go-github/v92/github"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-repo-manager/internal/identity"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

const (
	ToolValidateRepository = "validate_repository"

	argTeam    = "team"
	argEntry   = "entry"
	argEntries = "entries"

	// FirstRelease says where a created repository's v0.1.0 comes from when
	// its entry releases with auto-release; NoFirstRelease that none comes
	// on its own when it does not (firstRelease). FirstReleasePerRepository
	// is the result's firstRelease when its repositories differ.
	FirstRelease              = "v0.1.0 follows from the scaffold's auto-release"
	NoFirstRelease            = "no release follows on its own: the entry's release workflow (gen.ci.releaseWorkflow, else auto-release with gen.ci.generate: true) is not auto-release"
	FirstReleasePerRepository = "per repository: see repositories[].firstRelease"

	// releaseWorkflowAutoRelease is the gen.ci.releaseWorkflow that cuts
	// releases from the default branch.
	releaseWorkflowAutoRelease = "auto-release"

	// alignRefusal is why an entry that says align: false is refused: the
	// creation is the repository's opt-in to alignment, and the reconciler
	// run after the creation-only pull request sets the repository up only
	// from an opted-in entry.
	alignRefusal = "a repository created through the manager is opted in to alignment by its creation: leave `align` out or set it to true"
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
	// caller: the declaration pull request. Nil without the caller's token or
	// when the entries are refused (the problems say why nothing would be
	// written).
	Creation *CreationPlan `json:"creation,omitempty"`
}

// CreationPlan is the dry run of create_repository's one write as the
// caller: the declaration pull request. The reconciler creates, scaffolds and
// sets the repositories up once it merges, as its App.
type CreationPlan struct {
	// Refusal is why the pull request would not be opened — the team file
	// unreadable as the caller, an entry declared already. Nothing would be
	// written.
	Refusal string `json:"refusal,omitempty"`
	// PullRequest is the declaration pull request.
	PullRequest *PlannedPullRequest `json:"pullRequest,omitempty"`
}

// Created is create_repository's result in mode commit: the declaration
// pull request and the repositories the reconciler creates once it merges.
type Created struct {
	Repositories []CreatedRepository `json:"repositories"`
	*Committed
	// FirstRelease is the repositories' firstRelease when they agree, else
	// says to read each repository's.
	FirstRelease string `json:"firstRelease"`
	// Then says how to follow the repositories to readiness.
	Then string `json:"then"`
}

// createdThen is what follows a creation: the repository is minutes from
// usable, and watch_repository follows it there.
const createdThen = "the repositories do not exist yet: the pull request merges, the reconciler creates and sets them up and, where firstRelease says v0.1.0 follows, the first release builds — " +
	"follow each with " + ToolWatchRepository + " (repository, pullRequest: pullRequest.number) and report the link when it answers ready; " +
	"get_repository shows setup.pendingRun until the reconciler run of the pull request has reported"

// CreatedRepository is one repository the declaration pull request creates.
type CreatedRepository struct {
	Name string `json:"name"`
	// Repository is the URL on GitHub once the reconciler created it.
	Repository string `json:"repository"`
	// FirstRelease says whether its v0.1.0 follows on its own, as its
	// committed entry declares the release workflow.
	FirstRelease string `json:"firstRelease"`
	// PendingRun is the record's expectation of the reconciler run that
	// follows the pull request's merge, the way an Align now leaves one: the
	// inventory reads the run's artifact within its pending interval.
	PendingRun *inventory.PendingRun `json:"pendingRun,omitempty"`
}

// creationArguments are the arguments create_repository and its dry run
// validate_repository declare, one list for both: a client checks a call
// against the tool's schema and refuses an argument it does not declare, and
// it runs the dry run with exactly the arguments it commits.
func creationArguments() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString(argTeam, mcp.Required(), mcp.Description("The owning team's file, as its GitHub team slug: team-bumblebee, team-planeteers, …")),
		mcp.WithObject(argEntry, mcp.Description("One declaration as it goes into the team file: name, componentType, gen: {language, flavours}, description, visibility, …"), mcp.AdditionalProperties(true)),
		mcp.WithArray(argEntries, mcp.Description("Several declarations at once (a batch above three entries gets a person's review)."), mcp.Items(map[string]any{"type": "object", "additionalProperties": true})),
		mcp.WithString(argReason, mcp.Description("Why, for the pull request body.")),
	}
}

func (t *tools) registerValidate(s *mcpserver.MCPServer) {
	opts := append([]mcp.ToolOption{
		tooldesc.Describe("Preview create_repository with the same arguments: rendered entries, template, name check, refusals. Writes nothing.",
			"Also the review notices a person sees before the pull request exists. Run it with exactly the arguments you then commit."),
		readOnly(),
	}, creationArguments()...)
	s.AddTool(mcp.NewTool(ToolValidateRepository, opts...), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return result(t.validate(ctx, req.GetArguments()))
	})
}

// createRepository declares new repositories: after the dry run, the pull
// request adding the entries to repositories/<team>.yaml, opened as the
// caller. Once it merges, the reconciler of giantswarm/github creates each
// repository as its App, pushes the scaffold and sets it up.
func (t *tools) createRepository() WriteTool {
	return WriteTool{
		Name:    ToolCreateRepository,
		Summary: "Create new giantswarm repositories via a team-file pull request. For an existing repository: adopt_repository.",
		Details: "Once it merges, the reconciler creates, scaffolds and sets up each repository. Preview with validate_repository. " +
			"Visibility defaults to private: say `visibility: public` for a public repository.",
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

// validate is the dry run with the declaration pull request planned as the
// caller.
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
// in perr).
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
			v.AuthorLogin, v.AuthorTeams, v.TeamsSource = p.login, p.teams, teamsSourceGitHub
			req.AuthorTeams = p.teams
		}
	}
	res, err := t.runValidator(ctx, t.schemaReader(p), req)
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

// planCreation is the declaration pull request planned as the caller; a
// refusal is reported in its place. Nothing is written.
func (t *tools) planCreation(ctx context.Context, p *person, v *Validation, args map[string]any) *CreationPlan {
	entries, err := renderedEntries(v.Team, v.Entries)
	if err != nil {
		return &CreationPlan{Refusal: err.Error()}
	}
	pl, err := t.declaration(ctx, p, v, entries, args)
	if err != nil {
		return &CreationPlan{Refusal: err.Error()}
	}
	return &CreationPlan{PullRequest: &pl.PullRequest}
}

// parseEntries renders the tool's entries as a team file of the team, each
// opted in to alignment and with its visibility written out: the one place
// a created entry gets its fields, so the dry run's rendered entry, the
// validation and the pull request's diff all carry them.
func parseEntries(team string, entries []any) (*reposetup.TeamFile, error) {
	var b strings.Builder
	for _, e := range entries {
		e, err := optedIn(e)
		if err != nil {
			return nil, err
		}
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

// optedIn returns the entry with align: true — the creation is the opt-in
// to alignment — and refuses one that says otherwise. An entry without a
// visibility (the key missing, null or empty) gets visibility: private, the
// org's default: the developer portal writes none for its "Private" choice,
// and without the field the engine's create step made the repository public
// (devctl before giantswarm/devctl#2448) and align-files, which reads the
// file, treats it as public (the OSSF Scorecard workflow). A value that is
// not a mapping is returned as it is for the parser to refuse.
func optedIn(entry any) (any, error) {
	m, ok := entry.(map[string]any)
	if !ok {
		return entry, nil
	}
	if v, set := m[teamfiles.FieldAlign]; set && v != true {
		name, _ := m[teamfiles.FieldName].(string)
		return nil, fmt.Errorf("%s: %s", name, alignRefusal)
	}
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out[teamfiles.FieldAlign] = true
	if v := out[teamfiles.FieldVisibility]; v == nil || v == "" {
		out[teamfiles.FieldVisibility] = teamfiles.VisibilityPrivate
	}
	return out, nil
}

// runValidator runs the engine's validator over the repositories schema the
// team files in repo declare (validatorSchema), the App answering the name
// checks from its own budget.
func (t *tools) runValidator(ctx context.Context, repo *teamfiles.Repo, req reposetup.Request) (*reposetup.Result, error) {
	schema, err := t.validatorSchema(ctx, repo)
	if err != nil {
		return nil, err
	}
	v := reposetup.Validator{Schema: schema, Owner: t.org()}
	if t.d.App != nil {
		v.Names = reposetup.GitHubNameChecker{Repositories: repositoryGetter{t.d.App.Installation()}}
	}
	return v.Validate(ctx, req)
}

// declaration plans the declaration pull request: the entries
// (renderedEntries) inserted into the team file read as the person, refused
// when one is declared already. It writes nothing.
func (t *tools) declaration(ctx context.Context, p *person, v *Validation, entries []reposetup.Declaration, args map[string]any) (*Plan, error) {
	team := v.Team
	tf, err := p.repo.TeamFile(ctx, team)
	if err != nil {
		return nil, err
	}
	content, names, err := insertEntries(team, tf, entries)
	if err != nil {
		return nil, err
	}
	reason, _ := args[argReason].(string)
	pl := &Plan{Repository: t.org() + "/" + strings.Join(names, ","), Team: team, Accepted: true, change: teamfiles.Change{Files: map[string][]byte{tf.Path: content}}}
	repos := ""
	for _, d := range entries {
		repos += "\n- https://github.com/" + t.org() + "/" + d.Name + ": " + firstRelease(d)
	}
	notices := ""
	for _, n := range v.Notices {
		notices += "\n- " + string(n.Kind) + ": " + n.Message
	}
	if notices != "" {
		notices = "\n\nNotices from the dry run:" + notices
	}
	pl.finish(p.repo, p.login, "reposetup/create-"+strings.Join(names, "-"), fmt.Sprintf("feat(repositories): declare %s for %s", strings.Join(names, ", "), team),
		fmt.Sprintf("## Problem\n\nNew repositories of %s: `%s`.\n\n## Solution\n\nEntries added to `%s`. Once this merges, the reconciler creates the repositories as its App, pushes their scaffold and sets them up:%s%s\n\n%s\n\nOpened by giantswarm-repo-manager (`%s`) as @%s.",
			team, strings.Join(names, "`, `"), tf.Path, repos, notices, reasonLine(reason), ToolCreateRepository, p.login))
	return pl, nil
}

// commitCreate opens the declaration pull request as the person: the dry
// run and the pull request's plan (the refusals — the engine's, a taken
// name, a declaration already there — before any write), then the pull
// request. The reconciler creates the repositories once it merges.
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
		return nil, fmt.Errorf("the engine refuses the declaration: %s — fix it and run again (dryRun: true shows the rendered entries); no pull request was opened", strings.Join(refused, "; "))
	}
	entries, err := renderedEntries(v.Team, v.Entries)
	if err != nil {
		return nil, err
	}
	pl, err := t.declaration(ctx, p, v, entries, args)
	if err != nil {
		return nil, err
	}
	committed, err := t.commit(ctx, p, pl)
	if err != nil {
		return nil, err
	}
	out := &Created{Committed: committed, Then: createdThen}
	for _, d := range entries {
		name, first := t.org()+"/"+d.Name, firstRelease(d)
		out.Repositories = append(out.Repositories, CreatedRepository{
			Name:         d.Name,
			Repository:   "https://github.com/" + name,
			FirstRelease: first,
			PendingRun:   t.expectRun(ctx, p, name, inventory.ChangeCreated, committed.PullRequest),
		})
		switch out.FirstRelease {
		case "", first:
			out.FirstRelease = first
		default:
			out.FirstRelease = FirstReleasePerRepository
		}
	}
	return out, nil
}

// renderedEntries are the entries as the dry run rendered them, the
// creation defaults written out (gen.ci.generate: true): the pull request
// commits what validate_repository showed and the engine accepted, and
// align-files reads the file, not the dry run.
func renderedEntries(team string, entries []reposetup.Entry) ([]reposetup.Declaration, error) {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Rendered)
		if !strings.HasSuffix(e.Rendered, "\n") {
			b.WriteByte('\n')
		}
	}
	tf, err := reposetup.ParseTeamFile(team, strings.NewReader(b.String()))
	if err != nil {
		return nil, fmt.Errorf("parse the rendered entries: %w", err)
	}
	return tf.Entries, nil
}

// firstRelease says whether a created repository's v0.1.0 follows on its
// own: its entry's release workflow is auto-release (autoReleases).
func firstRelease(d reposetup.Declaration) string {
	f, err := d.Fields()
	if err == nil && autoReleases(&f) {
		return FirstRelease
	}
	return NoFirstRelease
}

// autoReleases says whether an entry's release workflow, resolved as
// align-files resolves it — gen.ci.releaseWorkflow when set, else
// auto-release with generated CI — is auto-release. It is devctl's
// releasewait.EntryReleaseModel, whose package the manager does not import
// for its registry dependencies.
func autoReleases(f *reposetup.Fields) bool {
	if f.Gen == nil || f.Gen.CI == nil {
		return false
	}
	ci := f.Gen.CI
	if ci.ReleaseWorkflow != "" {
		return ci.ReleaseWorkflow == releaseWorkflowAutoRelease
	}
	return ci.Generate != nil && *ci.Generate
}

// repositoryGetter adapts the App installation client to the engine's
// RepositoryGetter — the App's own rate budget answers the name checks.
type repositoryGetter struct{ c *github.Client }

func (g repositoryGetter) GetRepository(ctx context.Context, owner, repo string) (*github.Repository, error) {
	r, _, err := g.c.Repositories.Get(ctx, owner, repo)
	return r, err
}
