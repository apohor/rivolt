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

{{/*
DB_USER and DB_PASSWORD env entries, shared by the Deployment and the
migrate Job. DB_USER: from CNPG's <cluster>-app Secret, a user-supplied
Secret, or the literal externalDatabase.user. DB_PASSWORD: always a
secretKeyRef.
*/}}
{{- define "rivolt.dbCredentialsEnv" -}}
{{- $dbUserSecret := include "rivolt.dbUserSecretName" . }}
{{- if $dbUserSecret }}
- name: DB_USER
  valueFrom:
    secretKeyRef:
      name: {{ $dbUserSecret }}
      key: {{ include "rivolt.dbUserSecretKey" . }}
{{- else }}
- name: DB_USER
  value: {{ include "rivolt.dbUserLiteral" . | quote }}
{{- end }}
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "rivolt.dbPasswordSecretName" . }}
      key: {{ include "rivolt.dbPasswordSecretKey" . }}
{{- end }}

{{/*
The image tag as a label value (labels allow [A-Za-z0-9._-], 63 chars):
"+" in a semver build suffix becomes "_".
*/}}
{{- define "rivolt.versionLabel" -}}
{{- .Values.image.tag | default .Chart.AppVersion | toString | replace "+" "_" | trunc 63 | trimSuffix "-" | trimSuffix "." -}}
{{- end }}

{{/*
Labels for the migrate Job and its pod. Deliberately NOT the selector
labels (app.kubernetes.io/name is "<name>-migrate"): a Job pod has no
readiness probe, so with the app's labels it would count as Ready and
become a Service endpoint, the PDB would count it, and network policies
written for the app would apply to it. rivolt.dev/version lets a deploy
script wait for this release's Job rather than the previous one.
*/}}
{{- define "rivolt.migrateLabels" -}}
helm.sh/chart: {{ include "rivolt.chart" . }}
app.kubernetes.io/name: {{ include "rivolt.name" . }}-migrate
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: migrate
app.kubernetes.io/managed-by: {{ .Release.Service }}
rivolt.dev/version: {{ include "rivolt.versionLabel" . | quote }}
{{- end }}

{{/*
The migrate Job's spec (templates/migrate-job.yaml), rendered once so that
its hash can be part of the Job's name.
*/}}
{{- define "rivolt.migrateJobSpec" -}}
backoffLimit: {{ .Values.migrate.job.backoffLimit }}
activeDeadlineSeconds: {{ .Values.migrate.job.activeDeadlineSeconds }}
ttlSecondsAfterFinished: {{ .Values.migrate.job.ttlSecondsAfterFinished }}
parallelism: 1
completions: 1
template:
  metadata:
    labels:
      {{- include "rivolt.migrateLabels" . | nindent 6 }}
  spec:
    restartPolicy: Never
    serviceAccountName: {{ include "rivolt.serviceAccountName" . }}
    # The migrator never talks to the Kubernetes API.
    automountServiceAccountToken: false
    {{- with .Values.imagePullSecrets }}
    imagePullSecrets:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    securityContext:
      {{- toYaml .Values.podSecurityContext | nindent 6 }}
    {{- with .Values.nodeSelector }}
    nodeSelector:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    {{- with .Values.tolerations }}
    tolerations:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    containers:
      - name: migrate
        image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
        imagePullPolicy: {{ .Values.image.pullPolicy }}
        args: ["migrate"]
        env:
          - name: DB_HOST
            value: {{ include "rivolt.dbHost" . | quote }}
          - name: DB_PORT
            value: {{ include "rivolt.dbPort" . | quote }}
          - name: DB_NAME
            value: {{ include "rivolt.dbName" . | quote }}
          - name: DB_SSLMODE
            value: {{ include "rivolt.dbSslMode" . | quote }}
          {{- include "rivolt.dbCredentialsEnv" . | trim | nindent 10 }}
          # The roles `rivolt migrate` grants to after migrating (the request
          # role for row-level security, the read-only reporting role), from
          # the same config.extraEnv the Deployment reads, only when set.
          # Nothing else from config.extraEnv reaches the migrator.
          {{- range $k := list "RIVOLT_APP_ROLE" "RIVOLT_READONLY_ROLE" }}
          {{- with index $.Values.config.extraEnv $k }}
          - name: {{ $k }}
            value: {{ . | quote }}
          {{- end }}
          {{- end }}
        securityContext:
          {{- toYaml .Values.securityContext | nindent 10 }}
        resources:
          {{- toYaml .Values.migrate.job.resources | nindent 10 }}
{{- end }}

{{/*
The migrate Job's name: <fullname>-migrate-<version>-<first 8 of sha256(spec)>,
DNS-safe (the version lowercased, "+" and "_" turned into "-") and at most 63
characters. Called with (dict "root" . "spec" <the rendered spec>).
*/}}
{{- define "rivolt.migrateJobName" -}}
{{- $v := .root.Values.image.tag | default .root.Chart.AppVersion | toString | lower | replace "+" "-" | replace "_" "-" -}}
{{- $h := .spec | sha256sum | trunc 8 -}}
{{- $prefix := printf "%s-migrate-" (include "rivolt.fullname" .root) -}}
{{- $room := sub 63 (add (len $prefix) 9) | int -}}
{{- printf "%s%s-%s" $prefix ($v | trunc $room | trimSuffix "-" | trimSuffix ".") $h -}}
{{- end }}
