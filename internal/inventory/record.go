package inventory

import (
	"fmt"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

// Sources of a record's last write.
const (
	SourceSweep      = "sweep"
	SourceRefresh    = "refresh"
	SourceReconciler = "reconciler"
)

// Record is one repository of the org as the inventory knows it (D8): the
// declaration from the team files, the reality from GitHub, the set-up state
// and the findings nobody repairs. The shape is documented in
// docs/inventory-record.md; the reconciler's per-repository artifact matches
// LastRun. A stored record may carry fields of an earlier shape (an orphan
// score, a decision note); they are ignored on read.
type Record struct {
	// Repository is owner/name; the key of the record.
	Repository string `json:"repository"`
	Name       string `json:"name"`
	// Declaration is the entry of the team file on main; nil means unassigned.
	Declaration *Declaration `json:"declaration"`
	// Reality is the repository on GitHub; nil means gone.
	Reality *Reality `json:"reality"`
	// CircleCI is the project's state on CircleCI as GitHub and the
	// reconciler tell it; nil when the repository is gone.
	CircleCI *CircleCI `json:"circleci,omitempty"`
	// CI is what the repository's CircleCI configuration says; nil when the
	// default branch carries none.
	CI       *CI       `json:"ci,omitempty"`
	Renovate Renovate  `json:"renovate"`
	Catalog  Catalog   `json:"catalog"`
	Mapping  Mapping   `json:"mapping"`
	Setup    Setup     `json:"setup"`
	Findings []Finding `json:"findings"`
	// RefreshedAt is when the record was last written, Source by what.
	RefreshedAt time.Time `json:"refreshedAt"`
	Source      string    `json:"source"`
	// Age is RefreshedAt to now, filled when the record is read.
	Age string `json:"age,omitempty"`
}

// Declaration is the team file's entry, with the engine's verdict on it.
type Declaration struct {
	Team          string   `json:"team"`
	File          string   `json:"file"`
	ComponentType string   `json:"componentType,omitempty"`
	Lifecycle     string   `json:"lifecycle,omitempty"`
	Language      string   `json:"language,omitempty"`
	Flavours      []string `json:"flavours,omitempty"`
	// Entry is the declaration as the team file carries it (one-item YAML list).
	Entry string `json:"entry"`
	// Accepted is the engine's validation verdict; Problems the refusals.
	Accepted bool     `json:"accepted"`
	Problems []string `json:"problems,omitempty"`
}

// Reality is what GitHub says (GraphQL, the App's budget).
type Reality struct {
	URL           string    `json:"url"`
	Description   string    `json:"description,omitempty"`
	Visibility    string    `json:"visibility"`
	DefaultBranch string    `json:"defaultBranch,omitempty"`
	IsArchived    bool      `json:"isArchived"`
	IsFork        bool      `json:"isFork"`
	IsTemplate    bool      `json:"isTemplate"`
	IsEmpty       bool      `json:"isEmpty"`
	CreatedAt     time.Time `json:"createdAt"`
	PushedAt      time.Time `json:"pushedAt,omitempty"`
	Language      string    `json:"language,omitempty"`
	Topics        []string  `json:"topics,omitempty"`
	// LastCommit and LastPersonCommit come from the default branch's recent
	// history; the person one excludes the bot identities.
	LastCommit       *Commit `json:"lastCommit,omitempty"`
	LastPersonCommit *Commit `json:"lastPersonCommit,omitempty"`
	// HistorySampled is how many recent commits were inspected, BotCommits how
	// many of them bots made.
	HistorySampled   int          `json:"historySampled"`
	BotCommits       int          `json:"botCommits"`
	OpenPullRequests PullRequests `json:"openPullRequests"`
	OpenIssues       int          `json:"openIssues"`
	LatestRelease    *Release     `json:"latestRelease,omitempty"`
	CodeownersTeams  []string     `json:"codeownersTeams,omitempty"`
	// UnknownCodeownersTeams are the CODEOWNERS teams the org does not have
	// (dissolved or misspelt).
	UnknownCodeownersTeams []string `json:"unknownCodeownersTeams,omitempty"`
	Has                    Presence `json:"has"`
}

// Commit is one commit of the default branch.
type Commit struct {
	Date    time.Time `json:"date"`
	Author  string    `json:"author"`
	Message string    `json:"message"`
}

// PullRequests splits the open pull requests into people and bots.
type PullRequests struct {
	Total    int `json:"total"`
	People   int `json:"people"`
	Bots     int `json:"bots"`
	Renovate int `json:"renovate"`
	// OldestBotAt is the creation time of the oldest open bot PR.
	OldestBotAt *time.Time `json:"oldestBotAt,omitempty"`
	// Onboarding are open pull requests of the set-up automation (reposetup/*
	// branches, align-files, Renovate's onboarding).
	Onboarding []PullRequest `json:"onboarding,omitempty"`
}

// PullRequest is one pull request.
type PullRequest struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
}

// Release is the latest release.
type Release struct {
	Tag         string    `json:"tag"`
	PublishedAt time.Time `json:"publishedAt"`
	// Build is the tag commit's `ci/circleci:` statuses — whether CircleCI
	// built the release; nil when the commit carries none.
	Build *HeadStatus `json:"build,omitempty"`
	// BuildTruncated says the commit's status contexts were more than were
	// read and none of the read ones was CircleCI's.
	BuildTruncated bool `json:"buildTruncated,omitempty"`
}

// Presence says which set-up files the default branch carries.
type Presence struct {
	Renovate   bool `json:"renovate"`
	Dependabot bool `json:"dependabot"`
	CircleCI   bool `json:"circleci"`
	Workflows  bool `json:"workflows"`
	Dockerfile bool `json:"dockerfile"`
	Helm       bool `json:"helm"`
	Readme     bool `json:"readme"`
	Codeowners bool `json:"codeowners"`
}

// CircleCI is the project's state on CircleCI without a CircleCI token: the
// `ci/circleci:` commit statuses on the default branch head say whether
// CircleCI builds the repository (it posts them for the projects it builds,
// nothing else does), the reconciler's last run — its circleci step — says
// whether the project is followed, setup workflows are on and CircleCI's
// webhook is installed. Source names the sources that answered, Unknown the
// facts none of them yields; the last pipeline is not derivable and is not
// part of the record.
type CircleCI struct {
	// Followed says CircleCI builds the repository: statuses on the head, or
	// the reconciler's circleci step found (or made) the project followed.
	Followed bool `json:"followed"`
	// SetupWorkflows is the project's setup-workflows setting as the
	// reconciler's last run saw or set it; nil when no run tells.
	SetupWorkflows *bool `json:"setupWorkflows,omitempty"`
	// Webhook says whether the repository carries the webhook CircleCI
	// installs on the follow (active, push events), without which no push
	// and no tag reaches CircleCI, as the reconciler's last run verified it;
	// nil when no run tells.
	Webhook *bool `json:"webhook,omitempty"`
	// Head is the default branch head's CircleCI statuses; nil when it has
	// none.
	Head *HeadStatus `json:"head,omitempty"`
	// Source is what answered: statuses, artifact, or statuses+artifact.
	Source string `json:"source"`
	// Unknown names the facts no source yields: followed (the head's status
	// contexts were truncated and none was CircleCI's), setupWorkflows,
	// webhook.
	Unknown []string `json:"unknown,omitempty"`
	// Error is the reconciler's circleci step failing, as its run reported it.
	Error string `json:"error,omitempty"`
}

// Sources of the CircleCI state and the facts that can be unknown.
const (
	CircleCISourceStatuses = "statuses"
	CircleCISourceArtifact = "artifact"
	CircleCISourceBoth     = CircleCISourceStatuses + "+" + CircleCISourceArtifact

	CircleCIFactFollowed       = "followed"
	CircleCIFactSetupWorkflows = "setupWorkflows"
	CircleCIFactWebhook        = "webhook"
)

// HeadStatus is a commit's `ci/circleci:` commit statuses: the default
// branch head's, or a release's tag commit's. A status is per commit, not
// per pipeline: every pipeline that built the commit posted its jobs'
// statuses here, and the reader tells them apart by the jobs' names against
// the declaration (CI.Jobs).
type HeadStatus struct {
	// State is the worst state among the contexts: failure, error, pending,
	// expected or success.
	State string `json:"state"`
	// Contexts are the status contexts, `ci/circleci: <job>`, sorted.
	Contexts []string `json:"contexts"`
	// Failed are the contexts in failure or error, Pending those in pending
	// or expected, each sorted; the rest are success. Both absent on a record
	// read before they were kept.
	Failed  []string `json:"failed,omitempty"`
	Pending []string `json:"pending,omitempty"`
	// At is when the newest of them was posted.
	At time.Time `json:"at"`
}

// Renovate is the repository's Renovate configuration and activity.
type Renovate struct {
	Configured bool   `json:"configured"`
	Path       string `json:"path,omitempty"`
	// Enabled is false only for a top-level `enabled: false`; an `enabled:
	// false` inside packageRules is not a disabled Renovate.
	Enabled bool `json:"enabled"`
	// Preset says whether the config extends giantswarm/renovate-presets.
	Preset          bool         `json:"preset"`
	DashboardIssue  *Issue       `json:"dashboardIssue,omitempty"`
	LastPullRequest *PullRequest `json:"lastPullRequest,omitempty"`
	LastCommit      *time.Time   `json:"lastCommit,omitempty"`
}

// Issue is one issue.
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// Catalog is the repository's entity in giantswarm/github's catalog.
type Catalog struct {
	Present bool `json:"present"`
}

// Mapping is the repository's entry in the apps-to-teams mapping.
type Mapping struct {
	Present bool   `json:"present"`
	Team    string `json:"team,omitempty"`
}

// Setup is the set-up state: the engine's checks in read mode and the last
// reconciler run.
type Setup struct {
	// Checks is the engine's read-mode result (mode check); nil when the
	// repository is unassigned, gone, or the checks did not run. An entry the
	// engine refuses carries its Refused result: the entry step reported with
	// one finding per problem, no step run.
	Checks    *reconcile.Result `json:"checks,omitempty"`
	CheckedAt *time.Time        `json:"checkedAt,omitempty"`
	// CheckError says why Checks is missing.
	CheckError string `json:"checkError,omitempty"`
	// LastRun is the reconciler's last run over this repository — the
	// reconcile-<name> artifact its workflow run uploaded, read by the poller.
	LastRun *LastRun `json:"lastRun,omitempty"`
	// PendingRun is a reconciler run expected for this repository that has
	// not reported yet: an Align now dispatched (a workflow_dispatch returns
	// no run id), or the run that follows the merge of a team-file pull
	// request (a creation, a lifecycle change, a transfer, a configuration
	// change). The run's artifact clears it; so does the pending window
	// running out — from the dispatch, or from the pull request's merge —
	// which leaves MissingRun.
	PendingRun *PendingRun `json:"pendingRun,omitempty"`
	// MissingRun is an expected run that did not report within the pending
	// window: the finding reconcile-run-missing, until the next artifact,
	// dispatch or creation.
	MissingRun *MissingRun `json:"missingRun,omitempty"`
	// Told is the findings the team has been told about in its channel, as
	// the sentences they were told in. A finding already here is not told
	// again, however many team-file changes follow: it asks for a decision
	// or a chore, which is standing on the record and on the page until
	// someone does it, not news on every edit of the entry. The set is what
	// the last run's verified findings said, so a finding that goes away and
	// comes back is told again.
	Told []string `json:"told,omitempty"`
	// Release is the latest release as the release watch follows it: the
	// tag's own CircleCI pipeline read every interval until it settles, then
	// what the record and the team know of the release. One tag, the latest;
	// the next release replaces it, and what was told about the last one
	// goes with it.
	Release *ReleaseWatch `json:"release,omitempty"`
}

// ReleaseWatch is one release from its tag to the end of the tag's own
// CircleCI pipeline — never a branch pipeline at the same commit, which the
// commit's statuses cannot tell apart — and what became of it: built, red
// (nothing was published; the failed jobs named), unbuilt (no pipeline
// within the grace period), or unchecked (the pipeline is out of reach: a
// private project without a CircleCI token, a project CircleCI does not
// know, a tag outside the vX.Y.Z flow, a repository without a pipeline).
type ReleaseWatch struct {
	Tag string `json:"tag"`
	// CreatedAt is when the release, and with it the tag, was created.
	CreatedAt time.Time `json:"createdAt"`
	// PullRequest is the merged pull request behind the tag's commit; nil
	// when GitHub associates none.
	PullRequest *ChangePullRequest `json:"pullRequest,omitempty"`
	// State is one of the Release… states.
	State string `json:"state"`
	// Pipeline is the tag's pipeline once found.
	Pipeline *ReleasePipeline `json:"pipeline,omitempty"`
	// FailedJobs are the failed workflows' failed jobs, `<job> (<how>)`, of
	// a red release.
	FailedJobs []string `json:"failedJobs,omitempty"`
	// Reason says why the release is unchecked.
	Reason string `json:"reason,omitempty"`
	// CheckedAt is the last read; SettledAt when the state became final.
	CheckedAt time.Time  `json:"checkedAt"`
	SettledAt *time.Time `json:"settledAt,omitempty"`
	// Told is the sentence the team heard about the release — a red or an
	// unbuilt one, once — and ToldAt when; empty while nothing was posted.
	// A team that is not messaged (no channel file naming asks, no team-review endpoint)
	// gets the sentence recorded here too, so the watch stops asking.
	Told   string     `json:"told,omitempty"`
	ToldAt *time.Time `json:"toldAt,omitempty"`
}

// ReleasePipeline names the tag's pipeline on CircleCI.
type ReleasePipeline struct {
	Number int64  `json:"number"`
	URL    string `json:"url"`
	// Workflow is the failed workflow's page of a red release, where its
	// jobs and the rerun from failed are; empty otherwise.
	Workflow string `json:"workflow,omitempty"`
}

// The states of a watched release.
const (
	// ReleaseWatching: the tag's pipeline has not settled (or not appeared
	// within the grace period yet).
	ReleaseWatching = "watching"
	// ReleaseBuilt: every workflow of the tag's pipeline succeeded.
	ReleaseBuilt = "built"
	// ReleaseRed: a workflow of the tag's pipeline failed; nothing was
	// published for the tag until a rerun from failed succeeds.
	ReleaseRed = "red"
	// ReleaseUnbuilt: no pipeline for the tag within the grace period.
	ReleaseUnbuilt = "unbuilt"
	// ReleaseUnchecked: the pipeline is out of reach; Reason says why.
	ReleaseUnchecked = "unchecked"
)

// Settled says whether the release has a final state.
func (w *ReleaseWatch) Settled() bool { return w != nil && w.State != ReleaseWatching }

// Following says whether the watch still reads the release's pipeline: one
// not settled yet, and a red one — a rerun from failed may turn it built,
// silently — until the next release replaces it.
func (w *ReleaseWatch) Following() bool {
	return w != nil && (w.State == ReleaseWatching || w.State == ReleaseRed)
}

// Untold says whether the release is one the team hears about — red or
// unbuilt — and has not heard about yet.
func (w *ReleaseWatch) Untold() bool {
	return w != nil && (w.State == ReleaseRed || w.State == ReleaseUnbuilt) && w.Told == ""
}

// Settle ends the watch at now in state.
func (w *ReleaseWatch) Settle(now time.Time, state string) {
	w.State = state
	t := now
	w.SettledAt = &t
}

// LastRun is one reconciler run: the engine's result, the run, when, and
// the team-file change the run followed.
type LastRun struct {
	Result    reconcile.Result `json:"result"`
	RunURL    string           `json:"runUrl"`
	Timestamp time.Time        `json:"timestamp"`
	// RunID and Attempt name the Actions run (and its attempt) the artifact
	// came from: the poller reads no artifact twice.
	RunID   int64 `json:"runId,omitempty"`
	Attempt int   `json:"attempt,omitempty"`
	// Change is the artifact's change block: what the run was about for the
	// team — the kind of team-file change, who made it and its pull request.
	// nil for an artifact without one.
	Change *Change `json:"change,omitempty"`
	// Told says the team heard of the run: its change sentence reached the
	// team's channel, or it had none. A run stored and not told — the pod
	// replaced before it posted, the post refused — is told by the next
	// poll that reads the run, and never twice.
	Told bool `json:"told,omitempty"`
}

// Names says whether the run is the Actions run id at attempt.
func (r *LastRun) Names(id int64, attempt int) bool {
	return r != nil && r.RunID == id && r.Attempt == attempt
}

// Change is the team-file change a reconciler run followed, as the
// reconciler classifies it in the artifact's change block.
type Change struct {
	// Kind is one of the Change… kinds.
	Kind string `json:"kind"`
	// By is the login of the pull request's author (a push) or of the person
	// who dispatched the run; empty for the schedule.
	By string `json:"by,omitempty"`
	// PullRequest is the merged pull request that made the change.
	PullRequest *ChangePullRequest `json:"pullRequest,omitempty"`
	// FromTeam is the giving team of a transfer.
	FromTeam string `json:"fromTeam,omitempty"`
}

// ChangePullRequest names the pull request of a change.
type ChangePullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// The kinds of change a run follows.
const (
	// ChangeCreated: the repository is younger than its pull request.
	ChangeCreated = "created"
	// ChangeAdded: an existing repository was declared.
	ChangeAdded = "added"
	// ChangeTransferred: the entry moved from another team's file (FromTeam).
	ChangeTransferred = "transferred"
	ChangeArchived    = "archived"
	ChangeDeleted     = "deleted"
	ChangeDeprecated  = "deprecated"
	// ChangeChanged: any other edit of the entry.
	ChangeChanged = "changed"
	// ChangeDispatched: an Align now.
	ChangeDispatched = "dispatched"
	// ChangeNightly: the schedule.
	ChangeNightly = "nightly"
)

// PersonMade says whether a person's team-file change is behind the run: one
// of the kinds the reconciler derives from a merged pull request. An Align
// now (dispatched), the schedule (nightly) and an artifact without a change
// block are the reconciler's own runs.
func (ch *Change) PersonMade() bool {
	if ch == nil {
		return false
	}
	switch ch.Kind {
	case ChangeCreated, ChangeAdded, ChangeTransferred, ChangeArchived, ChangeDeleted, ChangeDeprecated, ChangeChanged:
		return true
	}
	return false
}

// PendingRun is an expected reconciler run that has not reported yet.
type PendingRun struct {
	// DispatchedAt is when the run was marked: the dispatch of an Align
	// now, or the opening of the pull request.
	DispatchedAt time.Time `json:"dispatchedAt"`
	// By is the login of the person who dispatched it, or who opened the
	// pull request.
	By string `json:"by"`
	// Kind is the change the run follows: dispatched (an Align now) or the
	// kind of the team-file pull request — created, archived, deprecated,
	// transferred, changed.
	Kind string `json:"kind,omitempty"`
	// PullRequest is the team-file pull request whose merge the run
	// follows; nil for an Align now.
	PullRequest *ChangePullRequest `json:"pullRequest,omitempty"`
	// MergedAt is when the pull request merged, once the poller has read it
	// from the pull request: the pending window counts from it. nil while
	// the pull request is open — no run is due yet — and for an Align now.
	MergedAt *time.Time `json:"mergedAt,omitempty"`
	// ConflictsSince is when the poller found GitHub reporting the open pull
	// request conflicting with its base (mergeable: false): a neighbouring
	// entry of the team file changed first, and the pull request cannot
	// merge as it stands. A member's approve_change re-renders it on the
	// base before approving; nil while GitHub reports it mergeable.
	ConflictsSince *time.Time `json:"conflictsSince,omitempty"`
}

// Conflicting says whether the pending run's pull request is noted as
// conflicting with its base.
func (p *PendingRun) Conflicting() bool {
	return p != nil && p.ConflictsSince != nil
}

// Follows says whether the pending run is the one of pull request number.
func (p *PendingRun) Follows(number int) bool {
	return p != nil && p.PullRequest != nil && p.PullRequest.Number == number
}

// AwaitedFrom is when the run is awaited from — the pending window's start:
// the dispatch of an Align now, the merge of a pull request. Zero while the
// pull request is open: the window has not started.
func (p *PendingRun) AwaitedFrom() time.Time {
	switch {
	case p.PullRequest == nil:
		return p.DispatchedAt
	case p.MergedAt != nil:
		return *p.MergedAt
	}
	return time.Time{}
}

// MissingRun is an expected reconciler run that never reported: its run
// completed without a report, or the pending window ran out.
type MissingRun struct {
	PendingRun
	// NoticedAt is when the poller found the run completed without a
	// report, or when the pending window ran out.
	NoticedAt time.Time `json:"noticedAt"`
	// RunsURL is the workflow's Actions page: where the run is, if any.
	RunsURL string `json:"runsUrl"`
	// RunURL and Conclusion name the run that completed without a report —
	// failed or cancelled before its report step — when the poller found
	// one; empty when the window ran out with no run to name.
	RunURL     string `json:"runUrl,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
}

// Finding is something the inventory shows rather than anyone repairs.
type Finding struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
	// Source is inventory or engine.
	Source string `json:"source"`
	// Advisory is the engine's weight of the finding: for a person only,
	// the repository counts as in sync with it. The inventory's own
	// findings are never advisory.
	Advisory bool `json:"advisory,omitempty"`
}

// The inventory's own finding kinds; the engine's kinds pass through.
const (
	FindingDeclaredButGone     = "declared-but-gone"
	FindingUndeclaredOnGitHub  = "undeclared-on-github"
	FindingReconcileRunMissing = "reconcile-run-missing"
	FindingSourceInventory     = "inventory"
	FindingSourceEngine        = "engine"
)

// SweepSummary is the outcome of the last full sweep.
type SweepSummary struct {
	StartedAt    time.Time `json:"startedAt"`
	FinishedAt   time.Time `json:"finishedAt"`
	Duration     string    `json:"duration"`
	Repositories int       `json:"repositories"`
	Declared     int       `json:"declared"`
	Undeclared   int       `json:"undeclared"`
	Gone         int       `json:"gone"`
	Archived     int       `json:"archived"`
	EngineChecks int       `json:"engineChecks"`
	Removed      int       `json:"removed"`
	// Resumes counts the pods that took the sweep up after the one that
	// started it; Carried the records an earlier pod (or a refresh) had
	// written since the start and this pass kept. EngineChecks, GraphQL and
	// REST are the last pod's part.
	Resumes int      `json:"resumes,omitempty"`
	Carried int      `json:"carried,omitempty"`
	GraphQL Budget   `json:"graphql"`
	REST    Budget   `json:"rest"`
	Errors  []string `json:"errors,omitempty"`
}

// Budget is what a sweep drew from one rate budget.
type Budget struct {
	Calls int `json:"calls"`
	// Cost is GraphQL points (calls for REST).
	Cost      int        `json:"cost"`
	Remaining int        `json:"remaining"`
	Limit     int        `json:"limit,omitempty"`
	ResetAt   *time.Time `json:"resetAt,omitempty"`
}

// Finalize computes the derived field — the findings — from the record's
// facts. It is idempotent.
func (r *Record) Finalize() {
	r.Findings = r.findings()
}

// WithAge returns the record with Age filled for now.
func (r Record) WithAge(now time.Time) Record {
	r.Age = now.Sub(r.RefreshedAt).Round(time.Second).String()
	return r
}

func (r *Record) findings() []Finding {
	out := []Finding{}
	switch {
	case r.Declaration != nil && r.Reality == nil:
		if f := r.declaredButGone(); f != nil {
			out = append(out, *f)
		}
	case r.Declaration == nil && r.Reality != nil:
		out = append(out, Finding{Kind: FindingUndeclaredOnGitHub, Source: FindingSourceInventory,
			Message: fmt.Sprintf("%s exists on GitHub and no team file declares it", r.Repository),
			Fix:     "declare it in the owning team's repositories/<team>.yaml, or archive it"})
	}
	if f := r.MissingRunFinding(); f != nil {
		out = append(out, *f)
	}
	if r.Setup.Checks != nil {
		for _, f := range r.Setup.Checks.Findings() {
			out = append(out, Finding{Kind: string(f.Kind), Message: f.Message, Fix: f.Fix, Source: FindingSourceEngine, Advisory: f.Advisory})
		}
	}
	return out
}

// declaredButGone is the finding of a declaration whose repository does not
// exist on GitHub, in the reconciler's terms: none for an entry declared
// deleted, which is the record of the deletion ("deleted, as declared"); an
// archived entry is recorded as deleted or removed, since an archived
// repository is never recreated; any other entry is created from its
// declaration, or recorded as deleted when it was deleted on purpose.
func (r *Record) declaredButGone() *Finding {
	d := r.Declaration
	var fix string
	switch d.Lifecycle {
	case teamfiles.LifecycleDeleted:
		return nil
	case teamfiles.LifecycleArchived:
		fix = fmt.Sprintf("an archived repository is never recreated: set lifecycle: deleted on the entry in %s as the record (Delete on the Repositories page, set_lifecycle), or remove the entry", d.File)
	default:
		fix = fmt.Sprintf("create the repository as yourself (Create on the Repositories page, the Repo Manager agent or `devctl repo create`) and the next run reconciles the entry in %s; if it was deleted on purpose, set lifecycle: deleted on the entry as the record (Delete on the Repositories page, set_lifecycle)", d.File)
	}
	return &Finding{Kind: FindingDeclaredButGone, Source: FindingSourceInventory,
		Message: fmt.Sprintf("%s is declared in %s but does not exist on GitHub", r.Repository, d.File), Fix: fix}
}

// MissingRunFinding is the finding reconcile-run-missing for the record's
// missing run, nil without one, in the words of the run that was expected:
// an Align now, or the run of a team-file pull request (named by its kind)
// that follows the merge. A run found completed without a report is named
// with its conclusion; one that never showed had not reported by the time
// the window ran out.
func (r *Record) MissingRunFinding() *Finding {
	m := r.Setup.MissingRun
	if m == nil {
		return nil
	}
	f := &Finding{Kind: FindingReconcileRunMissing, Source: FindingSourceInventory}
	dispatched, noticed := m.DispatchedAt.Format(time.RFC3339), m.NoticedAt.Format(time.RFC3339)
	expected := fmt.Sprintf("the reconciler run %s dispatched at %s for %s", m.By, dispatched, r.Repository)
	if m.PullRequest != nil {
		merged := ""
		if m.MergedAt != nil {
			merged = " was merged at " + m.MergedAt.Format(time.RFC3339)
		}
		expected = fmt.Sprintf("the reconciler run for %s, whose %s pull request %s %s opened at %s%s,", r.Repository, PullRequestNoun(m.Kind), m.PullRequest.URL, m.By, dispatched, merged)
	}
	switch {
	case m.RunURL != "":
		f.Message = fmt.Sprintf("%s completed with conclusion %s and uploaded no report: %s", expected, m.Conclusion, m.RunURL)
		f.Fix = fmt.Sprintf("look at the run %s — it failed before its report step; dispatch again with align_repository", m.RunURL)
	case m.PullRequest != nil:
		f.Message = fmt.Sprintf("%s had not reported by %s", expected, noticed)
		f.Fix = fmt.Sprintf("the run follows the pull request's merge: look for it on %s — it may have failed before its report step; the next artifact clears this", m.RunsURL)
	default:
		f.Message = fmt.Sprintf("%s had not reported by %s", expected, noticed)
		f.Fix = fmt.Sprintf("look for the run on %s — it may have failed before its report step, or the dispatch started none; dispatch again with align_repository", m.RunsURL)
	}
	return f
}

// PullRequestNoun names a team-file pull request by the kind of change it
// makes: "declaration pull request", "archive pull request", …
func PullRequestNoun(kind string) string {
	switch kind {
	case ChangeCreated, ChangeAdded:
		return "declaration"
	case ChangeArchived:
		return "archive"
	case ChangeDeleted:
		return "deletion"
	case ChangeDeprecated:
		return "deprecation"
	case ChangeTransferred:
		return "transfer"
	}
	return "change"
}

// Dispatched marks an Align now by login at now: setup.pendingRun, until
// the run's artifact or the pending window's end; an earlier missing run is
// forgotten. The findings follow.
func (r *Record) Dispatched(now time.Time, by string) {
	r.expectRun(PendingRun{DispatchedAt: now, By: by, Kind: ChangeDispatched})
}

// Opened marks a team-file pull request pr of kind (created, archived,
// deleted, deprecated, transferred, changed) opened by login at now: setup.pendingRun
// expects the reconciler run that follows the merge — without a deadline
// while the pull request is open, within the pending window once it has
// merged (Merged) — until the run's artifact or the window's end.
func (r *Record) Opened(now time.Time, by, kind string, pr ChangePullRequest) {
	r.expectRun(PendingRun{DispatchedAt: now, By: by, Kind: kind, PullRequest: &pr})
}

// Merged notes that the pending run's pull request merged at: the pending
// window counts from there.
func (r *Record) Merged(at time.Time) {
	r.Setup.PendingRun.MergedAt = &at
}

// Closed forgets the pending run of a pull request closed without a merge:
// no run follows, nothing is missing.
func (r *Record) Closed() {
	r.Setup.PendingRun = nil
	r.Findings = r.findings()
}

// Conflicts notes at as when the pending run's pull request was found
// conflicting with its base; a conflict noted already keeps its time.
func (r *Record) Conflicts(at time.Time) {
	if p := r.Setup.PendingRun; p != nil && p.ConflictsSince == nil {
		p.ConflictsSince = &at
	}
}

// Mergeable drops the note of a conflict: the pull request was re-rendered
// on its base, or GitHub reports it mergeable again.
func (r *Record) Mergeable() {
	if p := r.Setup.PendingRun; p != nil {
		p.ConflictsSince = nil
	}
}

func (r *Record) expectRun(p PendingRun) {
	r.Setup.PendingRun = &p
	r.Setup.MissingRun = nil
	r.Findings = r.findings()
}

// RunMissing ends the pending run at now without an artifact: the finding
// reconcile-run-missing names runsURL, the workflow's Actions page.
func (r *Record) RunMissing(now time.Time, runsURL string) {
	r.giveUp(MissingRun{NoticedAt: now, RunsURL: runsURL})
}

// RunFailed ends the pending run at now because its run completed with
// conclusion (failure, cancelled) and uploaded no report: the finding
// reconcile-run-missing names the run, runURL, beside the workflow's Actions
// page, so the failure shows at once rather than at the pending window's end.
func (r *Record) RunFailed(now time.Time, runsURL, runURL, conclusion string) {
	r.giveUp(MissingRun{NoticedAt: now, RunsURL: runsURL, RunURL: runURL, Conclusion: conclusion})
}

// giveUp moves the pending run into m; a record without one is unchanged.
func (r *Record) giveUp(m MissingRun) {
	if r.Setup.PendingRun == nil {
		return
	}
	m.PendingRun = *r.Setup.PendingRun
	r.Setup.MissingRun = &m
	r.Setup.PendingRun = nil
	r.Findings = r.findings()
}

// CI is what the repository's CircleCI configuration on the default branch
// says — .circleci/config.yml, workflows.yml and custom.yml — read for the
// questions a person asks about a repository's CI before aligning it: which
// architect orb, arm64 images or not, how the images reach China, whether
// images and charts are signed. Read with the repository, no CircleCI token.
type CI struct {
	// Files are the .circleci files found: config.yml, workflows.yml,
	// custom.yml, in that order.
	Files []string `json:"files"`
	// Generated says config.yml carries devctl's generator header.
	Generated bool `json:"generated"`
	// Orb is the giantswarm/architect orb version the pipeline pins; ""
	// without the orb.
	Orb string `json:"orb,omitempty"`
	// ImagePush says the pipeline pushes an image (an architect
	// push-to-registries job), ChartPush a chart (push-to-app-catalog).
	ImagePush bool `json:"imagePush"`
	ChartPush bool `json:"chartPush"`
	// Platforms are the image platforms the push jobs build, resolved the
	// way the orb does; nil when no image is pushed or the configuration
	// does not say.
	Platforms []string `json:"platforms,omitempty"`
	// ARM64 says the images include linux/arm64; nil when the configuration
	// does not say.
	ARM64 *bool `json:"arm64,omitempty"`
	// ChinaPush is how the images reach the China registry: split, inline,
	// custom, none.
	ChinaPush string `json:"chinaPush"`
	// Signing says whether the pushed images and charts are signed with
	// cosign: signed, unsigned, unknown, none; SigningReason says why they
	// are not signed.
	Signing       string `json:"signing"`
	SigningReason string `json:"signingReason,omitempty"`
	// Jobs are the workflows' jobs with the refs that run them, the way to
	// tell whose a commit status is; empty when no file parsed.
	Jobs []CIJob `json:"jobs,omitempty"`
	// Error names a file that did not parse.
	Error string `json:"error,omitempty"`
}

// The ways images reach the China registry.
const (
	// ChinaPushSplit: the push job leaves the China registry to the
	// in-China sync-china-registry job (`split-china-push`).
	ChinaPushSplit = "split"
	// ChinaPushInline: the push job pushes to every registry itself.
	ChinaPushInline = "inline"
	// ChinaPushCustom: the push job overrides the registry list
	// (`registries-data`); which hosts, the configuration does not say.
	ChinaPushCustom = "custom"
	// ChinaPushNone: no image push.
	ChinaPushNone = "none"
)

// Whether the pushed images and charts are signed with cosign.
const (
	SigningSigned   = "signed"
	SigningUnsigned = "unsigned"
	// SigningUnknown: the configuration does not say (no architect orb, a
	// development orb version).
	SigningUnknown = "unknown"
	// SigningNone: nothing is pushed.
	SigningNone = "none"
)
