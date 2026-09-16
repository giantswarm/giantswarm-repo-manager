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
inventory record per repository in Valkey — declaration, reality, set-up state and an orphan score — and
exposes it, with the repository lifecycle, as MCP tools with the prefix `giantswarm-repo-manager`
(`x_giantswarm-repo-manager_<tool>` through muster):

| Tool | Purpose |
|---|---|
| `get_info` | the identity chain of the call: caller, GitHub grant (proven with a read), App identity, inventory store, engine, write modes |
| `list_repositories`, `get_repository` | the inventory: one row per repository (team, lifecycle, visibility, Renovate state, orphan score with reasons, finding kinds, set-up state, record age) with the last sweep's summary, scoped per caller — `mine` (the caller's teams: their GitHub teams through the grant, else the IdP groups), `team`, `unassigned`, `all` — and filtered as on the Repositories page (search, renovate, team incl. none, visibility, fork, lifecycle, inactiveDays, minOrphanScore, decision, finding); the full record of one repository. Shape in [`docs/inventory-record.md`](docs/inventory-record.md) |
| `refresh_repository`, `decide_repository` | rebuild one record now; leave a decision note (`keep`, with text) as the caller — annotations of the cache, nothing on GitHub |
| `validate_repository` | the dry run of one or more new declarations: the engine's rendered entries with defaults, the implied template and options, the name check through the App, the creation rules' refusals as data, and the guard notices a person sees before any pull request exists (`team-review` for an author outside the owning team and team-planeteers, `batch-review` above three entries) |
| `create_repository` | the creation-only pull request adding the entries to `repositories/<team>.yaml` as the caller — machine-approved by giantswarm/github's Validate workflow when no notice stands |
| `update_repository` | the entry replaced by the one passed (held to the schema, not the creation rules), one pull request as the caller |
| `transfer_repository` | the entry moved between two team files in one pull request naming the giving and the receiving team; the ask to the receiving team's channel, a notice to the giving team's |
| `set_lifecycle` | `deprecated` or `archived` set on the entry; the ask to the owning team's channel |
| `approve_change` | the caller's GitHub team membership checked (the team named in the pull request), then the approving review submitted as the caller — what the Slack ask's Approve button calls as the clicking member |
| `reconcile_repository` | the reconciler workflow (`reconcile-repositories.yaml` in giantswarm/github) dispatched for one repository as the caller; the run reports back through `/internal/refresh` and the completion message ("repository · catalog entity · first release") reaches the team's channel |

Every tool's description and input schema, as muster exposes them: [`docs/tools.md`](docs/tools.md).

**Writes are pull requests.** Every write tool takes `dryRun` and `mode`. `dryRun: true` returns the plan —
the entry as it will read, the schema's verdict, the pull request and the ask that would follow — and
writes nothing. `mode: "commit"` is the only write mode: a branch and a pull request in giantswarm/github,
committed and opened with the caller's own GitHub grant, so the author is the person. `mode: "apply"` is
refused for every write: a repository changed on GitHub without its declaration is the drift the reconciler
reports. Team files are edited byte for byte — header comment, comments on entries and the authors' key
order stay; only the one entry changes.

**Existing entries are held to the schema, not the creation rules.** `gen.flavours`, `gen.language` and
`gen.ci.generate` are mandatory for an entry the reconciler *creates*; for a declared repository the
inventory validates the entry against the schema alone (the engine's checks then run for it), and the
creation rules appear only in `validate_repository`'s dry run for an added entry.

**Asks and messages go through Swarmgeist.** Lifecycle and transfer asks are posted to klaus-gateway's
team-review endpoint (`POST /reviews`, an Approve button calling `approve_change` as the clicking member),
notices and completion messages to `POST /notices`; the channel comes from the team's policy file
(`repository-setup/<team>.yaml`, `slackChannel`), mapped to its Slack ID through `reviews.channels` when
the file carries a name. Authentication is this pod's projected ServiceAccount token (audience
`klaus-gateway`). An undelivered ask is reported in the result; approving on GitHub is equivalent.

## The pattern

The server is one of the platform's *managers*: the logic lives here, MCP is the only surface, and every
write tool takes `dryRun` and `mode`.

- **Behind muster, acting as the caller.** The chart registers the server as a muster `MCPServer` with
  `auth.forwardToken`: muster forwards the session's Dex `id_token` byte-identical, the server validates
  it against Dex (an OAuth 2.1 resource server on [mcp-oauth](https://github.com/giantswarm/mcp-oauth),
  the audience must be one of the trusted audiences), and the caller's identity and token travel with the
  request. Without a bearer the MCP endpoint is a 401 — nothing runs as the ServiceAccount.
- **A muster broker client for the person's GitHub grant.** The server is a confidential client of
  muster's token-exchange broker (`tokenExchangeBroker.brokerClients.giantswarm-repo-manager`, allowed
  audience `github`). Per call it exchanges the forwarded `id_token` (RFC 8693, `audience=github`) and
  receives the person's own GitHub access token — the grant the person filed when they connected GitHub
  in muster — with its remaining lifetime and never the refresh token. A person without a grant is told
  to connect GitHub in muster (`invalid_target`). Every write on GitHub lands with that token, as the person.
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
| **The person** (muster's GitHub grant, released by the broker) | every write: team-file pull requests, and the read that proves the grant in `get_info` | `muster.url`, `broker.clientID`, `broker.existingSecret` (`client-secret`), `broker.audience` |
| **The giantswarm-align-files App installation** | unattended, read-only inventory reads (GraphQL sweeps, the engine's checks in read mode) and the engine's name checks — its own rate budget; `GITHUB_TOKEN` stands in for it in development only | `githubApp.appID`, `githubApp.installationID`, `githubApp.existingSecret` (`private-key`) |
| **The caller towards this server** (Dex `id_token` forwarded by muster) | who is calling; the subject token of the broker exchange | `oauth.*` (or the platform's `global.identity.*`), `muster.mcpServer.auth.requiredAudiences` |
| **`CIRCLECI_API_TOKEN`** (architectbot's token, read scope) | the inventory's CircleCI state (followed, setup workflows, last pipeline) and the engine's circleci/release checks; `get_info` reports whether it is set | `circleci.existingSecret` (`token`) |

Effective rights of a write are the intersection of the Dex GitHub App's permissions and the person's.
No personal token beyond the two org secrets that already exist, no new GitHub App, no new OAuth client.

## The inventory

One Valkey record per repository of the org, filled by a full sweep on a schedule (`inventory.sweep.interval`,
default daily), one repository after each reconciler run (`POST /internal/refresh` with the run, authenticated by the
token of `inventory.internal.existingSecret`) and on demand (`refresh_repository`). GitHub is read as the App through
GraphQL — repository metadata 20 a page (halved when GitHub cannot answer a page), default-branch history in aliased batches of 20 — and the engine's checks run
in read mode per accepted declaration (`inventory.sweep.engineChecks`). `--sweep-once` runs one sweep and prints the
summary (calls, GraphQL points, REST calls, duration); `inventory.sweep.graphqlBudgetFloor` stops a sweep cleanly when
the budget runs low. The record, its findings and the orphan score are described in
[`docs/inventory-record.md`](docs/inventory-record.md).

## What the tests prove

- **The tools** (`internal/e2e/tools_test.go`, a fake giantswarm/github with contents, git data, pull requests,
  reviews and dispatches, a fake klaus-gateway): `validate_repository` renders an accepted entry with its template and
  refuses a bad one as data, carries `team-review` for an outsider and `batch-review` for four entries;
  `create_repository` with `dryRun` opens nothing; `mode: apply` is refused on every write; a commit opens the pull
  request under the person's login (a person without a grant is told to connect GitHub); a transfer's pull request
  touches both files and names both teams, the ask reaches the receiving team's channel and the notice the giving
  team's; `set_lifecycle: archived` opens the pull request and posts the ask whose Approve calls
  `approve_change`; `approve_change` refuses the outsider and lands the member's review; `update_repository` rewrites
  one entry and leaves the rest of the file byte-identical; `list_repositories` scopes per caller (GitHub teams, IdP
  groups) and applies the page's filters; `reconcile_repository` dispatches the workflow as the person and the
  reconciler's report back posts the completion message.
- `go test ./...` — the write framework refuses `mode: apply` for every registered write tool and
  advertises `commit` alone; the dry run is the engine's result; and, in `internal/e2e`, the whole
  identity chain against fakes: a forwarded `id_token` from a fake Dex is validated, exchanged at a fake
  muster broker as this client for the person's grant, the grant reads a fake GitHub as the person, the
  App answers `GET /app`, a seeded Valkey is reported (a real one through `VALKEY_ADDR`, else in-process).
- **The inventory** (`internal/e2e/inventory_test.go`, a fake org behind the App's GraphQL): the full sweep leaves one
  record per repository — declared and present, declared but gone, a refused declaration, undeclared, archived — with
  the findings, the engine's read-mode result, CircleCI, Renovate and the orphan scores; the tools list, filter, rescore,
  refresh and decide; the reconciler's trigger stores its run and survives the next refresh; a sweep stops cleanly at the
  GraphQL budget floor.
- `make scenario-test` — muster's own scenario harness (`tests/scenarios`) with a mocked GitHub: muster
  forwards the Dex token to a backend registered the way this chart registers the server, and the broker
  releases the person's grant to the `giantswarm-repo-manager` client (no grant, wrong secret, foreign
  audience refused). The harness runs its mocks on ports of its own choosing, so the server half runs
  against fakes in Go; both halves run in the `scenario-test` CI job with a Valkey service container.
- The muster-in-agentlab proof — this server registered with a real muster and Dex, a lab person's call
  carrying their grant — is the next step and is not covered by CI.

## Deploying

The chart is published to the Giant Swarm catalog as `giantswarm-repo-manager`; see
[`helm/giantswarm-repo-manager`](helm/giantswarm-repo-manager/README.md) for its values. On the platform
it reads the identity contract (`global.identity.*`, `global.domain`) as the defaults of `oauth.*`;
`muster.mcpServer.enabled` registers it with muster, `muster.url` plus `broker.existingSecret` turn on the
broker client, `githubApp.*` the App identity, `circleci.existingSecret` the CircleCI token.

## Development

See [docs/development.md](docs/development.md).
