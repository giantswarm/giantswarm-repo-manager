package collect

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/giantswarm/devctl/v8/pkg/circleciclient"
	"github.com/giantswarm/devctl/v8/pkg/githubclient"
	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/sirupsen/logrus"

	"github.com/giantswarm/giantswarm-repo-manager/internal/gh"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
)

// Engine is the Checker on devctl's reconcile runner in check mode — the
// same steps `devctl repo status` reports, run as the read identity. The
// reported-checks rule comes from devctl's githubclient, built on the
// identity's current token per run (the installation token rotates).
type Engine struct {
	org    string
	reader *gh.Reader
	circle *circleciclient.Client
	log    *logrus.Logger
}

// NewEngine builds the checker; circle may be nil (the circleci and release
// steps are then skipped).
func NewEngine(org string, reader *gh.Reader, circle *circleciclient.Client) *Engine {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return &Engine{org: org, reader: reader, circle: circle, log: log}
}

// Check runs every step in check mode for the accepted entry.
func (e *Engine) Check(ctx context.Context, team string, entry reposetup.Entry) (*reconcile.Result, error) {
	tok, err := e.reader.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine: read identity token: %w", err)
	}
	checks, err := githubclient.New(githubclient.Config{Logger: e.log, AccessToken: tok, BaseURL: e.reader.APIURL()})
	if err != nil {
		return nil, fmt.Errorf("engine: github client: %w", err)
	}
	runner := &reconcile.Runner{GitHub: e.reader.REST(), Checks: checks, CircleCI: e.circle, Log: io.Discard}
	res, err := runner.Run(ctx, reconcile.Request{Owner: e.org, Team: team, Entry: entry, Mode: reconcile.ModeCheck})
	if err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	return res, nil
}

// CircleCIClient reads projects with devctl's CircleCI client (read scope).
type CircleCIClient struct {
	c     *circleciclient.Client
	calls atomic.Int64
}

// NewCircleCI builds the client for token.
func NewCircleCI(token string) (*CircleCIClient, error) {
	c, err := circleciclient.New(circleciclient.Config{Token: token})
	if err != nil {
		return nil, fmt.Errorf("circleci: %w", err)
	}
	return &CircleCIClient{c: c}, nil
}

// Client is the underlying client, for the engine.
func (c *CircleCIClient) Client() *circleciclient.Client { return c.c }

// Calls counts the API calls made so far.
func (c *CircleCIClient) Calls() int { return int(c.calls.Load()) }

// Project is followed / setup workflows / last pipeline for org/repo.
func (c *CircleCIClient) Project(ctx context.Context, org, repo string) *inventory.CircleCI {
	out := &inventory.CircleCI{}
	c.calls.Add(1)
	if _, err := c.c.GetProject(ctx, org, repo); err != nil {
		if !circleciclient.IsNotFound(err) {
			out.Error = err.Error()
		}
		return out
	}
	out.Followed = true
	c.calls.Add(1)
	if s, err := c.c.GetProjectSettings(ctx, org, repo); err != nil {
		out.Error = err.Error()
	} else {
		out.SetupWorkflows = s.Advanced.SetupWorkflows
	}
	c.calls.Add(1)
	ps, err := c.c.ListPipelines(ctx, org, repo)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if len(ps) > 0 {
		p := ps[0]
		out.LastPipeline = &inventory.Pipeline{Number: p.Number, State: p.State, CreatedAt: p.CreatedAt, Ref: firstNonEmpty(p.VCS.Tag, p.VCS.Branch)}
	}
	return out
}
