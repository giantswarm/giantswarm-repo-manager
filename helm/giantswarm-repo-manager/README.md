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
