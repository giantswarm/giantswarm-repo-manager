package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
	"github.com/google/go-github/v92/github"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/review"
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

// The write tools beyond create_repository. Every one renders a Plan (the
// dry run: the entry as it will read, the schema's verdict, the pull request
// and the ask that would follow) and, in mode commit, opens the pull request
// as the person and hands the ask to klaus-gateway.
const (
	ToolUpdateRepository   = "update_repository"
	ToolTransferRepository = "transfer_repository"
	ToolSetLifecycle       = "set_lifecycle"
	ToolApproveChange      = "approve_change"
	ToolAlignRepository    = "align_repository"

	argToTeam      = "toTeam"
	argLifecycle   = "lifecycle"
	argPullRequest = "pullRequest"
	argReason      = "reason"
	// argConfirm is the repository's name typed by the person: what a
	// deletion needs beside the lifecycle.
	argConfirm = "confirm"
)

// marker is the machine-readable line every pull request this server opens
// carries: approve_change reads the deciding team from it.
const marker = "<!-- giantswarm-repo-manager: team=%s -->"

// teamFromMarker is the deciding team a pull request body's marker names,
// "" without one.
func teamFromMarker(body string) string {
	_, after, ok := strings.Cut(body, "<!-- giantswarm-repo-manager: team=")
	if !ok {
		return ""
	}
	team, _, ok := strings.Cut(after, " -->")
	if !ok {
		return ""
	}
	return team
}

// Plan is a write's dry run.
type Plan struct {
	Repository string `json:"repository"`
	// Team owns the entry after the change (the receiving team of a transfer).
	Team     string `json:"team"`
	FromTeam string `json:"fromTeam,omitempty"`
	// Before and Entry are the entry as it reads now and as it will read.
	Before string `json:"before,omitempty"`
	Entry  string `json:"entry,omitempty"`
	// Problems are the schema's refusals of the changed entry (existing
	// entries are held to the schema, not to the creation rules).
	Problems []reposetup.Problem `json:"problems,omitempty"`
	Accepted bool                `json:"accepted"`
	// PullRequest is the change as it would land.
	PullRequest PlannedPullRequest `json:"pullRequest"`
	// Ask is the review request that follows the pull request, when the
	// change needs the team's decision; Notice the message to the giving team.
	Ask    *PlannedMessage `json:"ask,omitempty"`
	Notice *PlannedMessage `json:"notice,omitempty"`

	change teamfiles.Change
	// kind is the change the pull request makes, as the reconciler classifies
	// it (archived, deprecated, transferred, changed): the record's expected
	// run is marked with it.
	kind string
}

// PlannedPullRequest is the pull request before it exists.
type PlannedPullRequest struct {
	Repository string   `json:"repository"`
	Branch     string   `json:"branch"`
	Title      string   `json:"title"`
	Files      []string `json:"files"`
	Body       string   `json:"body"`
	// As is who opens it: the caller's GitHub login, or the reason it is unknown.
	As string `json:"as"`
}

// PlannedMessage is an ask or notice before it is posted.
type PlannedMessage struct {
	Team    string `json:"team"`
	Channel string `json:"channel,omitempty"`
	Text    string `json:"text"`
	// Deliverable says whether the gateway and channel are configured.
	Deliverable bool   `json:"deliverable"`
	Reason      string `json:"reason,omitempty"`
}

// Committed is a write's outcome in mode commit.
type Committed struct {
	PullRequest *teamfiles.PullRequest `json:"pullRequest"`
	Ask         *Delivery              `json:"ask,omitempty"`
	Notice      *Delivery              `json:"notice,omitempty"`
	// PendingRun is the record's expectation of the reconciler run that
	// follows the pull request's merge: the poller reads its artifact within
	// the pending interval. Nil when the mark could not be stored.
	PendingRun *inventory.PendingRun `json:"pendingRun,omitempty"`
}

// pendingRunSentence closes the description of every write that opens a
// pull request: what the record shows until the run of the merge reports.
const pendingRunSentence = " The record shows setup.pendingRun until the reconciler run of the merged pull request has reported (get_repository)."

// Delivery is what became of an ask or notice.
type Delivery struct {
	Team string `json:"team"`
	// Channel is where the gateway posted (Posted.Channel): the debug
	// channel under reviews.debugChannel, else the policy channel.
	Channel string `json:"channel,omitempty"`
	// IntendedChannel is the policy channel, present only when a debug
	// redirect sent the message somewhere else.
	IntendedChannel string `json:"intendedChannel,omitempty"`
	Delivered       bool   `json:"delivered"`
	ReviewID        string `json:"reviewId,omitempty"`
	Error           string `json:"error,omitempty"`
}

// planner renders a write's plan with the repository read as whom the call
// can be: the person when the call carries their token (the pull request's
// author), else the App for a dry run.
type planner struct {
	repo   teamfiles.Repo
	person *person
	as     string
}

func (t *tools) planner(ctx context.Context) (*planner, error) {
	p, err := t.person(ctx)
	if err == nil {
		return &planner{repo: p.repo, person: p, as: p.login}, nil
	}
	repo, uerr := t.unattended()
	if uerr != nil {
		return nil, fmt.Errorf("%v; and no unattended read identity either: %v", err, uerr)
	}
	return &planner{repo: repo, as: "unknown until you connect GitHub in muster (" + err.Error() + ")"}, nil
}

// entryFor finds the repository's declaration; hint is the team the caller or
// the inventory named.
func (t *tools) entryFor(ctx context.Context, repo teamfiles.Repo, name, hint string) (*teamfiles.TeamFile, reposetup.Declaration, error) {
	if hint == "" && t.d.Inventory != nil {
		if rec, err := t.d.Inventory.Get(ctx, t.org()+"/"+name); err == nil && rec.Declaration != nil {
			hint = rec.Declaration.Team
		}
	}
	tf, err := repo.FindEntry(ctx, name, hint)
	if err != nil {
		return nil, reposetup.Declaration{}, err
	}
	d, _ := tf.Entries.Entry(name)
	return tf, d, nil
}

// schemaProblems validates one existing entry the engine's way for a
// repository that exists (reposetup.ModeExisting): the schema, not the
// creation rules.
func (t *tools) schemaProblems(ctx context.Context, team string, d reposetup.Declaration) ([]reposetup.Problem, error) {
	y, err := d.YAML()
	if err != nil {
		return nil, err
	}
	tf, err := reposetup.ParseTeamFile(team, strings.NewReader(y))
	if err != nil {
		return nil, err
	}
	res, err := t.validator().Validate(ctx, reposetup.Request{TeamFile: tf, Mode: reposetup.ModeExisting})
	if err != nil {
		return nil, err
	}
	for _, e := range res.Entries {
		if e.Name == d.Name {
			return e.Problems, nil
		}
	}
	return nil, fmt.Errorf("the engine returned no verdict for %s", d.Name)
}

// finish fills the plan's rendering and pull-request text.
func (pl *Plan) finish(repo teamfiles.Repo, as, branch, title, body string) {
	paths := make([]string, 0, len(pl.change.Files))
	for p := range pl.change.Files {
		paths = append(paths, p)
	}
	body = strings.TrimSpace(body) + "\n\n" + fmt.Sprintf(marker, pl.Team) + "\n"
	pl.change.Branch, pl.change.Title, pl.change.Body = branch, title, body
	pl.PullRequest = PlannedPullRequest{Repository: repo.Owner + "/" + repo.Name, Branch: branch, Title: title, Files: paths, Body: body, As: as}
	pl.Accepted = len(pl.Problems) == 0
}

// message plans an ask or a notice to a team from its policy file: an ask
// (an Approve button) goes to the team's slackChannel, a notice to its
// standupChannel.
func (t *tools) message(ctx context.Context, repo teamfiles.Repo, team, text string, ask bool) *PlannedMessage {
	m := &PlannedMessage{Team: team, Text: text}
	if t.d.Review == nil {
		m.Reason = review.ErrNotConfigured.Error()
		return m
	}
	channel, err := t.policyChannel(ctx, repo, team, ask)
	m.Channel = channel
	if err != nil {
		m.Reason = err.Error()
		return m
	}
	m.Deliverable = true
	return m
}

// policyChannel reads a team's policy file and resolves the channel an ask
// (slackChannel) or a notice (standupChannel) goes to, as the ID the
// gateway takes. A name the map does not resolve comes back with the error.
func (t *tools) policyChannel(ctx context.Context, repo teamfiles.Repo, team string, ask bool) (string, error) {
	pol, err := repo.Policy(ctx, team)
	if err != nil {
		return "", err
	}
	name := pol.StandupChannel
	if ask {
		name = pol.SlackChannel
	}
	id, err := t.d.Review.ChannelID(name)
	if err != nil {
		return name, err
	}
	return id, nil
}

// commit opens the plan's pull request as the person and posts its messages.
func (t *tools) commit(ctx context.Context, p *person, pl *Plan) (*Committed, error) {
	if !pl.Accepted {
		return nil, fmt.Errorf("the schema refuses the entry: %s — fix it and run again (dryRun: true shows the rendered entry)", problemsText(pl.Problems))
	}
	pr, err := p.repo.OpenPullRequest(ctx, pl.change)
	if err != nil {
		return nil, err
	}
	// The team's approval is the last thing the change waits for: with
	// auto-merge armed as the author, GitHub merges the pull request the
	// moment a member's review lands and its checks are green — from the
	// Slack button or from GitHub alike. A refusal (the repository does not
	// allow it) leaves the landing to approve_change.
	if !pr.AutoMerge {
		if err := p.repo.EnableAutoMerge(ctx, pr.NodeID); err != nil {
			t.d.Log.Warn("auto-merge not armed", "pr", pr.URL, "error", err)
		} else {
			pr.AutoMerge = true
		}
	}
	out := &Committed{PullRequest: pr}
	if pl.Ask != nil {
		out.Ask = t.deliver(ctx, pl.Ask, pr, true)
	}
	if pl.Notice != nil {
		out.Notice = t.deliver(ctx, pl.Notice, pr, false)
	}
	if pl.kind != "" {
		out.PendingRun = t.expectRun(ctx, p, pl.Repository, pl.kind, pr)
	}
	t.d.Log.Info("pull request opened", "tool", pl.change.Title, "pr", pr.URL, "as", pr.Author)
	return out, nil
}

// expectRun marks the record of repository (owner/name) as expecting the
// reconciler run that follows the merge of pull request pr, a team-file
// change of kind by the person, the way an Align now marks a dispatch: the
// poller reads the run's artifact within its pending interval, and a run
// that does not report within the window leaves the finding
// reconcile-run-missing, worded for the kind. A repository the inventory
// has not seen yet gets its record built first. Nil, with a log line, when
// the mark could not be stored — the pull request stands either way.
func (t *tools) expectRun(ctx context.Context, p *person, repository, kind string, pr *teamfiles.PullRequest) *inventory.PendingRun {
	if t.d.Inventory == nil || t.d.Collector == nil {
		return nil
	}
	rec, err := t.d.Inventory.Get(ctx, repository)
	if errors.Is(err, inventory.ErrNotFound) {
		rec, err = t.d.Collector.Refresh(ctx, repository, nil, inventory.SourceRefresh)
	}
	if err != nil {
		t.d.Log.Error("pending run not stored: record unreadable", "repository", repository, "error", err)
		return nil
	}
	rec.Opened(time.Now().UTC(), p.login, kind, inventory.ChangePullRequest{Number: pr.Number, URL: pr.URL})
	if err := t.putExpectedRun(ctx, rec); err != nil {
		t.d.Log.Error("pending run not stored", "repository", repository, "error", err)
		return nil
	}
	return rec.Setup.PendingRun
}

// putExpectedRun stores rec, whose pending run was just marked, and wakes the
// reconciler poller: the run's artifact is looked for at once and then every
// pending interval, not at the poller's next tick.
func (t *tools) putExpectedRun(ctx context.Context, rec *inventory.Record) error {
	if err := t.d.Inventory.Put(ctx, rec); err != nil {
		return err
	}
	if t.d.Collector != nil {
		t.d.Collector.WakeReconciler()
	}
	return nil
}

// deliver posts a planned message; a failure is reported, not fatal — the
// pull request exists and approving on GitHub is equivalent (PRD D6).
func (t *tools) deliver(ctx context.Context, m *PlannedMessage, pr *teamfiles.PullRequest, ask bool) *Delivery {
	d := &Delivery{Team: m.Team, Channel: m.Channel}
	if !m.Deliverable {
		d.Error = m.Reason
		return d
	}
	// The pull request travels as the message's link — the gateway renders it
	// as the Open PR button of an ask and the Open PR line of a notice — and
	// not in the text, which would show it twice.
	text := m.Text
	var posted *review.Posted
	var err error
	if ask {
		posted, err = t.d.Review.Review(ctx, review.Ask{Team: m.Team, Channel: m.Channel, Text: text, Link: pr.URL,
			Approve: review.Approve{Tool: "x_" + ToolPrefix + "_" + ToolApproveChange, Arguments: map[string]any{argPullRequest: pr.Number, ArgMode: string(ModeCommit)}}})
	} else {
		posted, err = t.d.Review.Notify(ctx, review.Notice{Team: m.Team, Channel: m.Channel, Text: text, Link: pr.URL})
	}
	if err != nil {
		d.Error = err.Error()
		return d
	}
	d.Delivered = true
	d.ReviewID = posted.ID
	if posted.Channel != "" && posted.Channel != d.Channel {
		d.IntendedChannel = d.Channel
	}
	if posted.Channel != "" {
		d.Channel = posted.Channel
	}
	return d
}

func problemsText(ps []reposetup.Problem) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, "; ")
}

// repositoryName is the repository argument without the org.
func (t *tools) repositoryName(args map[string]any) (string, error) {
	key, err := t.repositoryKey(args)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(key, t.org()+"/"), nil
}

// --- update_repository ---------------------------------------------------

func (t *tools) updateRepository() WriteTool {
	return WriteTool{
		Name: ToolUpdateRepository,
		Description: "Change the configuration of a declared repository: its team-file entry is replaced by the entry you pass " +
			"(the whole entry — name, componentType, gen and every other field as it should read afterwards). The entry is validated against " +
			"the repositories schema (not the creation rules, which apply to new repositories only); the reconciler applies the change after " +
			"the team's review. Use set_lifecycle to deprecate or archive and transfer_repository to move a repository to another team." + pendingRunSentence,
		Options: []mcp.ToolOption{
			mcp.WithString(argRepository, mcp.Required(), mcp.Description("Repository name, with or without the org.")),
			mcp.WithObject(argEntry, mcp.Required(), mcp.Description("The entry as it should read in the team file afterwards (the full entry, not a patch)."), mcp.AdditionalProperties(true)),
			mcp.WithString(argReason, mcp.Description("Why, for the pull request body.")),
		},
		DryRun: func(ctx context.Context, args map[string]any) (any, error) {
			pl, err := t.planUpdate(ctx, args)
			return pl, err
		},
		Commit: func(ctx context.Context, args map[string]any) (any, error) {
			p, err := t.person(ctx)
			if err != nil {
				return nil, err
			}
			pl, err := t.planUpdateAs(ctx, p.repo, p.login, args)
			if err != nil {
				return nil, err
			}
			return t.commit(ctx, p, pl)
		},
	}
}

func (t *tools) planUpdate(ctx context.Context, args map[string]any) (*Plan, error) {
	pn, err := t.planner(ctx)
	if err != nil {
		return nil, err
	}
	return t.planUpdateAs(ctx, pn.repo, pn.as, args)
}

func (t *tools) planUpdateAs(ctx context.Context, repo teamfiles.Repo, as string, args map[string]any) (*Plan, error) {
	name, err := t.repositoryName(args)
	if err != nil {
		return nil, err
	}
	entry, _ := args[argEntry].(map[string]any)
	if entry == nil {
		return nil, fmt.Errorf("%s is required", argEntry)
	}
	if n, _ := entry[teamfiles.FieldName].(string); n != "" && n != name {
		return nil, fmt.Errorf("the entry's name %q is not %s: renames are followed by the reconciler, not declared", n, name)
	}
	entry[teamfiles.FieldName] = name
	after, err := teamfiles.EntryFromValue(entry)
	if err != nil {
		return nil, err
	}
	tf, before, err := t.entryFor(ctx, repo, name, "")
	if err != nil {
		return nil, err
	}
	pl, err := t.replacePlan(ctx, tf, name, before, after)
	if err != nil {
		return nil, err
	}
	reason, _ := args[argReason].(string)
	pl.finish(repo, as, "reposetup/update-"+name, fmt.Sprintf("chore(repositories): update %s (%s)", name, tf.Team),
		fmt.Sprintf("## Problem\n\nThe configuration of `%s/%s` changes.\n\n## Solution\n\nThe entry in `%s` is replaced; the reconciler applies the difference after this merges.\n\n%s\n\nOpened by giantswarm-repo-manager (`%s`) as the caller.",
			t.org(), name, tf.Path, reasonLine(reason), ToolUpdateRepository))
	return pl, nil
}

// replacePlan is the plan of rewriting one entry in its file.
func (t *tools) replacePlan(ctx context.Context, tf *teamfiles.TeamFile, name string, before, after reposetup.Declaration) (*Plan, error) {
	problems, err := t.schemaProblems(ctx, tf.Team, after)
	if err != nil {
		return nil, err
	}
	content, err := teamfiles.ReplaceEntry(tf.Content, name, after)
	if err != nil {
		return nil, err
	}
	pl := &Plan{Repository: t.org() + "/" + name, Team: tf.Team, Problems: problems, change: teamfiles.Change{Files: map[string][]byte{tf.Path: content}}, kind: inventory.ChangeChanged}
	pl.Before, _ = before.YAML()
	pl.Entry, _ = after.YAML()
	return pl, nil
}

func reasonLine(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return "Reason: " + strings.TrimSpace(reason)
}

// --- transfer_repository -------------------------------------------------

func (t *tools) transferRepository() WriteTool {
	return WriteTool{
		Name: ToolTransferRepository,
		Description: "Move a declared repository to another team: its entry leaves the giving team's file and enters the receiving team's " +
			"file in one pull request that names both teams. The ask goes to the receiving team's channel (its member approves), the giving team " +
			"gets a notice in its standup channel. The reconciler then re-applies permissions, CODEOWNERS and the catalog mapping for the new owner." + pendingRunSentence,
		Options: []mcp.ToolOption{
			mcp.WithString(argRepository, mcp.Required(), mcp.Description("Repository name, with or without the org.")),
			mcp.WithString(argToTeam, mcp.Required(), mcp.Description("The receiving team's slug (team-planeteers, …).")),
			mcp.WithString(argReason, mcp.Description("Why, for the pull request body and the ask.")),
		},
		DryRun: func(ctx context.Context, args map[string]any) (any, error) {
			pn, err := t.planner(ctx)
			if err != nil {
				return nil, err
			}
			return t.planTransfer(ctx, pn.repo, pn.as, args)
		},
		Commit: func(ctx context.Context, args map[string]any) (any, error) {
			p, err := t.person(ctx)
			if err != nil {
				return nil, err
			}
			pl, err := t.planTransfer(ctx, p.repo, p.login, args)
			if err != nil {
				return nil, err
			}
			return t.commit(ctx, p, pl)
		},
	}
}

func (t *tools) planTransfer(ctx context.Context, repo teamfiles.Repo, as string, args map[string]any) (*Plan, error) {
	name, err := t.repositoryName(args)
	if err != nil {
		return nil, err
	}
	to, _ := args[argToTeam].(string)
	to = strings.TrimSpace(to)
	if to == "" {
		return nil, fmt.Errorf("%s is required", argToTeam)
	}
	from, d, err := t.entryFor(ctx, repo, name, "")
	if err != nil {
		return nil, err
	}
	if from.Team == to {
		return nil, fmt.Errorf("%s is owned by %s already", name, to)
	}
	dst, err := repo.TeamFile(ctx, to)
	if err != nil {
		return nil, fmt.Errorf("receiving team: %w", err)
	}
	if _, dup := dst.Entries.Entry(name); dup {
		return nil, fmt.Errorf("%s declares %s already", dst.Path, name)
	}
	problems, err := t.schemaProblems(ctx, to, d)
	if err != nil {
		return nil, err
	}
	without, err := teamfiles.RemoveEntry(from.Content, name)
	if err != nil {
		return nil, err
	}
	with, err := reposetup.InsertEntry(to, dst.Content, d)
	if err != nil {
		return nil, fmt.Errorf("insert into %s: %w", dst.Path, err)
	}
	pl := &Plan{Repository: t.org() + "/" + name, Team: to, FromTeam: from.Team, Problems: problems,
		change: teamfiles.Change{Files: map[string][]byte{from.Path: without, dst.Path: with}}, kind: inventory.ChangeTransferred}
	pl.Entry, _ = d.YAML()
	reason, _ := args[argReason].(string)
	pl.finish(repo, as, "reposetup/transfer-"+name, fmt.Sprintf("chore(repositories): transfer %s from %s to %s", name, from.Team, to),
		fmt.Sprintf("## Problem\n\n`%s/%s` changes owner.\n\n## Solution\n\nThe entry moves from `%s` (giving team: **%s**) to `%s` (receiving team: **%s**), unchanged. "+
			"The reconciler re-applies team permissions, CODEOWNERS and the catalog mapping for %s after this merges.\n\n%s\n\nOpened by giantswarm-repo-manager (`%s`) as the caller.",
			t.org(), name, from.Path, from.Team, dst.Path, to, to, reasonLine(reason), ToolTransferRepository))
	pl.Ask = t.message(ctx, repo, to, fmt.Sprintf("%s asks to transfer `%s/%s` from %s to %s: your team receives it.%s%s", as, t.org(), name, from.Team, to, reasonSuffix(reason), decides(to, as)), true)
	pl.Notice = t.message(ctx, repo, from.Team, fmt.Sprintf("%s asks to transfer `%s/%s` from %s to %s: your team gives it; %s decides.%s", as, t.org(), name, from.Team, to, to, reasonSuffix(reason)), false)
	return pl, nil
}

// reasonSuffix is the asker's reason as a sentence of the ask, so that what
// follows it — who decides — starts a sentence of its own.
func reasonSuffix(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	if !strings.HasSuffix(reason, ".") && !strings.HasSuffix(reason, "!") && !strings.HasSuffix(reason, "?") {
		reason += "."
	}
	return " Reason: " + reason
}

// decides closes an ask with who may approve it: a member of the deciding
// team who is not the asker — GitHub does not accept an author's approval of
// their own pull request, and approve_change refuses it in the same words.
func decides(team, as string) string {
	return fmt.Sprintf(" A member of %s other than %s approves.", team, as)
}

// --- set_lifecycle --------------------------------------------------------

func (t *tools) setLifecycle() WriteTool {
	return WriteTool{
		Name: ToolSetLifecycle,
		Description: "Deprecate, archive or delete a declared repository by setting lifecycle in its team-file entry. deprecated: security-only Renovate " +
			"and a catalog flag. archived: the reconciler archives the repository on GitHub and unfollows it on CircleCI; the entry stays as the " +
			"record. deleted: the reconciler unfollows the repository on CircleCI and deletes it on GitHub — code, issues, pull requests, releases and " +
			"packages with it (an organization owner can restore it on GitHub for 90 days); the entry stays as the record of the deletion; needs confirm, " +
			"the repository's name typed by the person, and is refused without it. An entry without " + alignTrue + " gets it beside the lifecycle — the change opts the repository in to alignment, else the reconciler " +
			"would record the lifecycle and apply nothing — and the ask says so. The ask goes to the owning team's channel; " +
			"a member's Approve (or an approving review on GitHub) lands it." + pendingRunSentence,
		Options: []mcp.ToolOption{
			mcp.WithString(argRepository, mcp.Required(), mcp.Description("Repository name, with or without the org.")),
			mcp.WithString(argLifecycle, mcp.Required(), mcp.Enum(teamfiles.LifecycleDeprecated, teamfiles.LifecycleArchived, teamfiles.LifecycleDeleted), mcp.Description("deprecated, archived or deleted.")),
			mcp.WithString(argReason, mcp.Description("Why, for the pull request body and the ask.")),
			mcp.WithString(argConfirm, mcp.Description("For deleted: the repository's name, with or without the org, as the person typed it. Refused when absent or another name.")),
		},
		DryRun: func(ctx context.Context, args map[string]any) (any, error) {
			pn, err := t.planner(ctx)
			if err != nil {
				return nil, err
			}
			return t.planLifecycle(ctx, pn.repo, pn.as, args)
		},
		Commit: func(ctx context.Context, args map[string]any) (any, error) {
			p, err := t.person(ctx)
			if err != nil {
				return nil, err
			}
			pl, err := t.planLifecycle(ctx, p.repo, p.login, args)
			if err != nil {
				return nil, err
			}
			return t.commit(ctx, p, pl)
		},
	}
}

func (t *tools) planLifecycle(ctx context.Context, repo teamfiles.Repo, as string, args map[string]any) (*Plan, error) {
	name, err := t.repositoryName(args)
	if err != nil {
		return nil, err
	}
	lc, _ := args[argLifecycle].(string)
	switch lc {
	case teamfiles.LifecycleDeprecated, teamfiles.LifecycleArchived:
	case teamfiles.LifecycleDeleted:
		if err := t.confirmed(name, args); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%s must be %s, %s or %s", argLifecycle, teamfiles.LifecycleDeprecated, teamfiles.LifecycleArchived, teamfiles.LifecycleDeleted)
	}
	tf, before, err := t.entryFor(ctx, repo, name, "")
	if err != nil {
		return nil, err
	}
	f, err := before.Fields()
	if err != nil {
		return nil, fmt.Errorf("reading the entry of %s: %w", name, err)
	}
	if f.Lifecycle == lc {
		return nil, fmt.Errorf("%s is %s already", name, lc)
	}
	after, err := teamfiles.SetField(before, teamfiles.FieldLifecycle, lc)
	if err != nil {
		return nil, err
	}
	// The reconciler applies a lifecycle only to an opted-in entry: one
	// without the field gets it beside the lifecycle, and the ask says so.
	change, optsIn := "`lifecycle: "+lc+"`", ""
	if !f.Align {
		if after, err = teamfiles.SetField(after, teamfiles.FieldAlign, "true"); err != nil {
			return nil, err
		}
		change += " and " + alignTrue
		optsIn = " The change also opts the repository in to alignment (" + alignTrue + " in its entry), so the reconciler applies the lifecycle."
	}
	pl, err := t.replacePlan(ctx, tf, name, before, after)
	if err != nil {
		return nil, err
	}
	reason, _ := args[argReason].(string)
	effect, kind := lifecycleEffect(lc)
	pl.kind = kind
	pl.finish(repo, as, "reposetup/"+lc+"-"+name, fmt.Sprintf("chore(repositories): %s %s (%s)", verb(lc), name, tf.Team),
		fmt.Sprintf("## Problem\n\n`%s/%s` is to be %s.\n\n## Solution\n\n%s in `%s` — %s.%s\n\n%s\n\nOpened by giantswarm-repo-manager (`%s`) as the caller.",
			t.org(), name, lc, change, tf.Path, effect, optsIn, reasonLine(reason), ToolSetLifecycle))
	pl.Ask = t.message(ctx, repo, tf.Team, fmt.Sprintf("%s asks to %s `%s/%s` (owned by %s).%s%s%s", as, verb(lc), t.org(), name, tf.Team, optsIn, reasonSuffix(reason), decides(tf.Team, as)), true)
	return pl, nil
}

// confirmed checks a deletion's confirmation: the repository's name, with or
// without the org, as the person typed it. Anything else refuses the
// deletion with why — an agent cannot reach it by paraphrase.
func (t *tools) confirmed(name string, args map[string]any) error {
	typed := stringArg(args, argConfirm)
	if typed == "" {
		return fmt.Errorf("deleting %s/%s needs %s: the repository's name, typed by the person", t.org(), name, argConfirm)
	}
	if !strings.EqualFold(strings.TrimPrefix(typed, t.org()+"/"), name) {
		return fmt.Errorf("%s %q does not name %s/%s: the deletion is refused", argConfirm, typed, t.org(), name)
	}
	return nil
}

// lifecycleEffect is what the reconciler does with a lifecycle, for the pull
// request body, and the change kind the record's expected run is marked with.
func lifecycleEffect(lifecycle string) (effect, kind string) {
	switch lifecycle {
	case teamfiles.LifecycleArchived:
		return "the reconciler archives the repository on GitHub and unfollows it on CircleCI; the entry stays in the team file as the record", inventory.ChangeArchived
	case teamfiles.LifecycleDeleted:
		return "the reconciler unfollows the repository on CircleCI and deletes it on GitHub — code, issues, pull requests, releases and packages with it; " +
			"an organization owner can restore it on GitHub for 90 days; the entry stays in the team file as the record of the deletion", inventory.ChangeDeleted
	}
	return "security-only Renovate updates and the catalog's deprecation flag", inventory.ChangeDeprecated
}

func verb(lifecycle string) string {
	switch lifecycle {
	case teamfiles.LifecycleArchived:
		return "archive"
	case teamfiles.LifecycleDeleted:
		return "delete"
	}
	return "deprecate"
}

// --- approve_change -------------------------------------------------------

// Approval is approve_change's result.
type Approval struct {
	PullRequest int    `json:"pullRequest"`
	Team        string `json:"team"`
	// Author opened the pull request; the approval is somebody else's to give.
	Author    string   `json:"author,omitempty"`
	Login     string   `json:"login"`
	Teams     []string `json:"teams,omitempty"`
	Member    bool     `json:"member"`
	ReviewURL string   `json:"reviewUrl,omitempty"`
	// Merged says the approval landed the pull request; AutoMerge that GitHub
	// merges it by itself once its checks are green. Neither: Message says
	// why, and the pull request is merged on GitHub by hand.
	Merged    bool `json:"merged"`
	AutoMerge bool `json:"autoMerge"`
	// Rerendered says the pull request conflicted with its base — a
	// neighbouring entry changed first — and was re-rendered on the current
	// base before the approval: the branch force-pushed as the approver
	// with the entries the pull request changes, the pull request, its ask
	// and its auto-merge kept. RerenderError says it conflicted and could
	// not be re-rendered; the approval stands, the pull request waits for a
	// rebase.
	Rerendered    *teamfiles.Rerendered `json:"rerendered,omitempty"`
	RerenderError string                `json:"rerenderError,omitempty"`
	// Message is the approval's outcome in one sentence, for the channel the
	// Approve button was clicked in.
	Message string `json:"message,omitempty"`
}

func (t *tools) approveChange() WriteTool {
	return WriteTool{
		Name: ToolApproveChange,
		Description: "Approve a team-file pull request as you, after this server has checked on GitHub that you are a member of the team " +
			"the change belongs to (the owning team; for a transfer the receiving team), and land it: merged as you when GitHub lets it, else left " +
			"to GitHub's auto-merge (armed if it was not) — the answer says which, or why neither. A pull request GitHub reports conflicting with " +
			"its base (a neighbouring entry of the team file changed first) is re-rendered on the current base before the approval — the entries it " +
			"changes re-applied to the files as they read now and the branch force-pushed as you, the pull request, its ask and its auto-merge kept — " +
			"and the answer names it (rerendered). The Approve button of a Slack ask calls this tool " +
			"as the clicking member; a member may also call it directly, and approving on GitHub is equivalent (GitHub does not re-render). A non-member is refused, and so is the " +
			"person who opened the pull request: GitHub does not accept an author's approval of their own pull request, another member has to approve.",
		Options: []mcp.ToolOption{
			mcp.WithNumber(argPullRequest, mcp.Required(), mcp.Description("The pull request number in the team-files repository (giantswarm/github).")),
		},
		DryRun: func(ctx context.Context, args map[string]any) (any, error) {
			return t.approve(ctx, args, false)
		},
		Commit: func(ctx context.Context, args map[string]any) (any, error) {
			return t.approve(ctx, args, true)
		},
	}
}

// ErrNotAMember is the refusal of approve_change and sweep_inventory: the
// caller is not in the team the tool is reserved for.
var ErrNotAMember = errors.New("not a member of the deciding team")

// ErrOwnPullRequest is approve_change's refusal of the person who opened the
// pull request: GitHub does not accept an author's approval of their own pull
// request, so the click would fail there; refusing it here says why.
var ErrOwnPullRequest = errors.New("the author cannot approve their own pull request")

func (t *tools) approve(ctx context.Context, args map[string]any, submit bool) (*Approval, error) {
	n := int(number(args, argPullRequest, 0))
	if n <= 0 {
		return nil, fmt.Errorf("%s is required", argPullRequest)
	}
	p, err := t.person(ctx)
	if err != nil {
		return nil, err
	}
	d, err := t.decision(ctx, p.repo, n)
	if err != nil {
		return nil, err
	}
	a, err := d.approvalBy(p)
	if err != nil {
		return nil, err
	}
	if !submit {
		return a, nil
	}
	// A pull request its base moved under — a neighbouring entry changed
	// first — cannot merge as it stands: it is re-rendered on the base first,
	// as the approver, so the review lands on a commit GitHub can merge.
	a.Rerendered, err = t.rerender(ctx, p, d)
	if err != nil {
		a.RerenderError = err.Error()
	}
	url, err := p.repo.Approve(ctx, n, fmt.Sprintf("Approved as a member of %s through giantswarm-repo-manager.", d.team))
	if err != nil {
		return nil, err
	}
	a.ReviewURL = url
	landing := p.repo.Land(ctx, n)
	a.Merged, a.AutoMerge, a.Message = landing.Merged, landing.AutoMerge, d.outcome(p.login, landing, a.Rerendered, a.RerenderError)
	t.d.Log.Info("pull request approved", "pr", n, "team", d.team, "as", p.login, "merged", a.Merged, "autoMerge", a.AutoMerge, "reason", landing.Reason, "rerendered", a.Rerendered != nil)
	return a, nil
}

// rerender re-renders the pull request on its base as the person when GitHub
// reports it conflicting (mergeable: false), and drops the conflict noted on
// the records of the entries it changes. Nil, nil for a mergeable pull
// request; a mergeability GitHub has not computed, or could not be read, is
// logged and lets the approval go ahead — Land says what GitHub does.
func (t *tools) rerender(ctx context.Context, p *person, d decision) (*teamfiles.Rerendered, error) {
	pr, conflicts, err := p.repo.Conflicts(ctx, d.pr)
	if err != nil {
		t.d.Log.Warn("pull request's mergeability not read", "pr", d.number, "error", err)
		return nil, nil
	}
	if !conflicts {
		return nil, nil
	}
	rr, err := p.repo.Rerender(ctx, pr)
	if err != nil {
		t.d.Log.Error("conflicting pull request not re-rendered", "pr", d.number, "as", p.login, "error", err)
		return nil, err
	}
	t.d.Log.Info("conflicting pull request re-rendered on its base", "pr", d.number, "as", p.login, "branch", rr.Branch, "base", rr.Base, "entries", rr.Entries)
	t.mergeableAgain(ctx, d.number, rr.Entries)
	return rr, nil
}

// mergeableAgain drops the conflict the poller noted on the records of the
// repositories whose pending run is pull request number.
func (t *tools) mergeableAgain(ctx context.Context, number int, names []string) {
	if t.d.Inventory == nil {
		return
	}
	for _, name := range names {
		rec, err := t.d.Inventory.Get(ctx, t.org()+"/"+name)
		if err != nil || !rec.Setup.PendingRun.Follows(number) || !rec.Setup.PendingRun.Conflicting() {
			continue
		}
		rec.Mergeable()
		if err := t.d.Inventory.Put(ctx, rec); err != nil {
			t.d.Log.Error("record not updated after the re-render", "repository", rec.Repository, "error", err)
		}
	}
}

// outcome is the approval's one sentence for the channel: approved as whom,
// what became of the pull request, and — when its base had moved — that it
// was re-rendered first, or that it could not be and why.
func (d decision) outcome(login string, l teamfiles.Landing, rr *teamfiles.Rerendered, rerenderError string) string {
	pr := fmt.Sprintf("%s/%s#%d", d.repo.Owner, d.repo.Name, d.number)
	if rerenderError != "" {
		return fmt.Sprintf("Approved as %s; %s is not merged: it conflicts with %s (a neighbouring entry changed first) and re-rendering it failed: %s Rebase it on GitHub.",
			login, pr, d.repo.Ref, strings.TrimSuffix(rerenderError, ".")+".")
	}
	// The re-render is a clause on the pull request; mid-sentence it is
	// closed by a comma.
	subject := pr
	if rr != nil {
		subject += fmt.Sprintf(", re-rendered on %s first (a neighbouring entry had changed),", d.repo.Ref)
	}
	switch {
	case l.Merged:
		return fmt.Sprintf("Approved as %s and merged: %s.", login, strings.TrimSuffix(subject, ","))
	case l.AutoMerge:
		return fmt.Sprintf("Approved as %s; %s merges by itself once its checks pass.", login, subject)
	default:
		return fmt.Sprintf("Approved as %s; %s is not merged: %s Merge it on GitHub.", login, subject, strings.TrimSuffix(l.Reason, ".")+".")
	}
}

// decision is what a pull request's approval turns on: the team whose member
// may give it and the person who opened it, who may not — and the pull
// request as read, for the re-render.
type decision struct {
	repo   teamfiles.Repo
	number int
	team   string
	author string
	pr     *github.PullRequest
}

// approvalBy is the approval p may give, or the refusal in the person's own
// words: the author of the pull request first (their membership does not
// matter), then a non-member of the deciding team.
func (d decision) approvalBy(p *person) (*Approval, error) {
	a := &Approval{PullRequest: d.number, Team: d.team, Author: d.author, Login: p.login, Teams: p.teams, Member: p.member(d.team)}
	pr := fmt.Sprintf("%s/%s#%d", d.repo.Owner, d.repo.Name, d.number)
	if strings.EqualFold(d.author, p.login) {
		return nil, fmt.Errorf("%w: %s opened %s, and GitHub does not accept an author's approval of their own pull request; another member of %s has to approve", ErrOwnPullRequest, p.login, pr, d.team)
	}
	if !a.Member {
		return nil, p.notAMember(d.team, fmt.Sprintf("the review of %s is not yours to give", pr))
	}
	return a, nil
}

// decision reads the pull request once for its author and the deciding team:
// the marker this server wrote into the body, else the one team file the
// pull request touches.
func (t *tools) decision(ctx context.Context, repo teamfiles.Repo, n int) (decision, error) {
	pr, _, err := repo.Client.PullRequests.Get(ctx, repo.Owner, repo.Name, n)
	if err != nil {
		return decision{}, fmt.Errorf("%s/%s#%d: %w", repo.Owner, repo.Name, n, err)
	}
	d := decision{repo: repo, number: n, author: pr.GetUser().GetLogin(), pr: pr}
	if team := teamFromMarker(pr.GetBody()); team != "" {
		d.team = team
		return d, nil
	}
	teams, err := repo.ChangedTeamFiles(ctx, n)
	if err != nil {
		return decision{}, err
	}
	switch len(teams) {
	case 0:
		return decision{}, fmt.Errorf("%s/%s#%d changes no team file: nothing for this tool to decide", repo.Owner, repo.Name, n)
	case 1:
		d.team = teams[0]
		return d, nil
	default:
		return decision{}, fmt.Errorf("%s/%s#%d changes the files of %s and was not opened by this server: approve it on GitHub", repo.Owner, repo.Name, n, strings.Join(teams, " and "))
	}
}

// --- align_repository -----------------------------------------------------

// Mode of an align_repository run, decided by the repository's entry: the
// repository opted in and the dispatched run applies what it finds; it is
// declared without the opt-in and the run is the pull request that opts it
// in (nothing is dispatched); it has no entry and the dispatched run checks
// from the team alone.
const (
	DispatchModeAlign = "align"
	DispatchModeCheck = "check"
	DispatchModeOptIn = "opt-in"
)

// OptIn is what an Align now does for a declared repository without
// `align: true` instead of a dispatch: the team-file pull request that sets
// the field, planned (the dry run) and, in mode commit, opened as the person.
type OptIn struct {
	Plan      Plan       `json:"plan"`
	Committed *Committed `json:"committed,omitempty"`
}

// PlannedStep is one step's planned changes from the inventory's last check:
// what an alignment applies.
type PlannedStep struct {
	Step    string   `json:"step"`
	Changes []string `json:"changes"`
}

// Dispatch is align_repository's result.
type Dispatch struct {
	Workflow string         `json:"workflow"`
	Inputs   map[string]any `json:"inputs"`
	// As is the identity the dispatch runs under.
	As string `json:"as"`
	// Dispatched says the workflow was dispatched; false in mode opt-in,
	// where nothing is.
	Dispatched bool   `json:"dispatched"`
	RunsURL    string `json:"runsUrl"`
	// Then says how the result comes back.
	Then string `json:"then"`
	// Findings is the inventory's refusal of the entry when its last check
	// refused it: the run would report the refusal and run no step.
	Findings []reconcile.Finding `json:"findings,omitempty"`
	// PendingRun is the record's pending run after the dispatch: the
	// inventory reads the run's artifact within seconds of its completion,
	// get_repository shows setup.lastRun then. An opt-in's pending run is
	// in OptIn.Committed.
	PendingRun *inventory.PendingRun `json:"pendingRun,omitempty"`
	// Team is the team the run is for: the team input, else the declaring
	// entry's file.
	Team string `json:"team,omitempty"`
	// Declared says the repository has an entry in that team's file (in
	// any team's file without a team input). Without one the run checks
	// from the team alone.
	Declared bool `json:"declared"`
	// OptedIn is the declaring entry's align (`align: true` in
	// repositories/<team>.yaml): the repository's opt-in to alignment.
	// Without it the run is a check whatever the mode asked.
	OptedIn bool `json:"optedIn"`
	// Mode is what the run does to the repository: align, check or opt-in.
	Mode string `json:"mode"`
	// OptIn is the pull request that opts the repository in, in mode opt-in
	// only: its plan and, after the commit, what became of it.
	OptIn *OptIn `json:"optIn,omitempty"`
	// Planned are the changes the inventory's last check found, per step —
	// what an alignment applies; absent without a check or when converged.
	Planned []PlannedStep `json:"planned,omitempty"`
	// CheckedAt is when that check ran.
	CheckedAt string `json:"checkedAt,omitempty"`
	// Warning says in one paragraph what an alignment changes on the
	// repository and whether this run changes anything.
	Warning string `json:"warning"`
}

// alignChanges is what an alignment does to a repository, for the warning
// and the tool description: the engine's baseline in one sentence.
const alignChanges = "merge settings (squash only, auto-merge, delete branch on merge, update branch), wiki and projects off, issues on, " +
	"team permissions (employees admin, bots push), branch protection (one approving review, enforce_admins on, strict up-to-date, " +
	"every reporting check required), the CircleCI follow and setup workflows, a CODEOWNERS pull request, description and visibility, " +
	"lifecycle, catalog and mapping, and a missed release build"

// alignTrue is the field of an entry that opts the repository in to
// alignment, as the answers spell it.
const alignTrue = "`align: true`"

// runsAsYou closes the warning's account of what changes: the identity.
const runsAsYou = "It runs as you."

// alignWarning is the paragraph a person reads before confirming: what an
// alignment changes on this repository, that it runs as them, and what this
// run does — the entry's opt-in decides: applied, opted in first, or checked.
func alignWarning(repository, team string, optedIn, declared bool) string {
	head := fmt.Sprintf("Align now changes %s on GitHub and CircleCI to its declared set-up and the company baseline: %s. %s ",
		repository, alignChanges, runsAsYou)
	switch {
	case !declared && team == "":
		return head + fmt.Sprintf("%s has no entry and no team is known for it: pass team. The run then checks from the team alone and changes nothing; declare the repository with %s in its entry to have it aligned.", repository, alignTrue)
	case !declared:
		return head + fmt.Sprintf("%s has no entry: this run checks from the team alone and changes nothing; declare the repository with %s in its entry to have it aligned.", repository, alignTrue)
	case optedIn:
		return head + fmt.Sprintf("%s is opted in to alignment (%s in its entry): the planned changes are applied.", repository, alignTrue)
	default:
		return fmt.Sprintf("%s has not opted in to alignment. Align now opts it in — %s in its entry, in a pull request %s reviews (the ask goes to %s's channel; a member other than you approves) — and the reconciler applies the planned changes when it merges: %s. %s",
			repository, alignTrue, team, team, alignChanges, runsAsYou)
	}
}

// dispatchThen says how a dispatched run's result comes back.
func dispatchThen(name string) string {
	return "the inventory reads the run's reconcile-" + name + " artifact from GitHub within seconds of the run completing: get_repository shows setup.pendingRun until then, setup.lastRun after, with its failed steps and findings; the team's standup channel hears nothing about a dispatch"
}

// optInThen says how an opt-in's result comes back: through the reconciler's
// run of the merged pull request.
func optInThen(repository string) string {
	return fmt.Sprintf("when the pull request merges, the reconciler aligns %s (its push run); the record shows setup.pendingRun until that run reports", repository)
}

// plannedSentence says what the reconciler applies once the opt-in merges:
// the last check's changes, counted, and the steps they belong to — or why
// no change is known.
func plannedSentence(planned []PlannedStep, checkedAt string) string {
	if len(planned) == 0 {
		why := "the last check found the repository converged"
		if checkedAt == "" {
			why = "no check has run yet"
		}
		return "the reconciler applies what its run finds once merged (" + why + ")"
	}
	n, steps := 0, make([]string, 0, len(planned))
	for _, s := range planned {
		n += len(s.Changes)
		steps = append(steps, s.Step)
	}
	count := "the planned changes"
	switch n {
	case 0:
	case 1:
		count = "1 planned change"
	default:
		count = fmt.Sprintf("%d planned changes", n)
	}
	return fmt.Sprintf("the reconciler applies %s once merged — %s", count, strings.Join(steps, ", "))
}

// plannedList is the last check's planned changes per step, for the pull
// request body; empty without any.
func plannedList(planned []PlannedStep) string {
	if len(planned) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nPlanned changes from the last check:\n")
	for _, s := range planned {
		fmt.Fprintf(&b, "\n- **%s**: %s", s.Step, strings.Join(s.Changes, "; "))
	}
	return b.String()
}

// planOptIn is the plan of the pull request an Align now opens for a
// declared repository without `align: true`: its entry with the field added
// and nothing else changed, the last check's planned changes in the body and
// in the ask to the owning team. The reconciler's run of the merged pull
// request aligns the repository; the record expects it as a change.
func (t *tools) planOptIn(ctx context.Context, repo teamfiles.Repo, as string, tf *teamfiles.TeamFile, before reposetup.Declaration, planned []PlannedStep, checkedAt string) (*Plan, error) {
	name := before.Name
	after, err := teamfiles.SetField(before, teamfiles.FieldAlign, "true")
	if err != nil {
		return nil, err
	}
	pl, err := t.replacePlan(ctx, tf, name, before, after)
	if err != nil {
		return nil, err
	}
	applies := plannedSentence(planned, checkedAt)
	pl.finish(repo, as, "reposetup/align-"+name, fmt.Sprintf("chore(repositories): opt %s in to alignment (%s)", name, tf.Team),
		fmt.Sprintf("## Problem\n\n`%s/%s` is to be aligned with its declared set-up and the company baseline, and its entry has not opted in: without %s the reconciler checks and changes nothing.\n\n## Solution\n\n%s in the entry in `%s`, nothing else changed; %s.%s\n\nOpened by giantswarm-repo-manager (`%s`) as the caller.",
			t.org(), name, alignTrue, alignTrue, tf.Path, applies, plannedList(planned), ToolAlignRepository))
	pl.Ask = t.message(ctx, repo, tf.Team, fmt.Sprintf("%s asks to align `%s/%s` (owned by %s): the change opts it in to alignment (%s) and %s.%s", as, t.org(), name, tf.Team, alignTrue, applies, decides(tf.Team, as)), true)
	return pl, nil
}

func (t *tools) alignRepository() WriteTool {
	return WriteTool{
		Name: ToolAlignRepository,
		Description: "Align now: aligns one repository with its declared set-up and the company baseline. WARNING — an alignment changes the repository " +
			"on GitHub and CircleCI: " + alignChanges + ". The repository's entry in repositories/<team>.yaml decides the mode, and the answer (dry run and " +
			"commit alike) says which. align: the repository has opted in (" + alignTrue + " in its entry) — the reconcile-repositories workflow in giantswarm/github " +
			"is dispatched for it as you and applies the planned changes. opt-in: the repository is declared without the field — nothing is dispatched; the team-file " +
			"pull request that sets " + alignTrue + " in its entry (nothing else changes) is planned (dry run) or opened as you (commit) with auto-merge armed, the ask " +
			"with the Approve button goes to the owning team's channel (a member other than you approves), and the reconciler's run aligns the repository when it merges. " +
			"check: the repository has no entry (pass team) — the workflow is dispatched as you and checks from the team alone, changing nothing. The answer carries " +
			"mode, optedIn, declared, team, the planned changes from the inventory's last check (per step, with checkedAt), a warning paragraph to show the person " +
			"before they confirm, and for opt-in the plan (optIn.plan: the entry before and after, the pull request, the ask) and after the commit what became of it " +
			"(optIn.committed: pullRequest, ask, pendingRun). The record shows setup.pendingRun until the inventory has read the run's artifact (within seconds of a " +
			"dispatched run completing; after the merge for an opt-in) as setup.lastRun, with the run's change block (kind, by, pullRequest) — its failed steps and " +
			"findings are on the record and in the run. The team's standup channel hears nothing about a dispatch or the opt-in itself: the sentences about who created, " +
			"added, transferred, archived or deprecated a repository, and the failed steps and findings of a run, follow a merged pull request only. A run that does not " +
			"report within 15 minutes leaves the finding reconcile-run-missing. Here mode commit means: dispatch — or, for opt-in, open the pull request.",
		Options: []mcp.ToolOption{
			mcp.WithString(argRepository, mcp.Required(), mcp.Description("Repository name, with or without the org.")),
			mcp.WithString(argTeam, mcp.Description("Team slug; required for a repository without an entry (it is then aligned from the team alone), optional otherwise.")),
		},
		DryRun: func(ctx context.Context, args map[string]any) (any, error) {
			return t.dispatch(ctx, args, false)
		},
		Commit: func(ctx context.Context, args map[string]any) (any, error) {
			return t.dispatch(ctx, args, true)
		},
	}
}

func (t *tools) dispatch(ctx context.Context, args map[string]any, run bool) (*Dispatch, error) {
	name, err := t.repositoryName(args)
	if err != nil {
		return nil, err
	}
	inputs := map[string]any{argRepository: name}
	if team, _ := args[argTeam].(string); strings.TrimSpace(team) != "" {
		inputs[argTeam] = strings.TrimSpace(team)
	}
	workflow := t.d.reconcilerWorkflow()
	repository := t.org() + "/" + name
	d := &Dispatch{Workflow: workflow, Inputs: inputs}
	// The inventory's last check says what an alignment would apply, and
	// whether it refused the entry.
	var rec *inventory.Record
	if t.d.Inventory != nil {
		key, _ := t.repositoryKey(args)
		if r, err := t.d.Inventory.Get(ctx, key); err == nil {
			rec = r
		}
	}
	refused := ""
	if rec != nil && rec.Setup.Checks != nil {
		if rec.Setup.Checks.Step(reconcile.StepEntry) != nil {
			d.Findings = rec.Setup.Checks.Findings()
			refused = "the inventory's last check refused the entry (findings): unless the team file changed since, the run reports the refusal and runs no step — fix the entry with update_repository first; "
		}
		d.CheckedAt = rec.Setup.Checks.FinishedAt.UTC().Format(time.RFC3339)
		for _, s := range rec.Setup.Checks.Steps {
			if s.Verdict == reconcile.VerdictDrift {
				d.Planned = append(d.Planned, PlannedStep{Step: string(s.Step), Changes: s.Changes})
			}
		}
	}
	p, err := t.person(ctx)
	if err != nil {
		return nil, err
	}
	d.As, d.RunsURL = p.login, p.repo.WorkflowURL(workflow)
	// The entry's opt-in decides what the run does; the last check says what
	// an alignment would apply. Both are in the answer for the person to read
	// before confirming. The entry is read from the team files on main: the
	// team input names the file, else the inventory's record is the hint
	// that saves reading every file.
	d.Team, _ = inputs[argTeam].(string)
	hint := d.Team
	if hint == "" && rec != nil && rec.Declaration != nil {
		hint = rec.Declaration.Team
	}
	var tf *teamfiles.TeamFile
	var entry reposetup.Declaration
	switch found, err := p.repo.FindEntry(ctx, name, hint); {
	case err == nil:
		tf = found
		entry, _ = tf.Entries.Entry(name)
		f, err := entry.Fields()
		if err != nil {
			return nil, fmt.Errorf("reading the entry of %s: %w", name, err)
		}
		d.Declared, d.OptedIn = true, f.Align
		if d.Team == "" {
			d.Team = tf.Team
		}
	case !errors.Is(err, teamfiles.ErrEntryNotFound):
		return nil, fmt.Errorf("reading the team files for %s: %w", name, err)
	}
	d.Mode, d.Then = DispatchModeCheck, refused+dispatchThen(name)
	var optIn *Plan
	switch {
	case d.OptedIn:
		d.Mode = DispatchModeAlign
	case d.Declared:
		// Declared without the opt-in: the run is the pull request that opts
		// the repository in; the reconciler's run of its merge aligns it.
		if optIn, err = t.planOptIn(ctx, p.repo, p.login, tf, entry, d.Planned, d.CheckedAt); err != nil {
			return nil, err
		}
		d.Mode, d.Then, d.OptIn = DispatchModeOptIn, refused+optInThen(repository), &OptIn{Plan: *optIn}
	}
	d.Warning = alignWarning(repository, d.Team, d.OptedIn, d.Declared)
	if !run {
		return d, nil
	}
	if optIn != nil {
		committed, err := t.commit(ctx, p, optIn)
		if err != nil {
			return nil, err
		}
		d.OptIn.Committed = committed
		return d, nil
	}
	if err := p.repo.Dispatch(ctx, workflow, inputs); err != nil {
		return nil, fmt.Errorf("%w (the dispatch runs as you: it needs Actions write on %s/%s for you through the App giantswarm-repo-manager)", err, p.repo.Owner, p.repo.Name)
	}
	d.Dispatched = true
	t.d.Log.Info("reconciler dispatched", "repository", name, "as", p.login)
	// A workflow_dispatch returns no run id: the record waits for the run's
	// artifact as pendingRun, which the woken poller answers or gives up.
	if rec != nil {
		rec.Dispatched(time.Now().UTC(), p.login)
		if err := t.putExpectedRun(ctx, rec); err != nil {
			t.d.Log.Error("pending run not stored", "repository", name, "error", err)
		} else {
			d.PendingRun = rec.Setup.PendingRun
		}
	}
	return d, nil
}
