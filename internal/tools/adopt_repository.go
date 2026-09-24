package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/google/go-github/v92/github"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

// ToolAdoptRepository declares a repository that exists on GitHub and no
// team file declares: the way through for the inventory's unassigned scope.
const ToolAdoptRepository = "adopt_repository"

// ErrAdoptDeleted is the refusal of `lifecycle: deleted` in an adoption: a
// deletion needs the repository's name typed (set_lifecycle's confirm), and
// declaring and deleting in one step would hide it in a plain addition.
var ErrAdoptDeleted = errors.New("adopting and deleting in one step is refused: declare the repository first, then delete it with set_lifecycle, which needs its name typed as confirm")

// adoptRepository is the write for an existing, undeclared repository: its
// entry added to the team's file in a pull request the team reviews. It is
// create_repository without the create and scaffold steps -- the same entry
// shaping, the same insert, the same pull request -- validated for a
// repository that exists (the schema alone).
func (t *tools) adoptRepository() WriteTool {
	return WriteTool{
		Name: ToolAdoptRepository,
		Description: "Adopt a repository of the org that exists on GitHub and no team file declares (the inventory's unassigned scope): the entry you pass is added to the " +
			"team's file (repositories/<team>.yaml in giantswarm/github) in a pull request opened as you with auto-merge armed, and the ask with the Approve button goes to the team's " +
			"channel — an existing name is a plain addition, so the team reviews it: a member other than you approves. The entry is validated against the repositories schema (not the " +
			"creation rules, which are for repositories the manager creates) and rendered as create_repository renders a declaration; `align` is yours to set: with true the reconciler " +
			"run of the merge aligns the repository with its declared set-up and the company baseline, without it that run checks the repository and reports the drift, changing nothing. " +
			"`lifecycle: deprecated` or `archived` in the entry adopts the repository and ends its life in the one pull request; the entry then gets " + alignTrue + " beside the " +
			"lifecycle — else the reconciler would record the lifecycle and apply nothing — and the plan and the ask say so. `deleted` is refused here: declare first, then set_lifecycle " +
			"with the name typed. Refused before any write when the name is free on GitHub (a new repository: use create_repository) or declared already (the refusal names the team; " +
			"use update_repository, transfer_repository or set_lifecycle)." + pendingRunSentence,
		Options: []mcp.ToolOption{
			mcp.WithString(argRepository, mcp.Required(), mcp.Description("Repository name, with or without the org; it exists on GitHub and no team file declares it.")),
			mcp.WithString(argTeam, mcp.Required(), mcp.Description("The adopting team's file, as its GitHub team slug: team-bumblebee, team-planeteers, …")),
			mcp.WithObject(argEntry, mcp.Required(), mcp.Description("The declaration as it goes into the team file: componentType, description, visibility, lifecycle, align, gen and the other fields of the repositories schema; name is the repository and may be left out."), mcp.AdditionalProperties(true)),
			mcp.WithString(argReason, mcp.Description("Why, for the pull request body and the ask.")),
		},
		DryRun: func(ctx context.Context, args map[string]any) (any, error) {
			pn, err := t.planner(ctx)
			if err != nil {
				return nil, err
			}
			return t.planAdopt(ctx, pn, args)
		},
		Commit: func(ctx context.Context, args map[string]any) (any, error) {
			p, err := t.person(ctx)
			if err != nil {
				return nil, err
			}
			pl, err := t.planAdopt(ctx, &planner{repo: p.repo, person: p, as: p.login}, args)
			if err != nil {
				return nil, err
			}
			return t.commit(ctx, p, pl)
		},
	}
}

// adoptedEntry shapes the entry of an adoption: the repository is its name
// (another name is refused: the entry declares this repository), a lifecycle
// that ends the repository's life brings the opt-in with it -- the
// reconciler applies a lifecycle to an opted-in entry only -- and a deletion
// is refused. optedIn says the opt-in was added for the lifecycle.
func adoptedEntry(name string, entry map[string]any) (d reposetup.Declaration, optedIn bool, err error) {
	if n, _ := entry[teamfiles.FieldName].(string); n != "" && n != name {
		return d, false, fmt.Errorf("the entry's name %q is not %s: the entry declares the repository being adopted", n, name)
	}
	shaped := make(map[string]any, len(entry)+1)
	for k, v := range entry {
		shaped[k] = v
	}
	shaped[teamfiles.FieldName] = name
	if d, err = teamfiles.EntryFromValue(shaped); err != nil {
		return d, false, fmt.Errorf("parse entry as a team-file entry: %w", err)
	}
	f, err := d.Fields()
	if err != nil {
		return d, false, fmt.Errorf("reading the entry of %s: %w", name, err)
	}
	switch f.Lifecycle {
	case teamfiles.LifecycleDeleted:
		return d, false, ErrAdoptDeleted
	case teamfiles.LifecycleDeprecated, teamfiles.LifecycleArchived:
		if !f.Align {
			if d, err = teamfiles.SetField(d, teamfiles.FieldAlign, "true"); err != nil {
				return d, false, err
			}
			optedIn = true
		}
	}
	return d, optedIn, nil
}

// adoptVerb is what the pull request and the ask call the adoption: adopt,
// or adopt and end the repository's life.
func adoptVerb(lifecycle string) string {
	switch lifecycle {
	case teamfiles.LifecycleDeprecated, teamfiles.LifecycleArchived, teamfiles.LifecycleDeleted:
		return "adopt and " + verb(lifecycle)
	}
	return "adopt"
}

// adoptEffect is what the reconciler does once the adoption merges, for the
// pull request body: the lifecycle's effect, else the alignment or the
// check the entry's opt-in decides.
func adoptEffect(f reposetup.Fields) string {
	switch f.Lifecycle {
	case teamfiles.LifecycleDeprecated, teamfiles.LifecycleArchived, teamfiles.LifecycleDeleted:
		effect, _ := lifecycleEffect(f.Lifecycle)
		return effect
	}
	if f.Align {
		return "the reconciler aligns the repository with its declared set-up and the company baseline once this merges (" + alignTrue + " in the entry)"
	}
	return "the reconciler checks the repository against its declaration once this merges and reports the drift; without " + alignTrue + " in the entry nothing on GitHub or CircleCI changes"
}

// planAdopt renders the adoption as pn can read: the entry shaped, the
// adopting team's file read, the repository refused when declared anywhere
// or free on GitHub, the schema's verdict, the pull request and the ask.
func (t *tools) planAdopt(ctx context.Context, pn *planner, args map[string]any) (*Plan, error) {
	name, err := t.repositoryName(args)
	if err != nil {
		return nil, err
	}
	team := stringArg(args, argTeam)
	if team == "" {
		return nil, fmt.Errorf("%s is required", argTeam)
	}
	entry, _ := args[argEntry].(map[string]any)
	if entry == nil {
		return nil, fmt.Errorf("%s is required", argEntry)
	}
	d, optedIn, err := adoptedEntry(name, entry)
	if err != nil {
		return nil, err
	}
	f, err := d.Fields()
	if err != nil {
		return nil, fmt.Errorf("reading the entry of %s: %w", name, err)
	}
	tf, err := pn.repo.TeamFile(ctx, team)
	if err != nil {
		return nil, fmt.Errorf("adopting team: %w", err)
	}
	if by, err := t.declaredBy(ctx, pn.repo, name); err != nil {
		return nil, err
	} else if by != nil {
		return nil, fmt.Errorf("%s/%s is declared by %s in %s already: use update_repository, transfer_repository or set_lifecycle", t.org(), name, by.Team, by.Path)
	}
	verdict, err := t.existingVerdict(ctx, team, d)
	if err != nil {
		return nil, err
	}
	if verdict.NameCheck.Verdict == reposetup.VerdictFree {
		return nil, fmt.Errorf("%s/%s does not exist on GitHub: %s declares a repository that exists; create a new one with %s", t.org(), name, ToolAdoptRepository, ToolCreateRepository)
	}
	content, _, err := insertEntries(team, tf, []reposetup.Declaration{d})
	if err != nil {
		return nil, err
	}
	pl := &Plan{Repository: t.org() + "/" + name, Team: team, Problems: verdict.Problems,
		change: teamfiles.Change{Files: map[string][]byte{tf.Path: content}}, kind: inventory.ChangeAdded}
	pl.Entry, _ = d.YAML()
	reason, _ := args[argReason].(string)
	what := adoptVerb(f.Lifecycle)
	optsIn := ""
	if optedIn {
		optsIn = " The entry also opts the repository in to alignment (" + alignTrue + "), so the reconciler applies the lifecycle."
	}
	pl.finish(pn.repo, pn.as, "reposetup/adopt-"+name, fmt.Sprintf("chore(repositories): %s %s into %s", what, name, team),
		fmt.Sprintf("## Problem\n\n`%s/%s` exists on GitHub and no team file declares it: it cannot be configured, transferred, deprecated or archived, and every reconciler run checks it from a team alone.\n\n## Solution\n\nThe entry is added to `%s`; %s.%s\n\n%s\n\nOpened by giantswarm-repo-manager (`%s`) as the caller.",
			t.org(), name, tf.Path, adoptEffect(f), optsIn, reasonLine(reason), ToolAdoptRepository))
	pl.Ask = t.message(ctx, pn.repo, team, fmt.Sprintf("%s asks to %s `%s/%s` into %s: the repository exists on GitHub and no team file declares it.%s%s%s", pn.as, what, t.org(), name, team, optsIn, reasonSuffix(reason), decides(team, pn.as)), true)
	return pl, nil
}

// declaredBy is the team file declaring name, nil when none does. The
// inventory's record names the team when it knows one, which spares reading
// every team file; a record saying unassigned is not trusted, the files are.
func (t *tools) declaredBy(ctx context.Context, repo teamfiles.Repo, name string) (*teamfiles.TeamFile, error) {
	if t.d.Inventory != nil {
		if rec, err := t.d.Inventory.Get(ctx, t.org()+"/"+name); err == nil && rec.Declaration != nil {
			tf, err := repo.FindEntry(ctx, name, rec.Declaration.Team)
			if err == nil {
				return tf, nil
			}
			if !errors.Is(err, teamfiles.ErrEntryNotFound) {
				return nil, fmt.Errorf("reading the team files for %s: %w", name, err)
			}
			// The inventory's team no longer declares it: every file decides.
		}
	}
	tf, err := repo.FindEntry(ctx, name, "")
	switch {
	case err == nil:
		return tf, nil
	case errors.Is(err, teamfiles.ErrEntryNotFound):
		return nil, nil
	}
	return nil, fmt.Errorf("reading the team files for %s: %w", name, err)
}

// existingVerdict validates one entry the engine's way for a repository
// that exists (reposetup.ModeExisting): the schema, not the creation rules,
// and the name check reported without refusing.
func (t *tools) existingVerdict(ctx context.Context, team string, d reposetup.Declaration) (reposetup.Entry, error) {
	y, err := d.YAML()
	if err != nil {
		return reposetup.Entry{}, err
	}
	tf, err := reposetup.ParseTeamFile(team, strings.NewReader(y))
	if err != nil {
		return reposetup.Entry{}, err
	}
	res, err := t.runValidator(ctx, reposetup.Request{TeamFile: tf, Mode: reposetup.ModeExisting})
	if err != nil {
		return reposetup.Entry{}, err
	}
	for _, e := range res.Entries {
		if e.Name == d.Name {
			return e, nil
		}
	}
	return reposetup.Entry{}, fmt.Errorf("the engine returned no verdict for %s", d.Name)
}

// insertEntries adds the declarations to the team file's content, refusing
// one the file declares already; the names in order. The one insert of the
// creation-only pull request and of an adoption.
func insertEntries(team string, tf *teamfiles.TeamFile, ds []reposetup.Declaration) ([]byte, []string, error) {
	content := tf.Content
	names := make([]string, 0, len(ds))
	for _, d := range ds {
		if _, dup := tf.Entries.Entry(d.Name); dup {
			return nil, nil, fmt.Errorf("%s declares %s already: use %s", tf.Path, d.Name, ToolUpdateRepository)
		}
		var err error
		if content, err = reposetup.InsertEntry(team, content, d); err != nil {
			return nil, nil, fmt.Errorf("insert %s: %w", d.Name, err)
		}
		names = append(names, d.Name)
	}
	return content, names, nil
}

// initialOnly says whether the default branch of owner/name carries at most
// one commit -- the initial README of a creation, or the scaffold the engine
// makes the only commit -- read as the person: a creation interrupted after
// the create or the scaffold step. A history is somebody's work.
func initialOnly(ctx context.Context, c *github.Client, owner, name, branch string) (bool, error) {
	commits, resp, err := c.Repositories.ListCommits(ctx, owner, name, &github.CommitsListOptions{SHA: branch, ListOptions: github.ListOptions{PerPage: 2}})
	switch {
	case err != nil && resp != nil && (resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusNotFound):
		return true, nil // no commit yet ("Git Repository is empty")
	case err != nil:
		return false, fmt.Errorf("read the history of %s/%s as you: %w", owner, name, err)
	}
	return len(commits) <= 1, nil
}
