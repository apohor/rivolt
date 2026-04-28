{{/*
Expand the name of the chart.
*/}}
{{- define "rivolt.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully-qualified app name: <release>-<chart>, truncated to 63 chars.
*/}}
{{- define "rivolt.fullname" -}}
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

{{- define "rivolt.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "rivolt.labels" -}}
helm.sh/chart: {{ include "rivolt.chart" . }}
{{ include "rivolt.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "rivolt.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rivolt.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "rivolt.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "rivolt.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the chart-managed app Secret. When `secrets.existingSecret`
is set we never create or template our own — the deployment refs
the user-supplied object directly.
*/}}
{{- define "rivolt.appSecretName" -}}
{{- if .Values.secrets.existingSecret -}}
{{- .Values.secrets.existingSecret -}}
{{- else -}}
{{- printf "%s-app" (include "rivolt.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
CNPG Cluster name. Defaults to `<release>-pg` so multi-app
clusters don't collide on a single Postgres.
*/}}
{{- define "rivolt.cnpgClusterName" -}}
{{- if .Values.cnpg.clusterName -}}
{{- .Values.cnpg.clusterName -}}
{{- else -}}
{{- printf "%s-pg" .Release.Name -}}
{{- end -}}
{{- end -}}

{{/*
DB host. CNPG exposes a `<cluster>-rw` Service that always points at
the current primary; using -rw means failovers don't require a chart
re-render.
*/}}
{{- define "rivolt.dbHost" -}}
{{- if .Values.cnpg.enabled -}}
{{- printf "%s-rw" (include "rivolt.cnpgClusterName" .) -}}
{{- else -}}
{{- required "externalDatabase.host is required when cnpg.enabled=false" .Values.externalDatabase.host -}}
{{- end -}}
{{- end -}}

{{- define "rivolt.dbPort" -}}
{{- if .Values.cnpg.enabled -}}5432{{- else -}}{{ .Values.externalDatabase.port }}{{- end -}}
{{- end -}}

{{- define "rivolt.dbName" -}}
{{- if .Values.cnpg.enabled -}}{{ .Values.cnpg.database }}{{- else -}}{{ .Values.externalDatabase.database }}{{- end -}}
{{- end -}}

{{/*
DB user / password sourcing.

  - cnpg.enabled  → CNPG auto-generates a `<cluster>-app` Secret
                    with keys `username` and `password`. We source
                    both via valueFrom so we never copy creds.
  - external w/ existingSecret → user-supplied Secret. `username`
                    is optional (existingSecretUserKey); when not
                    set, externalDatabase.user is used as a literal.
  - external w/ inline password → chart-managed `<release>-app`.
*/}}
{{- define "rivolt.dbUserSecretName" -}}
{{- if .Values.cnpg.enabled -}}
{{- printf "%s-app" (include "rivolt.cnpgClusterName" .) -}}
{{- else if and .Values.externalDatabase.existingSecret .Values.externalDatabase.existingSecretUserKey -}}
{{- .Values.externalDatabase.existingSecret -}}
{{- end -}}
{{- end -}}

{{- define "rivolt.dbUserSecretKey" -}}
{{- if .Values.cnpg.enabled -}}username
{{- else if and .Values.externalDatabase.existingSecret .Values.externalDatabase.existingSecretUserKey -}}{{ .Values.externalDatabase.existingSecretUserKey }}
{{- end -}}
{{- end -}}

{{- define "rivolt.dbUserLiteral" -}}
{{- if .Values.cnpg.enabled -}}{{ .Values.cnpg.owner }}{{- else -}}{{ .Values.externalDatabase.user }}{{- end -}}
{{- end -}}

{{- define "rivolt.dbPasswordSecretName" -}}
{{- if .Values.cnpg.enabled -}}
{{- printf "%s-app" (include "rivolt.cnpgClusterName" .) -}}
{{- else if .Values.externalDatabase.existingSecret -}}
{{- .Values.externalDatabase.existingSecret -}}
{{- else -}}
{{- include "rivolt.appSecretName" . -}}
{{- end -}}
{{- end -}}

{{- define "rivolt.dbPasswordSecretKey" -}}
{{- if .Values.cnpg.enabled -}}password
{{- else if .Values.externalDatabase.existingSecret -}}{{ .Values.externalDatabase.existingSecretPasswordKey }}
{{- else -}}DB_PASSWORD
{{- end -}}
{{- end -}}

{{- define "rivolt.dbSslMode" -}}
{{- if .Values.cnpg.enabled -}}require{{- else -}}{{ .Values.externalDatabase.sslmode }}{{- end -}}
{{- end -}}
