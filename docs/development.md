# Developing on giantswarm-repo-manager

## Build and test

```sh
make test               # go test ./... (unit tests and internal/e2e on an in-process Valkey)
make test-e2e           # internal/e2e alone; VALKEY_ADDR=host:port runs it against a real store
make scenario-test      # muster's scenario harness on tests/scenarios (needs `muster` on PATH)
make build-linux-amd64  # the binary the Dockerfile expects
make docker-build       # a local image, giantswarm-repo-manager:dev
```

`internal/e2e` starts the server in-process behind fakes — a Dex (TLS, RS256 tokens), muster's token
broker, GitHub — and calls it through a real MCP client with the forwarded `id_token` as the bearer.
`tests/scenarios` are muster scenarios (`muster test --config tests/scenarios`); the CI job
`scenario-test` (`.circleci/custom.yml`) runs both against a Valkey service container.

## Running locally

Every flag has an environment variable (`giantswarm-repo-manager -h`). Without OAuth the server answers
anonymously — `get_info` reports no caller and no grant; with `--enable-oauth` it needs a Dex
(`--dex-issuer-url`, `--dex-client-id`, `--dex-client-secret`, `--oauth-trusted-audiences`) and a public
base URL. The broker client (`--muster-url`, `--broker-client-id`, `--broker-client-secret`), the App
(`--github-app-*`) and the store (`--valkey-addr`) are each optional and reported by `get_info`.

The image only assembles the runtime around the binary CircleCI's `go-build` produces; it is not a
multi-stage build.

## The chart

```sh
make helm-deps      # helm dependency build — resolves Chart.lock, never updates it
make helm-lint
make helm-template
make helm-schema    # regenerate values.schema.json after a values.yaml change
make helm-docs      # regenerate helm/giantswarm-repo-manager/README.md
```

The Valkey subchart is pinned in `Chart.yaml` and mirrored in `Chart.lock`; `helm/*/charts/` is not
committed. Bumping the pin means editing both (`helm dependency update` rewrites the lock).

The pre-commit hooks (`pre-commit run --all-files`) run the same schema, docs and lint checks CI does.

## Release

Every merge to `main` is tagged by the auto-release workflow; CircleCI builds the tag and pushes the
image to `gsoci.azurecr.io/giantswarm/giantswarm-repo-manager` and the chart to the Giant Swarm catalog.
