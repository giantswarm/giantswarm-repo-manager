package inventory

import (
	"fmt"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"
)

// Sources of a record's last write.
const (
	SourceSweep      = "sweep"
	SourceRefresh    = "refresh"
	SourceReconciler = "reconciler"
)

// Record is one repository of the org as the inventory knows it (D8): the
// declaration from the team files, the reality from GitHub, the set-up state,
// the orphan score with its reasons and the findings nobody repairs. The
// shape is documented in docs/inventory-record.md; the reconciler's per-
// repository artifact matches LastRun.
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
	Renovate Renovate  `json:"renovate"`
	Catalog  Catalog   `json:"catalog"`
	Mapping  Mapping   `json:"mapping"`
	Setup    Setup     `json:"setup"`
	Orphan   Orphan    `json:"orphan"`
	Findings []Finding `json:"findings"`
	// Decision is the note a person left; it survives every refresh.
	Decision *Decision `json:"decision,omitempty"`
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
// whether the project is followed and setup workflows are on. Source names
// the sources that answered, Unknown the facts none of them yields; the last
// pipeline is not derivable and is not part of the record.
type CircleCI struct {
	// Followed says CircleCI builds the repository: statuses on the head, or
	// the reconciler's circleci step found (or made) the project followed.
	Followed bool `json:"followed"`
	// SetupWorkflows is the project's setup-workflows setting as the
	// reconciler's last run saw or set it; nil when no run tells.
	SetupWorkflows *bool `json:"setupWorkflows,omitempty"`
	// Head is the default branch head's CircleCI statuses; nil when it has
	// none.
	Head *HeadStatus `json:"head,omitempty"`
	// Source is what answered: statuses, artifact, or statuses+artifact.
	Source string `json:"source"`
	// Unknown names the facts no source yields: followed (the head's status
	// contexts were truncated and none was CircleCI's), setupWorkflows.
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
)

// HeadStatus is the default branch head's `ci/circleci:` commit statuses.
type HeadStatus struct {
	// State is the worst state among the contexts: failure, error, pending,
	// expected or success.
	State string `json:"state"`
	// Contexts are the status contexts, `ci/circleci: <job>`, sorted.
	Contexts []string `json:"contexts"`
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
	// LastRun is the reconciler's last run over this repository — what the
	// reconciler workflow uploads as reconcile-<name>.json.
	LastRun *LastRun `json:"lastRun,omitempty"`
}

// LastRun is one reconciler run: the engine's result, the run and when.
type LastRun struct {
	Result    reconcile.Result `json:"result"`
	RunURL    string           `json:"runUrl"`
	Timestamp time.Time        `json:"timestamp"`
}

// Orphan is the orphan score with its reasons.
type Orphan struct {
	// Score is 0 to 100; 0 means nothing points at an orphan.
	Score   int      `json:"score"`
	Reasons []string `json:"reasons"`
	// StalePeriod is the period the reasons were judged against.
	StalePeriod string `json:"stalePeriod"`
}

// Finding is something the inventory shows rather than anyone repairs.
type Finding struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
	// Source is inventory or engine.
	Source string `json:"source"`
}

// The inventory's own finding kinds; the engine's kinds pass through.
const (
	FindingDeclaredButGone    = "declared-but-gone"
	FindingUndeclaredOnGitHub = "undeclared-on-github"
	FindingSourceInventory    = "inventory"
	FindingSourceEngine       = "engine"
)

// Decision is the note a person left on a repository.
type Decision struct {
	// Verdict is keep; other verdicts are not defined yet.
	Verdict string    `json:"verdict"`
	Note    string    `json:"note,omitempty"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
}

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
	GraphQL      Budget    `json:"graphql"`
	REST         Budget    `json:"rest"`
	Errors       []string  `json:"errors,omitempty"`
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

// Finalize computes the derived fields — findings and the orphan score — from
// the record's facts. It is idempotent.
func (r *Record) Finalize(stale time.Duration, now time.Time) {
	r.Findings = r.findings()
	r.Orphan = Score(r, stale, now)
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
		out = append(out, Finding{Kind: FindingDeclaredButGone, Source: FindingSourceInventory,
			Message: fmt.Sprintf("%s is declared in %s but does not exist on GitHub", r.Repository, r.Declaration.File),
			Fix:     "add the entry back by creating the repository from the declaration, or remove the entry from the team file"})
	case r.Declaration == nil && r.Reality != nil:
		out = append(out, Finding{Kind: FindingUndeclaredOnGitHub, Source: FindingSourceInventory,
			Message: fmt.Sprintf("%s exists on GitHub and no team file declares it", r.Repository),
			Fix:     "declare it in the owning team's repositories/<team>.yaml, or archive it"})
	}
	if r.Setup.Checks != nil {
		for _, f := range r.Setup.Checks.Findings() {
			out = append(out, Finding{Kind: string(f.Kind), Message: f.Message, Fix: f.Fix, Source: FindingSourceEngine})
		}
	}
	return out
}

// Weights of the orphan reasons.
const (
	weightUndeclared    = 40
	weightStaleCommits  = 30
	weightEmpty         = 20
	weightDissolvedTeam = 15
	weightNoRenovate    = 10
	weightRenovateQuiet = 10
	weightNoRelease     = 10
	weightNoCI          = 10
	weightOnboardingPR  = 10
	weightOldBotPRs     = 5
)

// Score judges a record against the stale period; it depends on nothing but
// the record's facts, so a client may recompute it with another period.
func Score(r *Record, stale time.Duration, now time.Time) Orphan {
	o := Orphan{StalePeriod: stale.String(), Reasons: []string{}}
	if r.Reality == nil {
		o.Reasons = append(o.Reasons, "gone from GitHub: not scored, see the findings")
		return o
	}
	if r.Reality.IsArchived {
		o.Reasons = append(o.Reasons, "archived: not scored")
		return o
	}
	add := func(w int, reason string) {
		o.Score += w
		o.Reasons = append(o.Reasons, reason)
	}
	if r.Declaration == nil {
		add(weightUndeclared, "no team file declares the repository")
	}
	rl := r.Reality
	if rl.IsEmpty {
		add(weightEmpty, "the repository is empty")
	} else {
		switch {
		case rl.LastPersonCommit == nil:
			add(weightStaleCommits, fmt.Sprintf("no commit by a person in the last %d commits", rl.HistorySampled))
		case now.Sub(rl.LastPersonCommit.Date) > stale:
			add(weightStaleCommits, fmt.Sprintf("last commit by a person on %s, older than %s", rl.LastPersonCommit.Date.Format("2006-01-02"), stale))
		}
		switch {
		case !r.Renovate.Configured || !r.Renovate.Enabled:
			add(weightNoRenovate, "Renovate is not configured or disabled")
		case renovateQuiet(r, stale, now):
			add(weightRenovateQuiet, fmt.Sprintf("Renovate is silent: no Renovate pull request or commit within %s", stale))
		}
		if rl.LatestRelease == nil {
			add(weightNoRelease, "no release")
		}
		if !rl.Has.CircleCI && !rl.Has.Workflows {
			add(weightNoCI, "no CI: neither .circleci/config.yml nor GitHub workflows")
		}
	}
	if len(rl.UnknownCodeownersTeams) > 0 {
		add(weightDissolvedTeam, fmt.Sprintf("CODEOWNERS names a team the org does not have: %v", rl.UnknownCodeownersTeams))
	}
	if len(rl.OpenPullRequests.Onboarding) > 0 {
		add(weightOnboardingPR, fmt.Sprintf("%d onboarding pull request(s) open", len(rl.OpenPullRequests.Onboarding)))
	}
	if t := rl.OpenPullRequests.OldestBotAt; t != nil && now.Sub(*t) > stale {
		add(weightOldBotPRs, fmt.Sprintf("the oldest open bot pull request is from %s", t.Format("2006-01-02")))
	}
	if o.Score > 100 {
		o.Score = 100
	}
	return o
}

func renovateQuiet(r *Record, stale time.Duration, now time.Time) bool {
	if pr := r.Renovate.LastPullRequest; pr != nil && now.Sub(pr.CreatedAt) <= stale {
		return false
	}
	if c := r.Renovate.LastCommit; c != nil && now.Sub(*c) <= stale {
		return false
	}
	return true
}
