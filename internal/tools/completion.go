package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/review"
)

// Reconciled is the collector's hook after a reconciler run refreshed a
// record (POST /internal/refresh with lastRun): the completion message —
// repository · catalog entity · first release — to the owning team's channel,
// read from its policy file as the unattended identity. Nothing to approve.
func (ts *Tools) Reconciled(ctx context.Context, rec *inventory.Record) {
	t := ts.t
	if rec == nil || rec.Declaration == nil || rec.Setup.LastRun == nil {
		return
	}
	team := rec.Declaration.Team
	if t.d.Review == nil {
		t.d.Log.Info("completion message not delivered", "repository", rec.Repository, "team", team, "reason", review.ErrNotConfigured)
		return
	}
	repo, err := t.unattended()
	if err != nil {
		t.d.Log.Warn("completion message not delivered", "repository", rec.Repository, "error", err)
		return
	}
	m := t.message(ctx, repo, team, CompletionText(rec))
	if !m.Deliverable {
		t.d.Log.Warn("completion message not delivered", "repository", rec.Repository, "team", team, "reason", m.Reason)
		return
	}
	if _, err := t.d.Review.Notify(ctx, review.Notice{Team: team, Channel: m.Channel, Text: m.Text, Link: rec.Setup.LastRun.RunURL}); err != nil {
		t.d.Log.Warn("completion message not delivered", "repository", rec.Repository, "team", team, "error", err)
		return
	}
	t.d.Log.Info("completion message posted", "repository", rec.Repository, "team", team, "channel", m.Channel)
}

// CompletionText renders the completion message from the record.
func CompletionText(rec *inventory.Record) string {
	run := rec.Setup.LastRun
	state := "converged"
	if !run.Result.Converged {
		state = "not converged"
	}
	var findings []string
	for _, st := range run.Result.Steps {
		for _, f := range st.Findings {
			findings = append(findings, string(st.Step)+": "+f.Message)
		}
	}
	repoPart := "repository: missing on GitHub"
	if rec.Reality != nil {
		repoPart = fmt.Sprintf("repository: <%s|%s> (%s)", rec.Reality.URL, rec.Repository, rec.Reality.Visibility)
	}
	catalog := "catalog entity: absent"
	if rec.Catalog.Present {
		catalog = "catalog entity: present"
	}
	release := "first release: none yet"
	if rec.Reality != nil && rec.Reality.LatestRelease != nil {
		release = "first release: " + rec.Reality.LatestRelease.Tag
	}
	text := fmt.Sprintf("*Reconciled* `%s` for %s — %s.\n%s · %s · %s", rec.Repository, rec.Declaration.Team, state, repoPart, catalog, release)
	if len(findings) > 0 {
		text += "\nFindings: " + strings.Join(findings, "; ")
	}
	return text
}
