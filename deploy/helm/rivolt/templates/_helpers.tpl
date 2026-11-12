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

{{/*
The pod template of the app: shared by the Deployment and, with rollout.enabled, the Rollout, so the two can never differ. Rendered at the
indentation of a "template:" key two spaces in (its content at four), starting on a new line; call it as
  template:{{ include "rivolt.podTemplate" . }}
*/}}
{{- define "rivolt.podTemplate" }}
{{- /* With roles.enabled, templates/roles.yaml renders this once per role with "role" set; otherwise it is empty. */}}
{{- $role := .role | default "" }}
{{- $matrix := .Values.roles.secrets | default dict }}
    metadata:
      labels:
        {{- include "rivolt.selectorLabels" . | nindent 8 }}
        {{- with $role }}
        rivolt.dev/role: {{ . }}
        {{- end }}
        {{- with .Values.podLabels }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      annotations:
        # Roll the pod when config or app secrets change so we don't
        # serve stale env until the next manual restart. Only the CONTENT is
        # hashed (data / stringData), not the metadata: the helm.sh/chart label
        # changes with every chart version, and a chart bump alone must not roll pods.
        checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | fromYaml | dig "data" (dict) | toJson | sha256sum }}
        {{- if not .Values.secrets.existingSecret }}
        checksum/secret: {{ $sec := include (print $.Template.BasePath "/secret.yaml") . | fromYaml }}{{ dict "data" ($sec.data | default (dict)) "stringData" ($sec.stringData | default (dict)) | toJson | sha256sum }}
        {{- end }}
        # Deploy-time image tag. The binary's compile-time VERSION
        # stamp is fixed to the main commit a retag was based on; the
        # actual tag we deployed (set by ops in apps/platform/rivolt/values.yaml)
        # lives here and is surfaced into the pod via the downward-API
        # volume mount below. /api/health reads /etc/podinfo/image-tag
        # and prefers it over the compile-time stamp.
        rivolt.dev/image-tag: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
        {{- with .Values.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      serviceAccountName: {{ include "rivolt.serviceAccountName" . }}
      # Give the binary time to (a) stop receiving new traffic via
      # the preStop sleep, then (b) finish in-flight requests +
      # release Rivian websocket subscriptions cleanly. 75s covers
      # the 35s HTTP drain on top of the lease release; bump it
      # together with httpDrainTimeout in cmd/rivolt/shutdown.go.
      terminationGracePeriodSeconds: {{ .Values.terminationGracePeriodSeconds }}
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        {{- toYaml .Values.podSecurityContext | nindent 8 }}
      containers:
        - name: rivolt
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          ports:
            - name: http
              containerPort: {{ .Values.service.targetPort }}
              protocol: TCP
            {{- if .Values.metrics.port }}
            - name: metrics
              containerPort: {{ .Values.metrics.port }}
              protocol: TCP
            {{- end }}
          envFrom:
            - configMapRef:
                name: {{ include "rivolt.fullname" . }}-config
            {{- if not $role }}
            - secretRef:
                name: {{ include "rivolt.appSecretName" . }}
            {{- with .Values.extraEnvFrom }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
            {{- else }}
            {{- /* A role mounts the secrets its row of roles.secrets.secretRefs names, and every non-secret entry. */}}
            {{- range .Values.extraEnvFrom }}
            {{- if or (not .secretRef) (include "rivolt.roleMounts" (dict "rows" $matrix.secretRefs "name" .secretRef.name "role" $role)) }}
            - {{ toYaml . | nindent 14 | trim }}
            {{- end }}
            {{- end }}
            {{- end }}
          env:
            {{- with $role }}
            - name: RIVOLT_ROLE
              value: {{ . | quote }}
            {{- end }}
            {{- if not .Values.adminOnly }}
            # Pod identity for the subscription-lease coordinator
            # (internal/leases). Must be unique per pod across the
            # ReplicaSet — `metadata.name` from the downward API
            # gives us exactly that.
            - name: RIVOLT_POD_ID
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            # This pod's generation for the single-owner rollout rules: the
            # ReplicaSet's pod-template-hash, so a new image and a rollout
            # restart both make a new generation (design 4.5). Read by
            # internal/leases (generation()); inert until M2's preference lands.
            - name: RIVOLT_POD_GENERATION
              valueFrom:
                fieldRef:
                  fieldPath: metadata.labels['pod-template-hash']
            {{- end }}
            # The container's memory limit, in bytes: the binary sets the Go
            # runtime's soft limit (GOMEMLIMIT) to 90% of it, so the GC works
            # harder before the kernel OOM-kills the pod. An explicit
            # GOMEMLIMIT in env wins.
            - name: RIVOLT_MEMORY_LIMIT_BYTES
              valueFrom:
                resourceFieldRef:
                  resource: limits.memory
            {{- if .Values.metrics.port }}
            - name: RIVOLT_METRICS_ADDR
              value: {{ printf ":%v" .Values.metrics.port | quote }}
            {{- end }}
            {{- with .Values.ratelimit }}
            {{- if .redis.addr }}
            # Global token bucket: when set, every
            # outbound Rivian call passes through a Redis-backed
            # main+priority bucket. Empty string disables it
            # cleanly — useful for single-binary local dev.
            - name: RIVOLT_REDIS_ADDR
              value: {{ .redis.addr | quote }}
            {{- if .redis.keyPrefix }}
            # Install-wide Redis key prefix. Defaults to "rivolt"
            # in the binary; preview env overrides to e.g.
            # "rivolt-preview" so two environments sharing one
            # Redis don't collide on rate-limiter token buckets
            # (livestate keys are already disjoint by user_id,
            # but limiter classes are install-wide constants).
            - name: RIVOLT_REDIS_KEY_PREFIX
              value: {{ .redis.keyPrefix | quote }}
            {{- end }}
            {{- end }}
            {{- end }}
            {{- with .Values.maps }}
            {{- if and .valhalla .valhalla.baseUrl }}
            # Same-origin Valhalla proxy. When set, /api/maps/valhalla/*
            # forwards to this URL and /api/config advertises the path
            # so the SPA uses it for /trace_route (snap) and /route
            # (gap fill). Empty/unset disables snap entirely.
            - name: RIVOLT_VALHALLA_BASE_URL
              value: {{ .valhalla.baseUrl | quote }}
            {{- end }}
            {{- if and .photon .photon.baseUrl }}
            # Self-hosted Photon geocoder. When set, /api/geocode
            # tries Photon first for street-level resolution;
            # Open-Meteo (city-level) is the fallback.
            - name: RIVOLT_PHOTON_BASE_URL
              value: {{ .photon.baseUrl | quote }}
            {{- end }}
            {{- if and .weather .weather.baseUrl }}
            # Our own Open-Meteo for weather (planner, outside temperature,
            # recaps). When set, the public API is not used at all; the
            # server keeps a few days of past hours and has no archive.
            - name: RIVOLT_WEATHER_BASE_URL
              value: {{ .weather.baseUrl | quote }}
            {{- end }}
            {{- if and .radar .radar.baseUrl }}
            # Forecast radar grids (NOAA RRFS, built in-cluster). The planner
            # tags each leg rain, snow or dry at its arrival time; unset: no tags.
            - name: RIVOLT_RADAR_BASE_URL
              value: {{ .radar.baseUrl | quote }}
            {{- end }}
            {{- if .tiles.baseUrl }}
            # Same-origin PMTiles proxy. When set, the rivolt API
            # mounts /api/maps/tiles/* and forwards to this URL,
            # and /api/config advertises the .pmtiles file so the
            # SPA's protomaps-leaflet layer fetches it via
            # byte-range reads. Empty/unset falls back to CARTO's
            # public dark raster basemap.
            - name: RIVOLT_TILES_BASE_URL
              value: {{ .tiles.baseUrl | quote }}
            {{- end }}
            {{- if and .dem .dem.baseUrl }}
            # Same-origin terrain tile proxy (Terrarium PNG, for the hillshade): mounts /api/maps/dem/{z}/{x}/{y}.png.
            - name: RIVOLT_DEM_BASE_URL
              value: {{ .dem.baseUrl | quote }}
            {{- end }}
            {{- end }}
            {{- if .Values.grafana.baseUrl }}
            # Operator's Grafana origin. Surfaced via /api/config so
            # the admin user drawer can deep-link to Explore queries
            # scoped to a user_id + time range. Empty/unset hides
            # the links — no broken anchors on self-hosted installs
            # without Grafana.
            - name: RIVOLT_GRAFANA_BASE_URL
              value: {{ .Values.grafana.baseUrl | quote }}
            {{- end }}
            {{- if .Values.tracing }}
            {{- if .Values.tracing.environment }}
            # Stamp deployment.environment on every OTel span (and
            # any resource-attribute-aware metric) so Grafana can
            # distinguish prod traces from preview traces. The
            # tracing package already runs resource.WithFromEnv(),
            # which picks up OTEL_RESOURCE_ATTRIBUTES.
            - name: OTEL_RESOURCE_ATTRIBUTES
              value: "deployment.environment={{ .Values.tracing.environment }}"
            {{- end }}
            {{- end }}
            {{- with .Values.elevation }}
            {{- if .enabled }}
            # Elevation lookup is opt-in. ELEVATION_ENABLED gates the
            # whole feature; ELEVATION_TILES_URL points at a self-
            # hosted Terrarium mirror (empty -> Mapzen on AWS Open
            # Data, which is off-LAN); ELEVATION_CACHE_DIR persists
            # fetched PNGs to disk so pod restarts don't re-fetch
            # and so a pre-built dump can be rsynced in for fully
            # offline operation. See values.yaml for the full story.
            - name: ELEVATION_ENABLED
              value: "1"
            {{- if .tilesUrl }}
            - name: ELEVATION_TILES_URL
              value: {{ .tilesUrl | quote }}
            {{- end }}
            {{- if .cacheDir }}
            - name: ELEVATION_CACHE_DIR
              value: {{ .cacheDir | quote }}
            {{- end }}
            {{- end }}
            {{- end }}
            # DB_USER and DB_PASSWORD (CNPG's <cluster>-app Secret, a
            # user-supplied one, or the chart's): rivolt.dbCredentialsEnv,
            # shared with the migrate Job.
            {{- include "rivolt.dbCredentialsEnv" . | trim | nindent 12 }}
            {{- if $role }}
            # The app Secret's keys in this role's row of roles.secrets.appSecretKeys, one by one (no envFrom: that would mount them all).
            {{- $appSecret := include "rivolt.appSecretName" . }}
            {{- range $k := keys ($matrix.appSecretKeys | default dict) | sortAlpha }}
            {{- if include "rivolt.roleMounts" (dict "rows" $matrix.appSecretKeys "name" $k "role" $role) }}
            - name: {{ $k }}
              valueFrom:
                secretKeyRef:
                  name: {{ $appSecret }}
                  key: {{ $k }}
                  optional: true
            {{- end }}
            {{- end }}
            {{- end }}
            {{- if .Values.migrate.job.enabled }}
            # The migrate Job (templates/migrate-job.yaml) changes the
            # schema before this Deployment rolls; the server only checks
            # that the schema is current and refuses to start if not.
            - name: RIVOLT_MIGRATE_ON_START
              value: "false"
            {{- end }}
            {{- if not $role }}
            {{- with .Values.extraEnv }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
            {{- else }}
            {{- range .Values.extraEnv }}
            {{- if or (not (dig "valueFrom" "secretKeyRef" "" .)) (include "rivolt.roleMounts" (dict "rows" $matrix.env "name" .name "role" $role)) }}
            - {{ toYaml . | nindent 14 | trim }}
            {{- end }}
            {{- end }}
            {{- end }}
          {{- with .Values.startupProbe }}
          startupProbe:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          livenessProbe:
            {{- toYaml .Values.livenessProbe | nindent 12 }}
          readinessProbe:
            {{- toYaml .Values.readinessProbe | nindent 12 }}
          # Drain hook: kubelet pulls the pod out of the Service's
          # Endpoints concurrently with sending SIGTERM. There's a
          # brief race where new traffic can still hit a terminating
          # pod. A short preStop sleep gives kube-proxy / Traefik
          # time to remove the endpoint before the binary starts
          # tearing down — eliminates 502s during rollouts. The hook is an exec of the binary itself (the image is distroless, so there is
          # no shell): `rivolt prestop` signals the server (PID 1) with SIGUSR1 so it stops
          # taking leases, then sleeps. No HTTP endpoint: the kubelet runs httpGet hooks
          # against the pod IP, so anything that reached the port could force a drain.
          {{- if gt (int .Values.preStopSleepSeconds) 0 }}
          lifecycle:
            preStop:
              exec:
                command: ["/app/rivolt", "prestop", "--sleep", "{{ int .Values.preStopSleepSeconds }}s"]
          {{- end }}
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
          securityContext:
            {{- toYaml .Values.securityContext | nindent 12 }}
          volumeMounts:
            - name: data
              mountPath: /data
            # readOnlyRootFilesystem=true means we need a writable
            # /tmp for any transient scratch (Go's os.MkdirTemp,
            # net/http multipart, …). Mounted from emptyDir.
            - name: tmp
              mountPath: /tmp
            # Downward-API projected pod info. /etc/podinfo/image-tag
            # holds the rivolt.dev/image-tag annotation value (the
            # deploy-time tag from Helm values), which /api/health
            # reads to override the binary's compile-time VERSION
            # stamp.
            - name: podinfo
              mountPath: /etc/podinfo
              readOnly: true
      volumes:
        - name: data
          {{- if .Values.persistence.enabled }}
          persistentVolumeClaim:
            claimName: {{ .Values.persistence.existingClaim | default (printf "%s-data" (include "rivolt.fullname" .)) }}
          {{- else }}
          emptyDir: {}
          {{- end }}
        - name: tmp
          emptyDir: {}
        - name: podinfo
          downwardAPI:
            items:
              - path: image-tag
                fieldRef:
                  fieldPath: metadata.annotations['rivolt.dev/image-tag']
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.topologySpreadConstraints }}
      topologySpreadConstraints:
        {{- range . }}
        {{- $c := deepCopy . }}
        {{- if not (hasKey $c "labelSelector") }}
        {{- $sel := include "rivolt.selectorLabels" $ | fromYaml }}
        {{- with $role }}{{ $_ := set $sel "rivolt.dev/role" . }}{{ end }}
        {{- $_ := set $c "labelSelector" (dict "matchLabels" $sel) }}
        {{- end }}
        - {{ toYaml $c | nindent 10 | trim }}
        {{- end }}
      {{- end }}
      {{- with .Values.priorityClassName }}
      priorityClassName: {{ . | quote }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
{{- end }}

{{/*
Per-role Deployments (roles.enabled; zero-downtime design §2.6, S19 9f). The roles are fixed: the binary knows these three (RIVOLT_ROLE).
*/}}
{{- define "rivolt.roleNames" -}}ingest api jobs{{- end }}

{{/*
rivolt.roleMounts: "true" when the role in .role mounts the secret .name of the matrix row set .rows (roles.secrets.appSecretKeys,
secretRefs or env).
*/}}
{{- define "rivolt.roleMounts" -}}
{{- if has .role (index (.rows | default dict) .name | default list) }}true{{ end }}
{{- end }}

{{/*
rivolt.rolesCheck fails the render on what roles.enabled cannot run with yet (the cutover, S19 9h, brings the api Rollout, surge and the
per-role pool shares), on a matrix entry naming an unknown role, and on a secret no matrix entry assigns.
*/}}
{{- define "rivolt.rolesCheck" -}}
{{- $v := .Values }}
{{- $m := $v.roles.secrets | default dict }}
{{- if $v.rollout.enabled }}{{ fail "roles.enabled: the api Rollout comes with the cutover (S19 9h); turn rollout.enabled off" }}{{ end }}
{{- if $v.autoscaling.enabled }}{{ fail "roles.enabled: set roles.<role>.replicaCount; autoscaling does not apply per role yet" }}{{ end }}
{{- if $v.persistence.enabled }}{{ fail "roles.enabled: three Deployments cannot share the /data claim; turn persistence.enabled off" }}{{ end }}
{{- if $v.adminOnly }}{{ fail "roles.enabled: the admin release runs no roles" }}{{ end }}
{{- if $v.metrics.serviceMonitor.enabled }}{{ fail "roles.enabled: the Service selects the api pods only; scrape with metrics.podMonitor instead of metrics.serviceMonitor" }}{{ end }}
{{- if not $v.dbConnections.systemMaxConns }}{{ fail "roles.enabled: set dbConnections.systemMaxConns (and requestMaxConns) for the pods of all three roles plus surge; per-role shares come with S19 9h" }}{{ end }}
{{- if not $v.config.replicaDivisor }}{{ fail "roles.enabled: set config.replicaDivisor to the pods of all three roles that may run at once" }}{{ end }}
{{- $names := splitList " " (include "rivolt.roleNames" .) }}
{{- range $kind := list "appSecretKeys" "secretRefs" "env" }}
{{- range $name, $roles := index $m $kind | default dict }}
{{- range $roles }}
{{- if not (has . $names) }}{{ fail (printf "roles.secrets.%s.%s: unknown role %q (want ingest, api or jobs)" $kind $name .) }}{{ end }}
{{- end }}
{{- end }}
{{- end }}
{{- range $v.extraEnvFrom }}
{{- with .secretRef }}
{{- if not (hasKey ($m.secretRefs | default dict) .name) }}{{ fail (printf "roles.enabled: extraEnvFrom secret %q has no roles.secrets.secretRefs entry: name the roles that mount it" .name) }}{{ end }}
{{- end }}
{{- end }}
{{- range $v.extraEnv }}
{{- if and (dig "valueFrom" "secretKeyRef" "" .) (not (hasKey ($m.env | default dict) .name)) }}{{ fail (printf "roles.enabled: extraEnv %s comes from a Secret and has no roles.secrets.env entry: name the roles that mount it" .name) }}{{ end }}
{{- end }}
{{- if not $v.secrets.existingSecret }}
{{- $s := $v.secrets }}
{{- $held := dict "RIVOLT_KEK" (or $s.kek $s.autoGenerateKek) "RIVOLT_KEK_ROTATION" $s.kekRotation "RIVOLT_COOKIE_SECRET" $s.cookieSecret "OPENAI_API_KEY" $s.openAIApiKey "ANTHROPIC_API_KEY" $s.anthropicApiKey "GEMINI_API_KEY" $s.geminiApiKey "VAPID_SUBJECT" $s.vapidSubject "VAPID_PUBLIC_KEY" $s.vapidPublicKey "VAPID_PRIVATE_KEY" $s.vapidPrivateKey }}
{{- range $k, $set := $held }}
{{- if and $set (not (hasKey ($m.appSecretKeys | default dict) $k)) }}{{ fail (printf "roles.enabled: the chart's Secret holds %s and roles.secrets.appSecretKeys does not assign it" $k) }}{{ end }}
{{- end }}
{{- end }}
{{- end }}
