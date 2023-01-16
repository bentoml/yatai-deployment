{{/*
Expand the name of the chart.
*/}}
{{- define "yatai-deployment.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "yatai-deployment.fullname" -}}
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

{{- define "yatai-deployment.envname" -}}
yatai-deployment-env
{{- end }}

{{- define "yatai-deployment.shared-envname" -}}
yatai-deployment-shared-env
{{- end }}

{{- define "yatai-deployment.yatai-common-envname" -}}
yatai-common-env
{{- end }}

{{- define "yatai-deployment.yatai-rolename-in-yatai-system-namespace" -}}
yatai-role-for-yatai-deployment
{{- end }}

{{- define "yatai-deployment.yatai-image-builder-with-bento-deployment-rolename" -}}
yatai-image-builder-with-bento-deployment
{{- end }}

{{- define "yatai-deployment.yatai-with-bento-deployment-rolename" -}}
yatai-with-bento-deployment
{{- end }}

{{- define "yatai-deployment.yatai-with-yatai-deployment-rolename" -}}
yatai-with-yatai-deployment
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "yatai-deployment.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "yatai-deployment.labels" -}}
helm.sh/chart: {{ include "yatai-deployment.chart" . }}
{{ include "yatai-deployment.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "yatai-deployment.selectorLabels" -}}
app.kubernetes.io/name: {{ include "yatai-deployment.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "yatai-deployment.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "yatai-deployment.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "yatai-deployment.serviceAccountNameInYataiSystemNamespace" -}}
{{- printf "%s-in-yatai-system" (include "yatai-deployment.serviceAccountName" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "yatai-deployment.serviceAccountNameWithBentoDeployment" -}}
{{- printf "%s-with-bento-deployment" (include "yatai-deployment.serviceAccountName" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Generate k8s robot token
*/}}
{{- define "yatai-deployment.yataiApiToken" -}}
    {{- $secretObj := (lookup "v1" "Secret" .Release.Namespace (include "yatai-deployment.envname" .)) | default (lookup "v1" "Secret" .Release.Namespace "env") | default dict }}
    {{- $secretData := (get $secretObj "data") | default dict }}
    {{- (get $secretData "YATAI_API_TOKEN") | default (randAlphaNum 16 | nospace | b64enc) | b64dec }}
{{- end -}}
