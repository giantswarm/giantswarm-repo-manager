# The tools

Generated from the registered tools (`make tools-doc`); through muster every tool is `x_giantswarm-repo-manager_<name>`. Every write tool takes `dryRun` and `mode`; `commit` is the only write mode, `apply` is refused.

| Tool | Kind |
|---|---|
| `adopt_repository` | write (dryRun, mode: commit) |
| `align_repository` | write (dryRun, mode: commit) |
| `approve_change` | write (dryRun, mode: commit) |
| `create_repository` | write (dryRun, mode: commit) |
| `get_info` | read-only |
| `get_repository` | read-only |
| `list_repositories` | read-only |
| `refresh_repository` | cache annotation |
| `set_lifecycle` | write (dryRun, mode: commit) |
| `sweep_inventory` | cache annotation |
| `transfer_repository` | write (dryRun, mode: commit) |
| `update_repository` | write (dryRun, mode: commit) |
| `validate_repository` | read-only |
| `watch_repository` | read-only |

## `adopt_repository`

WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). Adopt a repository of the org that exists on GitHub and no team file declares (the inventory's unassigned scope): the entry you pass is added to the team's file (repositories/<team>.yaml in giantswarm/github) in a pull request opened as you with auto-merge armed, and the ask with the Approve button goes to the team's channel — an existing name is a plain addition, so the team reviews it: a member other than you approves. The entry is validated against the repositories schema (not the creation rules, which are for repositories the manager creates) and rendered as create_repository renders a declaration; `align` is yours to set: with true the reconciler run of the merge aligns the repository with its declared set-up and the company baseline, without it that run checks the repository and reports the drift, changing nothing. `lifecycle: deprecated` or `archived` in the entry adopts the repository and ends its life in the one pull request; the entry then gets `align: true` beside the lifecycle — else the reconciler would record the lifecycle and apply nothing — and the plan and the ask say so. `deleted` is refused here: declare first, then set_lifecycle with the name typed. Refused before any write when the name is free on GitHub (a new repository: use create_repository) or declared already (the refusal names the team; use update_repository, transfer_repository or set_lifecycle). The record shows setup.pendingRun until the reconciler run of the merged pull request has reported (get_repository). Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; mode: "commit" opens the team-file pull request as you and may take up to a minute (its writes run on GitHub within the call) — wait for the one answer. mode: "apply" is refused for every write tool (a repository without its declaration is drift), and mode is required unless dryRun is true.

```json
{
  "properties": {
    "dryRun": {
      "description": "Render the change and write nothing (default false).",
      "type": "boolean"
    },
    "entry": {
      "additionalProperties": true,
      "description": "The declaration as it goes into the team file: componentType, description, visibility, lifecycle, align, gen and the other fields of the repositories schema; name is the repository and may be left out.",
      "properties": {},
      "type": "object"
    },
    "mode": {
      "description": "How the change lands: \"commit\" (a team-file pull request as you). \"apply\" is refused.",
      "enum": [
        "commit"
      ],
      "type": "string"
    },
    "reason": {
      "description": "Why, for the pull request body and the ask.",
      "type": "string"
    },
    "repository": {
      "description": "Repository name, with or without the org; it exists on GitHub and no team file declares it.",
      "type": "string"
    },
    "team": {
      "description": "The adopting team's file, as its GitHub team slug: team-bumblebee, team-planeteers, …",
      "type": "string"
    }
  },
  "required": [
    "repository",
    "team",
    "entry"
  ],
  "type": "object"
}
```

## `align_repository`

WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). Align now: aligns one repository with its declared set-up and the company baseline. WARNING — an alignment changes the repository on GitHub and CircleCI: merge settings (squash only, auto-merge, delete branch on merge, update branch), wiki and projects off, issues on, team permissions (employees admin, bots push), branch protection (one approving review, enforce_admins on, strict up-to-date, every reporting check required), the CircleCI follow and setup workflows, a CODEOWNERS pull request, description and visibility, lifecycle, catalog and mapping, and a missed release build. The repository's entry in repositories/<team>.yaml decides the mode, and the answer (dry run and commit alike) says which. align: the repository has opted in (`align: true` in its entry) — the reconcile-repositories workflow in giantswarm/github is dispatched for it as you and applies the planned changes. opt-in: the repository is declared without the field — nothing is dispatched; the team-file pull request that sets `align: true` in its entry (nothing else changes) is planned (dry run) or opened as you (commit) with auto-merge armed, the ask with the Approve button goes to the owning team's channel (a member other than you approves), and the reconciler's run aligns the repository when it merges. check: the repository has no entry (pass team) — the workflow is dispatched as you and checks from the team alone, changing nothing. The answer carries mode, optedIn, declared, team, the planned changes from the inventory's last check (per step, with checkedAt), a warning paragraph to show the person before they confirm, and for opt-in the plan (optIn.plan: the entry before and after, the pull request, the ask) and after the commit what became of it (optIn.committed: pullRequest, ask, pendingRun). The record shows setup.pendingRun until the inventory has read the run's artifact (within seconds of a dispatched run completing; after the merge for an opt-in) as setup.lastRun, with the run's change block (kind, by, pullRequest) — its failed steps and findings are on the record and in the run. The team's standup channel hears nothing about a dispatch or the opt-in itself: the sentences about who created, added, transferred, archived or deprecated a repository, and the failed steps and findings of a run, follow a merged pull request only. A run that does not report within 15 minutes leaves the finding reconcile-run-missing. Here mode commit means: dispatch — or, for opt-in, open the pull request. Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; mode: "commit" opens the team-file pull request as you and may take up to a minute (its writes run on GitHub within the call) — wait for the one answer. mode: "apply" is refused for every write tool (a repository without its declaration is drift), and mode is required unless dryRun is true.

```json
{
  "properties": {
    "dryRun": {
      "description": "Render the change and write nothing (default false).",
      "type": "boolean"
    },
    "mode": {
      "description": "How the change lands: \"commit\" (a team-file pull request as you). \"apply\" is refused.",
      "enum": [
        "commit"
      ],
      "type": "string"
    },
    "repository": {
      "description": "Repository name, with or without the org.",
      "type": "string"
    },
    "team": {
      "description": "Team slug; required for a repository without an entry (it is then aligned from the team alone), optional otherwise.",
      "type": "string"
    }
  },
  "required": [
    "repository"
  ],
  "type": "object"
}
```

## `approve_change`

WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). Approve a team-file pull request as you, after this server has checked on GitHub that you are a member of the team the change belongs to (the owning team; for a transfer the receiving team), and land it: merged as you when GitHub lets it, else left to GitHub's auto-merge (armed if it was not) — the answer says which, or why neither. A pull request GitHub reports conflicting with its base (a neighbouring entry of the team file changed first) is re-rendered on the current base before the approval — the entries it changes re-applied to the files as they read now and the branch force-pushed as you, the pull request, its ask and its auto-merge kept — and the answer names it (rerendered). The Approve button of a Slack ask calls this tool as the clicking member; a member may also call it directly, and approving on GitHub is equivalent (GitHub does not re-render). A non-member is refused, and so is the person who opened the pull request: GitHub does not accept an author's approval of their own pull request, another member has to approve. Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; mode: "commit" opens the team-file pull request as you and may take up to a minute (its writes run on GitHub within the call) — wait for the one answer. mode: "apply" is refused for every write tool (a repository without its declaration is drift), and mode is required unless dryRun is true.

```json
{
  "properties": {
    "dryRun": {
      "description": "Render the change and write nothing (default false).",
      "type": "boolean"
    },
    "mode": {
      "description": "How the change lands: \"commit\" (a team-file pull request as you). \"apply\" is refused.",
      "enum": [
        "commit"
      ],
      "type": "string"
    },
    "pullRequest": {
      "description": "The pull request number in the team-files repository (giantswarm/github).",
      "type": "number"
    }
  },
  "required": [
    "pullRequest"
  ],
  "type": "object"
}
```

## `create_repository`

WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). Create one or more new repositories of the giantswarm org as you, in this order: the repository (you are its admin), one scaffold commit on its default branch rendered by the engine from the declaration (v0.1.0 follows from the scaffold's auto-release), then the pull request adding the entries to the team's file (repositories/<team>.yaml in giantswarm/github) — the reconciler sets the repositories up once it merges and never creates. Every entry is written with `align: true` — the creation is the repository's opt-in to alignment, so the reconciler run after the pull request merges sets the repository up; an entry saying `align: false` is refused. An owner role in the org is required: the org lets only owners create repositories, and the dry run tells you so before any write. dryRun: true is validate_repository's result with the creation plan (creation: the create and scaffold steps, the pull request) and writes nothing; refusals are data (entries[].problems, creation.refusal), an error means the validation could not run. mode commit refuses before any write — the engine's refusals, a taken name, a missing owner role — and resumes a creation interrupted by a failure: a repository you administer whose default branch carries at most one commit is not created again, a scaffold on the default branch not pushed again, an open pull request for the branch is reported; a repository with a history is somebody's work and is refused, its refusal naming adopt_repository, which declares it. The pull request is machine-approved when you are in the team (or team-planeteers) and at most three entries are added, else your team reviews it. Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; mode: "commit" opens the team-file pull request as you and may take up to a minute (its writes run on GitHub within the call) — wait for the one answer. mode: "apply" is refused for every write tool (a repository without its declaration is drift), and mode is required unless dryRun is true.

```json
{
  "properties": {
    "dryRun": {
      "description": "Render the change and write nothing (default false).",
      "type": "boolean"
    },
    "entries": {
      "description": "Several declarations at once (a batch above three entries gets a person's review).",
      "items": {
        "additionalProperties": true,
        "type": "object"
      },
      "type": "array"
    },
    "entry": {
      "additionalProperties": true,
      "description": "One declaration as it goes into the team file: name, componentType, gen: {language, flavours}, description, visibility and the other fields of the repositories schema.",
      "properties": {},
      "type": "object"
    },
    "mode": {
      "description": "How the change lands: \"commit\" (a team-file pull request as you). \"apply\" is refused.",
      "enum": [
        "commit"
      ],
      "type": "string"
    },
    "reason": {
      "description": "Why, for the pull request body: the dry run plans it, create_repository writes it.",
      "type": "string"
    },
    "team": {
      "description": "The owning team's file, as its GitHub team slug: team-bumblebee, team-planeteers, …",
      "type": "string"
    }
  },
  "required": [
    "team"
  ],
  "type": "object"
}
```

## `get_info`

Read-only. Report the service version and how this call is authenticated: the caller (the GitHub login and id GET /user answered for the bearer muster put on the call — the person's own user token through the App giantswarm-repo-manager) and the authorization server pinned for it; whether your credential reaches the team files (teamFiles.readable); the identity of the unattended inventory reads (the read-only App giantswarm-repo-manager-inventory, or not configured) and the inventory store; where the inventory's CircleCI facts come from (commit statuses and the reconciler's run artifact — this server holds no CircleCI token); the team-review endpoint (reviews.configured, and reviews.debugChannel when one channel receives every ask and notice instead of the teams' channels); the engine (devctl reposetup package) and the write modes. Call first.

```json
{
  "properties": {},
  "required": [],
  "type": "object"
}
```

## `get_repository`

Read-only. The full inventory record of one repository: declaration, GitHub reality, CircleCI, Renovate, catalog and mapping, set-up state (the engine's read-mode checks and the last reconciler run), findings and age.

```json
{
  "properties": {
    "repository": {
      "description": "Repository name, with or without the org (giantswarm/muster or muster).",
      "type": "string"
    }
  },
  "required": [
    "repository"
  ],
  "type": "object"
}
```

## `list_repositories`

Read-only. The inventory of the org's repositories from the store: one row per repository with team, lifecycle, visibility, archived (on GitHub), fork, Renovate state, finding kinds, CI facts (architect orb version, arm64 images, China push, cosign signing), set-up state and record age, sorted by repository name, plus the last sweep's summary. Scope per caller: mine (the teams you belong to on GitHub, read as you), team (the team argument, or your teams), unassigned (on GitHub without a declaration), all. Filters as on the Repositories page: search, team (in every scope: under mine one of your teams — another selects no rows and note says so; none under all: undeclared), renovate, visibility, fork, lifecycle, archived, inactiveDays, finding, orb, arm64, chinaPush, signing. get_repository has the full record.

```json
{
  "properties": {
    "archived": {
      "description": "Only repositories whose life is over — declared archived or deleted, or archived on GitHub (true) — or only those that are not (false). Independent of lifecycle.",
      "type": "boolean"
    },
    "arm64": {
      "description": "Only repositories whose CircleCI pipeline builds linux/arm64 images (true) or builds images without it (false).",
      "type": "boolean"
    },
    "chinaPush": {
      "description": "How the images reach the China registry: split (the in-China sync job), inline (the push job pushes there itself), custom (an overridden registry list), none (no image push).",
      "enum": [
        "split",
        "inline",
        "custom",
        "none"
      ],
      "type": "string"
    },
    "finding": {
      "description": "Only repositories with a finding of this kind (declared-but-gone, undeclared-on-github, entry-refused, gen-circleci-refused, default-icon, …).",
      "type": "string"
    },
    "fork": {
      "description": "Only forks (true) or only non-forks (false).",
      "type": "boolean"
    },
    "inactiveDays": {
      "description": "Only repositories whose last commit by a person is older than this many days (or that have none).",
      "type": "number"
    },
    "lifecycle": {
      "description": "Only repositories with this lifecycle: active (none declared, and not archived on GitHub), deprecated (declared), archived (declared archived, or archived on GitHub), deleted (declared; the repository is gone or about to be); another value is matched against the declared lifecycle.",
      "type": "string"
    },
    "limit": {
      "description": "Rows to return (default 100).",
      "type": "number"
    },
    "orb": {
      "description": "Only repositories whose CircleCI pipeline pins this giantswarm/architect orb version, or one starting with it (10 selects every 10.x.y).",
      "type": "string"
    },
    "renovate": {
      "description": "Renovate state: configured (a renovate.json5), missing, active (a Renovate pull request or commit within the server's Renovate activity period, default 180 days), inactive.",
      "enum": [
        "configured",
        "missing",
        "active",
        "inactive"
      ],
      "type": "string"
    },
    "scope": {
      "description": "mine | team | unassigned | all (default all).",
      "enum": [
        "mine",
        "team",
        "unassigned",
        "all"
      ],
      "type": "string"
    },
    "search": {
      "description": "Only repositories whose name or description contains this text (case-insensitive).",
      "type": "string"
    },
    "signing": {
      "description": "Whether the pushed images and charts are signed with cosign: signed, unsigned (the record says why), unknown, none (nothing pushed).",
      "enum": [
        "signed",
        "unsigned",
        "unknown",
        "none"
      ],
      "type": "string"
    },
    "team": {
      "description": "Only repositories declared by this team (slug: team-bumblebee). Under all, none: only undeclared repositories; under mine: one of your teams (another selects no rows, and note says so); under unassigned: ignored.",
      "type": "string"
    },
    "undeclared": {
      "description": "Only repositories on GitHub without a declaration (same as scope unassigned).",
      "type": "boolean"
    },
    "visibility": {
      "description": "Only public or only private repositories.",
      "enum": [
        "public",
        "private"
      ],
      "type": "string"
    }
  },
  "required": [],
  "type": "object"
}
```

## `refresh_repository`

Rebuild one repository's inventory record now from GitHub, CircleCI and the team files, run the engine's checks in read mode, and return it. Writes the inventory cache only, nothing on GitHub.

```json
{
  "properties": {
    "repository": {
      "description": "Repository name, with or without the org.",
      "type": "string"
    }
  },
  "required": [
    "repository"
  ],
  "type": "object"
}
```

## `set_lifecycle`

WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). Deprecate, archive or delete a declared repository by setting lifecycle in its team-file entry. deprecated: security-only Renovate and a catalog flag. archived: the reconciler archives the repository on GitHub and unfollows it on CircleCI; the entry stays as the record. deleted: the reconciler unfollows the repository on CircleCI and deletes it on GitHub — code, issues, pull requests, releases and packages with it (an organization owner can restore it on GitHub for 90 days); the entry stays as the record of the deletion; needs confirm, the repository's name typed by the person, and is refused without it. An entry without `align: true` gets it beside the lifecycle — the change opts the repository in to alignment, else the reconciler would record the lifecycle and apply nothing — and the ask says so. The ask goes to the owning team's channel; a member's Approve (or an approving review on GitHub) lands it. The record shows setup.pendingRun until the reconciler run of the merged pull request has reported (get_repository). Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; mode: "commit" opens the team-file pull request as you and may take up to a minute (its writes run on GitHub within the call) — wait for the one answer. mode: "apply" is refused for every write tool (a repository without its declaration is drift), and mode is required unless dryRun is true.

```json
{
  "properties": {
    "confirm": {
      "description": "For deleted: the repository's name, with or without the org, as the person typed it. Refused when absent or another name.",
      "type": "string"
    },
    "dryRun": {
      "description": "Render the change and write nothing (default false).",
      "type": "boolean"
    },
    "lifecycle": {
      "description": "deprecated, archived or deleted.",
      "enum": [
        "deprecated",
        "archived",
        "deleted"
      ],
      "type": "string"
    },
    "mode": {
      "description": "How the change lands: \"commit\" (a team-file pull request as you). \"apply\" is refused.",
      "enum": [
        "commit"
      ],
      "type": "string"
    },
    "reason": {
      "description": "Why, for the pull request body and the ask.",
      "type": "string"
    },
    "repository": {
      "description": "Repository name, with or without the org.",
      "type": "string"
    }
  },
  "required": [
    "repository",
    "lifecycle"
  ],
  "type": "object"
}
```

## `sweep_inventory`

Start the full inventory sweep over the org now — every repository's record rebuilt from GitHub and the team files the way the schedule does it — for a member of the teams that own this service (the server's sweep teams, checked on GitHub as you); a non-member is refused. The sweep runs in the background: the answer carries started (false while one already runs), running and the last sweep's summary; list_repositories shows the new one when it is done. Writes the inventory cache only, nothing on GitHub.

```json
{
  "properties": {},
  "required": [],
  "type": "object"
}
```

## `transfer_repository`

WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). Move a declared repository to another team: its entry leaves the giving team's file and enters the receiving team's file in one pull request that names both teams. The ask goes to the receiving team's channel (its member approves), the giving team gets a notice in its standup channel. The reconciler then re-applies permissions, CODEOWNERS and the catalog mapping for the new owner. The record shows setup.pendingRun until the reconciler run of the merged pull request has reported (get_repository). Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; mode: "commit" opens the team-file pull request as you and may take up to a minute (its writes run on GitHub within the call) — wait for the one answer. mode: "apply" is refused for every write tool (a repository without its declaration is drift), and mode is required unless dryRun is true.

```json
{
  "properties": {
    "dryRun": {
      "description": "Render the change and write nothing (default false).",
      "type": "boolean"
    },
    "mode": {
      "description": "How the change lands: \"commit\" (a team-file pull request as you). \"apply\" is refused.",
      "enum": [
        "commit"
      ],
      "type": "string"
    },
    "reason": {
      "description": "Why, for the pull request body and the ask.",
      "type": "string"
    },
    "repository": {
      "description": "Repository name, with or without the org.",
      "type": "string"
    },
    "toTeam": {
      "description": "The receiving team's slug (team-planeteers, …).",
      "type": "string"
    }
  },
  "required": [
    "repository",
    "toTeam"
  ],
  "type": "object"
}
```

## `update_repository`

WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). Change the configuration of a declared repository: its team-file entry is replaced by the entry you pass (the whole entry — name, componentType, gen and every other field as it should read afterwards). The entry is validated against the repositories schema (not the creation rules, which apply to new repositories only); the reconciler applies the change after the team's review. Use set_lifecycle to deprecate or archive and transfer_repository to move a repository to another team. The record shows setup.pendingRun until the reconciler run of the merged pull request has reported (get_repository). Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; mode: "commit" opens the team-file pull request as you and may take up to a minute (its writes run on GitHub within the call) — wait for the one answer. mode: "apply" is refused for every write tool (a repository without its declaration is drift), and mode is required unless dryRun is true.

```json
{
  "properties": {
    "dryRun": {
      "description": "Render the change and write nothing (default false).",
      "type": "boolean"
    },
    "entry": {
      "additionalProperties": true,
      "description": "The entry as it should read in the team file afterwards (the full entry, not a patch).",
      "properties": {},
      "type": "object"
    },
    "mode": {
      "description": "How the change lands: \"commit\" (a team-file pull request as you). \"apply\" is refused.",
      "enum": [
        "commit"
      ],
      "type": "string"
    },
    "reason": {
      "description": "Why, for the pull request body.",
      "type": "string"
    },
    "repository": {
      "description": "Repository name, with or without the org.",
      "type": "string"
    }
  },
  "required": [
    "repository",
    "entry"
  ],
  "type": "object"
}
```

## `validate_repository`

Read-only. The dry run of creating one or more new repositories for a team, exactly what create_repository would do: each entry rendered with the schema's defaults, the implied template (giantswarm/template for Go, template-app for a chart, the minimal scaffold otherwise) and its options, whether the name is free on GitHub, and the refusals of the creation rules as data (entries[].problems, and as the engine's findings entry-refused / gen-circleci-refused). Every entry is written with `align: true` — the creation is the repository's opt-in to alignment, so the reconciler run after the pull request merges sets the repository up; an entry saying `align: false` is refused. Plus the guard notices a person sees before any pull request exists: team-review when the author is outside the owning team and team-planeteers, batch-review above three entries, names-unchecked without the App. And the creation as you (creation): the create and scaffold steps the engine would run with your token and the pull request that follows, its body carrying your reason — or the refusal when you are not an owner of the org (the org lets only owners create repositories). Writes nothing. Takes the same arguments as create_repository (team, entry or entries, reason), so you run it with exactly the arguments you commit. Use it before create_repository; an existing repository that no team file declares is declared with adopt_repository, and its state is read with get_repository.

```json
{
  "properties": {
    "entries": {
      "description": "Several declarations at once (a batch above three entries gets a person's review).",
      "items": {
        "additionalProperties": true,
        "type": "object"
      },
      "type": "array"
    },
    "entry": {
      "additionalProperties": true,
      "description": "One declaration as it goes into the team file: name, componentType, gen: {language, flavours}, description, visibility and the other fields of the repositories schema.",
      "properties": {},
      "type": "object"
    },
    "reason": {
      "description": "Why, for the pull request body: the dry run plans it, create_repository writes it.",
      "type": "string"
    },
    "team": {
      "description": "The owning team's file, as its GitHub team slug: team-bumblebee, team-planeteers, …",
      "type": "string"
    }
  },
  "required": [
    "team"
  ],
  "type": "object"
}
```

## `watch_repository`

Read-only. Follow a new repository to readiness after create_repository, and return when it is ready, when a phase fails, as soon as a phase completes that was not complete when the call started (changed is true — narrate it and call again), or when timeout runs out — with the phases reached either way, each with its timestamp and the seconds since the phase before: created (the repository exists), scaffolded (its default branch carries the scaffold commit), declared (the declaration pull request is open), merged, setUp (the reconciler run of that pull request has reported: a failed step or a refused entry fails the phase, the run's other findings are carried as findings), released (the first release exists and the CircleCI statuses on its commit are green, complete and settled: CircleCI posts one status per job as the job starts, so a green set counts only once it holds the jobs the declaration implies — a chart job when the flavours produce a chart, push-to-registries when the default branch carries a Dockerfile — and has not changed for 60 s; a failing status fails the phase at once; while the statuses are pending, incomplete or settling, pendingReason lists the ones reported and what is awaited; while CircleCI has not reported on the release's commit at all — a repository the reconciler has only just followed — the reconciler run's release step decides: a failed step or a red-release finding fails the phase, anything else keeps waiting for the statuses). ready is true when every phase is done; pending names the phase still waited for when the timeout ran out — call again to keep following. Who reads what: the repository, its commits, the pull request and the release are read as you every few seconds; the commit statuses and the Dockerfile as the inventory App giantswarm-repo-manager-inventory, the identity of the unattended reads (your token through the App giantswarm-repo-manager cannot read them) — without that App the reconciler run's release step alone decides the released phase and pendingReason says so. A read GitHub refuses (403) keeps its phase pending with the refusal in pendingReason; the call errors only on wrong arguments. Takes the repository and the pull request number create_repository's answer carries.

```json
{
  "properties": {
    "pullRequest": {
      "description": "The declaration pull request's number in the team files repository (create_repository's pullRequest.number).",
      "type": "number"
    },
    "repository": {
      "description": "Repository name, with or without the org.",
      "type": "string"
    },
    "timeout": {
      "default": 120,
      "description": "Seconds to wait before answering with what is pending (default 120, at most 150 — under the MCPServer's 180 s).",
      "maximum": 150,
      "type": "number"
    }
  },
  "required": [
    "repository",
    "pullRequest"
  ],
  "type": "object"
}
```
