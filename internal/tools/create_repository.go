package tools

import (
	"bytes"
	"context"
	"fmt"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/google/go-github/v92/github"
	"github.com/mark3labs/mcp-go/mcp"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-repo-manager/internal/identity"
)

const (
	argTeam  = "team"
	argEntry = "entry"
)

// createRepository is the first write tool: the dry run is the engine's
// validation of the declaration (rendered entry with defaults, implied
// template, name check on GitHub through the App), exactly what the team-file
// PR will carry. The commit — the pull request to
// repositories/<team>.yaml as the caller — lands with the next slice.
func (t *tools) createRepository() WriteTool {
	return WriteTool{
		Name: ToolCreateRepository,
		Description: "Declare a new repository of the giantswarm org: an entry added to the team's file " +
			"(repositories/<team>.yaml in giantswarm/github). The dry run validates the entry with the devctl reposetup engine — " +
			"schema, creation rules, the implied template and whether the name is free on GitHub — and returns the rendered entry; " +
			"refusals are data (entries[].problems), an error means the validation could not run.",
		Options: []mcp.ToolOption{
			mcp.WithString(argTeam, mcp.Required(), mcp.Description("The owning team's file, as its GitHub team slug: team-bumblebee, team-planeteers, …")),
			mcp.WithObject(argEntry, mcp.Required(), mcp.Description("The declaration as it goes into the team file: name, componentType, gen: {language, flavours}, and the other fields of the repositories schema."), mcp.AdditionalProperties(true)),
		},
		DryRun: t.validateDeclaration,
	}
}

// validateDeclaration runs the engine's dry run for one entry.
func (t *tools) validateDeclaration(ctx context.Context, args map[string]any) (any, error) {
	team, _ := args[argTeam].(string)
	entry, _ := args[argEntry].(map[string]any)
	if team == "" || entry == nil {
		return nil, fmt.Errorf("%s and %s are required", argTeam, argEntry)
	}
	var buf bytes.Buffer
	if err := yaml.NewEncoder(&buf).Encode([]any{entry}); err != nil {
		return nil, fmt.Errorf("encode entry: %w", err)
	}
	tf, err := reposetup.ParseTeamFile(team, &buf)
	if err != nil {
		return nil, fmt.Errorf("parse entry as a team-file entry: %w", err)
	}
	schema, err := reposetup.EmbeddedSchema()
	if err != nil {
		return nil, fmt.Errorf("engine schema: %w", err)
	}
	v := reposetup.Validator{Schema: schema, Owner: reposetup.DefaultOwner}
	if t.d.App != nil {
		v.Names = reposetup.GitHubNameChecker{Repositories: repositoryGetter{t.d.App.Installation()}}
	}
	req := reposetup.Request{TeamFile: tf}
	if id, ok := identity.FromContext(ctx); ok {
		req.Author = id.String()
	}
	return v.Validate(ctx, req)
}

// repositoryGetter adapts the App installation client to the engine's
// RepositoryGetter — the App's own rate budget answers the name checks.
type repositoryGetter struct{ c *github.Client }

func (g repositoryGetter) GetRepository(ctx context.Context, owner, repo string) (*github.Repository, error) {
	r, _, err := g.c.Repositories.Get(ctx, owner, repo)
	return r, err
}
