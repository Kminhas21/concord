{{/*
Reusable template snippets: names and the standard app.kubernetes.io labels, so
every object in the chart is named and labelled consistently. Each workload adds
its own app.kubernetes.io/component (coordination | datastore) on top of the
shared selector labels, so the two Services never select each other's Pods.
*/}}

{{- define "concord.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "concord.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "concord.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* Dragonfly resource name — the data store for this unit. */}}
{{- define "concord.dragonflyName" -}}
{{- printf "%s-dragonfly" (include "concord.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Full set of recommended labels, put on every object's metadata. */}}
{{- define "concord.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "concord.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: concord
{{- end -}}

{{/* Stable selector labels (name + instance). Each workload appends its own
     app.kubernetes.io/component to both the selector and the Pod labels. */}}
{{- define "concord.selectorLabels" -}}
app.kubernetes.io/name: {{ include "concord.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
