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
// its circleci and release steps are skipped; the record's CircleCI state
// comes from the head's statuses and the reconciler's run instead.
type Engine struct {
	org    string
	reader *gh.Reader
	log    *logrus.Logger
}

// NewEngine builds the checker.
func NewEngine(org string, reader *gh.Reader) *Engine {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return &Engine{org: org, reader: reader, log: log}
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
	runner := &reconcile.Runner{GitHub: e.reader.REST(), Checks: checks, Log: io.Discard}
	res, err := runner.Run(ctx, reconcile.Request{Owner: e.org, Team: team, Entry: entry, Mode: reconcile.ModeCheck})
	if err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	return res, nil
}
