# The inventory record

giantswarm-repo-manager keeps one record per repository of the org in Valkey (key `repo:<owner>/<name>`), plus the last
sweep's summary (`inventory:sweep`). The inventory is a cache: the team files in `giantswarm/github` stay the desired
state, GitHub the reality; a lost store costs one sweep. The Go types are `internal/inventory/record.go`; the JSON below
is what `get_repository`, `refresh_repository`, `decide_repository` and `POST /internal/refresh` return.

## Refresh

| Trigger | What | Source |
|---|---|---|
| Schedule (`inventory.sweep.interval`, default `24h`) | Full sweep: every repository of the org, records of repositories that are neither on GitHub nor declared are removed; the summary is stored | `sweep` |
| `POST /internal/refresh` with `lastRun` | One repository after a reconciler run; the run is stored as `setup.lastRun` | `reconciler` |
| `POST /internal/refresh` without `lastRun`, tool `refresh_repository` | One repository on demand | `refresh` |

Every read fills `age` (now minus `refreshedAt`). A refresh rebuilds the whole record except `decision` and
`setup.lastRun`, which survive; a sweep with `engineChecks: false` also keeps the previous `setup.checks`.

### The reconciler's trigger (github#6031)

The reconciler workflow has no muster identity; it presents the static bearer token of `inventory.internal.existingSecret`
(key `token`, `INTERNAL_TOKEN` on the server) on the same listener as the MCP endpoint — the platform's route to the
server has to admit `/internal/refresh` for the workflow's runner. Body:

```json
{
  "repository": "giantswarm/<name>",
  "lastRun": {
    "result": { "...": "reconcile.Result — the engine's structured run, exactly what the workflow uploads as reconcile-<name>.json" },
    "runUrl": "https://github.com/giantswarm/github/actions/runs/<id>",
    "timestamp": "2026-09-16T22:00:00Z"
  }
}
```

The answer is the rebuilt record (200), 404 when the repository is neither declared nor on GitHub (its record is
removed), 401 without the token, 502 when GitHub could not be read. `POST /internal/sweep` starts a full sweep (202; 409
while one runs), `GET /internal/sweep` reports the last summary and whether one is running.

**Artifact contract for 6031a:** `reconcile-<name>.json` is `reconcile.Result` as `devctl repo reconcile` prints it
(`pkg/reposetup/reconcile`, JSON tags on every field: `repository`, `declared`, `team`, `mode`, `added`, `startedAt`,
`finishedAt`, `steps[]{step, verdict, summary, changes[], findings[]{kind, message, fix}}`, `converged`). The workflow
posts it unchanged as `lastRun.result`, with the run's URL and the time it finished.

## Shape

```jsonc
{
  "repository": "giantswarm/present-service",
  "name": "present-service",
  "declaration": {                      // null: unassigned (no team file declares it)
    "team": "team-bumblebee",
    "file": "repositories/team-bumblebee.yaml",
    "componentType": "service",
    "lifecycle": "",                    // deprecated | archived when set
    "language": "go",
    "flavours": ["app"],
    "entry": "- name: present-service\n  ...",   // the entry as the team file carries it
    "accepted": true,                   // the engine's validation; problems[] names the refusals
    "problems": []
  },
  "reality": {                          // null: gone from GitHub
    "url": "https://github.com/giantswarm/present-service",
    "description": "", "visibility": "public", "defaultBranch": "main",
    "isArchived": false, "isFork": false, "isTemplate": false, "isEmpty": false,
    "createdAt": "…", "pushedAt": "…", "language": "Go", "topics": [],
    "lastCommit": {"date": "…", "author": "renovate", "message": "…"},
    "lastPersonCommit": {"date": "…", "author": "alice", "message": "…"},   // bot identities excluded
    "historySampled": 30, "botCommits": 12,
    "openPullRequests": {"total": 2, "people": 1, "bots": 1, "renovate": 1, "oldestBotAt": "…",
                         "onboarding": [{"number": 1, "title": "Configure Renovate", "author": "renovate", "createdAt": "…"}]},
    "openIssues": 1,
    "latestRelease": {"tag": "v1.0.0", "publishedAt": "…"},
    "codeownersTeams": ["team-bumblebee"],
    "unknownCodeownersTeams": [],       // CODEOWNERS teams the org does not have
    "has": {"renovate": true, "dependabot": false, "circleci": true, "workflows": true, "dockerfile": true, "helm": true, "readme": true, "codeowners": true}
  },
  "circleci": {                         // absent without a CircleCI token
    "followed": true, "setupWorkflows": true,
    "lastPipeline": {"number": 42, "state": "created", "createdAt": "…", "ref": "main"}
  },
  "renovate": {
    "configured": true, "path": "renovate.json5",
    "enabled": true,                    // false only for a top-level enabled: false
    "preset": true,                     // extends giantswarm/renovate-presets
    "dashboardIssue": {"number": 3, "title": "Dependency Dashboard"},
    "lastPullRequest": {"number": 10, "title": "…", "author": "renovate", "createdAt": "…"},   // newest open Renovate PR
    "lastCommit": "…"                   // newest Renovate commit in the sampled history
  },
  "catalog": {"present": true},         // giantswarm/github catalog/components.yaml
  "mapping": {"present": true, "team": "bumblebee"},   // management-cluster-bases apps-to-teams mapping
  "setup": {
    "checks": { "…": "reconcile.Result in mode check — what devctl repo status prints" },
    "checkedAt": "…",
    "checkError": "",                   // why checks is missing (refused declaration, no read identity, …)
    "lastRun": {"result": {"…": "reconcile.Result"}, "runUrl": "…", "timestamp": "…"}
  },
  "orphan": {
    "score": 0,                         // 0–100
    "reasons": [],
    "stalePeriod": "4320h0m0s"          // the period judged against; clients may rescore (stalePeriodDays)
  },
  "findings": [
    {"kind": "default-icon", "message": "…", "fix": "…", "source": "engine"}
  ],
  "decision": {"verdict": "keep", "note": "…", "by": "alice (alice@example.com)", "at": "…"},   // optional
  "refreshedAt": "…",
  "source": "sweep",                    // sweep | refresh | reconciler
  "age": "5m3s"                         // filled on read
}
```

### Findings

The inventory's own kinds (`source: inventory`): `declared-but-gone` (declaration, no repository), `undeclared-on-github`
(repository, no declaration — archived ones included), `declaration-refused` (the engine's creation rules refuse the
entry, so its set-up checks cannot run). The engine's kinds pass through with `source: engine`: `repository-missing`,
`renamed`, `gen-circleci-refused`, `abs-prerequisite`, `default-icon`, `red-release`, `renovate-missing`,
`archived-undeclared`, `pending-pull-request`, `unchecked`.

### Orphan score

A sum of weighted reasons, capped at 100, computed from the record's facts alone (so `list_repositories` and
`get_repository` recompute it for another `stalePeriodDays`): no declaration 40; no commit by a person within the stale
period (or none in the sampled history) 30; empty repository 20; CODEOWNERS naming a team the org does not have 15;
Renovate not configured or disabled 10; Renovate silent (no Renovate pull request or commit within the stale period) 10;
no release 10; no CI (neither `.circleci/config.yml` nor workflows) 10; an onboarding pull request open (`reposetup/*`,
align-files, Renovate's) 10; the oldest open bot pull request older than the stale period 5. Archived and gone
repositories are not scored (score 0, one reason saying so).

## Sweep summary (`inventory:sweep`)

`startedAt`, `finishedAt`, `duration`, `repositories`, `declared`, `undeclared`, `gone`, `archived`, `engineChecks`,
`removed`, `graphql {calls, cost, remaining, limit, resetAt}`, `rest {calls, remaining, limit, resetAt}`,
`circleciCalls`, `errors[]` (source problems, a budget stop). The GitHub reads follow the prototype's paging:
repository metadata 20 a page (halved down to 5 on a page GitHub cannot answer — over the whole org 50 a page was answered with 502 and 25 with a truncated body), default-branch history in aliased batches of 20 (halved on a failing batch), the team
files, catalog, mapping and org teams in one query — one combined metadata+history query made GitHub answer 502. With
`inventory.sweep.graphqlBudgetFloor` set, a sweep stops cleanly when the GraphQL budget falls below it: what was
collected is stored, nothing is removed, the summary's `errors` say where it stopped.
