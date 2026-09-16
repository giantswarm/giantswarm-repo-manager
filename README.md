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
exposes it, with the repository lifecycle, as MCP tools with the prefix `giantswarm-repo-manager`:

| Tool | Purpose |
|---|---|
| `list_repositories` | the inventory by scope (`mine`, `team`, `unassigned`, `all`) and filters |
| `get_repository` | one record: declaration, reality, set-up state, orphan score |
| `validate_repository` | the dry run of a declaration: rendered entry, implied template, name checks |
| `create_repository`, `update_repository`, `transfer_repository`, `set_lifecycle` | a team-file change as the person (`mode: commit`, the only write mode) |
| `approve_change` | the team's review of such a change |
| `reconcile_repository` | one run of the set-up steps for a repository |

Every write is a pull request to the team file opened as the person, obtained through muster's token
exchange broker (the person's GitHub grant). `mode: apply` is refused: a repository without its
declaration is exactly the drift the reconciler reports.

## Status

The repository is bootstrapped: the module, the image, the chart with its Valkey dependency and the
generated CI. The binary serves the health endpoints the chart probes; the MCP server, the broker client
and the tools follow.

## Deploying

The chart is published to the Giant Swarm catalog as `giantswarm-repo-manager`; see
[`helm/giantswarm-repo-manager`](helm/giantswarm-repo-manager/README.md) for its values.

## Development

See [docs/development.md](docs/development.md).
