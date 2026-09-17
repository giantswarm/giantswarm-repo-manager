# The tools

Generated from the registered tools (`make tools-doc`); through muster every tool is `x_giantswarm-repo-manager_<name>`. Every write tool takes `dryRun` and `mode`; `commit` is the only write mode, `apply` is refused.

| Tool | Kind |
|---|---|
| `approve_change` | write (dryRun, mode: commit) |
| `create_repository` | write (dryRun, mode: commit) |
| `get_info` | read-only |
| `get_repository` | read-only |
| `list_repositories` | read-only |
| `reconcile_repository` | write (dryRun, mode: commit) |
| `refresh_repository` | cache annotation |
| `set_lifecycle` | write (dryRun, mode: commit) |
| `sweep_inventory` | cache annotation |
| `transfer_repository` | write (dryRun, mode: commit) |
| `update_repository` | write (dryRun, mode: commit) |
| `validate_repository` | read-only |

## `approve_change`

WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). Approve a team-file pull request as you, after this server has checked on GitHub that you are a member of the team the change belongs to (the owning team; for a transfer the receiving team). The Approve button of a Slack ask calls this tool as the clicking member; a member may also call it directly, and approving on GitHub is equivalent. A non-member is refused. Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; mode: "commit" opens the team-file pull request as you and may take up to a minute (its writes run on GitHub within the call) — wait for the one answer. mode: "apply" is refused for every write tool (a repository without its declaration is drift), and mode is required unless dryRun is true.

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

WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). Create one or more new repositories of the giantswarm org as you, in this order: the repository (you are its admin), one scaffold commit on its default branch rendered by the engine from the declaration (v0.1.0 follows from the scaffold's auto-release), then the pull request adding the entries to the team's file (repositories/<team>.yaml in giantswarm/github) — the reconciler sets the repositories up once it merges and never creates. An owner role in the org is required: the org lets only owners create repositories, and the dry run tells you so before any write. dryRun: true is validate_repository's result with the creation plan (creation: the create and scaffold steps, the pull request) and writes nothing; refusals are data (entries[].problems, creation.refusal), an error means the validation could not run. mode commit refuses before any write — the engine's refusals, a taken name, a missing owner role — and resumes a creation interrupted by a failure: a repository you administer is not created again, a scaffold on the default branch not pushed again, an open pull request for the branch is reported. The pull request is machine-approved when you are in the team (or team-planeteers) and at most three entries are added, else your team reviews it. Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; mode: "commit" opens the team-file pull request as you and may take up to a minute (its writes run on GitHub within the call) — wait for the one answer. mode: "apply" is refused for every write tool (a repository without its declaration is drift), and mode is required unless dryRun is true.

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

Read-only. Report the service version and how this call is authenticated: the caller (the GitHub login and id GET /user answered for the bearer muster put on the call — the person's own user token through the App giantswarm-repo-manager) and the authorization server pinned for it; whether your credential reaches the team files (teamFiles.readable); the identity of the unattended inventory reads (the read-only App giantswarm-repo-manager-inventory, or not configured) and the inventory store; where the inventory's CircleCI facts come from (commit statuses and the reconciler's run artifact — this server holds no CircleCI token); the engine (devctl reposetup package) and the write modes. Call first.

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

Read-only. The inventory of the org's repositories from the store: one row per repository with team, lifecycle, visibility, archived (on GitHub), fork, Renovate state, finding kinds, set-up state and record age, sorted by repository name, plus the last sweep's summary. Scope per caller: mine (the teams you belong to on GitHub, read as you), team (the team argument, or your teams), unassigned (on GitHub without a declaration), all. Filters as on the Repositories page: search, team (in every scope: under mine one of your teams — another selects no rows and note says so; none under all: undeclared), renovate, visibility, fork, lifecycle, archived, inactiveDays, finding. get_repository has the full record.

```json
{
  "properties": {
    "archived": {
      "description": "Only repositories that are archived — declared archived or archived on GitHub (true) — or only those that are not (false). Independent of lifecycle.",
      "type": "boolean"
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
      "description": "Only repositories with this lifecycle: active (none declared, and not archived on GitHub), deprecated (declared), archived (declared archived, or archived on GitHub); another value is matched against the declared lifecycle.",
      "type": "string"
    },
    "limit": {
      "description": "Rows to return (default 100).",
      "type": "number"
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

## `reconcile_repository`

WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). Run the reconciler for one repository now (Reconcile now): dispatches the reconcile-repositories workflow in giantswarm/github as you, which runs the engine's set-up steps for that repository — settings, permissions, protection, CircleCI, Renovate check, CODEOWNERS, metadata, lifecycle, catalog, release. The record shows setup.pendingRun until the inventory has read the run's artifact (within seconds of the run completing) as setup.lastRun, with the run's change block (kind, by, pullRequest) — its failed steps and findings are on the record and in the run. The team's standup channel hears nothing about a dispatch: the sentences about who created, added, transferred, archived or deprecated a repository, and the failed steps and findings of that run, follow a merged pull request only. A run that does not report within 15 minutes leaves the finding reconcile-run-missing. Nothing is written to the team files. Here mode commit means: dispatch. Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; mode: "commit" opens the team-file pull request as you and may take up to a minute (its writes run on GitHub within the call) — wait for the one answer. mode: "apply" is refused for every write tool (a repository without its declaration is drift), and mode is required unless dryRun is true.

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
      "description": "Team slug; required for a repository without an entry (it is then reconciled from the team alone), optional otherwise.",
      "type": "string"
    }
  },
  "required": [
    "repository"
  ],
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

WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). Deprecate or archive a declared repository by setting lifecycle in its team-file entry. deprecated: security-only Renovate and a catalog flag. archived: the reconciler archives the repository on GitHub and unfollows it on CircleCI; the entry stays as the record. Deletion is not expressible. The ask goes to the owning team's channel; a member's Approve (or an approving review on GitHub) lands it. Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; mode: "commit" opens the team-file pull request as you and may take up to a minute (its writes run on GitHub within the call) — wait for the one answer. mode: "apply" is refused for every write tool (a repository without its declaration is drift), and mode is required unless dryRun is true.

```json
{
  "properties": {
    "dryRun": {
      "description": "Render the change and write nothing (default false).",
      "type": "boolean"
    },
    "lifecycle": {
      "description": "deprecated or archived.",
      "enum": [
        "deprecated",
        "archived"
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

WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). Move a declared repository to another team: its entry leaves the giving team's file and enters the receiving team's file in one pull request that names both teams. The ask goes to the receiving team's channel (its member approves), the giving team gets a notice in its standup channel. The reconciler then re-applies permissions, CODEOWNERS and the catalog mapping for the new owner. Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; mode: "commit" opens the team-file pull request as you and may take up to a minute (its writes run on GitHub within the call) — wait for the one answer. mode: "apply" is refused for every write tool (a repository without its declaration is drift), and mode is required unless dryRun is true.

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

WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). Change the configuration of a declared repository: its team-file entry is replaced by the entry you pass (the whole entry — name, componentType, gen and every other field as it should read afterwards). The entry is validated against the repositories schema (not the creation rules, which apply to new repositories only); the reconciler applies the change after the team's review. Use set_lifecycle to deprecate or archive and transfer_repository to move a repository to another team. Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; mode: "commit" opens the team-file pull request as you and may take up to a minute (its writes run on GitHub within the call) — wait for the one answer. mode: "apply" is refused for every write tool (a repository without its declaration is drift), and mode is required unless dryRun is true.

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

Read-only. The dry run of creating one or more new repositories for a team, exactly what create_repository would do: each entry rendered with the schema's defaults, the implied template (giantswarm/template for Go, template-app for a chart, the minimal scaffold otherwise) and its options, whether the name is free on GitHub, and the refusals of the creation rules as data (entries[].problems, and as the engine's findings entry-refused / gen-circleci-refused). Plus the guard notices a person sees before any pull request exists: team-review when the author is outside the owning team and team-planeteers, batch-review above three entries, names-unchecked without the App. And the creation as you (creation): the create and scaffold steps the engine would run with your token and the pull request that follows, its body carrying your reason — or the refusal when you are not an owner of the org (the org lets only owners create repositories). Writes nothing. Takes the same arguments as create_repository (team, entry or entries, reason), so you run it with exactly the arguments you commit. Use it before create_repository; for an existing repository's state use get_repository.

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
