# The inventory record

giantswarm-repo-manager keeps one record per repository of the org in Valkey (key `repo:<owner>/<name>`), plus the last
sweep's summary (`inventory:sweep`) and the reconciler poller's cursor (`inventory:reconciler`). The inventory is a cache: the team files in `giantswarm/github` stay the desired
state, GitHub the reality; a lost store costs one sweep. The Go types are `internal/inventory/record.go`; the JSON below
is what `get_repository` and `refresh_repository` return.

## Refresh

| Trigger | What | Source |
|---|---|---|
| Schedule (`inventory.sweep.interval`, default `24h`) | Full sweep: every repository of the org, records of repositories that are neither on GitHub nor declared are removed; the summary is stored | `sweep` |
| Reconciler poll (`inventory.reconciler.pollInterval`, default `5m`; every 30 s while an Align now is pending) | One repository per `reconcile-<name>` artifact of a completed reconciler run, read from GitHub as the inventory App; the run is stored as `setup.lastRun` | `reconciler` |
| Tool `refresh_repository` | One repository on demand | `refresh` |

Every read fills `age` (now minus `refreshedAt`). A refresh rebuilds the whole record except `setup.lastRun`, which
survives; a sweep with `engineChecks: false` also keeps the previous `setup.checks`. A record stored by an earlier
version may carry fields this version does not have (an orphan score, a decision note); they are ignored on read and
gone after the next refresh.

### The reconciler's runs

Nothing reaches this server from the reconciler: the workflow runs in the team files repository (`teamFiles.repository`)
and uploads one Actions artifact `reconcile-<name>` per repository it ran over; the inventory **pulls** them as the
inventory App (Actions read on that repository). Every `inventory.reconciler.pollInterval` (default `5m`) the poller lists
the runs of `inventory.reconciler.workflow` (default `reconcile-repositories.yaml`) created since its cursor, and for every
completed run not consumed yet lists the artifacts, downloads each `reconcile-<name>` (a zip with one JSON, at most 4 MiB;
the API answers with a redirect to a signed blob URL, fetched without the installation token) and stores it as the
repository's `setup.lastRun`: `result` is the JSON, `runUrl` its `workflowRun.url`, `timestamp` its `finishedAt`, `runId`
and `attempt` the Actions run — a run's artifact is read once, re-polls and restarts included; a re-run (the same run id,
the next attempt) is read again; an artifact older than the stored run does not replace it. A run without artifacts
(cancelled, failed before its report step) is consumed and logged; a repository that is neither declared nor on GitHub
loses its record.

The cursor (`inventory:reconciler`) is a watermark with the run attempts consumed at or after it: the watermark moves to
the oldest run still open — running, or whose artifacts could not be read this time — so a run that completes after a
newer one is not missed; it never lies more than seven days back (the first poll reads the last seven days). Without an
inventory App or a store there is no poller (one log line at start); `pollInterval: "0"` turns it off.

**Align now.** `align_repository` dispatches the workflow as the caller; a `workflow_dispatch` returns no run id,
so the record carries `setup.pendingRun {dispatchedAt, by}` until the repository's next artifact arrives. While any
`pendingRun` is younger than 15 minutes the poller runs every 30 s. After 15 minutes without an artifact the pending run
becomes `setup.missingRun` with the finding `reconcile-run-missing` (`source: inventory`), whose fix names the workflow's
Actions page; the next artifact or dispatch clears it. `get_repository` and `list_repositories` (`setup.pendingRun` in
the row) carry the state the Repositories page shows.

**Artifact contract for 6031a:** `reconcile-<name>.json` is `reconcile.Result` as `devctl repo reconcile` prints it
(`pkg/reposetup/reconcile`, JSON tags on every field: `repository`, `declared`, `team`, `mode`, `added`, `startedAt`,
`finishedAt`, `steps[]{step, verdict, summary, changes[], findings[]{kind, message, fix}}`, `converged`) plus
`workflowRun {id, url, attempt, event, trigger, devctl}`, `finishedAt` and `change {kind, by, pullRequest {number, url},
fromTeam}` — the reconciler's classification of the team-file change the run followed: `kind` is `created` (the
repository is younger than its pull request), `added` (an existing repository declared), `transferred` (`fromTeam` names
the giving team), `archived`, `deprecated`, `changed` (any other edit), `dispatched` (an Align now) or `nightly`; `by` is
the pull request's author or the dispatching person (absent for the schedule). The poller stores the result unchanged as
`lastRun.result`, with `workflowRun.url`, `finishedAt` and the change block as `lastRun.change`. The change block is what
the message to the team's standup channel is rendered from (README, "Asks and messages go through Swarmgeist").

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
  "circleci": {                         // absent when the repository is gone; no CircleCI token is involved
    "followed": true,                   // ci/circleci: statuses on the head, or the reconciler's circleci step found the project followed
    "setupWorkflows": true,             // from the reconciler's run artifact only; absent and named in unknown until a run tells
    "head": {"state": "success", "contexts": ["ci/circleci: go-build", "ci/circleci: push-to-registries"], "at": "…"},  // the default branch head's ci/circleci: statuses (worst state)
    "source": "statuses+artifact",      // statuses | artifact | statuses+artifact: the sources that answered
    "unknown": [],                      // the facts no source yields: setupWorkflows, followed (status contexts truncated, none CircleCI's)
    "error": ""                         // the reconciler's circleci step failing, as its run reported it
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
    "checkError": "",                   // why checks is missing (no read identity, …); a refused entry has checks = the engine's Refused result
    "lastRun": {"result": {"…": "reconcile.Result"}, "runUrl": "…", "timestamp": "…", "runId": 1, "attempt": 1,
                "change": {"kind": "created", "by": "alice", "pullRequest": {"number": 4711, "url": "…"}}},  // kind: created | added | transferred (+fromTeam) | archived | deprecated | changed | dispatched | nightly
    "pendingRun": {"dispatchedAt": "…", "by": "alice"},     // an Align now waiting for its run's artifact
    "missingRun": {"dispatchedAt": "…", "by": "alice", "noticedAt": "…", "runsUrl": "…"}   // one that did not report in 15 min
  },
  "findings": [
    {"kind": "default-icon", "message": "…", "fix": "…", "source": "engine"}
  ],
  "refreshedAt": "…",
  "source": "sweep",                    // sweep | refresh | reconciler
  "age": "5m3s"                         // filled on read
}
```

### Findings

The inventory's own kinds (`source: inventory`): `declared-but-gone` (declaration, no repository), `undeclared-on-github`
(repository, no declaration — archived ones included), `reconcile-run-missing` (an Align now whose run did not report
within 15 minutes; the fix names the workflow's Actions page). The engine's kinds pass through with `source: engine`:
`entry-refused` and `gen-circleci-refused` (the engine refuses the entry — `setup.checks` is its `Refused` result, the
`entry` step reported and no step run, one finding per problem naming the field to fix), `repository-missing`, `renamed`,
`abs-prerequisite`, `default-icon`, `red-release`, `renovate-missing`, `archived-undeclared`, `pending-pull-request`,
`unchecked`.

### Renovate state

`list_repositories` derives each row's `renovate` from the record when it is read: `missing` without a configuration,
`active` when `renovate.lastPullRequest` or `renovate.lastCommit` is within the server's Renovate activity period
(`inventory.renovate.activeDays`, default 180), else `inactive`. The record carries the facts, not the verdict.

## Sweep summary (`inventory:sweep`)

`startedAt`, `finishedAt`, `duration`, `repositories`, `declared`, `undeclared`, `gone`, `archived`, `engineChecks`,
`removed`, `graphql {calls, cost, remaining, limit, resetAt}`, `rest {calls, remaining, limit, resetAt}`,
`errors[]` (source problems, a budget stop). The GitHub reads follow the prototype's paging:
repository metadata 20 a page (halved down to 5 on a page GitHub cannot answer — over the whole org 50 a page was answered with 502 and 25 with a truncated body), default-branch history in aliased batches of 20 (halved on a failing batch), the team
files, catalog, mapping and org teams in one query — one combined metadata+history query made GitHub answer 502. With
`inventory.sweep.graphqlBudgetFloor` set, a sweep stops cleanly when the GraphQL budget falls below it: what was
collected is stored, nothing is removed, the summary's `errors` say where it stopped.
