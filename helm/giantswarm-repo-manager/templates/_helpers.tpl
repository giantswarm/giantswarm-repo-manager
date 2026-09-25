{{/*
Expand the name of the chart.
*/}}
{{- define "giantswarm-repo-manager.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "giantswarm-repo-manager.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart label value. A label value is at most 63 characters and begins and
ends alphanumeric: the cut of a long version (a branch build's
<version>-dev.<branch>.<date>.<time>.<sha>, or the <version>+<digest>
helm-controller installs) can land on any run of ".", "_" (from "+") and "-",
so the whole run is trimmed.
*/}}
{{- define "giantswarm-repo-manager.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimAll "-._" }}
{{- end }}

{{/*
Common labels. The team label is what the Giant Swarm chart validator reads.
*/}}
{{- define "giantswarm-repo-manager.labels" -}}
helm.sh/chart: {{ include "giantswarm-repo-manager.chart" . }}
{{ include "giantswarm-repo-manager.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
application.giantswarm.io/team: {{ index .Chart.Annotations "io.giantswarm.application.team" | quote }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "giantswarm-repo-manager.selectorLabels" -}}
app.kubernetes.io/name: {{ include "giantswarm-repo-manager.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "giantswarm-repo-manager.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "giantswarm-repo-manager.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Address of the Valkey the inventory lives in: inventory.valkeyAddress, else the
subchart's Service (its fullnameOverride, or Helm's <release>-valkey default).
*/}}
{{- define "giantswarm-repo-manager.valkeyAddress" -}}
{{- if .Values.inventory.valkeyAddress }}
{{- .Values.inventory.valkeyAddress }}
{{- else if .Values.valkey.enabled }}
{{- $name := dig "valkey" "fullnameOverride" "" .Values.valkey | default (printf "%s-valkey" .Release.Name) }}
{{- printf "%s.%s.svc:6379" $name .Release.Namespace }}
{{- else }}
{{- fail "inventory.valkeyAddress is required when valkey.enabled is false" }}
{{- end }}
{{- end }}

{{/*
The in-cluster URL muster reaches this server at, without the MCP path.
*/}}
{{- define "giantswarm-repo-manager.serviceURL" -}}
{{- printf "http://%s.%s.svc.cluster.local:%v" (include "giantswarm-repo-manager.fullname" .) .Release.Namespace .Values.service.port }}
{{- end }}

{{/*
The OAuth base URL — the resource of the protected-resource metadata:
oauth.baseURL, else the in-cluster Service URL.
*/}}
{{- define "giantswarm-repo-manager.oauthBaseURL" -}}
{{- .Values.oauth.baseURL | default (include "giantswarm-repo-manager.serviceURL" .) }}
{{- end }}
