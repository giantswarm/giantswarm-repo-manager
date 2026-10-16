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

Declare an existing, undeclared GitHub repository for a team. New: create_repository. Declared: update_repository.

list_repositories scope unassigned lists the candidates; the team reviews the pull request. Set `align: true` in the entry to have the reconciler apply the declared set-up; without it, drift is only reported. A deprecated or archived entry gets `align: true`, so the reconciler applies the lifecycle and the baseline: tell the person. Writes as you: dryRun: true previews and writes nothing; mode: "commit" carries it out and can take a minute — wait for the answer.

```json
{
  "properties": {
    "dryRun": {
      "description": "Preview the change and write nothing (default false).",
      "type": "boolean"
    },
    "entry": {
      "additionalProperties": true,
      "description": "The declaration as it goes into the team file: componentType, description, visibility, align, gen, …; name may be left out.",
      "properties": {},
      "type": "object"
    },
    "mode": {
      "description": "\"commit\": carry the change out as you. Required unless dryRun.",
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

Apply a repository's declared set-up now. Changes live GitHub/CircleCI settings: dry-run and show the person first.

The set-up is the declaration and the company baseline. The answer's warning lists what changes; show it before committing. Commit dispatches the reconciler as you; a declared repository without `align: true` gets the opt-in pull request instead, and one without an entry (pass team) is only checked. Writes as you: dryRun: true previews and writes nothing; mode: "commit" carries it out and can take a minute — wait for the answer.

```json
{
  "properties": {
    "dryRun": {
      "description": "Preview the change and write nothing (default false).",
      "type": "boolean"
    },
    "mode": {
      "description": "\"commit\": carry the change out as you. Required unless dryRun.",
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
      "description": "Team slug; required for a repository without an entry.",
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

Approve a team-file pull request as an owning-team member and land it: merged, or auto-merge armed. Not your own.

For a transfer, the receiving team approves. The answer says whether it merged, only armed auto-merge, or why neither. A pull request conflicting with its base is re-rendered on the current base and force-pushed as you first. The Approve button of a Slack ask calls this tool. Writes as you: dryRun: true previews and writes nothing; mode: "commit" carries it out and can take a minute — wait for the answer.

```json
{
  "properties": {
    "dryRun": {
      "description": "Preview the change and write nothing (default false).",
      "type": "boolean"
    },
    "mode": {
      "description": "\"commit\": carry the change out as you. Required unless dryRun.",
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

Create new giantswarm repositories via a team-file pull request. For an existing repository: adopt_repository.

Once it merges, the reconciler creates, scaffolds and sets up each repository. Preview with validate_repository. Visibility defaults to private: say `visibility: public` for a public repository. Writes as you: dryRun: true previews and writes nothing; mode: "commit" carries it out and can take a minute — wait for the answer.

```json
{
  "properties": {
    "dryRun": {
      "description": "Preview the change and write nothing (default false).",
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
      "description": "One declaration as it goes into the team file: name, componentType, gen: {language, flavours}, description, visibility, …",
      "properties": {},
      "type": "object"
    },
    "mode": {
      "description": "\"commit\": carry the change out as you. Required unless dryRun.",
      "enum": [
        "commit"
      ],
      "type": "string"
    },
    "reason": {
      "description": "Why, for the pull request body.",
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

Who you are here, whether you can write, and the allowed componentType, flavour, language, visibility, lifecycle.

Call it before composing a declaration, or when a call is refused for access. Also reports the service's version and configuration: team files, inventory, CircleCI, team reviews, engine and schema origin.

```json
{
  "properties": {},
  "required": [],
  "type": "object"
}
```

## `get_repository`

One repository's full inventory record: declaration, GitHub state, CI, set-up state, findings, pending run.

Use it to check a repository or a write's outcome: setup.pendingRun is set until the reconciler has reported.

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

Search and filter all giantswarm repositories: team, lifecycle, Renovate, CI, findings. Full record: get_repository.

One row per repository with team, lifecycle, visibility, Renovate state, CI facts (architect orb, arm64, China push, cosign signing), finding kinds, set-up state and age, plus the last sweep's summary.

```json
{
  "properties": {
    "archived": {
      "description": "Life over — declared archived or deleted, or archived on GitHub (true) — or not (false). Independent of lifecycle.",
      "type": "boolean"
    },
    "arm64": {
      "description": "The pipeline builds linux/arm64 images (true) or images without it (false).",
      "type": "boolean"
    },
    "chinaPush": {
      "description": "split (in-China sync job), inline (the push job pushes there), custom (overridden registries), none (no image push).",
      "enum": [
        "split",
        "inline",
        "custom",
        "none"
      ],
      "type": "string"
    },
    "finding": {
      "description": "A finding kind: declared-but-gone, undeclared-on-github, entry-refused, gen-circleci-refused, default-icon, …",
      "type": "string"
    },
    "fork": {
      "description": "Only forks (true) or only non-forks (false).",
      "type": "boolean"
    },
    "inactiveDays": {
      "description": "Last commit by a person older than this many days, or none at all.",
      "type": "number"
    },
    "lifecycle": {
      "description": "active, deprecated, archived (declared or on GitHub) or deleted.",
      "type": "string"
    },
    "limit": {
      "description": "Rows to return (default 100).",
      "type": "number"
    },
    "orb": {
      "description": "Pinned giantswarm/architect orb version, or its prefix (10 matches every 10.x.y).",
      "type": "string"
    },
    "renovate": {
      "description": "configured (has a renovate.json5), missing, active (Renovate activity within the server's period, default 180 days), inactive.",
      "enum": [
        "configured",
        "missing",
        "active",
        "inactive"
      ],
      "type": "string"
    },
    "scope": {
      "description": "mine (your teams), team, unassigned (no declaration) or all (default).",
      "enum": [
        "mine",
        "team",
        "unassigned",
        "all"
      ],
      "type": "string"
    },
    "search": {
      "description": "Name or description contains this text (case-insensitive).",
      "type": "string"
    },
    "signing": {
      "description": "cosign signing of the pushed images and charts: signed, unsigned (the record says why), unknown, none (nothing pushed).",
      "enum": [
        "signed",
        "unsigned",
        "unknown",
        "none"
      ],
      "type": "string"
    },
    "team": {
      "description": "Team slug (team-bumblebee); none: only undeclared repositories. Under mine, one of your teams; ignored under unassigned.",
      "type": "string"
    },
    "undeclared": {
      "description": "Only repositories without a declaration (same as scope unassigned).",
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

Rebuild one repository's inventory record now, when it is stale or changed outside the manager. Writes only the cache.

Reads GitHub, CircleCI and the team files, runs the engine's checks in read mode and returns the record.

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

Deprecate, archive or delete a declared repository. Deleting wipes code and issues; the person must type the name.

deprecated: security-only Renovate. archived: archived on GitHub, unfollowed on CircleCI. deleted: deleted on GitHub with code, issues, pull requests, releases and packages; an org owner can restore it for 90 days. The owning team approves. An entry without `align: true` gets it, which opts the repository in to the company baseline: tell the person. Writes as you: dryRun: true previews and writes nothing; mode: "commit" carries it out and can take a minute — wait for the answer.

```json
{
  "properties": {
    "confirm": {
      "description": "For deleted: the repository name exactly as the person typed it. Ask them; never fill it in yourself.",
      "type": "string"
    },
    "dryRun": {
      "description": "Preview the change and write nothing (default false).",
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
      "description": "\"commit\": carry the change out as you. Required unless dryRun.",
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

Rebuild every repository's inventory record now instead of on schedule. Service-owner teams only; writes only the cache.

Runs in the background: started is false while a sweep already runs; list_repositories shows the result when done.

```json
{
  "properties": {},
  "required": [],
  "type": "object"
}
```

## `transfer_repository`

Move a declared repository to another team via a team-file pull request; the receiving team approves.

The reconciler then re-applies permissions, CODEOWNERS and catalog ownership. Writes as you: dryRun: true previews and writes nothing; mode: "commit" carries it out and can take a minute — wait for the answer.

```json
{
  "properties": {
    "dryRun": {
      "description": "Preview the change and write nothing (default false).",
      "type": "boolean"
    },
    "mode": {
      "description": "\"commit\": carry the change out as you. Required unless dryRun.",
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

Change a declared repository's team-file entry. Pass the whole entry, not a patch: omitted fields are removed.

Lifecycle: set_lifecycle. Owner: transfer_repository. The team reviews the pull request. Writes as you: dryRun: true previews and writes nothing; mode: "commit" carries it out and can take a minute — wait for the answer.

```json
{
  "properties": {
    "dryRun": {
      "description": "Preview the change and write nothing (default false).",
      "type": "boolean"
    },
    "entry": {
      "additionalProperties": true,
      "description": "The whole entry as it should read afterwards, not a patch: omitted fields are removed.",
      "properties": {},
      "type": "object"
    },
    "mode": {
      "description": "\"commit\": carry the change out as you. Required unless dryRun.",
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

Preview create_repository with the same arguments: rendered entries, template, name check, refusals. Writes nothing.

Also the review notices a person sees before the pull request exists. Run it with exactly the arguments you then commit.

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
      "description": "One declaration as it goes into the team file: name, componentType, gen: {language, flavours}, description, visibility, …",
      "properties": {},
      "type": "object"
    },
    "reason": {
      "description": "Why, for the pull request body.",
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

After create_repository, follow the new repository until ready or a phase fails. Call again while changed or pending.

Phases: declared, merged, created, scaffolded, setUp, released. Returns when ready, when a phase fails (failure says why; the watch ends), when a phase completes (changed: true — tell the person) or at timeout (pending names the phase); pendingReason says what is awaited.

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
