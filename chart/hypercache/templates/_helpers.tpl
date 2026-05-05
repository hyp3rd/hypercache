{{/*
Standard Helm helpers — name + fullname trimmed to k8s's 63-char
limit, common labels block, headless-service name + per-pod DNS
helper used by the seed-list template in the StatefulSet.
*/}}

{{- define "hypercache.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "hypercache.fullname" -}}
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

{{- define "hypercache.headlessServiceName" -}}
{{- printf "%s-headless" (include "hypercache.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "hypercache.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "hypercache.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "hypercache.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "hypercache.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: cache
{{- end -}}

{{- define "hypercache.selectorLabels" -}}
app.kubernetes.io/name: {{ include "hypercache.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
hypercache.seedList builds the comma-separated `id@addr` value
the dist backend needs to bootstrap a multi-process ring. Every
pod gets the FULL list (including itself); the dist code's
parseSeedSpec drops the self-entry by ID match. This means the
SAME env value applies to every replica, so a StatefulSet (which
only supports a single pod template) can express it.

Format: `<podname>@<podname>.<headless>.<ns>.svc.cluster.local:<port>`
*/}}
{{- define "hypercache.seedList" -}}
{{- $fullname := include "hypercache.fullname" . -}}
{{- $svc := include "hypercache.headlessServiceName" . -}}
{{- $ns := .Release.Namespace -}}
{{- $port := .Values.ports.dist | int -}}
{{- $count := .Values.replicaCount | int -}}
{{- $entries := list -}}
{{- range $i, $_ := until $count -}}
{{- $entry := printf "%s-%d@%s-%d.%s.%s.svc.cluster.local:%d" $fullname $i $fullname $i $svc $ns $port -}}
{{- $entries = append $entries $entry -}}
{{- end -}}
{{- join "," $entries -}}
{{- end -}}
