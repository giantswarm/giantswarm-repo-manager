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
// made one, one per failed step and per finding of that person's run that is
// news; nothing when the run has nothing to tell, and nothing at all for a
// run nobody's change is behind (an Align now, the schedule). Nothing to
// approve. What was told is written back to the record, so a standing
// finding is told once and not again on the next change of the entry, and
// the change sentence once per run (LastRun.Told). Every way out logs why:
// a record without a declaration — the team files do not name the
// repository, read again after the run — has no team to tell and stays
// silent under the team the artifact names. The error says the change
// sentence did not reach the team (no identity, no channel, the post
// refused): the poller tells the run again.
func (ts *Tools) Reconciled(ctx context.Context, rec *inventory.Record) error {
	t := ts.t
	if rec == nil || rec.Setup.LastRun == nil {
		t.d.Log.Warn("reconciler run: completion hook called without a run")
		return nil
	}
	run := rec.Setup.LastRun
	if rec.Declaration == nil {
		t.d.Log.Info("reconciler run: nothing to tell the team", "repository", rec.Repository, "team", run.Result.Team, "change", changeKind(run),
			"reason", "the team files do not declare the repository")
		return nil
	}
	team := rec.Declaration.Team
	msgs := Completions(rec)
	if len(msgs) == 0 {
		t.d.Log.Info("reconciler run: nothing to tell the team", "repository", rec.Repository, "team", team, "change", changeKind(run),
			"reason", "the run is not behind a person's change or has nothing to report")
		return nil
	}
	if t.d.Review == nil {
		t.d.Log.Info("completion message not delivered", "repository", rec.Repository, "team", team, "reason", review.ErrNotConfigured)
		return nil
	}
	repo, err := t.unattended()
	if err != nil {
		t.d.Log.Warn("completion message not delivered", "repository", rec.Repository, "error", err)
		return err
	}
	channel, err := t.policyChannel(ctx, repo, team, false)
	if err != nil {
		t.d.Log.Warn("completion message not delivered", "repository", rec.Repository, "team", team, "reason", err.Error())
		return err
	}
	told, err := ts.tell(ctx, rec, team, channel, msgs)
	ts.remember(ctx, rec, told)
	return err
}

// tell posts what the team has not been told yet and returns the findings
// the record has told after it: the ones already in Setup.Told and still
// reported, plus the ones posted now. A finding whose message does not
// reach the channel stays untold and is told by the next run. The change
// sentence is posted once per run: posted, or absent, it marks the run
// told; the error is its post refused.
func (ts *Tools) tell(ctx context.Context, rec *inventory.Record, team, channel string, msgs []Completion) ([]string, error) {
	t := ts.t
	run := rec.Setup.LastRun
	known := set(rec.Setup.Told)
	told := make([]string, 0, len(msgs))
	var notTold error
	for _, m := range msgs {
		if m.Finding && known[m.Text] {
			told = append(told, m.Text)
			t.d.Log.Info("finding already told", "repository", rec.Repository, "team", team, "text", m.Text)
			continue
		}
		if m.Change && run.Told {
			t.d.Log.Info("change already told", "repository", rec.Repository, "team", team, "run", run.RunURL)
			continue
		}
		if _, err := t.d.Review.Notify(ctx, review.Notice{Team: team, Channel: channel, Text: m.Text, Link: m.Link}); err != nil {
			t.d.Log.Warn("completion message not delivered", "repository", rec.Repository, "team", team, "error", err)
			if m.Change {
				notTold = err
			}
			continue
		}
		t.d.Log.Info("completion message posted", "repository", rec.Repository, "team", team, "channel", channel, "text", m.Text)
		if m.Finding {
			told = append(told, m.Text)
		}
	}
	run.Told = notTold == nil
	return told, notTold
}

// remember writes the told findings and whether the run was told back to
// the record, so the next run over the repository — and the next poll
// reading this run — knows what the team has heard. The record was stored
// before the hook ran; nothing else writes it in between.
func (ts *Tools) remember(ctx context.Context, rec *inventory.Record, told []string) {
	t := ts.t
	rec.Setup.Told = told
	if t.d.Inventory == nil {
		return
	}
	if err := t.d.Inventory.Put(ctx, rec); err != nil {
		t.d.Log.Warn("told run not stored", "repository", rec.Repository, "error", err)
	}
}

// Completion is one message to the team about a reconciler run: the
// sentence and what it links — the pull request of a change, the run of a
// failed step or a finding.
type Completion struct {
	Text string `json:"text"`
	Link string `json:"link,omitempty"`
	// Finding says the sentence is about a finding, not about the change or
	// a failed step: a finding is told once, and again only after it has
	// gone away and come back.
	Finding bool `json:"finding,omitempty"`
	// Change says the sentence is about the change itself: told once per
	// run (LastRun.Told).
	Change bool `json:"-"`
}

// Completions renders what a run tells the team, in order: the sentence
// about the change when a person made one — created, added, transferred,
// archived, deleted or deprecated the repository — then one sentence per
// failed step and per finding of that person's run that is news, each with
// what to do. A run nobody's change is behind — an Align now, the schedule,
// an artifact without a change block — yields nothing, findings and failures
// included: the reconciler doing its job is not news, and the nightly's
// findings would repeat every night; they stay on the record and in the
// run's summary per team. A converged edit (`changed`) yields nothing
// either.
//
// Three kinds of finding never reach the team. `unchecked` — a check the
// reconciler's own token could not run — is the platform's to fix, not the
// team's. `pending-pull-request` is the engine's own repair in flight: the
// step opened that pull request in this very run, the bot-PR sweep merges
// it, and telling the team to merge it asks for work the automation is
// already doing. And a finding the record's live check no longer reports
// was fixed between the run and the poll ([verified]). The release step's
// findings are the release watch's to tell for a repository whose releases
// it reads ([releaseWatched]): one sentence per release, from the tag's own
// pipeline.
//
// The caller decides what of this the team has heard before: a finding
// carries [Completion.Finding] and is matched against Setup.Told.
func Completions(rec *inventory.Record) []Completion {
	run := rec.Setup.LastRun
	if !run.Change.PersonMade() {
		return nil
	}
	var out []Completion
	if s := changeSentence(rec); s != "" {
		out = append(out, Completion{Text: s, Link: changeLink(run), Change: true})
	}
	for _, st := range run.Result.Steps {
		if st.Verdict == reconcile.VerdictFailed {
			out = append(out, Completion{Text: failureSentence(rec.Name, st), Link: run.RunURL})
		}
		if st.Step == reconcile.StepRelease && releaseWatched(rec) {
			// The release watch tells the team about the latest release —
			// red, or never built — once, from the tag's own pipeline and
			// linking it; the run's release findings would say the same
			// thing a second time, from the commit's statuses.
			continue
		}
		for _, f := range verified(rec.Setup.Checks, st) {
			out = append(out, Completion{Text: findingSentence(rec.Name, f), Link: run.RunURL, Finding: true})
		}
	}
	return out
}

// releaseWatched says whether the release watch reads this repository's
// releases: it has a release and could reach its pipeline. A release the
// watch cannot read — a private repository without a CircleCI token — is
// unchecked, and the run's findings about it stay the team's news.
func releaseWatched(rec *inventory.Record) bool {
	w := rec.Setup.Release
	return w != nil && w.State != inventory.ReleaseUnchecked
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

// verified is the step's findings worth telling the team, as the live check
// has them. The poller reads a run's artifact minutes after the run
// finished, and the collector runs the engine's read-mode checks over the
// repository in the same refresh, immediately before this: those checks are
// the state now, the artifact the state during the run.
//
// Per kind the step reported: when the live check ran the step, its findings
// of that kind are the ones told — none when the finding was fixed in
// between, and in the live check's words when it stands, so the sentence
// describes the state now. When the live check did not run the step — it was
// skipped for want of a client, it failed, the checks did not run at all —
// the run's findings stand: an installation whose manager has no CircleCI
// client skips the release step, and silence about a missed tag build would
// be worse than a sentence from minutes ago. A kind the live check reports
// and the run did not is left to the run that reports it.
func verified(checks *reconcile.Result, st reconcile.StepResult) []reconcile.Finding {
	live, ran := liveStep(checks, st.Step)
	var out []reconcile.Finding
	done := make(map[reconcile.FindingKind]bool, len(st.Findings))
	for _, f := range st.Findings {
		if !tellable(f.Kind) {
			continue
		}
		if !ran {
			out = append(out, f)
			continue
		}
		// The live check answers for the whole kind at once: the step may
		// report the same kind more than once (a repository with two
		// rulesets of its own), and one of them may be gone.
		if done[f.Kind] {
			continue
		}
		done[f.Kind] = true
		for _, l := range live.Findings {
			if l.Kind == f.Kind {
				out = append(out, l)
			}
		}
	}
	return out
}

// tellable says whether a finding of the kind is the team's news at all.
func tellable(k reconcile.FindingKind) bool {
	switch k {
	case reconcile.FindingUnchecked, reconcile.FindingPendingPullRequest:
		return false
	}
	return true
}

// liveStep is the live check's result for the step and whether the check
// ran it: a step the check skipped or failed, one it has no result for, and
// a record without checks all answer false — there is nothing to verify a
// finding against.
func liveStep(checks *reconcile.Result, step reconcile.Step) (reconcile.StepResult, bool) {
	if checks == nil {
		return reconcile.StepResult{}, false
	}
	for _, s := range checks.Steps {
		if s.Step != step {
			continue
		}
		return s, s.Verdict != reconcile.VerdictSkipped && s.Verdict != reconcile.VerdictFailed
	}
	return reconcile.StepResult{}, false
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
	case inventory.ChangeDeleted:
		return fmt.Sprintf("%s deleted the repo %s", by, name)
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

// set is the strings as a lookup.
func set(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
