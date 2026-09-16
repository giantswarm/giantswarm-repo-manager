# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).



## [Unreleased]

### Added

- Repository bootstrap: the Go module, a health-serving binary, the Dockerfile and the Helm chart with its Valkey dependency.
- The MCP server behind muster: an OAuth 2.1 resource server (mcp-oauth) validating the Dex `id_token` muster forwards, a muster token-exchange broker client releasing the caller's GitHub grant (audience `github`), the giantswarm-align-files App identity for unattended reads, the Valkey inventory store and devctl's `pkg/reposetup` engine.
- `get_info`: the identity chain of a call (caller, grant proven with `GET /user`, App, inventory, engine, write modes).
- The write-tool framework: every write takes `dryRun` and `mode`; `mode: apply` is refused before any tool runs, `commit` is the only mode. `create_repository` with the engine's dry run.
- Chart values for OAuth (with the platform's `global.identity.*` defaults), the muster MCPServer registration, the broker client, the GitHub App and the CircleCI token, all through Secret references.
- Tests: the identity chain against fakes (`internal/e2e`), muster's scenario harness with a mocked GitHub (`tests/scenarios`), both in the `scenario-test` CI job with a Valkey service container.



[Unreleased]: https://github.com/giantswarm/giantswarm-repo-manager/tree/main
