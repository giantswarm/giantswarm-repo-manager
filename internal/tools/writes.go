package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
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
	ToolUpdateRepository    = "update_repository"
	ToolTransferRepository  = "transfer_repository"
	ToolSetLifecycle        = "set_lifecycle"
	ToolApproveChange       = "approve_change"
	ToolReconcileRepository = "reconcile_repository"

	argToTeam      = "toTeam"
	argLifecycle   = "lifecycle"
	argPullRequest = "pullRequest"
	argReason      = "reason"
)

// marker is the machine-readable line every pull request this server opens
// carries: approve_change reads the deciding team from it.
const marker = "<!-- giantswarm-repo-manager: team=%s -->"

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
}

// Delivery is what became of an ask or notice.
type Delivery struct {
	Team      string `json:"team"`
	Channel   string `json:"channel,omitempty"`
	Delivered bool   `json:"delivered"`
	ReviewID  string `json:"reviewId,omitempty"`
	Error     string `json:"error,omitempty"`
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
	out := &Committed{PullRequest: pr}
	if pl.Ask != nil {
		out.Ask = t.deliver(ctx, pl.Ask, pr, true)
	}
	if pl.Notice != nil {
		out.Notice = t.deliver(ctx, pl.Notice, pr, false)
	}
	t.d.Log.Info("pull request opened", "tool", pl.change.Title, "pr", pr.URL, "as", pr.Author)
	return out, nil
}

// deliver posts a planned message; a failure is reported, not fatal — the
// pull request exists and approving on GitHub is equivalent (PRD D6).
func (t *tools) deliver(ctx context.Context, m *PlannedMessage, pr *teamfiles.PullRequest, ask bool) *Delivery {
	d := &Delivery{Team: m.Team, Channel: m.Channel}
	if !m.Deliverable {
		d.Error = m.Reason
		return d
	}
	text := m.Text + " — " + pr.URL
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
			"the team's review. Use set_lifecycle to deprecate or archive and transfer_repository to move a repository to another team.",
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
	pl := &Plan{Repository: t.org() + "/" + name, Team: tf.Team, Problems: problems, change: teamfiles.Change{Files: map[string][]byte{tf.Path: content}}}
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
			"gets a notice in its standup channel. The reconciler then re-applies permissions, CODEOWNERS and the catalog mapping for the new owner.",
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
		change: teamfiles.Change{Files: map[string][]byte{from.Path: without, dst.Path: with}}}
	pl.Entry, _ = d.YAML()
	reason, _ := args[argReason].(string)
	pl.finish(repo, as, "reposetup/transfer-"+name, fmt.Sprintf("chore(repositories): transfer %s from %s to %s", name, from.Team, to),
		fmt.Sprintf("## Problem\n\n`%s/%s` changes owner.\n\n## Solution\n\nThe entry moves from `%s` (giving team: **%s**) to `%s` (receiving team: **%s**), unchanged. "+
			"The reconciler re-applies team permissions, CODEOWNERS and the catalog mapping for %s after this merges.\n\n%s\n\nOpened by giantswarm-repo-manager (`%s`) as the caller.",
			t.org(), name, from.Path, from.Team, dst.Path, to, to, reasonLine(reason), ToolTransferRepository))
	pl.Ask = t.message(ctx, repo, to, fmt.Sprintf("%s asks to transfer `%s/%s` from %s to %s: your team receives it.%s", as, t.org(), name, from.Team, to, reasonSuffix(reason)), true)
	pl.Notice = t.message(ctx, repo, from.Team, fmt.Sprintf("%s asks to transfer `%s/%s` from %s to %s: your team gives it; %s decides.%s", as, t.org(), name, from.Team, to, to, reasonSuffix(reason)), false)
	return pl, nil
}

func reasonSuffix(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return " " + strings.TrimSpace(reason)
}

// --- set_lifecycle --------------------------------------------------------

func (t *tools) setLifecycle() WriteTool {
	return WriteTool{
		Name: ToolSetLifecycle,
		Description: "Deprecate or archive a declared repository by setting lifecycle in its team-file entry. deprecated: security-only Renovate " +
			"and a catalog flag. archived: the reconciler archives the repository on GitHub and unfollows it on CircleCI; the entry stays as the " +
			"record. Deletion is not expressible. The ask goes to the owning team's channel; a member's Approve (or an approving review on GitHub) lands it.",
		Options: []mcp.ToolOption{
			mcp.WithString(argRepository, mcp.Required(), mcp.Description("Repository name, with or without the org.")),
			mcp.WithString(argLifecycle, mcp.Required(), mcp.Enum(teamfiles.LifecycleDeprecated, teamfiles.LifecycleArchived), mcp.Description("deprecated or archived.")),
			mcp.WithString(argReason, mcp.Description("Why, for the pull request body and the ask.")),
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
	if lc != teamfiles.LifecycleDeprecated && lc != teamfiles.LifecycleArchived {
		return nil, fmt.Errorf("%s must be %s or %s", argLifecycle, teamfiles.LifecycleDeprecated, teamfiles.LifecycleArchived)
	}
	tf, before, err := t.entryFor(ctx, repo, name, "")
	if err != nil {
		return nil, err
	}
	if f, err := before.Fields(); err == nil && f.Lifecycle == lc {
		return nil, fmt.Errorf("%s is %s already", name, lc)
	}
	after, err := teamfiles.SetField(before, teamfiles.FieldLifecycle, lc)
	if err != nil {
		return nil, err
	}
	pl, err := t.replacePlan(ctx, tf, name, before, after)
	if err != nil {
		return nil, err
	}
	reason, _ := args[argReason].(string)
	effect := "security-only Renovate updates and the catalog's deprecation flag"
	if lc == teamfiles.LifecycleArchived {
		effect = "the reconciler archives the repository on GitHub and unfollows it on CircleCI; the entry stays in the team file as the record"
	}
	pl.finish(repo, as, "reposetup/"+lc+"-"+name, fmt.Sprintf("chore(repositories): %s %s (%s)", verb(lc), name, tf.Team),
		fmt.Sprintf("## Problem\n\n`%s/%s` is to be %s.\n\n## Solution\n\n`lifecycle: %s` in `%s` — %s.\n\n%s\n\nOpened by giantswarm-repo-manager (`%s`) as the caller.",
			t.org(), name, lc, lc, tf.Path, effect, reasonLine(reason), ToolSetLifecycle))
	pl.Ask = t.message(ctx, repo, tf.Team, fmt.Sprintf("%s asks to %s `%s/%s` (owned by %s).%s", as, verb(lc), t.org(), name, tf.Team, reasonSuffix(reason)), true)
	return pl, nil
}

func verb(lifecycle string) string {
	if lifecycle == teamfiles.LifecycleArchived {
		return "archive"
	}
	return "deprecate"
}

// --- approve_change -------------------------------------------------------

// Approval is approve_change's result.
type Approval struct {
	PullRequest int      `json:"pullRequest"`
	Team        string   `json:"team"`
	Login       string   `json:"login"`
	Teams       []string `json:"teams,omitempty"`
	Member      bool     `json:"member"`
	ReviewURL   string   `json:"reviewUrl,omitempty"`
}

func (t *tools) approveChange() WriteTool {
	return WriteTool{
		Name: ToolApproveChange,
		Description: "Approve a team-file pull request as you, after this server has checked on GitHub that you are a member of the team " +
			"the change belongs to (the owning team; for a transfer the receiving team). The Approve button of a Slack ask calls this tool as the " +
			"clicking member; a member may also call it directly, and approving on GitHub is equivalent. A non-member is refused.",
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

func (t *tools) approve(ctx context.Context, args map[string]any, submit bool) (*Approval, error) {
	n := int(number(args, argPullRequest, 0))
	if n <= 0 {
		return nil, fmt.Errorf("%s is required", argPullRequest)
	}
	p, err := t.person(ctx)
	if err != nil {
		return nil, err
	}
	team, err := t.decidingTeam(ctx, p.repo, n)
	if err != nil {
		return nil, err
	}
	a := &Approval{PullRequest: n, Team: team, Login: p.login, Teams: p.teams, Member: p.member(team)}
	if !a.Member {
		return nil, p.notAMember(team, fmt.Sprintf("the review of %s#%d is not yours to give", p.repo.Owner+"/"+p.repo.Name, n))
	}
	if !submit {
		return a, nil
	}
	url, err := p.repo.Approve(ctx, n, fmt.Sprintf("Approved as a member of %s through giantswarm-repo-manager.", team))
	if err != nil {
		return nil, err
	}
	a.ReviewURL = url
	t.d.Log.Info("pull request approved", "pr", n, "team", team, "as", p.login)
	return a, nil
}

// decidingTeam is the team whose member may approve: the marker this server
// wrote into the body, else the one team file the pull request touches.
func (t *tools) decidingTeam(ctx context.Context, repo teamfiles.Repo, n int) (string, error) {
	pr, _, err := repo.Client.PullRequests.Get(ctx, repo.Owner, repo.Name, n)
	if err != nil {
		return "", fmt.Errorf("%s#%d: %w", repo.Owner+"/"+repo.Name, n, err)
	}
	if _, after, ok := strings.Cut(pr.GetBody(), "<!-- giantswarm-repo-manager: team="); ok {
		if team, _, ok := strings.Cut(after, " -->"); ok && team != "" {
			return team, nil
		}
	}
	teams, err := repo.ChangedTeamFiles(ctx, n)
	if err != nil {
		return "", err
	}
	switch len(teams) {
	case 0:
		return "", fmt.Errorf("%s#%d changes no team file: nothing for this tool to decide", repo.Owner+"/"+repo.Name, n)
	case 1:
		return teams[0], nil
	default:
		return "", fmt.Errorf("%s#%d changes the files of %s and was not opened by this server: approve it on GitHub", repo.Owner+"/"+repo.Name, n, strings.Join(teams, " and "))
	}
}

// --- reconcile_repository -------------------------------------------------

// Dispatch is reconcile_repository's result.
type Dispatch struct {
	Workflow string         `json:"workflow"`
	Inputs   map[string]any `json:"inputs"`
	// As is the identity the dispatch runs under.
	As         string `json:"as"`
	Dispatched bool   `json:"dispatched"`
	RunsURL    string `json:"runsUrl"`
	// Then says how the result comes back.
	Then string `json:"then"`
	// Findings is the inventory's refusal of the entry when its last check
	// refused it: the run would report the refusal and run no step.
	Findings []reconcile.Finding `json:"findings,omitempty"`
	// PendingRun is the record's pending run after the dispatch: the
	// inventory reads the run's artifact within seconds of its completion,
	// get_repository shows setup.lastRun then.
	PendingRun *inventory.PendingRun `json:"pendingRun,omitempty"`
}

func (t *tools) reconcileRepository() WriteTool {
	return WriteTool{
		Name: ToolReconcileRepository,
		Description: "Run the reconciler for one repository now (Reconcile now): dispatches the reconcile-repositories workflow in giantswarm/github " +
			"as you, which runs the engine's set-up steps for that repository — settings, permissions, protection, CircleCI, Renovate check, CODEOWNERS, " +
			"metadata, lifecycle, catalog, release. The record shows setup.pendingRun until the inventory has read the run's artifact (within " +
			"seconds of the run completing) as setup.lastRun, with the run's change block (kind, by, pullRequest). The team's standup channel " +
			"gets one sentence per failed step or finding with the fix; a run with nothing to fix posts nothing (the sentence about who created, " +
			"added, transferred, archived or deprecated a repository follows the merged pull request, not a dispatch). A run that does not report " +
			"within 15 minutes leaves the finding reconcile-run-missing. Nothing is written to the team files. Here mode commit means: dispatch.",
		Options: []mcp.ToolOption{
			mcp.WithString(argRepository, mcp.Required(), mcp.Description("Repository name, with or without the org.")),
			mcp.WithString(argTeam, mcp.Description("Team slug; required for a repository without an entry (it is then reconciled from the team alone), optional otherwise.")),
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
	d := &Dispatch{Workflow: workflow, Inputs: inputs,
		Then: "the inventory reads the run's reconcile-" + name + " artifact from GitHub within seconds of the run completing: get_repository shows setup.pendingRun until then, setup.lastRun after; the team's standup channel gets one sentence per failed step or finding, nothing when there is nothing to fix"}
	var rec *inventory.Record
	if t.d.Inventory != nil {
		key, _ := t.repositoryKey(args)
		if r, err := t.d.Inventory.Get(ctx, key); err == nil {
			rec = r
		}
		if rec != nil && rec.Setup.Checks != nil && rec.Setup.Checks.Step(reconcile.StepEntry) != nil {
			d.Findings = rec.Setup.Checks.Findings()
			d.Then = "the inventory's last check refused the entry (findings): unless the team file changed since, the run reports the refusal and runs no step — fix the entry with update_repository first; " + d.Then
		}
	}
	p, err := t.person(ctx)
	if err != nil {
		return nil, err
	}
	d.As, d.RunsURL = p.login, p.repo.WorkflowURL(workflow)
	if !run {
		return d, nil
	}
	if err := p.repo.Dispatch(ctx, workflow, inputs); err != nil {
		return nil, fmt.Errorf("%w (the dispatch runs as you: it needs Actions write on %s/%s for you through the App giantswarm-repo-manager)", err, p.repo.Owner, p.repo.Name)
	}
	d.Dispatched = true
	t.d.Log.Info("reconciler dispatched", "repository", name, "as", p.login)
	// A workflow_dispatch returns no run id: the record waits for the run's
	// artifact as pendingRun, which the poller answers or gives up.
	if rec != nil {
		rec.Dispatched(time.Now().UTC(), p.login)
		if err := t.d.Inventory.Put(ctx, rec); err != nil {
			t.d.Log.Error("pending run not stored", "repository", name, "error", err)
		} else {
			d.PendingRun = rec.Setup.PendingRun
		}
	}
	return d, nil
}
