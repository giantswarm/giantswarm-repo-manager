package tools

import (
	"context"
	"fmt"

	"github.com/google/go-github/v92/github"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

// Conflicted is the collector's hook when the poller first finds an open
// pull request of this server conflicting with its base — mergeable: false,
// a neighbouring entry of the team file changed first — on rec's pending run.
// The poller reads as the inventory App and cannot push; approve_change
// re-renders the pull request as the approving member. While the pull request
// awaits its review, the ask standing in the team's channel is that click and
// nothing is posted. A pull request approved already — its ask spent, its
// auto-merge unable to fire — gets a fresh ask to the deciding team's
// channel, whose Approve re-renders and lands it. Every way out logs why.
func (ts *Tools) Conflicted(ctx context.Context, rec *inventory.Record, pr *github.PullRequest) {
	t := ts.t
	p := rec.Setup.PendingRun
	if p == nil || p.PullRequest == nil {
		t.d.Log.Warn("conflict hook called without a pull request", "repository", rec.Repository)
		return
	}
	log := t.d.Log.With("repository", rec.Repository, "pr", p.PullRequest.URL)
	repo, err := t.unattended()
	if err != nil {
		log.Warn("conflicting pull request: no ask", "error", err)
		return
	}
	approved, err := repo.Approved(ctx, p.PullRequest.Number)
	if err != nil {
		log.Warn("conflicting pull request: reviews not read, no ask", "error", err)
		return
	}
	if !approved {
		log.Info("conflicting pull request awaits its review: the standing ask's Approve re-renders it")
		return
	}
	team := teamFromMarker(pr.GetBody())
	if team == "" && rec.Declaration != nil {
		team = rec.Declaration.Team
	}
	if team == "" {
		log.Warn("conflicting pull request: no deciding team known, no ask")
		return
	}
	text := fmt.Sprintf("%s's approved %s pull request for %s conflicts with %s: a neighbouring entry changed first, and it cannot merge as it stands. Approve re-renders it on %s as you and lands it.%s",
		p.By, inventory.PullRequestNoun(p.Kind), rec.Repository, repo.Ref, repo.Ref, decides(team, p.By))
	d := t.deliver(ctx, t.message(ctx, repo, team, text, true), &teamfiles.PullRequest{Number: p.PullRequest.Number, URL: p.PullRequest.URL}, true)
	if !d.Delivered {
		log.Warn("conflicting pull request: ask not delivered", "team", team, "error", d.Error)
		return
	}
	log.Info("conflicting pull request: ask posted", "team", team, "channel", d.Channel, "reviewId", d.ReviewID)
}
