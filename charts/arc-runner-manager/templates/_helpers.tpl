{{/*
Expand the name of the chart.
*/}}
{{- define "arc-runner-manager.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "arc-runner-manager.fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "arc-runner-manager.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "arc-runner-manager.labels" -}}
helm.sh/chart: {{ include "arc-runner-manager.chart" . }}
{{ include "arc-runner-manager.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "arc-runner-manager.selectorLabels" -}}
app.kubernetes.io/name: {{ include "arc-runner-manager.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "arc-runner-manager.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "arc-runner-manager.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/* API tokens secret name */}}
{{- define "arc-runner-manager.apiSecretName" -}}
{{- if .Values.api.existingSecret }}
{{- .Values.api.existingSecret }}
{{- else }}
{{- include "arc-runner-manager.fullname" . }}-api-tokens
{{- end }}
{{- end }}