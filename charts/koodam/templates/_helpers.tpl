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

{{/* The controller gateway address. In the bundled mode, it is the bundled LiteLLM Service. */}}
{{- define "koodam.gatewayUrl" -}}
{{- if eq .Values.gateway.mode "bundled" -}}
{{- printf "http://%s-litellm.%s.svc:4000" (include "koodam.fullname" .) .Release.Namespace -}}
{{- else -}}
{{- required "gateway.url is necessary in the existing mode." .Values.gateway.url -}}
{{- end -}}
{{- end -}}

{{/* The name of the Secret with the PostgreSQL password. */}}
{{- define "koodam.postgresSecret" -}}
{{- default (printf "%s-postgres" (include "koodam.fullname" .)) .Values.gateway.bundled.postgres.passwordSecret.name -}}
{{- end -}}

{{/* The labels of a bundled gateway component. The argument is (list $ "<component>"). */}}
{{- define "koodam.gatewaySelector" -}}
app.kubernetes.io/name: {{ (index . 0).Chart.Name }}
app.kubernetes.io/instance: {{ (index . 0).Release.Name }}
app.kubernetes.io/component: {{ index . 1 }}
{{- end -}}

{{/*
The default workspace image. If the tag is empty, the tag is the chart appVersion.
If the tag starts with "sha256:", the tag is a digest. A null workspace or workspace.image
gives the "required" error.
*/}}
{{- define "koodam.workspaceImage" -}}
{{- $image := get (.Values.workspace | default dict) "image" | default dict -}}
{{- $repository := required "workspace.image.repository is necessary." (get $image "repository") -}}
{{- $tag := get $image "tag" | default .Chart.AppVersion -}}
{{- if hasPrefix "sha256:" $tag -}}
{{- printf "%s@%s" $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end -}}

{{/*
The public internet peers of a NetworkPolicy egress rule. They exclude the private, shared,
link-local, loopback and NAT64 ranges. The argument is the list of more blocked CIDRs.
*/}}
{{- define "koodam.publicPeers" -}}
{{- $blocked := . | default list -}}
- ipBlock:
    cidr: 0.0.0.0/0
    except:
    - 0.0.0.0/8
    - 10.0.0.0/8
    - 100.64.0.0/10
    - 127.0.0.0/8
    - 169.254.0.0/16
    - 172.16.0.0/12
    - 192.168.0.0/16
    {{- range $blocked }}
    {{- if not (contains ":" .) }}
    - {{ ternary . (printf "%s/32" .) (contains "/" .) }}
    {{- end }}
    {{- end }}
- ipBlock:
    cidr: ::/0
    except:
    - ::1/128
    - 64:ff9b::/96
    - fc00::/7
    - fe80::/10
    {{- range $blocked }}
    {{- if contains ":" . }}
    - {{ ternary . (printf "%s/128" .) (contains "/" .) }}
    {{- end }}
    {{- end }}
{{- end -}}
