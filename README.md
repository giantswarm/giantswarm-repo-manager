# giantswarm-repo-manager

Giant Swarm's repository set-up service: an MCP server behind [muster](https://github.com/giantswarm/muster)
that lists, validates, creates and reconciles the repositories of the giantswarm GitHub org — every write
as the person calling it.

The `giantswarm-` prefix marks what sets it apart from `model-manager`, `agent-manager` and
`cluster-manager`: it is specific to the giantswarm org and of no use to other Agent Platform users, so it
ships as its own Giant Swarm app (this chart, its own Valkey), not in the `agent-platform` chart.

## What it does

The team files in [giantswarm/github](https://github.com/giantswarm/github) (`repositories/team-*.yaml`)
are the desired state of every repository; GitHub is the reality. giantswarm-repo-manager keeps one
inventory record per repository in Valkey — declaration, reality, set-up state and findings — and
exposes it, with the repository lifecycle, as MCP tools with the prefix `giantswarm-repo-manager`
(`x_giantswarm-repo-manager_<tool>` through muster):

| Tool | Purpose |
|---|---|
| `get_info` | how the call is authenticated: the caller (login and id, verified with `GET /user`), the pinned authorization server, App identity, inventory store, engine, write modes |
| `list_repositories`, `get_repository` | the inventory: one row per repository (team, lifecycle, visibility, archived on GitHub, fork, Renovate state, finding kinds, set-up state, record age), sorted by name, with the last sweep's summary, scoped per caller — `mine` (the caller's GitHub teams, read as them), `team`, `unassigned`, `all` — and filtered as on the Repositories page: `search`, `team` in every scope (under `mine` one of the caller's teams — another selects no rows and the answer's `note` says so; `none` under `all` is the undeclared), `renovate` (`configured`, `missing`, `active`, `inactive` against `inventory.renovate.activeDays`), `visibility`, `fork`, `lifecycle` (`active`, `deprecated`, `archived` — a repository archived on GitHub counts as archived whatever its declaration says), the boolean `archived`, `inactiveDays`, `finding`; the full record of one repository. Shape in [`docs/inventory-record.md`](docs/inventory-record.md) |
| `refresh_repository` | rebuild one record now — writes the inventory cache, nothing on GitHub |
| `validate_repository` | the dry run of one or more new declarations: the engine's rendered entries with defaults, the implied template and options, the name check through the App, the creation rules' refusals as data, and the guard notices a person sees before any pull request exists (`team-review` for an author outside the owning team and team-planeteers, `batch-review` above three entries) |
| `create_repository` | the creation-only pull request adding the entries to `repositories/<team>.yaml` as the caller — machine-approved by giantswarm/github's Validate workflow when no notice stands |
| `update_repository` | the entry replaced by the one passed (held to the schema, not the creation rules), one pull request as the caller |
| `transfer_repository` | the entry moved between two team files in one pull request naming the giving and the receiving team; the ask to the receiving team's channel, a notice to the giving team's |
| `set_lifecycle` | `deprecated` or `archived` set on the entry; the ask to the owning team's channel |
| `approve_change` | the caller's GitHub team membership checked (the team named in the pull request), then the approving review submitted as the caller — what the Slack ask's Approve button calls as the clicking member |
| `reconcile_repository` | the reconciler workflow (`reconcile-repositories.yaml` in giantswarm/github) dispatched for one repository as the caller; the record shows `setup.pendingRun` until the inventory has read the run's artifact as `setup.lastRun` (with the run's `change` block); the team's standup channel hears nothing about it -- a Reconcile now is the caller's, and its failed steps and findings are on the record and in the run |

Every tool's description and input schema, as muster exposes them: [`docs/tools.md`](docs/tools.md).

**Writes are pull requests.** Every write tool takes `dryRun` and `mode`. `dryRun: true` returns the plan —
the entry as it will read, the schema's verdict, the pull request and the ask that would follow — and
writes nothing. `mode: "commit"` is the only write mode: a branch and a pull request in giantswarm/github,
committed and opened with the caller's own GitHub token, so the author is the person. `mode: "apply"` is
refused for every write: a repository changed on GitHub without its declaration is the drift the reconciler
reports. Team files are edited byte for byte — header comment, comments on entries and the authors' key
order stay; only the one entry changes.

**Existing entries are held to the schema, not the creation rules.** `gen.flavours`, `gen.language` and
`gen.ci.generate` are mandatory for an entry the reconciler *creates*; for a declared repository the
inventory validates the entry against the schema alone (the engine's checks then run for it), and the
creation rules appear only in `validate_repository`'s dry run for an added entry.

**Asks and messages go through Swarmgeist.** Lifecycle and transfer asks — naming the asking person — are
posted to klaus-gateway's team-review endpoint (`POST /reviews`, an Approve button calling `approve_change`
as the clicking member) into the team's `slackChannel`. Notices go to `POST /notices` into the team's
`standupChannel`: the giving team's transfer notice, and after a reconciler run one sentence about the
change for the team, rendered from the artifact's `change` block and the declaration — `alice created a
new repo: bumblebee-repo (app, go)`, `alice added the existing repo … to team-bumblebee`, `alice
transferred the repo … (app, go) from team-planeteers to team-bumblebee`, `alice archived the repo …`,
`alice deprecated the repo …` — linking the pull request, plus one sentence per failed step or finding
of that person's run with what to do, linking the run — bar a finding of kind `unchecked`, a check the
reconciler's own token could not run, which is the platform's to fix and stays on the record. A run nobody's change is behind — a Reconcile now,
the schedule, an artifact without a `change` block — posts nothing, findings and failures included: the
reconciler doing its job is not news, and the nightly's findings would repeat every night; they stay on the
record and in the run's summary per team. An edit a person made (`changed`) posts its failed steps and
findings alone. Both channels come from the team's policy file (`repository-setup/<team>.yaml`);
a file without either is refused, nothing stands in. A channel name is mapped to its Slack ID through
`reviews.channels` (the gateway takes IDs). Authentication is this pod's projected ServiceAccount token
(audience `klaus-gateway`). An undelivered ask is reported in the result; approving on GitHub is equivalent.

## The pattern

The server is one of the platform's *managers*: the logic lives here, MCP is the only surface, and every
write tool takes `dryRun` and `mode`.

- **Behind muster, acting as the caller.** The chart registers the server as a muster `MCPServer` in
  OAuth mode, pinned to the GitHub App `giantswarm-repo-manager` as its authorization server (issuer
  identity `https://github.com/apps/giantswarm-repo-manager`, GitHub's authorize and token endpoints, the
  App's client from the Secret `giantswarm-repo-manager-oauth-client`, `grantScope: subject`) — the
  pattern of the `github` and `pro` servers on the platform. muster runs the consent once per person,
  stores and refreshes their user token and puts it on every call as the bearer; the server verifies it
  with `GET /user` (once per token, cached for 15 minutes) and the caller and the token travel with the
  request. Without a bearer, or with one GitHub refuses, the MCP endpoint is a 401 naming the sign-in
  (`core_auth_login server=giantswarm-repo-manager`) — nothing runs as the ServiceAccount. Its
  protected-resource metadata names the pinned App. muster and the Dev Portal changed nothing for this.
- **Every GitHub call as the person.** The bearer is the person's own token through the App: their teams,
  the team-file commit, the pull request and the workflow dispatch land as them, bounded by GitHub to the
  person's rights ∩ the App's. This server holds no token, exchanges none and lets no personal token stand in.
- **The engine is imported, not re-implemented.** Validation of a declaration against the repositories
  schema, the creation rules, template derivation and the name check come from devctl's
  `pkg/reposetup` package; `get_info` reports the engine's module version from the build info.
- **Only `mode: commit`.** The write framework (`internal/tools/write.go`) owns `dryRun` and `mode` for
  every write tool: `dryRun: true` returns the rendered change and writes nothing; `mode: "commit"` opens
  the team-file pull request as the person; **`mode: "apply"` is refused before any tool body runs** — a
  repository changed on GitHub without its declaration is exactly the drift the reconciler reports. The
  tools' input schemas advertise `commit` as the only mode.

## Identities

| Identity | Used for | Configured by |
|---|---|---|
| **The person** (their GitHub user token through the App `giantswarm-repo-manager`, put on every call by muster) | who is calling (`get_info`'s `caller`, verified with `GET /user`); every write: team-file pull requests, the reconciler dispatch; the reads as the person (their teams) | `oauth.enabled`, `muster.mcpServer.auth.authorizationServer.*` (the pin; the Secret `giantswarm-repo-manager-oauth-client` holds the App's client) |
| **The inventory App installation** (`giantswarm-repo-manager-inventory`, read-only: Administration, Contents, Pull requests, Issues, Commit statuses, Checks — the engine's protection step reads the default branch head's check runs to learn which conditional checks exist, and without it every repository carries the finding `unchecked` (the checks on main not readable with this token, 403) —, Metadata, Organization members) | unattended, read-only inventory reads (GraphQL sweeps, the engine's checks in read mode) and the engine's name checks — its own rate budget; `get_info` names it as `inventory.identity` (`app giantswarm-repo-manager-inventory (installation <id>)`, or `not configured`). Nothing stands in for it: without the App there are no unattended reads, and the tools that need them say so | `githubApp.appID`, `githubApp.installationID`, `githubApp.existingSecret` (`private-key`) |

No CircleCI token. The inventory's CircleCI facts come from GitHub and the reconciler: whether CircleCI builds
a repository from the `ci/circleci:` commit statuses on its default branch head (read with the repository, as
the inventory App — CircleCI posts them for the projects it builds) together with the `.circleci/config.yml`
blob; whether the project is followed and setup workflows are on from the reconciler's run artifact, read from
GitHub after every run (its `circleci` step). What neither source yields, the record names as
`unknown` — the last pipeline is not derivable and is not part of the record. `get_info` reports
`circleci.source: statuses+artifact`. The engine's read-mode checks run without a CircleCI client, so their
`circleci` and `release` steps are skipped.

Effective rights of a write are the person's own ∩ the App `giantswarm-repo-manager`'s permissions, on
the repository at hand. No personal token, no token held or exchanged by this server, no broker; the
login App that signs people in to the portal is untouched and gains no write scope.

## The inventory

One Valkey record per repository of the org, filled by a full sweep on a schedule (`inventory.sweep.interval`,
default daily), one repository per artifact of each completed reconciler run (the poller reads the workflow's runs from
GitHub as the inventory App every `inventory.reconciler.pollInterval`, default 5 min, every 30 s while a Reconcile now is
pending — nothing reaches the server from the workflow) and on demand (`refresh_repository`). GitHub is read as the App through
GraphQL — repository metadata 20 a page (halved when GitHub cannot answer a page), default-branch history in aliased batches of 20 — and the engine's checks run
in read mode per accepted declaration (`inventory.sweep.engineChecks`). `--sweep-once` runs one sweep and prints the
summary (calls, GraphQL points, REST calls, duration); `inventory.sweep.graphqlBudgetFloor` stops a sweep cleanly when
the budget runs low. The record and its findings are described in
[`docs/inventory-record.md`](docs/inventory-record.md).

## What the tests prove

- **The tools** (`internal/e2e/tools_test.go`, a fake giantswarm/github with contents, git data, pull requests,
  reviews and dispatches, a fake klaus-gateway): `validate_repository` renders an accepted entry with its template and
  refuses a bad one as data, carries `team-review` for an outsider and `batch-review` for four entries;
  `create_repository` with `dryRun` opens nothing; `mode: apply` is refused on every write; a commit opens the pull
  request under the person's login; a transfer's pull request
  touches both files and names both teams, the ask reaches the receiving team's channel and the notice the giving
  team's; `set_lifecycle: archived` opens the pull request and posts the ask whose Approve calls
  `approve_change`; `approve_change` refuses the outsider and lands the member's review; `update_repository` rewrites
  one entry and leaves the rest of the file byte-identical; `list_repositories` scopes per caller (their GitHub
  teams, read as them) and applies the page's filters; `reconcile_repository` dispatches the workflow as the person, the
  run's artifact lands as `setup.lastRun` with its `change` block and a converged Reconcile now posts nothing, while the
  run of a merged pull request that created the repository posts the one sentence about it to the team's standup
  channel, linking the pull request, and its finding as a second sentence linking the run.
- `go test ./...` — the write framework refuses `mode: apply` for every registered write tool and
  advertises `commit` alone; the dry run is the engine's result; and, in `internal/e2e`, the whole
  identity chain against a fake GitHub: the person's user token as the bearer is verified with `GET /user`
  once (the next call is answered from the cache) and names the caller in `get_info` (`auth.mode: bearer`,
  the pinned App); a request without a bearer and one GitHub refuses are 401s whose challenge names the
  protected-resource metadata and whose body names the sign-in; the metadata names the pinned App; the
  App answers `GET /app`; a seeded Valkey is reported (a real one through `VALKEY_ADDR`, else in-process).
- **The inventory** (`internal/e2e/inventory_test.go`, a fake org behind the App's GraphQL): the full sweep leaves one
  record per repository — declared and present, declared but gone, a refused declaration, undeclared, archived — with
  the findings, the engine's read-mode result, CircleCI and Renovate; the tools list the rows by name, filter them (`team`
  under `mine`, `archived`, `lifecycle` counting GitHub's archived flag) and refresh one; the reconciler's run is stored and
  survives the next refresh; a sweep stops cleanly at the GraphQL budget floor. Every filter value of `list_repositories`
  has a table test (`internal/tools/filter_test.go`).
- `make scenario-test` — muster's own scenario harness (`tests/scenarios`) with a mocked GitHub: a backend
  registered the way this chart registers the server (OAuth mode, pinned to the App as the authorization
  server, `grantScope: subject`); a person authorizes the App once and muster puts their token on the call
  (the backend echoes the bearer it received); a second session of the same person adopts the grant
  without a sign-in; a person without a grant is not authenticated and `core_auth_login` answers them the
  sign-in URL. The harness runs its mocks on ports of its own choosing, so the server half runs against
  fakes in Go; both halves run in the `scenario-test` CI job with a Valkey service container.
- The proof on an installation — this server registered with the installation's muster, a person's
  portal call carrying their App token — is the release's acceptance and is not covered by CI.

## Deploying

The chart is published to the Giant Swarm catalog as `giantswarm-repo-manager`; see
[`helm/giantswarm-repo-manager`](helm/giantswarm-repo-manager/README.md) for its values.
`muster.mcpServer.enabled` with `oauth.enabled` registers it with muster in OAuth mode, pinned to the App
(`muster.mcpServer.auth.authorizationServer.*`; the Secret `giantswarm-repo-manager-oauth-client` in the
release namespace carries the App's `client-id` and `client-secret`), `githubApp.*` sets the inventory App
`giantswarm-repo-manager-inventory` as the identity of the unattended reads.

## Development

See [docs/development.md](docs/development.md).
