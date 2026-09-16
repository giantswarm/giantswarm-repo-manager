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
Chart label value.
*/}}
{{- define "giantswarm-repo-manager.chart" -}}
{{- /* A label value ends alphanumeric: the 63-char cut of a branch build's
       version (0.x.y-dev.<branch>.<timestamp>.<sha>) can land on a "." too. */}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" | trimSuffix "." }}
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
OAuth: the platform identity contract (global.identity.*) is the default of
every oauth.* value.
*/}}
{{- define "giantswarm-repo-manager.identity" -}}
{{- dig "identity" (dict) .Values.global | toJson }}
{{- end }}

{{- define "giantswarm-repo-manager.dexIssuerURL" -}}
{{- .Values.oauth.dex.issuerURL | default (dig "identity" "issuerUrl" "" .Values.global) }}
{{- end }}

{{- define "giantswarm-repo-manager.dexClientID" -}}
{{- .Values.oauth.dex.clientID | default (dig "identity" "clientId" "" .Values.global) }}
{{- end }}

{{/*
The Secret the Dex client secret is read from: oauth.existingSecret, else the
platform's, else the one this chart renders.
*/}}
{{- define "giantswarm-repo-manager.oauthExistingSecret" -}}
{{- .Values.oauth.existingSecret | default (dig "identity" "existingSecret" "" .Values.global) }}
{{- end }}

{{- define "giantswarm-repo-manager.oauthSecretName" -}}
{{- include "giantswarm-repo-manager.oauthExistingSecret" . | default (printf "%s-oauth" (include "giantswarm-repo-manager.fullname" .)) }}
{{- end }}

{{- define "giantswarm-repo-manager.dexCASecretName" -}}
{{- .Values.oauth.dex.caSecret.name | default (dig "identity" "ca" "secretName" "" .Values.global) }}
{{- end }}

{{- define "giantswarm-repo-manager.dexCASecretKey" -}}
{{- if .Values.oauth.dex.caSecret.name }}{{ .Values.oauth.dex.caSecret.key }}{{ else }}{{ dig "identity" "ca" "key" .Values.oauth.dex.caSecret.key .Values.global }}{{ end }}
{{- end }}

{{/*
The OAuth base URL: oauth.baseURL, else https://<fullname>.<global.domain>.
*/}}
{{- define "giantswarm-repo-manager.oauthBaseURL" -}}
{{- if .Values.oauth.baseURL }}
{{- .Values.oauth.baseURL }}
{{- else if dig "domain" "" .Values.global }}
{{- printf "https://%s.%s" (include "giantswarm-repo-manager.fullname" .) (dig "domain" "" .Values.global) }}
{{- else }}
{{- fail "oauth.baseURL is required with oauth.enabled when global.domain is not set" }}
{{- end }}
{{- end }}

{{/*
Trusted bearer audiences: oauth.trustedAudiences (else the platform client)
plus the MCPServer's required audiences, comma-separated without duplicates.
*/}}
{{- define "giantswarm-repo-manager.trustedAudiences" -}}
{{- $auds := .Values.oauth.trustedAudiences }}
{{- if not $auds }}{{- $auds = list (include "giantswarm-repo-manager.dexClientID" .) }}{{- end }}
{{- concat $auds .Values.muster.mcpServer.auth.requiredAudiences | uniq | compact | join "," }}
{{- end }}

{{/*
Slack channel IDs by policy-file channel name, as name=ID pairs.
*/}}
{{- define "giantswarm-repo-manager.reviewChannels" -}}
{{- $pairs := list }}
{{- range $name, $id := .Values.reviews.channels }}{{- $pairs = append $pairs (printf "%s=%s" $name $id) }}{{- end }}
{{- join "," $pairs }}
{{- end }}
