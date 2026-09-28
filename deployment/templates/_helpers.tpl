{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "dcgm-exporter.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "dcgm-exporter.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}


{{/*
Allow the release namespace to be overridden for multi-namespace deployments in combined charts
*/}}
{{- define "dcgm-exporter.namespace" -}}
  {{- if .Values.namespaceOverride -}}
    {{- .Values.namespaceOverride -}}
  {{- else -}}
    {{- .Release.Namespace -}}
  {{- end -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "dcgm-exporter.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "dcgm-exporter.labels" -}}
helm.sh/chart: {{ include "dcgm-exporter.chart" . }}
{{ include "dcgm-exporter.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels
*/}}
{{- define "dcgm-exporter.selectorLabels" -}}
app.kubernetes.io/name: {{ include "dcgm-exporter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: {{ include "dcgm-exporter.name" . }}
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "dcgm-exporter.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
    {{ default (include "dcgm-exporter.fullname" .) .Values.serviceAccount.name }}
{{- else -}}
    {{ default "default" .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}


{{/*
Determine whether the service account token should be mounted.
*/}}
{{- define "dcgm-exporter.automountServiceAccountToken" -}}
{{- ternary (or (and (or .Values.kubernetes.enablePodLabels .Values.kubernetes.enablePodUID) .Values.kubernetes.rbac.create) .Values.kubernetesDRA.enabled) .Values.serviceAccount.automountServiceAccountToken (kindIs "invalid" .Values.serviceAccount.automountServiceAccountToken) -}}
{{- end -}}


{{/*
Create the name of the tls secret to use
*/}}
{{- define "dcgm-exporter.tlsCertsSecretName" -}}
{{- if .Values.tlsServerConfig.existingSecret -}}
    {{- printf "%s" (tpl .Values.tlsServerConfig.existingSecret $) -}}
{{- else -}}
    {{ printf "%s-tls" (include "dcgm-exporter.fullname" .) }}
{{- end -}}
{{- end -}}


{{/*
Create the name of the web-config configmap name to use
*/}}
{{- define "dcgm-exporter.webConfigConfigMap" -}}
  {{ printf "%s-web-config.yml" (include "dcgm-exporter.fullname" .) }}
{{- end -}}

{{/*
Create the name of the dcgm-exporter YAML config configmap to use
*/}}
{{- define "dcgm-exporter.configConfigMap" -}}
{{- if .Values.config.name -}}
  {{- .Values.config.name -}}
{{- else if .Values.config.create -}}
  {{- printf "%s-config" (include "dcgm-exporter.fullname" .) -}}
{{- else -}}
  {{- fail "config.name must be set when config.create is false" -}}
{{- end -}}
{{- end -}}
