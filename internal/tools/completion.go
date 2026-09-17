package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/review"
)

// Reconciled is the collector's hook after a reconciler run refreshed a
// record (the poller read the run's artifact as lastRun): what the run tells
// the team, to the owning team's standup channel, read from its policy file
// as the unattended identity — one sentence about the change when a person
// made one, one per failed step and per finding of that person's run; nothing
// when the run has nothing to tell, and nothing at all for a run nobody's
// change is behind (an Align now, the schedule). Nothing to approve.
func (ts *Tools) Reconciled(ctx context.Context, rec *inventory.Record) {
	t := ts.t
	if rec == nil || rec.Declaration == nil || rec.Setup.LastRun == nil {
		return
	}
	team := rec.Declaration.Team
	msgs := Completions(rec)
	if len(msgs) == 0 {
		t.d.Log.Info("reconciler run: nothing to tell the team", "repository", rec.Repository, "team", team, "change", changeKind(rec.Setup.LastRun))
		return
	}
	if t.d.Review == nil {
		t.d.Log.Info("completion message not delivered", "repository", rec.Repository, "team", team, "reason", review.ErrNotConfigured)
		return
	}
	repo, err := t.unattended()
	if err != nil {
		t.d.Log.Warn("completion message not delivered", "repository", rec.Repository, "error", err)
		return
	}
	channel, err := t.policyChannel(ctx, repo, team, false)
	if err != nil {
		t.d.Log.Warn("completion message not delivered", "repository", rec.Repository, "team", team, "reason", err.Error())
		return
	}
	for _, m := range msgs {
		if _, err := t.d.Review.Notify(ctx, review.Notice{Team: team, Channel: channel, Text: m.Text, Link: m.Link}); err != nil {
			t.d.Log.Warn("completion message not delivered", "repository", rec.Repository, "team", team, "error", err)
			continue
		}
		t.d.Log.Info("completion message posted", "repository", rec.Repository, "team", team, "channel", channel, "text", m.Text)
	}
}

// Completion is one message to the team about a reconciler run: the
// sentence and what it links — the pull request of a change, the run of a
// failed step or a finding.
type Completion struct {
	Text string `json:"text"`
	Link string `json:"link,omitempty"`
}

// Completions renders what a run tells the team, in order: the sentence
// about the change when a person made one — created, added, transferred,
// archived or deprecated the repository — then one sentence per failed step
// and per finding of that person's run, each with what to do. A run nobody's
// change is behind — an Align now, the schedule, an artifact without a
// change block — yields nothing, findings and failures included: the
// reconciler doing its job is not news, and the nightly's findings would
// repeat every night; they stay on the record and in the run's summary per
// team. A converged edit (`changed`) yields nothing either. A finding of
// kind `unchecked` — a check the reconciler's own token could not run — is
// the platform's to fix, not the team's, and stays on the record too.
func Completions(rec *inventory.Record) []Completion {
	run := rec.Setup.LastRun
	if !personMade(run.Change) {
		return nil
	}
	var out []Completion
	if s := changeSentence(rec); s != "" {
		out = append(out, Completion{Text: s, Link: changeLink(run)})
	}
	for _, st := range run.Result.Steps {
		if st.Verdict == reconcile.VerdictFailed {
			out = append(out, Completion{Text: failureSentence(rec.Name, st), Link: run.RunURL})
		}
		for _, f := range st.Findings {
			if f.Kind == reconcile.FindingUnchecked {
				continue
			}
			out = append(out, Completion{Text: findingSentence(rec.Name, f), Link: run.RunURL})
		}
	}
	return out
}

// CompletionText is the run's messages as one text, a sentence per line;
// empty when the run has nothing to tell.
func CompletionText(rec *inventory.Record) string {
	msgs := Completions(rec)
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		lines = append(lines, m.Text)
	}
	return strings.Join(lines, "\n")
}

// personMade says whether a person's team-file change is behind the run: one
// of the kinds the reconciler derives from a merged pull request. A Reconcile
// now (`dispatched`), the schedule (`nightly`) and an artifact without a
// change block are the reconciler's own runs.
func personMade(ch *inventory.Change) bool {
	if ch == nil {
		return false
	}
	switch ch.Kind {
	case inventory.ChangeCreated, inventory.ChangeAdded, inventory.ChangeTransferred,
		inventory.ChangeArchived, inventory.ChangeDeprecated, inventory.ChangeChanged:
		return true
	}
	return false
}

// changeSentence is the one sentence about the change for the team, empty
// for a change kind nobody needs to hear about.
func changeSentence(rec *inventory.Record) string {
	ch := rec.Setup.LastRun.Change
	if ch == nil {
		return ""
	}
	by := ch.By
	if by == "" {
		by = "someone"
	}
	name, team := rec.Name, rec.Declaration.Team
	switch ch.Kind {
	case inventory.ChangeCreated:
		return fmt.Sprintf("%s created a new repo: %s%s", by, name, describe(rec.Declaration))
	case inventory.ChangeAdded:
		return fmt.Sprintf("%s added the existing repo %s%s to %s", by, name, describe(rec.Declaration), team)
	case inventory.ChangeTransferred:
		from := ""
		if ch.FromTeam != "" {
			from = " from " + ch.FromTeam
		}
		return fmt.Sprintf("%s transferred the repo %s%s%s to %s", by, name, describe(rec.Declaration), from, team)
	case inventory.ChangeArchived:
		return fmt.Sprintf("%s archived the repo %s", by, name)
	case inventory.ChangeDeprecated:
		return fmt.Sprintf("%s deprecated the repo %s", by, name)
	}
	return ""
}

// describe is the declaration's flavour and language in parentheses —
// " (app, go)" — as far as the entry names them.
func describe(d *inventory.Declaration) string {
	var parts []string
	if len(d.Flavours) > 0 {
		parts = append(parts, strings.Join(d.Flavours, "/"))
	}
	if d.Language != "" {
		parts = append(parts, d.Language)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// changeLink is the pull request of the change, else the run.
func changeLink(run *inventory.LastRun) string {
	if pr := run.Change.PullRequest; pr != nil && pr.URL != "" {
		return pr.URL
	}
	return run.RunURL
}

// failureSentence says which step failed for the repository, why, and what
// to do: the run has the log, and an Align now retries.
func failureSentence(name string, st reconcile.StepResult) string {
	why := ""
	if st.Summary != "" {
		why = " (" + st.Summary + ")"
	}
	return fmt.Sprintf("%s: the %s step failed%s — look at the run, fix the cause and reconcile again", name, st.Step, why)
}

// findingSentence is the finding and its fix for the repository.
func findingSentence(name string, f reconcile.Finding) string {
	if f.Fix == "" {
		return fmt.Sprintf("%s: %s", name, f.Message)
	}
	return fmt.Sprintf("%s: %s — %s", name, f.Message, f.Fix)
}

// changeKind is the run's change kind for a log line; "none" for an
// artifact without a change block.
func changeKind(run *inventory.LastRun) string {
	if run.Change == nil {
		return "none"
	}
	return run.Change.Kind
}
