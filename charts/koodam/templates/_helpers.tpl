{{/* The name prefix for all objects. */}}
{{- define "koodam.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "koodam.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "koodam.selectorLabels" -}}
control-plane: controller-manager
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "koodam.serviceAccountName" -}}
{{- default (printf "%s-controller-manager" (include "koodam.fullname" .)) .Values.serviceAccount.name -}}
{{- end -}}

{{/*
The rules of the first object in a file under files/. "make helm-sync" copies these files
from config/rbac, so the chart has the same rules as the generated RBAC. Do not edit them by hand.
*/}}
{{- define "koodam.rules" -}}
{{- $rules := list -}}
{{- range regexSplit "(?m)^---\\s*$" (.ctx.Files.Get .file) -1 -}}
{{- $doc := fromYaml . -}}
{{- if and (not $rules) $doc.rules -}}{{- $rules = $doc.rules -}}{{- end -}}
{{- end -}}
{{- required (printf "%s has no rules" .file) $rules | toYaml -}}
{{- end -}}

{{/* The subject for the controller ServiceAccount. */}}
{{- define "koodam.subject" -}}
- kind: ServiceAccount
  name: {{ include "koodam.serviceAccountName" . }}
  namespace: {{ .Release.Namespace }}
{{- end -}}
