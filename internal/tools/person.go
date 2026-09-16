package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/google/go-github/v92/github"

	"github.com/giantswarm/giantswarm-repo-manager/internal/broker"
	"github.com/giantswarm/giantswarm-repo-manager/internal/gh"
	"github.com/giantswarm/giantswarm-repo-manager/internal/identity"
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

// person is the caller on GitHub: the client on their released grant, their
// login and the slugs of their teams in the org (read as themselves). Every
// write goes through person.repo — the commit and the pull request are theirs.
type person struct {
	login string
	teams []string
	repo  teamfiles.Repo
	gh    *github.Client
}

// ErrNoGrant is the caller without a GitHub grant.
var ErrNoGrant = errors.New("no GitHub grant for you yet: connect GitHub in muster (core_auth_login on the GitHub server), then call again")

// person exchanges the caller's id_token for their grant and reads who they
// are on GitHub. Without a broker, an identity or a grant it fails with a
// reason the caller can act on.
func (t *tools) person(ctx context.Context) (*person, error) {
	if t.d.Broker == nil {
		return nil, errors.New("broker client not configured (MUSTER_URL, BROKER_CLIENT_ID, BROKER_CLIENT_SECRET): no way to act as you")
	}
	tok, ok := identity.TokenFromContext(ctx)
	if !ok {
		return nil, errors.New("the request carried no id_token to exchange for your GitHub grant (is the server behind muster with OAuth on?)")
	}
	grant, err := t.d.Broker.Exchange(ctx, tok)
	if err != nil {
		if errors.Is(err, broker.ErrNoGrant) {
			return nil, ErrNoGrant
		}
		return nil, err
	}
	c, err := gh.AsPerson(t.d.GitHubAPIURL, grant.AccessToken)
	if err != nil {
		return nil, err
	}
	repo, err := t.teamFiles(c)
	if err != nil {
		return nil, err
	}
	p := &person{repo: repo, gh: c}
	who, err := reposetup.Remote{GitHub: c, Owner: repo.Owner, Repo: repo.Name, Ref: repo.Ref}.Person(ctx, t.org())
	if err != nil {
		// A grant without read:org still identifies the person; the teams
		// stay unknown and the guard notices say so.
		login, lerr := gh.Login(ctx, t.d.GitHubAPIURL, grant.AccessToken)
		if lerr != nil {
			return nil, fmt.Errorf("the grant was released but GitHub refuses it: %w", lerr)
		}
		p.login = login
		t.d.Log.Warn("caller's teams unreadable", "login", login, "error", err)
		return p, nil
	}
	p.login, p.teams = who.Login, who.Teams
	sort.Strings(p.teams)
	return p, nil
}

// member says whether the person is in the team.
func (p *person) member(team string) bool {
	for _, t := range p.teams {
		if strings.EqualFold(t, team) {
			return true
		}
	}
	return false
}

// teamFiles is giantswarm/github (or the configured stand-in) as client.
func (t *tools) teamFiles(c *github.Client) (teamfiles.Repo, error) {
	return teamfiles.New(c, t.d.TeamFilesRepository, t.d.TeamFilesRef)
}

// unattended is the team-files repository as the App installation (or the
// development token) — for reads that need no person: the dry run of a
// caller without a grant, the policy file for a completion message.
func (t *tools) unattended() (teamfiles.Repo, error) {
	if t.d.App != nil {
		return t.teamFiles(t.d.App.Installation())
	}
	if t.d.Reader != nil {
		return t.teamFiles(t.d.Reader.REST())
	}
	return teamfiles.Repo{}, errors.New("no GitHub identity for unattended reads (the App, or GITHUB_TOKEN in development)")
}

// callerTeams are the team slugs the caller belongs to as far as this call
// can tell: the person's GitHub teams when their grant is available, else the
// IdP groups normalised to slugs (giantswarm:Team Bumblebee → team-bumblebee).
func (t *tools) callerTeams(ctx context.Context) (teams []string, source string) {
	if p, err := t.person(ctx); err == nil && len(p.teams) > 0 {
		return p.teams, "github"
	}
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, None
	}
	seen := map[string]bool{}
	for _, g := range id.Groups {
		if s := teamSlug(g); s != "" && !seen[s] {
			seen[s] = true
			teams = append(teams, s)
		}
	}
	sort.Strings(teams)
	return teams, "idp-groups"
}

// teamSlug normalises an IdP group name to a GitHub team slug.
func teamSlug(group string) string {
	if i := strings.LastIndex(group, ":"); i >= 0 {
		group = group[i+1:]
	}
	s := strings.ToLower(strings.TrimSpace(group))
	s = strings.ReplaceAll(s, " ", "-")
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "team-") {
		s = "team-" + s
	}
	return s
}
