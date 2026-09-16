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

| Tool | Purpose | State |
|---|---|---|
| `get_info` | the identity chain of the call: caller, GitHub grant (proven with a read), App identity, inventory store, engine, write modes | shipped |
| `create_repository` | a declaration added to the team file; the dry run is the engine's validation (rendered entry, implied template, name check) | dry run shipped, `commit` next |
| `list_repositories`, `get_repository`, `validate_repository` | the inventory by scope, one record, the dry run of a declaration | next slices |
| `update_repository`, `transfer_repository`, `set_lifecycle`, `approve_change`, `reconcile_repository` | the rest of the lifecycle | next slices |

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
| **The giantswarm-align-files App installation** | unattended, read-only inventory reads and the engine's name checks — its own rate budget | `githubApp.appID`, `githubApp.installationID`, `githubApp.existingSecret` (`private-key`) |
| **The caller towards this server** (Dex `id_token` forwarded by muster) | who is calling; the subject token of the broker exchange | `oauth.*` (or the platform's `global.identity.*`), `muster.mcpServer.auth.requiredAudiences` |
| **`CIRCLECI_API_TOKEN`** (architectbot's token, read scope) | follow/setup-workflow checks of the reconciler — not used before that slice; `get_info` reports whether it is set | `circleci.existingSecret` (`token`) |

Effective rights of a write are the intersection of the Dex GitHub App's permissions and the person's.
No personal token beyond the two org secrets that already exist, no new GitHub App, no new OAuth client.

## What the tests prove

- `go test ./...` — the write framework refuses `mode: apply` for every registered write tool and
  advertises `commit` alone; the dry run is the engine's result; and, in `internal/e2e`, the whole
  identity chain against fakes: a forwarded `id_token` from a fake Dex is validated, exchanged at a fake
  muster broker as this client for the person's grant, the grant reads a fake GitHub as the person, the
  App answers `GET /app`, a seeded Valkey is reported (a real one through `VALKEY_ADDR`, else in-process).
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
