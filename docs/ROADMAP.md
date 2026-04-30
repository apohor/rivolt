# Rivolt — roadmap

> The scale target is **1000 vehicles, single region**. The
> architectural shape that gets us there is in
> [`ARCHITECTURE.md`](ARCHITECTURE.md). This roadmap stages the work
> into three phases so that early decisions don't need to be
> re-litigated later:
>
> 1. **Correctness now** — the code-level decisions that are
>    expensive to retrofit.
> 2. **Self-hosted k8s** — run multi-replica on our own cluster.
>    Everything in phase 1 becomes load-bearing.
> 3. **Managed cloud** — graduate to a hosted SaaS for real users.
>
> Features (live panel, iOS, home-energy, overland, Plus tier) are
> threaded across the phases where they fit — never pulled forward
> past the infra prerequisites they actually need.

## Status

- ✅ MVP shipped (v0.1 → v0.7). Single self-hosted binary on Synology.
  Rivian client, live panel, drive/charge history, AI smoke test,
  push notifications, charge-location clustering (Home / Public /
  Fast).
- ✅ **Phase 1 (correctness)** — all checklist items landed and
  load-bearing in production. RLS policies are declarative-dormant
  pending the Phase 2 app-role split.
- 🟡 **Phase 2 (self-hosted k8s)** — most of the platform is in
  place. ✅ Helm chart, container hardening, OIDC-only auth,
  CloudNativePG, ExternalSecrets-from-Vault, cert-manager+LE,
  Loki+Promtail, kube-prometheus-stack, ArgoCD-managed everything,
  CI to GHCR. The remaining work is mostly **app-side**:
  multi-replica runtime correctness (lease reconciliation, Redis
  token bucket, reconnect-storm controls), app-level structured
  logs / metrics / traces, and self-hosted map tiles + routing.
  v0.11.0 cut OIDC out of password login; v0.11.1 fixed Go's
  `oauth2` auth-style probing against Authelia's strict
  `client_secret_post`.
- 🟡 **iOS scaffold** landed (v0.9 track) — skeleton-only, runs via
  Xcode on a tethered iPhone. See [`../ios/README.md`](../ios/README.md).

---

## Phase 1 — Correctness decisions, right now

Code-level work that can land today, without waiting on infra. The
goal is that when we flip phase 2 on, every "how does this behave
multi-tenant / multi-replica" question already has an answer in the
code. See [`ARCHITECTURE.md`](ARCHITECTURE.md) decisions 1–4, 8, 9, 13.

- [x] **Tenant scoping everywhere.** Every live store method is
      bound to a `userID` at `OpenStore` time and every query
      filters on it: `charges`, `drives`, `samples` (via
      `vehicle_state`), `push_subscriptions`, `user_settings`,
      `user_secrets`, `sessions`, `imports`. Ownership is
      cross-checked at the HTTP boundary by the vehicle-ownership
      middleware (below). The only unscoped tables are singleton
      system state: `flags` (kill switch), `push_vapid` (install
      keypair), `migrations`. A dead `ai/usage.go` SQLite-era
      recorder exists but has zero callers and will be either
      wired to the Postgres `ai_usage` table or removed when AI
      metering graduates past smoke-test.
- [x] **Vehicle-ownership middleware.** Every route that takes a
      `{vehicleID}` param verifies the vehicle belongs to the
      session's `user_id` before the handler runs. Single chi
      middleware, used on every vehicle-scoped subtree. Prevents
      `/api/state/:id` from becoming a tenant-enumeration oracle
      the moment a second user signs up.
- [x] **Multi-vehicle-aware UI.** `useSelectedVehicle` hook
      binds the "which car is the overview showing" choice to
      `localStorage` and self-heals when the backing vehicle
      disappears from `/api/vehicles`. A compact `VehiclePicker`
      in the hero footer appears only when the account has 2+
      cars — the single-vehicle common case gets zero new
      chrome. The Live page already iterated per-vehicle, so no
      change there. iOS home screen is a follow-up. (`web/src/lib/selectedVehicle.ts`,
      `web/src/components/VehiclePicker.tsx`, `web/src/pages/HomePage.tsx`.)
- [x] **Row-level security on every user-scoped table.** Migration
      0008 installs RLS policies on every tenant table
      (`users`, `vehicles`, `locations`, `charges`, `drives`,
      `vehicle_state`, `imports`, `push_subscriptions`,
      `user_settings`, `user_secrets`, `sessions`, `ai_usage`)
      with a single predicate `user_id = current_setting('app.user_id')`.
      `rivolt_current_user_id()` helper returns NULL when the GUC
      isn't set, which means closed-by-default: a connection that
      forgets to pin sees zero rows. Staged rollout — Phase 1
      ENABLEs RLS without FORCE so the table-owner role (today's
      app) bypasses; Phase 2 flips FORCE + drops BYPASSRLS from
      the app role once request-scoped conn pinning is wired.
      `db.WithUserScope(ctx, pool, userID, fn)` helper ships with
      this migration as the seam Phase 2 will flow through.
- [x] **Credential envelope encryption.** `Sealer` interface with
      `EnvSealer` (KEK from `RIVOLT_KEK` env var) as the
      phase-1 implementation. Per-blob AES-256-GCM DEKs wrapped
      under the KEK; `userID` bound as AAD on both the wrap and
      the payload, so a cross-user ciphertext swap fails. Wire
      format is tagged with `kek_id` to support overlapping
      rotation via `RIVOLT_KEK_ROTATION` (comma-separated list of
      retained old keys). `rivian.Session` is now sealed in
      `user_secrets`; a one-shot startup migration moves legacy
      plaintext rows out of `settings_kv`. Phase 3 swaps in
      `KMSSealer` without touching callers. (`internal/crypto`,
      `internal/secrets`, `cmd/rivolt/main.go`.)
- [x] **Server-side opaque sessions.** Sessions table keyed by
      a random UUID; cookie carries a 32-byte opaque token, not
      signed claims. Server stores `HMAC(pepper, token)` so a
      DB dump alone can't forge sessions — pepper is the
      existing `RIVOLT_COOKIE_SECRET`. Revocation is a soft
      `revoked_at` stamp (janitor hard-deletes after grace).
      `last_seen_at` is touched at most once per minute per
      session to avoid DB write storms from live-reload tabs.
      Proxy-header auth stays stateless (upstream IdP owns the
      session lifecycle). (`internal/sessions`,
      `internal/db/migrations/0006_sessions.sql`,
      `internal/auth` rewired via `WithSessionStore`.)
- [x] **Rivian upstream wrapper with error classification.**
      Five-class taxonomy (transient / outage / rate-limited /
      user-action / unknown) derived from HTTP status,
      GraphQL `extensions.code`, and body-scan patterns for
      cases where the status alone is ambiguous (a 400 with
      "invalid password" is user-action, a bare 400 is
      transient). The classifier lives in
      `internal/rivian/errclass.go` as three pure functions
      (`ClassifyHTTP`, `ClassifyGraphQL`, `ClassifyNetwork`) so
      the decision table is fully unit-tested without a live
      gateway. Every outbound call through `doGraphQLAt`
      returns an `*UpstreamError` that carries class, HTTP
      status, `extensions.code`, a human reason and the
      underlying cause — the api layer can unwrap it and turn
      rate-limits into 503-with-Retry-After, user-action into
      a 401 that nudges the UI to Settings, etc. User-action
      flips a per-user `needs_reauth` flag stored in the
      `users` row and mirrored to an in-process atomic
      pointer so the hot-path gate doesn't hit Postgres. A
      failure storm fires the persistence sink exactly once
      (on the false→true edge) to protect the DB. Successful
      `Login` clears the flag. **Single biggest determinant
      of support load at scale.**
- [x] **`samples` partitioned by month** from day one. Migration
      0007 converts `vehicle_state` to RANGE-partitioned on `at`,
      copies the existing heap, and installs
      `rivolt_ensure_vehicle_state_partition(ts)` helper. A Go
      janitor (`samples.PartitionJanitor`) calls the helper at
      boot + hourly for `now + 3 months` so the live recorder
      never writes into an unpartitioned range. Retention is
      out-of-scope here — partition DROP is a one-liner once we
      want it. No pg_partman dependency; graduate to it in Phase
      3 if retention automation becomes more than a cron line.
      `drive_samples` / `charge_samples` don't exist yet; they'll
      adopt the same pattern when derived-sample tables land.
- [x] **Kill switch.** Single row in a `flags` table
      (`rivian_upstream_paused`), polled every 10s by every pod,
      returns `ErrUpstreamPaused` from every outbound Rivian call
      (REST + WS) when set. Flipped via `PUT
      /api/admin/kill-switch` so operators can pause the service
      without a deploy. Actor + reason stamped on the row for
      audit.
- [x] **Unit tests on the load-bearing pure logic.** v0.10.33
      filled the genuine gaps: `internal/secrets` (nil-store
      paths + `rivian.Session` JSON round-trip pin),
      `internal/samples` (partition janitor defaults + nil-receiver
      guards + ctx-cancel honour), `internal/charges` (the four
      `nullIf*` / `*FromNull` helpers + `OpenStore` validation).
      The other classes listed below were already covered when I
      audited the tree — `internal/crypto`, `internal/sessions`,
      `internal/auth`, `internal/oidc`, `internal/rivian`
      (`errclass`, `headers`, `recorder`, `killswitch`, `live`,
      `ws_parallax`), `internal/api/vehicle_mw`,
      `internal/electrafi/logic`, and the charge clustering pass
      that lives in `internal/analytics/cluster_test.go`. Project
      convention (see `internal/sessions/store_test.go` header) is
      pure DB-free surface only — DB-touching code stays under
      runtime smoke until we adopt testcontainers wholesale, which
      is its own line item. Original priority list kept below for
      historical context:
      - `internal/crypto` (envelope sealer) — KEK rotation,
        AAD binding, malformed-ciphertext rejection,
        `kek_id` mismatch.
      - `internal/sessions` (opaque token + HMAC pepper) —
        issue / lookup / revoke / janitor sweep, `last_seen_at`
        rate-limit.
      - `internal/auth` middleware — vehicle-ownership cross-check
        rejects foreign `{vehicleID}`; OIDC state/nonce
        round-trip.
      - `internal/secrets` (sealed `rivian.Session` + plaintext
        migration) — round-trip, legacy import path.
      - `internal/rivian` GraphQL client — `UpstreamError`
        unwrap into HTTP responses (rate-limit → 503,
        user-action → 401), `needs_reauth` edge-trigger fires
        once.
      - `internal/samples` partition janitor — boot-time backfill,
        idempotent re-runs, "now + 3 months" window.
      - `internal/charges` clustering (Home / Public / Fast) and
        the drive/charge derivation passes — table-driven on
        recorded sample fixtures.
      Goal isn't 100% coverage; it's that anything I'd be afraid
      to refactor blind has a fixture-level test. Snapshot the
      fixtures from real samples scrubbed of `vehicle_id` /
      `user_id`. Web-side tests stay deferred until the app gets
      a second contributor — one operator + Playwright smoke is
      enough today.
- [x] **Outbound user-agent identification.** Impersonate the iOS
      Rivian Owner App on every upstream request —
      `User-Agent: RivianApp/4400 CFNetwork/1498.700.2 Darwin/23.6.0`,
      `apollographql-client-name: com.rivian.ios.consumer`,
      `apollographql-client-version: 3.6.0-4400`, matching `Accept`
      / `Accept-Language`. Until we've had the phase-3 dev-rels
      conversation with Rivian, a non-allowlisted UA is the single
      easiest way for the gateway to block Rivolt; matching the
      iOS app verbatim is the path of least friction. We ship an
      `X-Rivolt-Version` trailer so Rivian's on-call (and our own
      logs) can still tell Rivolt traffic apart.

---

## Phase 2 — Self-hosted k8s cluster

Run on the operator's own k8s (k3s on Synology today, a dedicated
homelab cluster later). Multi-replica. All correctness plumbing
becomes real. Target: 1000 vehicles on one pod set, 3–8 replicas,
one managed Postgres. See [`ARCHITECTURE.md`](ARCHITECTURE.md)
decisions 5–7, 10–12.

### Infra

- [x] **Helm chart** at `deploy/helm/rivolt/` — single Deployment,
      HPA pre-wired but disabled-by-default (Phase 2 lease work
      isn't done; >1 replicas means duplicate Rivian websockets),
      ConfigMap for non-secrets, three secrets-wiring modes
      (inline values, `secrets.existingSecret` for ExternalSecrets/
      SOPS/sealed-secrets, `extraEnvFrom` escape hatch). Database
      is intentionally NOT bundled — chart takes either an
      external DSN (`externalDatabase.*`) or renders a CNPG
      `Cluster` CR (`cnpg.enabled=true`); no Bitnami subchart, no
      raw StatefulSet. CNPG operator install is documented but
      out-of-scope for the chart (cluster-scoped, one per cluster).
- [x] **Container hardening** — `/api/health` probes, non-root
      (uid 65532 from distroless base), `readOnlyRootFilesystem`
      with emptyDir `/tmp`, `seccompProfile: RuntimeDefault`,
      `capabilities: drop: [ALL]`, `automountServiceAccountToken:
      false`, resource requests/limits, PDB template (off by
      default at replicaCount=1 to avoid blocking node drains).
      Landed with the Helm chart.
- [x] **CloudNativePG** as the database. CNPG operator runs in
      `cnpg-system` (Helm chart `cloudnative-pg` 0.22.1, ArgoCD-managed
      via `apps/cnpg-operator.yaml` in rivolt-infra). Rivolt's own
      Helm chart renders a `Cluster` CR via `cnpg.enabled=true` —
      no Bitnami subchart, no single-pod StatefulSet.
- [ ] **Redis Deployment** for the global upstream token bucket.
      No persistence; `t4g.micro`-class resources.
- [x] **Secret delivery via External Secrets + Vault** (instead of
      SealedSecrets). HashiCorp Vault runs in-cluster; ExternalSecrets
      Operator syncs `kv/rivolt/*` paths into k8s Secrets. KEK is
      pulled the same way (`rivolt-app` Secret). Bootstrap script
      (`bootstrap/seed-vault.sh` in rivolt-infra) is idempotent
      across `vault kv delete` soft-deletes.
- [x] **cert-manager + Let's Encrypt** for TLS on the Ingress.
      cert-manager bootstrapped pre-ArgoCD; ClusterIssuer
      `letsencrypt-prod` is git-managed via
      `apps/cluster-issuers.yaml`. Every Ingress in the platform
      (rivolt, auth, grafana, argocd, vault) has a working cert.
- [x] **CI → registry** — GitHub Actions builds the image on tag,
      pushes to `ghcr.io/apohor/rivolt` with `vX.Y.Z`, `X.Y`,
      `latest` tags. Multi-arch (amd64 default; amd64+arm64 via
      `workflow_dispatch`). Helm chart packaging + GitHub Pages
      chart repo + SBOM/cosign signing remain — chart is
      consumed today via raw git path from rivolt-infra, which
      works but doesn't give a versioned dependency surface.
- [ ] **Self-hosted map tiles + routing.** Today the drive/charge
      maps fetch raster tiles from CARTO's free CDN
      (`*.basemaps.cartocdn.com`) and snap GPS traces with the
      public OSRM demo (`router.project-osrm.org`). Both have
      no-uptime-SLA, hammered-by-the-internet rate limits — fine
      for a single-operator instance, hostile at multi-tenant
      scale. Stand up:
      - A self-hosted tile server (TileServer-GL or a
        Protomaps-style PMTiles bundle on object storage with a
        `pmtiles://` viewer) — eliminates per-tile CDN calls and
        works offline for overland mode.
      - A self-hosted OSRM (or Valhalla) container with a regional
        OSM extract, exposed on the cluster network. Lifts the
        9-coord `/match` cap that currently forces the frontend
        to chunk traces, and removes the rate-limit dependency
        from drive-route rendering.
      The frontend already has the URLs centralized
      (`addCartoDark` in `DriveMap.tsx`, the OSRM base URL in
      `snapToRoads`); swap behind a runtime config flag so
      self-hosters can pick public-CDN or self-hosted at deploy
      time.

### Runtime correctness at N > 1 pods

- [x] **Zero-downtime deploys at replicaCount=1.** Helm chart
      strategy switched from `Recreate` to `RollingUpdate`
      (maxSurge=1, maxUnavailable=0), `persistence.enabled`
      defaults to `false` (cookie secret + VAPID keys come from
      the `rivolt-app` Secret), preStop sleep + 30s
      terminationGracePeriodSeconds drain the pod cleanly. The
      `Recreate` strategy is still selectable via `updateStrategy.type`
      for operators who want PVC-backed `/data`. This unblocks
      chart bumps from causing "no available server" but does NOT
      unblock steady-state replicaCount>1 — the three items below
      still gate that.
- [x] **Subscription lease reconciliation.** Migration `0011`
      adds a `subscription_leases (vehicle_id, pod_id, expires_at)`
      table; `internal/leases.Coordinator` polls every 30s, calls
      `INSERT … ON CONFLICT DO UPDATE WHERE expires_at < now() OR
      pod_id = EXCLUDED.pod_id RETURNING pod_id` to opportunistically
      claim unowned vehicles, renews held leases on every tick (TTL
      2 min), and diffs Renew's returned set against its in-memory
      `owned` to detect leases stolen by peers — firing
      `StateMonitor.Unsubscribe` for losers and `EnsureSubscribed`
      for new winners. SIGTERM calls `ReleaseAll` before HTTP
      shutdown so peers pick the vehicles up while we drain. Pod
      identity comes from `RIVOLT_POD_ID` (downward-API
      `metadata.name` in the chart) with `os.Hostname()` as a
      single-binary fallback. The `rivolt_subscription_leases`
      gauge tracks the per-pod count.
- [ ] **Reconnect-storm controls.** Jittered exponential backoff per
      subscription, 50ms startup stagger, global circuit breaker on
      Rivian 5xx/429. See architecture decision 6.
- [ ] **Global upstream token bucket** in Redis, main + priority
      classes, Lua-scripted atomic check-and-decrement. See
      architecture decision 7.

### Identity

- [x] **OIDC login** via `go-oidc`. Generic OIDC works against any
      compliant IdP (Google, Authentik, Authelia, Keycloak,
      Okta…). Configuration is per-provider env soup —
      `RIVOLT_OIDC_PROVIDERS=google,authentik` plus
      `RIVOLT_OIDC_<NAME>_{ISSUER,CLIENT_ID,CLIENT_SECRET,DISPLAY_NAME,SCOPES}` —
      so adding a provider is a deploy-time change, not a code
      change. Flow is OAuth2 auth-code with PKCE (S256), state +
      nonce reused as a single 32-byte random in an HttpOnly +
      SameSite=Lax cookie scoped to `/api/auth/oidc`. Identity
      resolves verified-email > preferred_username > unverified
      email > iss+sub so an OIDC sign-in joins cleanly with a
      password sign-in on the same email — same UUIDv5 either
      way. SPA fetches `/api/auth/oidc/` and renders one
      "Continue with X" button per provider on the login page;
      empty list = invisible chrome. (`internal/oidc`,
      `auth.IssueSession` extracted as the shared session-mint
      seam, `db.EnsureUserFull` populates email + display_name.)
      Username/password login remains for self-hosters who don't
      want an IdP. **GitHub** is not OIDC-native; pure OAuth2
      adapter is a follow-up using the same Service shape.

### Observability

- [x] **Log shipping pipeline.** Loki + Promtail run cluster-wide
      (rivolt-infra `apps/loki.yaml`, `apps/promtail.yaml`) and
      ingest stdout from every pod, including Rivolt. Grafana
      (deployed via kube-prometheus-stack) is the unified pane.
      OIDC-backed at `https://grafana.rivolt.dev`. Per-request
      structured slog with `user_id`/`vehicle_id`/`request_id`/
      `trace_id` from context **isn't done yet** — current logs
      are stdout-text and we grep them in Loki.
- [x] **Prometheus stack deployed.** kube-prometheus-stack runs
      cluster-wide and scrapes node/k8s/cnpg/argocd metrics out of
      the box. Rivolt-side instrumentation (`/metrics` endpoint
      with handler-latency histograms, Rivian-result-class
      counters, lease-count gauges, AI-token spend) is **not yet
      shipped**.
- [x] **App-level structured logs.** `internal/logging` package
      ships a `ContextHandler` wrapper around `slog.JSONHandler` that
      pulls `request_id` (chi), `user_id` (auth middleware) and
      `vehicle_id` (vehicle-ownership middleware) out of
      `context.Context` and stamps them on every record — no
      callsite changes in `internal/*` were needed thanks to
      `slog.SetDefault`. `trace_id` slot is plumbed but unset until
      OTel lands. Per-request access log emitted by
      `logging.HTTPMiddleware` (skips `/api/health`). New env vars:
      `RIVOLT_LOG_LEVEL` (debug|info|warn|error), `RIVOLT_LOG_FORMAT`
      (json|text). The Loki pipeline becomes filterable by user /
      vehicle / request without grep gymnastics.
- [x] **App-level Prometheus `/metrics`** — `internal/metrics`
      package owns a private registry; `cmd/rivolt` constructs a
      `*Metrics` and wires it into `api.Deps`. The chi middleware
      records `rivolt_http_requests_total` (method/route/status) and
      `rivolt_http_request_duration_seconds` (method/route) — `route`
      is the chi route pattern, NOT the raw URL, so cardinality
      stays bounded as vehicles scale. Also exposes (currently
      always-zero, wired up so dashboards can pre-build):
      `rivolt_rivian_results_total{op,class}`,
      `rivolt_subscription_leases`, `rivolt_ai_requests_total`.
      `/metrics` is mounted at the root (NOT under `/api`) with no
      auth — kube-prometheus-stack reaches it via the pod IP. The
      Helm chart ships a ServiceMonitor gated on
      `metrics.serviceMonitor.enabled` (off by default for
      docker-compose / k3s-without-KPS users).
- [x] **OpenTelemetry traces** via OTLP/HTTP to Grafana Tempo.
      `internal/tracing` builds an SDK TracerProvider with a batch
      OTLP/HTTP exporter when `RIVOLT_OTEL_ENABLED=true` (no-op
      shutdown when off, so docker-compose / single-binary boots
      stay quiet). Env: `RIVOLT_OTEL_ENDPOINT`, `RIVOLT_OTEL_INSECURE`,
      `RIVOLT_OTEL_SAMPLE_RATIO` (default 1.0 — dial down at scale),
      `RIVOLT_OTEL_SERVICE_NAME`. The chi router is wrapped with
      `otelhttp.NewHandler`; an inner `otelTraceRoute` middleware
      renames the root span to `HTTP <method> <chi-pattern>` once
      routing resolves so Tempo bucketizes by route, not by URL.
      `/api/health` and `/metrics` are filtered out so probes /
      scrapes don't drown trace storage. The Rivian client's
      `*http.Client` uses `otelhttp.NewTransport`, and
      `doGraphQLAt` opens a `rivian.<Op>` client span carrying the
      GraphQL operation name + error class as attributes — failed
      branches highlight red in Tempo. `slog.ContextHandler` reads
      the active `SpanContext` and stamps `trace_id` + `span_id` on
      every log line, so Loki ↔ Tempo navigation is one click.

### Native iOS app (live-panel-era)

Builds on the scaffold already in `ios/`. Most feature work below
depends on phase 2 infra (APNs needs server-side push fan-out,
websocket live panel needs the subscription lease plumbing).

- [x] **SwiftUI scaffold** — app shell, cookie auth, home screen
      with SoC / range for the first vehicle. Xcode Run only. See
      [`../ios/README.md`](../ios/README.md).
- [ ] **OpenAPI spec** emitted by the Go server; Swift client
      generated via `swift-openapi-generator`. Removes the
      hand-maintained `Models.swift` type-drift risk.
- [ ] **OIDC auth** via `ASWebAuthenticationSession`, tokens in iOS
      Keychain. Replaces the cookie path on iOS.
- [ ] **Live panel** — websocket subscription, background reconnect,
      same data as web `/live`.
- [ ] **Live Activities + Dynamic Island** — "Charging to 80%, 42
      min left" during active charge sessions.
- [ ] **APNs push** — swap VAPID/web-push for APNs on iOS; server
      fans out to the right channel based on which the user
      registered. `internal/push` abstraction absorbs the APNs
      HTTP/2 client.
- [ ] **Widgets** — small/medium SoC + range + last-known location;
      StandBy mode variant.
- [ ] **CarPlay** — "Next charger on route" + "Remaining range"
      cards. Read-only; writing commands waits until Rivian's API
      surface is stable enough to trust.
- [ ] **Install path: Xcode Run on a tethered iPhone.** Paid Apple
      Developer Program from day one so push / CarPlay / Live
      Activities entitlements work. Archive / Ad-hoc / TestFlight
      remain explicit non-goals until the app is worth sharing.

---

## Phase 3 — Managed cloud (hosted SaaS)

Architecture shape stays the same; only the infra primitives and
the auth posture change. Target: a public instance with real users,
running on managed services. See [`ARCHITECTURE.md`](ARCHITECTURE.md)
decisions 3, 11, 12 for the cloud-specific deltas.

### Infra

- [ ] **Managed k8s** (EKS / GKE / DOKS). One cluster, one region
      co-located with Rivian's upstream.
- [ ] **Managed Postgres** (RDS / Cloud SQL / Neon). Automated
      backups, PITR on. TimescaleDB extension if `samples` storage
      cost crosses the threshold.
- [ ] **Managed Redis** (ElastiCache / Memorystore), `t4g.micro`
      class. Only used for coordination primitives; no persistence
      needed.
- [ ] **External Secrets Operator** + cloud secret manager (AWS
      Secrets Manager / GCP Secret Manager / Vault). KEK lives in
      cloud KMS; `Sealer` swapped to `KMSSealer`. One line of
      config.
- [ ] **CloudFlare** in front of the Ingress for DDoS + WAF.
- [ ] **Managed L7 load balancer** fronting the Ingress.

### Security / trust

- [ ] **OIDC-only authentication** by default in cloud deploy.
      Self-hosters flip a flag to re-enable password login.
- [ ] **Dev-relations outreach to Rivian** completed before opening
      signups. An unofficial client Rivian's on-call knows about is
      tolerated; surprises are not.

### Compliance

- [ ] **Terms of Service** + **Privacy Policy**.
- [ ] **GDPR subject-access endpoints** — export + delete.
- [ ] **Incident-response runbook** — what do we do when Rivian
      rate-limits us, when Postgres falls over, when the KEK is
      compromised.

### Billing (if Plus tier lands)

- [ ] **Stripe integration.**
- [ ] **License-key validation** for self-hosters who want Plus
      features.
- [ ] Pricing feeds back into the iOS App Store listing model
      (paid vs free-with-Plus).

Expected managed-infra cost at 1000 vehicles: **$100–150/mo**.

---

## Deferred product work

Features that don't belong in phases 1–3's critical path. Ordered
by expected value, not by time.

- [ ] **Home-energy foundation** — Enphase Envoy + Tesla Powerwall
      local API adapters; "schedule charge to solar peak"
      scheduler; "effective cost per kWh after solar offset" line
      in charge detail.
- [ ] **Overland mode** — GPX export per drive, photo attachment
      per waypoint, offline OSM tile caching (pre-downloaded
      bounding box), trail logbook export.
- [ ] **Multi-vehicle household** — > 1 vehicle per account, "which
      vehicle for this trip" recommendation, shared home charger
      queue.
- [ ] **Plus tier** — see phase 3 billing.
- [ ] **Fleet** — mileage reports (IRS-grade CSV export),
      per-driver attribution, SSO (Google Workspace, Microsoft
      Entra).

---

## Non-roadmap

Explicit "not now, probably never":

- Social / leaderboards (Roamer owns this)
- Android Auto integration (requires OEM partnership we won't get —
  CarPlay is on the roadmap because Apple lets third parties ship
  EV/charging categories without OEM sign-off)
- Inventory scraping
- Microservices split of the Go binary (at 100k vehicles we might
  carve out subscription workers; not at 1k)
- Frontend/backend container split (see architecture decision 1)
- Separate repos per app (same)
- Multi-region deployment
- Self-hosted Postgres in cloud phase
- JWTs (see architecture decision 4)
