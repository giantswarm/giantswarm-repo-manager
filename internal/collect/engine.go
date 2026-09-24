package collect

import (
	"context"
	"fmt"
	"io"

	"github.com/giantswarm/devctl/v8/pkg/githubclient"
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
// The runner gets no devctl App id by default: GitHub shows a ruleset's bypass
// actors to identities that administer the repository, and the read identity
// reads, so the protection step compares the ruleset's rules alone and says
// `bypass actors not compared` in its summary. With an id the step would
// compare an empty list against the reconciler's and plan it on every
// aligned repository; the id is for a read identity that sees the actors.
type Engine struct {
	reader      *gh.Reader
	devctlAppID int64
	log         *logrus.Logger
}

// NewEngine builds the checker. devctlAppID is the numeric id of the devctl
// GitHub App; 0 leaves the ruleset's bypass list uncompared.
func NewEngine(reader *gh.Reader, devctlAppID int64) *Engine {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return &Engine{reader: reader, devctlAppID: devctlAppID, log: log}
}

// Check runs the request's steps for its accepted entry, in check mode
// whatever the request's mode.
func (e *Engine) Check(ctx context.Context, req reconcile.Request) (*reconcile.Result, error) {
	tok, err := e.reader.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine: read identity token: %w", err)
	}
	checks, err := githubclient.New(githubclient.Config{Logger: e.log, AccessToken: tok, BaseURL: e.reader.APIURL()})
	if err != nil {
		return nil, fmt.Errorf("engine: github client: %w", err)
	}
	runner := &reconcile.Runner{GitHub: e.reader.REST(), Checks: checks, DevctlAppID: e.devctlAppID, Log: io.Discard}
	req.Mode = reconcile.ModeCheck
	res, err := runner.Run(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	return res, nil
}
