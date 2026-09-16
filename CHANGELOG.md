# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).



## [Unreleased]

### Added

- The nine tools: `validate_repository` (the engine's dry run with the guard notices `team-review` and `batch-review`, the author's teams read as the person), `create_repository` in `mode: commit` (the creation-only pull request as the caller), `update_repository`, `transfer_repository` (one pull request over both team files, naming the giving and the receiving team), `set_lifecycle` (`deprecated` | `archived`), `approve_change` (team membership checked, the approving review as the caller) and `reconcile_repository` (the `reconcile-repositories.yaml` dispatch as the caller). Every write takes `dryRun` and `mode`; `commit` is the only mode, `apply` is refused. Team files are edited byte for byte.
- `list_repositories` scopes per caller (`mine` | `team` | `unassigned` | `all`; the caller's teams from their GitHub grant, else the IdP groups) and takes the Repositories page's filters (search, renovate, team incl. none, visibility, fork, lifecycle, inactiveDays, decision).
- Asks and messages through klaus-gateway's team-review endpoint (`POST /reviews`, `POST /notices`; the pod's projected ServiceAccount token for audience `klaus-gateway`): lifecycle and transfer asks with an Approve button calling `approve_change`, the notice to a transfer's giving team, the completion message after a reconciler run (`repository · catalog entity · first release`). Chart values `reviews.gatewayURL`, `reviews.audience`, `reviews.channels` (name → Slack ID for policy files that carry a channel name), `teamFiles.repository`, `teamFiles.ref`.
- `docs/tools.md`: every tool's description and input schema, kept current by a test.

### Changed

- Existing declarations are validated against the repositories schema alone; the creation rules (mandatory `gen.flavours`/`gen.language`, `gen.ci.generate`) apply to an added entry in `validate_repository`'s dry run only. The `declaration-refused` finding and the withheld engine checks no longer hit the 222 declared repositories the creation rules refused.
- The chart's Valkey subchart carries the restricted Pod Security Standard's security contexts (pod, container, metrics exporter) by default, so an installation enforcing PSS restricted admits it without overlay values.

- Repository bootstrap: the Go module, a health-serving binary, the Dockerfile and the Helm chart with its Valkey dependency.
- The MCP server behind muster: an OAuth 2.1 resource server (mcp-oauth) validating the Dex `id_token` muster forwards, a muster token-exchange broker client releasing the caller's GitHub grant (audience `github`), the giantswarm-align-files App identity for unattended reads, the Valkey inventory store and devctl's `pkg/reposetup` engine.
- `get_info`: the identity chain of a call (caller, grant proven with `GET /user`, App, inventory, engine, write modes).
- The write-tool framework: every write takes `dryRun` and `mode`; `mode: apply` is refused before any tool runs, `commit` is the only mode. `create_repository` with the engine's dry run.
- Chart values for OAuth (with the platform's `global.identity.*` defaults), the muster MCPServer registration, the broker client, the GitHub App and the CircleCI token, all through Secret references.
- Tests: the identity chain against fakes (`internal/e2e`), muster's scenario harness with a mocked GitHub (`tests/scenarios`), both in the `scenario-test` CI job with a Valkey service container.
- The inventory: one Valkey record per repository of the org — the declaration from the team files, the GitHub reality (GraphQL as the App: visibility, flags, last commit and last person commit, open pull requests split people/bots, issues, latest release, CODEOWNERS teams, presence of Renovate/Dependabot/CircleCI/workflows/Dockerfile/Helm), CircleCI (followed, setup workflows, last pipeline), Renovate (config, dashboard issue, last pull request), catalog entity, mapping entry, the set-up state (the engine's checks in read mode plus the last reconciler run) and the orphan score with its reasons and an adjustable stale period. Shape in `docs/inventory-record.md`.
- Refresh: a full sweep on a schedule (`inventory.sweep.interval`), one repository after a reconciler run (`POST /internal/refresh` with the run, behind `inventory.internal.existingSecret`) and on demand (`refresh_repository`); every record carries its age. The sweep pages the way the prototype learned (metadata 50 a page, histories in aliased batches of 20) and stops cleanly at a GraphQL budget floor.
- Findings the inventory shows: `declared-but-gone`, `undeclared-on-github`, `declaration-refused`, and the engine's (`default-icon`, `gen-circleci-refused`, …).
- Tools `list_repositories`, `get_repository`, `refresh_repository`, `decide_repository`; `--sweep-once` runs one sweep and prints the summary; `GITHUB_TOKEN` as a development stand-in for the App.
- devctl bumped to the engine with `pkg/reposetup/reconcile`.

### Fixed

- The server no longer exits when the inventory store is not reachable at start (it crash-looped for 20 minutes on the first gazelle rollout while Valkey came up late): it serves at once and connects to Valkey in the background, retrying with backoff for `inventory.connectTimeout` (`INVENTORY_CONNECT_TIMEOUT`, 5m; `0` waits for ever). Until the store answers, readiness fails with the reason, the inventory tools and `/internal/*` answer `inventory unavailable` (503), and the identity tools work; the sweep schedule starts on the connected store. A Valkey lost at runtime reads the same way and is reconnected by the client (#6).
- Secrets read from the environment are trimmed: a Secret created from a file carries the file's trailing newline, which muster's broker answered with `invalid_client`.



[Unreleased]: https://github.com/giantswarm/giantswarm-repo-manager/tree/main
