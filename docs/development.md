# Developing on giantswarm-repo-manager

## Build and test

```sh
make test               # go test ./...
make build-linux-amd64  # the binary the Dockerfile expects
make docker-build       # a local image, giantswarm-repo-manager:dev
```

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
