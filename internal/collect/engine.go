package collect

import (
	"context"
	"fmt"
	"io"

	"github.com/giantswarm/devctl/v8/pkg/circleciclient"
	"github.com/giantswarm/devctl/v8/pkg/githubclient"
	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/sirupsen/logrus"

	"github.com/giantswarm/giantswarm-repo-manager/internal/gh"
)

// Engine is the Checker on devctl's reconcile runner in check mode — the
// same steps `devctl repo status` reports, run as the read identity. The
// reported-checks rule comes from devctl's githubclient, built on the
// identity's current token per run (the installation token rotates). With a
// CircleCI client (the configured token) the runner reads each project —
// followed, setup workflows, checkout key, the latest release's pipeline —
// and check mode never writes to CircleCI; without one it skips its circleci
// and release steps, and the collector writes those two from the record's
// other sources, the head's statuses and the reconciler's run
// (fillClientlessSteps).
type Engine struct {
	org    string
	reader *gh.Reader
	circle *circleciclient.Client
	log    *logrus.Logger
}

// NewEngine builds the checker; circle may be nil.
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
