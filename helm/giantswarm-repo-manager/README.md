# giantswarm-repo-manager

Giant Swarm's repository set-up service — an MCP server behind muster that lists, validates, creates and reconciles the giantswarm org's repositories as the person, with its own Valkey for the inventory

The chart deploys the server as one Deployment behind a ClusterIP Service and
brings the inventory's store with it: the Giant Swarm `valkey` app as a
subchart (`valkey.*`, its upstream values under `valkey.valkey`). The server
learns the store's address through `VALKEY_ADDR` — the subchart's Service by
default, `inventory.valkeyAddress` for a Valkey outside the release.

Giant Swarm-specific: it manages the giantswarm org's repositories and ships as
its own app, not as a component of the `agent-platform` meta chart.

## Requirements

| Repository | Name | Version |
|------------|------|---------|
| oci://gsoci.azurecr.io/charts/giantswarm | valkey | 0.1.4 |

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| global | object | `{}` | Platform-wide values an umbrella chart shares with every component and Helm forwards here. `oauth.*` reads the identity contract as its defaults: `global.identity.issuerUrl`, `global.identity.clientId`, `global.identity.existingSecret`, `global.identity.ca.secretName` / `.key`, and `global.domain` for the OAuth base URL. Empty here; a standalone install sets `oauth.*` directly. |
| replicaCount | int | `1` | Number of replicas. The server holds its state in Valkey; more than one is fine. |
| image.registry | string | `"gsoci.azurecr.io"` | Image registry. |
| image.repository | string | `"giantswarm/giantswarm-repo-manager"` | Image repository. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.tag | string | `""` | Image tag. Defaults to the chart appVersion. |
| imagePullSecrets | list | `[]` | Image pull secrets. |
| nameOverride | string | `""` | Override the chart name. |
| fullnameOverride | string | `""` | Override the fully qualified app name. |
| valkey | object | `{"ciliumNetworkPolicy":{"enabled":false},"enabled":true,"valkey":{"deploymentStrategy":"Recreate","fullnameOverride":"giantswarm-repo-manager-valkey","podAnnotations":{"karpenter.sh/do-not-disrupt":"true"},"replicaCount":1},"vpa":{"enabled":false}}` | The inventory store: the Giant Swarm `valkey` app as a subchart (its values live under `valkey.valkey`, the upstream chart's). One replica on an RWO volume with `Recreate`, so a rollout does not deadlock on the volume; the store is a cache — every record is rebuilt from the team files and GitHub — so the restart gap costs nothing. Set `valkey.enabled: false` and `inventory.valkeyAddress` to use a Valkey outside the release. |
| inventory.valkeyAddress | string | `""` | Address (`host:port`) of the Valkey the inventory lives in, passed to the server as `VALKEY_ADDR`. Empty derives the subchart's Service, `<valkey.valkey.fullnameOverride>.<namespace>.svc:6379`; required when `valkey.enabled` is false. |
| mcp.path | string | `"/mcp"` | MCP streamable-HTTP endpoint path. |
| oauth.enabled | bool | `false` | Make the server an OAuth 2.1 resource server (mcp-oauth): the MCP endpoint requires a bearer token the platform Dex issued, and every call carries the caller's identity and id_token — the subject token of the broker exchange that releases the caller's GitHub grant. muster forwards the session's id_token byte-identical (MCPServer `auth.forwardToken`, rendered below); it is validated against Dex's JWKS when its audience is in `trustedAudiences`. Off: anonymous — no caller, no grant, tools report so; only for a server nothing but a trusted proxy can reach. |
| oauth.baseURL | string | `""` | Public base URL of this server: the issuer of its own OAuth metadata (https, or http on loopback). Empty derives `https://<fullname>.<global.domain>` when `global.domain` is set. |
| oauth.dex.issuerURL | string | `""` | Dex issuer URL. Empty falls back to `global.identity.issuerUrl`. |
| oauth.dex.clientID | string | `""` | Dex OAuth client ID this server is registered as. Empty falls back to `global.identity.clientId`. |
| oauth.dex.clientSecret | string | `""` | Dex OAuth client secret (prefer `oauth.existingSecret`). |
| oauth.dex.allowPrivateURLs | bool | `false` | Let the issuer resolve to a private or loopback address (an in-cluster Dex). |
| oauth.dex.caSecret | object | `{"key":"ca.crt","name":""}` | Secret with the CA of a Dex that serves a private certificate; mounted and passed as `DEX_CA_FILE`. Empty name falls back to `global.identity.ca.secretName` / `global.identity.ca.key`. |
| oauth.existingSecret | string | `""` | Existing Secret with the Dex client secret under `dex-client-secret`. Empty falls back to `global.identity.existingSecret`; without that, the chart renders a Secret from `oauth.dex.clientSecret`. |
| oauth.trustedAudiences | list | `[]` | OAuth client IDs whose Dex id_tokens are accepted as bearer tokens (SSO token forwarding). Empty falls back to `[global.identity.clientId]`. The server trusts the union of this list and `muster.mcpServer.auth.requiredAudiences`. |
| oauth.sso.allowPrivateIPs | bool | `false` | Let the IdP's JWKS endpoint resolve to a private address when validating forwarded tokens (an in-cluster Dex). |
| oauth.allowPublicClientRegistration | bool | `false` | Accept unauthenticated dynamic client registration (labs only). |
| muster.url | string | `""` | muster's base URL as reached from the pod (for example `http://muster.muster.svc:8090`): the broker exchange goes to `<url>/oauth/token`. Empty leaves the broker client off; `get_info` reports it. |
| muster.mcpServer.enabled | bool | `false` | Register this server with muster by rendering an `mcpservers.muster.giantswarm.io` CR in the release namespace. Tools then appear as `x_<name>_<tool>`. |
| muster.mcpServer.name | string | `"giantswarm-repo-manager"` | MCPServer CR name (drives the tool prefix). |
| muster.mcpServer.autoStart | bool | `true` | Start the server connection when muster initializes. |
| muster.mcpServer.description | string | `"Giant Swarm's repository set-up service — the giantswarm org's repositories, every write as the person"` | Human-readable description shown by muster. |
| muster.mcpServer.labels | object | `{}` | Extra labels on the MCPServer CR. |
| muster.mcpServer.auth | object | `{"forwardToken":true,"requiredAudiences":[]}` | How muster authenticates to this server; rendered only with `oauth.enabled`. `forwardToken` makes muster forward the session's Dex id_token byte-identical. `requiredAudiences` are extra audiences that token must carry (the Dex cross-client audience of the platform, e.g. `dex-k8s-authenticator`); they are trusted as bearer audiences by construction. |
| broker.clientID | string | `"giantswarm-repo-manager"` | This server's client id at muster's token-exchange broker (`tokenExchangeBroker.brokerClients.<id>`, allowed audience `github`). |
| broker.existingSecret | string | `""` | Existing Secret with the broker client credentials under `client-secret` (and optionally `client-id`) — the same Secret muster's `brokerClients.<id>.clientCredentialsSecretRef` reads. Empty leaves the broker client off. |
| broker.audience | string | `"github"` | Broker target that releases the person's GitHub grant (`tokenExchangeBroker.targets.<audience>.grantIssuer`). |
| githubApp.appID | int | `0` | The App used for unattended, read-only inventory reads (the giantswarm-align-files App) and its installation on the org. 0 leaves the App identity off. |
| githubApp.installationID | int | `0` |  |
| githubApp.existingSecret | string | `""` | Existing Secret with the App's PEM private key under `private-key`. |
| githubApp.apiURL | string | `""` | GitHub API base URL; empty is api.github.com. |
| circleci.existingSecret | string | `""` | Existing Secret with the CircleCI API token (read scope) under `token`, passed as `CIRCLECI_API_TOKEN`. Empty leaves it unset. |
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
