package collect

import (
	"context"
	"fmt"
	"io"

	"github.com/giantswarm/devctl/v8/pkg/githubclient"
	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/sirupsen/logrus"

	"github.com/giantswarm/giantswarm-repo-manager/internal/gh"
)

// Engine is the Checker on devctl's reconcile runner in check mode — the
// same steps `devctl repo status` reports, run as the read identity. The
// reported-checks rule comes from devctl's githubclient, built on the
// identity's current token per run (the installation token rotates). The
// runner gets no CircleCI client — this server holds no CircleCI token — so
// it skips its circleci and release steps; the collector writes those two
// from the record's own sources, the head's statuses and the reconciler's
// run (fillClientlessSteps), so the result reads like the reconciler's.
// The runner gets the devctl App's id, the reconciler's bypass actor on the
// ruleset `devctl: default branch`, so the protection step compares the
// ruleset's bypass list as the reconciler writes it and plans a repository
// still on classic protection as the reconciler would; the check writes
// nothing. Without the id the step compares the ruleset's rules alone and
// says so in its summary.
type Engine struct {
	org         string
	reader      *gh.Reader
	devctlAppID int64
	log         *logrus.Logger
}

// NewEngine builds the checker. devctlAppID is the numeric id of the devctl
// GitHub App; 0 leaves the ruleset's bypass list uncompared.
func NewEngine(org string, reader *gh.Reader, devctlAppID int64) *Engine {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return &Engine{org: org, reader: reader, devctlAppID: devctlAppID, log: log}
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
	runner := &reconcile.Runner{GitHub: e.reader.REST(), Checks: checks, DevctlAppID: e.devctlAppID, Log: io.Discard}
	res, err := runner.Run(ctx, reconcile.Request{Owner: e.org, Team: team, Entry: entry, Mode: reconcile.ModeCheck})
	if err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	return res, nil
}
