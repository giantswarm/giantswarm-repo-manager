package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/review"
)

// Released is the release watch's hook when it settles a release nothing was
// published for — the tag's own pipeline red, or no pipeline within the
// grace period: the one sentence about it to the owning team's standup
// channel, read from its policy file as the unattended identity, linking
// the failed workflow. It returns the sentence posted, empty when nothing
// reached the channel; the watch stores it as the release's Told, so the
// team hears about a release once, and a later pass over the same red tag
// posts nothing. A team without a policy file is not messaged. Every way
// out logs why.
func (ts *Tools) Released(ctx context.Context, rec *inventory.Record) string {
	t := ts.t
	n, ok := ReleaseNotice(rec)
	if !ok {
		return ""
	}
	if rec.Declaration == nil {
		t.d.Log.Info("release notice not delivered", "repository", rec.Repository, "reason", "the team files do not declare the repository")
		return ""
	}
	team := rec.Declaration.Team
	if t.d.Review == nil {
		t.d.Log.Info("release notice not delivered", "repository", rec.Repository, "team", team, "reason", review.ErrNotConfigured)
		return ""
	}
	repo, err := t.unattended()
	if err != nil {
		t.d.Log.Warn("release notice not delivered", "repository", rec.Repository, "team", team, "error", err)
		return ""
	}
	channel, err := t.policyChannel(ctx, repo, team, false)
	if err != nil {
		t.d.Log.Warn("release notice not delivered", "repository", rec.Repository, "team", team, "reason", err.Error())
		return ""
	}
	if _, err := t.d.Review.Notify(ctx, review.Notice{Team: team, Channel: channel, Text: n.Text, Link: n.Link}); err != nil {
		t.d.Log.Warn("release notice not delivered", "repository", rec.Repository, "team", team, "error", err)
		return ""
	}
	t.d.Log.Info("release notice posted", "repository", rec.Repository, "team", team, "channel", channel, "tag", rec.Setup.Release.Tag, "text", n.Text)
	return n.Text
}

// ReleaseNotice is the sentence for a settled release nothing was published
// for, and what it links — the failed workflow of a red release, the
// project's pipelines for an unbuilt one — in the shape of the other
// notices: the bare repository name, what is true, what to do. False for a
// release in any other state, and for one the team has heard about.
func ReleaseNotice(rec *inventory.Record) (Completion, bool) {
	w := rec.Setup.Release
	if !w.Untold() {
		return Completion{}, false
	}
	pr := ""
	if w.PullRequest != nil {
		pr = fmt.Sprintf(" (pull request #%d)", w.PullRequest.Number)
	}
	confirm := fmt.Sprintf("then devctl release wait %s %s confirms it", rec.Repository, w.Tag)
	switch w.State {
	case inventory.ReleaseRed:
		link := w.Pipeline.Workflow
		if link == "" {
			link = w.Pipeline.URL
		}
		return Completion{
			Text: fmt.Sprintf("%s: release %s%s is red, the tag pipeline %d failed in %s — rerun the workflow from failed on CircleCI, %s",
				rec.Name, w.Tag, pr, w.Pipeline.Number, strings.Join(w.FailedJobs, ", "), confirm),
			Link: link,
		}, true
	case inventory.ReleaseUnbuilt:
		since := ""
		if w.SettledAt != nil {
			since = " " + minutes(w.SettledAt.Sub(w.CreatedAt)) + " after the tag"
		}
		return Completion{
			Text: fmt.Sprintf("%s: release %s%s has no CircleCI pipeline%s, nothing was built or published — trigger the tag's pipeline by hand on CircleCI, %s",
				rec.Name, w.Tag, pr, since, confirm),
			Link: "https://app.circleci.com/pipelines/github/" + rec.Repository,
		}, true
	}
	return Completion{}, false
}

// minutes is d as a sentence says it, to the minute.
func minutes(d time.Duration) string {
	n := int(d.Round(time.Minute) / time.Minute)
	if n == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", n)
}
