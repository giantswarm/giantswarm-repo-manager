package tools

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/google/go-github/v92/github"

	"github.com/giantswarm/giantswarm-repo-manager/internal/gh"
	"github.com/giantswarm/giantswarm-repo-manager/internal/identity"
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

// person is the caller on GitHub: the client on the token muster put on the
// request — the person's own user token through the App
// giantswarm-repo-manager — their login (verified when the request was
// admitted) and the slugs of their teams in the org (read as themselves).
// Every write goes through person.repo — the commit and the pull request are
// theirs, and GitHub bounds them to the person's rights ∩ the App's.
type person struct {
	login string
	teams []string
	repo  teamfiles.Repo
	gh    *github.Client
}

// ErrNoToken is a request without a GitHub token: the server runs without
// OAuth, so nothing can act as a person.
var ErrNoToken = errors.New("no GitHub token on this request: the server acts as the caller only behind muster with oauth.enabled; " + identity.SignIn)

// person is the caller with the token of the request and their teams read as
// themselves. Without a token it fails with a reason the caller can act on.
func (t *tools) person(ctx context.Context) (*person, error) {
	id, ok := identity.FromContext(ctx)
	tok, hasToken := identity.TokenFromContext(ctx)
	if !ok || !hasToken {
		return nil, ErrNoToken
	}
	c, err := gh.AsPerson(t.d.GitHubAPIURL, tok)
	if err != nil {
		return nil, err
	}
	repo, err := t.teamFiles(c)
	if err != nil {
		return nil, err
	}
	p := &person{login: id.Login, repo: repo, gh: c}
	who, err := reposetup.Remote{GitHub: c, Owner: repo.Owner, Repo: repo.Name, Ref: repo.Ref}.Person(ctx, t.org())
	if err != nil {
		// The bearer was verified, so the person is known; the teams stay
		// unknown (the App lacks Organization members: read, or the person is
		// in no team) and the guard notices say so.
		t.d.Log.Warn("caller's teams unreadable", "login", p.login, "error", err)
		return p, nil
	}
	p.teams = who.Teams
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
// development token) — for reads that need no person: the policy file for a
// completion message.
func (t *tools) unattended() (teamfiles.Repo, error) {
	if t.d.App != nil {
		return t.teamFiles(t.d.App.Installation())
	}
	if t.d.Reader != nil {
		return t.teamFiles(t.d.Reader.REST())
	}
	return teamfiles.Repo{}, errors.New("no GitHub identity for unattended reads (the App, or GITHUB_TOKEN in development)")
}

// callerTeams are the team slugs the caller belongs to on GitHub, read as
// themselves; none when there is no caller or their teams are unreadable.
func (t *tools) callerTeams(ctx context.Context) (teams []string, source string) {
	p, err := t.person(ctx)
	if err != nil || len(p.teams) == 0 {
		return nil, None
	}
	return p.teams, "github"
}
