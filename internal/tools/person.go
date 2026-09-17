package tools

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

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
	p, err := t.caller(ctx)
	if err != nil {
		return nil, err
	}
	who, err := reposetup.Remote{GitHub: p.gh, Owner: p.repo.Owner, Repo: p.repo.Name, Ref: p.repo.Ref}.Person(ctx, t.org())
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

// caller is the person with the client on the request's token and the
// team-files repository as them — no call to GitHub yet; person adds the
// teams.
func (t *tools) caller(ctx context.Context) (*person, error) {
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
	repo.As = id.Login
	return &person{login: id.Login, repo: repo, gh: c}, nil
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

// memberOfAny says whether the person is in one of the teams.
func (p *person) memberOfAny(teams []string) bool {
	for _, team := range teams {
		if p.member(team) {
			return true
		}
	}
	return false
}

// notAMember is the refusal of a tool reserved for a team's members: who the
// person is, which team was required, which teams they are in, and what is
// therefore not theirs to do.
func (p *person) notAMember(team, consequence string) error {
	return fmt.Errorf("%w: %s is not a member of %s (your teams: %s), so %s", ErrNotAMember, p.login, team, strings.Join(p.teams, ", "), consequence)
}

// teamFiles is giantswarm/github (or the configured stand-in) as client.
func (t *tools) teamFiles(c *github.Client) (teamfiles.Repo, error) {
	return teamfiles.New(c, t.d.TeamFilesRepository, t.d.TeamFilesRef)
}

// ErrNoApp is a read that needs the inventory App while none is configured;
// nothing stands in for it.
var ErrNoApp = errors.New("the inventory App giantswarm-repo-manager-inventory is not configured (GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, GITHUB_APP_PRIVATE_KEY_FILE): no identity for unattended reads")

// unattended is the team-files repository as the inventory App's
// installation — for reads that need no person: the policy file for a
// completion message.
func (t *tools) unattended() (teamfiles.Repo, error) {
	if t.d.App == nil {
		return teamfiles.Repo{}, ErrNoApp
	}
	return t.teamFiles(t.d.App.Installation())
}

// probeTTL is how long a token's team-files probe holds.
const probeTTL = 15 * time.Minute

// probes remembers, per token, whether the person's credential reaches the
// team-files repository: one GET /repos/{owner}/{name} per token per TTL,
// so get_info stays cheap.
type probes struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]probe
}

type probe struct {
	reachable bool
	until     time.Time
}

// reachable probes as the person, from the cache while it holds.
func (c *probes) reachable(ctx context.Context, p *person) (bool, error) {
	tok, _ := identity.TokenFromContext(ctx)
	key := sha256.Sum256([]byte(tok))
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now.Before(e.until) {
		c.mu.Unlock()
		return e.reachable, nil
	}
	c.mu.Unlock()
	reachable, err := p.repo.Reachable(ctx)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[[sha256.Size]byte]probe{}
	}
	for k, e := range c.entries {
		if !now.Before(e.until) {
			delete(c.entries, k)
		}
	}
	c.entries[key] = probe{reachable: reachable, until: now.Add(probeTTL)}
	return reachable, nil
}

// callerTeams are the team slugs the caller belongs to on GitHub, read as
// themselves; none when there is no caller or their teams are unreadable.
func (t *tools) callerTeams(ctx context.Context) (teams []string, source string) {
	p, err := t.person(ctx)
	if err != nil || len(p.teams) == 0 {
		return nil, None
	}
	return p.teams, teamsSourceGitHub
}
