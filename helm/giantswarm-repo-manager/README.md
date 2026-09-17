# giantswarm-repo-manager

Giant Swarm's repository set-up service — an MCP server behind muster that lists, validates, creates and reconciles the giantswarm org's repositories as the person, with its own Valkey for the inventory

The chart deploys the server as one Deployment behind a ClusterIP Service and
brings the inventory's store with it: the Giant Swarm `valkey` app as a
subchart (`valkey.*`, its upstream values under `valkey.valkey`). The server
learns the store's address through `VALKEY_ADDR` — the subchart's Service by
default, `inventory.valkeyAddress` for a Valkey outside the release.

Valkey may start after the manager — its volume binding late on a fresh
install, a restart, an upgrade — and the manager tolerates it: the server
serves at once and connects to the store in the background, retrying with
backoff for `inventory.connectTimeout` (5m by default; `"0"` waits for ever)
before it gives up and exits. Until the store answers, liveness passes and
readiness fails with the reason (`/readyz` answers 503 `not ready: inventory
unavailable: …`), so the Deployment shows the pod unready but never restarts
it; the inventory tools answer `inventory unavailable`, the identity tools
(`get_info`, `validate_repository` dry run) keep working. A Valkey lost at
runtime reads the same way — readiness fails, the pod stays up — and the
connection re-establishes itself on the next command once Valkey is back; the
sweep schedule starts on a connected store.

Giant Swarm-specific: it manages the giantswarm org's repositories and ships as
its own app, not as a component of the `agent-platform` meta chart.

## Requirements

| Repository | Name | Version |
|------------|------|---------|
| oci://gsoci.azurecr.io/charts/giantswarm | valkey | 0.1.4 |

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| global | object | `{}` | Platform-wide values an umbrella chart shares with every component and Helm forwards here; this chart reads none of them. |
| replicaCount | int | `1` | Number of replicas. The server holds its state in Valkey; more than one is fine. |
| image.registry | string | `"gsoci.azurecr.io"` | Image registry. |
| image.repository | string | `"giantswarm/giantswarm-repo-manager"` | Image repository. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.tag | string | `""` | Image tag. Defaults to the chart appVersion. |
| imagePullSecrets | list | `[]` | Image pull secrets. |
| nameOverride | string | `""` | Override the chart name. |
| fullnameOverride | string | `""` | Override the fully qualified app name. |
| valkey | object | `{"ciliumNetworkPolicy":{"enabled":false},"enabled":true,"valkey":{"deploymentStrategy":"Recreate","fullnameOverride":"giantswarm-repo-manager-valkey","metrics":{"exporter":{"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}}}},"podAnnotations":{"karpenter.sh/do-not-disrupt":"true"},"podSecurityContext":{"fsGroup":1000,"runAsGroup":1000,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}},"replicaCount":1,"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}}},"vpa":{"enabled":false}}` | The inventory store: the Giant Swarm `valkey` app as a subchart (its values live under `valkey.valkey`, the upstream chart's). One replica on an RWO volume with `Recreate`, so a rollout does not deadlock on the volume; the store is a cache — every record is rebuilt from the team files and GitHub — so the restart gap costs nothing. Set `valkey.enabled: false` and `inventory.valkeyAddress` to use a Valkey outside the release. |
| inventory.valkeyAddress | string | `""` | Address (`host:port`) of the Valkey the inventory lives in, passed to the server as `VALKEY_ADDR`. Empty derives the subchart's Service, `<valkey.valkey.fullnameOverride>.<namespace>.svc:6379`; required when `valkey.enabled` is false. |
| inventory.connectTimeout | string | `"5m"` | How long the server waits for the store at start (a Go duration), retrying with backoff, before it gives up and exits; it serves meanwhile, liveness passing and readiness failing until the store answers, so a Valkey that starts after the manager — a volume binding late, a restart — costs no container restart within the window. `"0"` waits for ever. |
| inventory.org | string | `"giantswarm"` | The GitHub organization the inventory covers. |
| inventory.sweep.interval | string | `"24h"` | Full sweep over the org every interval (a Go duration); `"0"` turns the schedule off. The first sweep runs at start when the last one is older than the interval. |
| inventory.sweep.engineChecks | bool | `true` | Run the engine's set-up checks in read mode for every accepted declaration during a sweep (REST as the App, about ten calls per repository). A `refresh_repository` always runs them. |
| inventory.sweep.concurrency | int | `4` | Parallel engine reads during a sweep. |
| inventory.sweep.graphqlBudgetFloor | int | `0` | Stop a sweep cleanly when the GraphQL budget's remaining points fall below this; 0 never stops. |
| inventory.sweep.teams | list | `["team-bumblebee","team-planeteers"]` | GitHub team slugs whose members may start a sweep on demand with the tool `sweep_inventory` (membership checked on GitHub as the caller): the teams that own the manager and the reconciler. Passed as `SWEEP_TEAMS`. |
| inventory.reconciler.pollInterval | string | `"5m"` | Read the reconciler workflow's completed runs from GitHub as the inventory App every interval (a Go duration) and store each run's `reconcile-<repository>` artifact as the repository's `setup.lastRun`; every 30 s while a Reconcile now (`reconcile_repository`) is pending. `"0"` turns the poller off. Nothing reaches the server from the workflow: the App needs Actions read on `teamFiles.repository`. |
| inventory.reconciler.workflow | string | `"reconcile-repositories.yaml"` | The reconciler's workflow file in `teamFiles.repository`: the one `reconcile_repository` dispatches and the poller reads the runs of. |
| inventory.orphan.staleDays | int | `180` | Stale period of the orphan score in days (no commit by a person, no Renovate activity within it). |
| teamFiles.repository | string | `"giantswarm/github"` | The repository that holds the team files (`repositories/<team>.yaml`, the desired state) and the policy files (`repository-setup/<team>.yaml`); every write is a pull request against it, opened as the caller. |
| teamFiles.ref | string | `"main"` | The branch the files are read from and pull requests target. |
| reviews.gatewayURL | string | `""` | klaus-gateway's base URL as reached from the pod (for example `http://klaus-gateway.agent-platform.svc:8080`): lifecycle and transfer asks go to `POST /reviews`, notices and completion messages to `POST /notices`, authenticated with this pod's projected ServiceAccount token for `audience`. Empty leaves the asks undelivered; the tools say so and approving on GitHub stays equivalent. |
| reviews.audience | string | `"klaus-gateway"` | The audience of the projected token — the gateway's `reviews.audience`; this ServiceAccount must be in its `reviews.allowedCallers`. |
| reviews.channels | object | `{}` | Slack channel IDs by the channel name a team's policy file carries (`slackChannel`): the gateway takes IDs. A policy file that carries the ID itself needs no entry. |
| mcp.path | string | `"/mcp"` | MCP streamable-HTTP endpoint path. |
| oauth.enabled | bool | `false` | Require a GitHub user token as the bearer of every MCP request, verified with `GET /user`; the server then acts as that person. Behind muster the token is the person's own: muster runs the consent for the GitHub App `giantswarm-repo-manager` once (the MCPServer's `authorizationServer` pin under `muster.mcpServer.auth`), stores and refreshes the user token and puts it on every call. Off: anonymous — no caller, nothing acts as a person, the tools say so; only for a server nothing but a trusted proxy can reach. |
| oauth.baseURL | string | `""` | URL muster reaches this server at, without the MCP path: the resource of its OAuth protected-resource metadata. Empty derives the in-cluster Service URL, `http://<fullname>.<namespace>.svc.cluster.local:<service.port>`. |
| muster.mcpServer.enabled | bool | `false` | Register this server with muster by rendering an `mcpservers.muster.giantswarm.io` CR in the release namespace. Tools then appear as `x_<name>_<tool>`. |
| muster.mcpServer.name | string | `"giantswarm-repo-manager"` | MCPServer CR name (drives the tool prefix). |
| muster.mcpServer.autoStart | bool | `true` | Start the server connection when muster initializes. |
| muster.mcpServer.description | string | `"Giant Swarm's repository set-up service — the giantswarm org's repositories, every write as the person"` | Human-readable description shown by muster. |
| muster.mcpServer.labels | object | `{}` | Extra labels on the MCPServer CR. |
| muster.mcpServer.auth | object | `{"authorizationServer":{"authorizationEndpoint":"https://github.com/login/oauth/authorize","clientCredentialsSecretRef":{"name":"giantswarm-repo-manager-oauth-client","namespace":""},"expectedIssuer":"https://github.com/login/oauth","grantScope":"subject","issuer":"https://github.com/apps/giantswarm-repo-manager","tokenEndpoint":"https://github.com/login/oauth/access_token"}}` | How muster authenticates to this server; rendered only with `oauth.enabled`: `auth.type: oauth` with the GitHub App `giantswarm-repo-manager` pinned as the authorization server — the pattern of the `github` and `pro` servers on the platform. GitHub publishes no discovery document, so the endpoints are named; the App's client credentials come from a Secret; no `scopes` (the App's permissions are the App's). muster runs the consent once per person and puts their user token on every call. |
| muster.mcpServer.auth.authorizationServer.issuer | string | `"https://github.com/apps/giantswarm-repo-manager"` | The issuer identity the person's grant is filed under: the App's own, so the login App's GitHub grant stays separate. |
| muster.mcpServer.auth.authorizationServer.expectedIssuer | string | `"https://github.com/login/oauth"` | The issuer the authorization server puts in the RFC 9207 `iss` parameter of its authorization response — GitHub's is `https://github.com/login/oauth` for every App, while `issuer` stays the App's own identity the grant is filed under (muster#1277). Empty omits the field. |
| muster.mcpServer.auth.authorizationServer.authorizationEndpoint | string | `"https://github.com/login/oauth/authorize"` | GitHub's authorize endpoint. |
| muster.mcpServer.auth.authorizationServer.tokenEndpoint | string | `"https://github.com/login/oauth/access_token"` | GitHub's token endpoint. |
| muster.mcpServer.auth.authorizationServer.clientCredentialsSecretRef.name | string | `"giantswarm-repo-manager-oauth-client"` | Secret with the App's OAuth client under `client-id` and `client-secret`. |
| muster.mcpServer.auth.authorizationServer.clientCredentialsSecretRef.namespace | string | `""` | Namespace of that Secret; empty is the release namespace. |
| muster.mcpServer.auth.authorizationServer.grantScope | string | `"subject"` | `subject`: the grant belongs to the person, not to one login session — every session of theirs (the portal, an agent, a Slack click) carries the same token. |
| githubApp.appID | int | `0` | Id of the read-only GitHub App `giantswarm-repo-manager-inventory`, the one identity of the unattended reads: the org sweep, the engine's checks in read mode and the name check of a dry run run as its installation, on its own rate budget. Its permissions, all read: Administration, Contents, Pull requests, Issues, Commit statuses (the `ci/circleci:` contexts on the default branch head), Metadata, Organization members. 0 leaves the unattended reads unconfigured — nothing stands in for the App; the tools that need it say so. |
| githubApp.installationID | int | `0` | The inventory App's installation id on the org. |
| githubApp.existingSecret | string | `""` | Existing Secret with the inventory App's PEM private key under `private-key`. |
| githubApp.apiURL | string | `""` | GitHub API base URL; empty is api.github.com. |
| serviceAccount.create | bool | `true` | Create a ServiceAccount. |
| serviceAccount.annotations | object | `{}` | Annotations on the ServiceAccount. |
| serviceAccount.name | string | `""` | ServiceAccount name (generated when empty). |
| podAnnotations | object | `{}` | Annotations on the pod. |
| podLabels | object | `{}` | Labels on the pod. |
| podSecurityContext | object | `{"fsGroup":1000,"runAsGroup":1000,"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod security context. |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsGroup":1000,"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}}` | Container security context. |
| service.type | string | `"ClusterIP"` | Service type. |
| service.port | int | `8080` | Service port (the container listens on 8080). |
| resources | object | `{"limits":{"cpu":"500m","memory":"256Mi"},"requests":{"cpu":"50m","memory":"64Mi"}}` | Container resources. |
| extraArgs | list | `[]` | Extra container arguments. |
| extraEnv | list | `[]` | Extra environment variables. |
| nodeSelector | object | `{}` | Node selector. |
| tolerations | list | `[]` | Tolerations. |
| affinity | object | `{}` | Affinity. |
