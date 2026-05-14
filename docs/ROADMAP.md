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

- ✅ **In production at [rivolt.dev](https://rivolt.dev)**, preview
  at [preview.rivolt.dev](https://preview.rivolt.dev) tracking `main`
  via ArgoCD. v0.17.198 is the current prod release at time of
  writing; preview rolls forward on every tag push.
- ✅ MVP shipped (v0.1 → v0.7) — Rivian client, live panel,
  drive/charge history, charge-location clustering (Home / Public /
  Fast), push notification scaffolding.
- ✅ **Trip planner shipped (v0.17.x)** — Rivian planTrip2-backed
  routing with departure datetime picker, last-trip persistence,
  self-hosted Protomaps basemap, NREL-AFDC chargers archive with
  proper DCFC/L2 discrimination, route-corridor charger filter
  (perpendicular distance + endpoint arc-length trim), structured
  Trip analysis card (deterministic cost numbers + categorized
  LLM commentary on cost / efficiency / weather / vehicle).
- ✅ **Push notifications end-to-end** — VAPID, per-device subscribe
  UI in Settings → Notifications, charge-close hook fires
  NotifyChargingDone with the just-persisted session in the body.
  Plug-in-reminder + anomaly triggers still placeholder.
- ✅ **Forecast weather** — `weather.FetchHour` auto-routes to
  Open-Meteo's `/v1/forecast` for hours within the 80-day window
  (past or future) so trip advice for a planned future departure
  sees real conditions, not "no archive data".
- ✅ **Settings UX refactor** — 11-card scroll collapsed into 5
  tabs (Account / Vehicle / Charging / Notifications / Data);
  Backend version panel moved to /admin; admin-tunable GPS
  accuracy thresholds.
- ✅ **Phase 1 (correctness)** — all checklist items landed and
  load-bearing in production. RLS policies are declarative-dormant
  pending the Phase 2 app-role split.
- ✅ **Phase 2 (k8s)** — Helm chart, container hardening, OIDC-only
  auth, CloudNativePG, ExternalSecrets-from-Vault, cert-manager+LE,
  Loki+Promtail, kube-prometheus-stack, ArgoCD-managed everything,
  CI to GHCR. Multi-replica runtime correctness is the remaining
  app-side work (lease reconciliation, Redis token bucket,
  reconnect-storm controls).
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
cluster later). Multi-replica. All correctness plumbing
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
- [x] **Redis Deployment** for the global upstream token bucket.
      Plain-manifest single-replica `valkey/valkey:8-alpine`
      Deployment + Service in the `redis` namespace (Valkey is
      the LF Redis fork; we picked it over the Bitnami chart so we
      don't ride the Aug-2025 Bitnami premium-tier rails). No
      persistence — the bucket is pure rate-limit state, losing it
      on a pod bounce just means a fresh budget. Wired into
      rivolt via `RIVOLT_REDIS_ADDR` (`ratelimit.redis.addr` in
      `values.yaml`).
- [ ] **Migrate Redis to `ot-helm/redis-operator`** once a second
      Redis-shaped workload lands (session cache, job queue,
      websocket fan-out — anything beyond the rate-limit cache)
      OR persistence/HA becomes a hard requirement. Today the
      plain-manifest Deployment is the smallest correct thing:
      operator overhead (CRDs + reconciler Deployment + RBAC +
      webhooks) dwarfs the 64 MiB Valkey pod, and HA/failover/
      backups are explicitly out of scope for a fail-open
      rate-limit cache. The flip is a ~30-line YAML change at
      the point where it earns its keep — wraps the existing
      Deployment in a `Redis` (or `RedisReplication`) CR and
      gives us declarative ACLs, TLS rotation, scheduled S3
      backups, and Sentinel/Cluster topologies for free. Track
      this so we don't spend a year accreting bespoke Redis
      manifests across apps.
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
- [x] **Self-hosted map tiles + routing.** Today the drive/charge
      maps fetch raster tiles from CARTO's free CDN
      (`*.basemaps.cartocdn.com`) and snap GPS traces with the
      public OSRM demo (`router.project-osrm.org`). Both have
      no-uptime-SLA, hammered-by-the-internet rate limits — fine
      for a single-operator instance, hostile at multi-tenant
      scale. Status:
      - ✅ **Self-hosted OSRM**, Texas extract running on the
        cluster (`apps/osrm/` in rivolt-infra). The rivolt API
        reverse-proxies it at `/api/maps/osrm/*` when
        `RIVOLT_OSRM_BASE_URL` is set (`maps.osrm.baseUrl` in
        the chart) and `/api/config` advertises the path so the
        SPA picks it at runtime — no rebuild per deploy. Drive
        maps now send the whole trace as a single `/match`
        instead of walking 9-coord chunks (runtime is configured
        for `--max-matching-size 1000`). v0.17.24.
      - ✅ **Self-hosted tile server.** PMTiles bundle (Texas
        extract built from `build.protomaps.com` daily planet
        via HTTP range reads, ~500 MB) served by an
        unprivileged-nginx Deployment from the cluster NFS
        PVC, behind a same-origin proxy at `/api/maps/tiles/*`
        wired by `RIVOLT_TILES_BASE_URL` (`maps.tiles.baseUrl`
        in the chart). The SPA renders it via
        `protomaps-leaflet` with the built-in dark flavor when
        `/api/config` advertises a `tiles.url` — eliminates
        per-tile CDN calls and works offline. v0.17.25.
      - ✅ **Real GPS routes on the drives overview map.** Live
        recorder accumulates per-frame `(lat, lon)` into a
        Google-encoded polyline column on `drives` (migration
        0018, `route_polyline TEXT`); upsert path uses
        `COALESCE(EXCLUDED.route_polyline, drives.route_polyline)`
        so an ElectraFi re-import never blanks a recorded trace.
        `DrivesOverviewMap` decodes and draws the real route per
        drive when present, falls back to a straight start→end
        segment for legacy / imported drives that have no
        polyline. v0.17.34.
- [x] **Self-hosted elevation tiles.** v0.17.36 added per-sample
      altitude (`vehicle_state.altitude_m`) sourced from the
      Mapzen Terrarium DEM on AWS Open Data
      (`s3.amazonaws.com/elevation-tiles-prod/terrarium/...`).
      Same off-LAN-by-default footgun the CARTO/OSRM items above
      were closing. Mitigation in tree:
      - ✅ **Opt-in.** `ELEVATION_ENABLED=1` required to start the
        resolver; default leaves `altitude_m` NULL and hides the
        Elevation chart panel. v0.17.37.
      - ✅ **Self-hosted upstream URL.** `ELEVATION_TILES_URL`
        accepts an in-cluster Terrarium mirror (template:
        `{scheme}://{host}/{z}/{x}/{y}.png`). Chart wires
        `elevation.tilesUrl`. v0.17.37.
      - ✅ **Disk-backed read-through cache.** `ELEVATION_CACHE_DIR`
        persists fetched PNGs to a PVC-mounted path
        (`{dir}/{z}/{x}/{y}.png`, atomic temp-rename writes). Pod
        restarts don't re-fetch; an operator can rsync a pre-built
        Terrarium dump there for fully-offline operation, no
        upstream needed at runtime. Chart wires `elevation.cacheDir`.
        Tests in `internal/elevation/resolver_test.go`. v0.17.37.
      - ✅ **In-cluster tile mirror Deployment.** rivolt-infra
        `apps/maps/elevation/` (grouped with `osrm` and `tiles`
        under `apps/maps/`): NFS-backed PVC, a one-shot build
        Job that pulls the z=12 Texas Terrarium extract
        (x=834..984, y=1601..1743 = 21,593 tiles, ~1 GB) from
        the AWS public bucket via 20-way parallel curl, and an
        unprivileged-nginx Deployment serving
        `http://elevation.elevation.svc.cluster.local/{z}/{x}/{y}.png`.
        rivolt's `elevation.tilesUrl` in `apps/rivolt/values.yaml`
        points at it; the recorder's `/data/elevation/` PVC
        populates from the LAN with zero AWS egress at
        request time. Future region expansion = bump
        `ELEV_X/Y_MIN/MAX` env on the build Job and re-run.
- [ ] **Scale OSRM beyond a single state extract.** Current
      self-hosted OSRM (`apps/osrm/` in rivolt-infra) is pinned
      to `texas-latest` because that's where every recorded
      drive lives today. Going to full continental US (`us-latest`
      or `north-america-latest`) is non-trivial because OSRM does
      not support sharded / federated graphs — `osrm-extract`
      runs a global edge-expansion that needs the whole graph in
      RAM, and on the 32GB nuc11 it OOMs at ~32GB+ peak.
      Worse, the runtime `osrm-routed` mmaps a single graph file,
      so you can't run "north" + "south" pods and stitch results
      at the edges (cross-shard `/match` is undefined).
      Realistic paths:
      - **Bigger build host.** Run `osrm-extract` once on a 64GB
        cloud VM (Hetzner CPX51 ~€60/mo on-demand, or spot for
        ~€10 for the 60-min build), copy the produced `.osrm*`
        files onto the cluster's NFS, and run `osrm-routed`
        locally. Runtime mmap is ~10GB which the nuc11 can host.
      - **Regional shards + geo-router.** Run separate OSRM
        instances per region (us-west / us-central / us-east),
        write a tiny Go shim in `internal/osrm/` that picks the
        backend based on the trace's bbox. Breaks cross-region
        routes (rare in practice for EV drives but a real edge
        case for road-trippers).
      - **Switch to Valhalla.** Valhalla's tile-based architecture
        natively supports regional shards and is built to be
        rebuilt incrementally. Requires re-doing the chunking
        logic in `snapToRoads` because the response shape is
        different.
      Tracking issue should capture the per-option cost +
      runtime-RAM tradeoff and let the operator pick at deploy
      time.
- [ ] **Self-hosted geocoding (Photon).** Trip-planner slice 2
      (v0.17.132) ships with Open-Meteo geocoding for the
      destination text input — same off-LAN provider footgun
      OSRM/CARTO/elevation went through. Open-Meteo accepts
      city names, no API key, no per-user identifiers; but
      every "Dallas" / "Big Bend" the user types crosses the
      LAN boundary. The privacy posture matches existing weather
      wiring (`docs/ARCHITECTURE.md` "no GPS coordinates leave
      the box" already has the weather carve-out). Mitigation
      shape (when motivated):
      - **Photon** (https://github.com/komoot/photon) is the
        right fit — purpose-built autocomplete geocoder, ~3 GB
        index for North America extract, single-binary, no
        Postgres. Build time hours not days (vs full Nominatim
        which is ~50 GB + multi-hour Postgres import).
      - rivolt-infra layout would mirror `apps/maps/osrm` /
        `apps/maps/valhalla`: a one-shot build Job that pulls
        an OSM extract + Photon's prebuilt ES index from
        photon.komoot.io's mirror, then a Deployment serving
        `/api/v1/search`. Wire `RIVOLT_GEOCODING_BASE_URL` so
        the rivolt API reverse-proxies it the same way it does
        OSRM/Tiles, and switch `internal/geocoding/` from
        Open-Meteo to a self-hosted client.
      - Until then, Open-Meteo is the documented compromise:
        privacy-equivalent to weather (typed text, not lat/lon),
        and trivially swappable behind the existing
        `internal/geocoding.Client` interface.

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
- [x] **Reconnect-storm controls.** `internal/rivian.Breaker` is a
      sliding-window circuit breaker (60s window, trip on 3
      rate-limited or 8 outage classifications, 30s initial
      cooldown doubling to 5min on failed half-open probes); wired
      into `LiveClient.checkUpstream` so it gates every GraphQL
      call before the network and observes the classified outcome
      after. `StateMonitor.run` reconnect backoff is now
      `±50%`-jittered so concurrent vehicles whose sessions died
      from the same blip don't reconnect in lockstep, and a 50ms
      startup staggerer (`waitStaggerSlot`) spaces out the first
      WS connect of each subscription goroutine — cold-start of a
      pod with N existing leases now spreads over 50ms*N instead
      of firing all at once. Telemetry:
      `rivolt_rivian_breaker_state` (0/1/2 gauge),
      `rivolt_rivian_breaker_trips_total{reason}` counter.
- [x] **Boot-to-record integration test.** End-to-end test that
      stands up a testcontainers Postgres, drives the mock Rivian
      client through Login → Vehicles → State, writes a sample
      via `samples.InsertBatch`, and runs a real
      `leases.Coordinator` reconcile against the DB. Asserts a
      row landed in `vehicle_state` (with the right SoC + source)
      AND that `subscription_leases` was claimed by the test pod;
      also verifies re-inserting the same batch is idempotent.
      Lives at `internal/integration/boot_to_record_test.go`,
      gated behind `-tags integration` so the default unit run
      stays fast (~3s incl. container boot on a warm cache).
- [x] **Global upstream token bucket** in Redis, main + priority
      classes, Lua-scripted atomic check-and-decrement.
      `internal/ratelimit.Limiter` runs a single Redis EVAL per
      call: HMGET `tokens`+`ts`, refill by `elapsed * rate`
      capped at capacity, attempt subtract, on insufficient
      compute `retry_ms = ceil(short / refill * 1000)`, HSET back
      with PEXPIRE 10x refill-to-full (capped 1h). Two classes:
      `main` (60 cap, 2 rps — periodic pollers) and `priority`
      (20 cap, 1 rps — Login + reauth, set via
      `rivian.WithPriority(ctx)`). Wired into `LiveClient.checkUpstream`
      after the operator kill switch and the breaker; rejection
      surfaces as `*rivian.ErrRateLimited{RetryAfter}`. Fail-open
      on Redis errors (a Redis blip must not black-hole the
      upstream — the breaker still gates real 429s). Off when
      `RIVOLT_REDIS_ADDR` is unset, so single-binary local dev
      keeps working. Telemetry:
      `rivolt_rivian_ratelimit_blocked_total{class}` counter.

### Identity

- [x] **OIDC login** via `go-oidc`. Generic OIDC works against any
      compliant IdP (Google, Authentik, Hydra, Keycloak, Okta…).
      Configuration is per-provider env soup —
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

- [ ] **Invite-token user provisioning end-to-end with the IdP.**
      Today `POST /api/admin/users` only pre-provisions a rivolt
      DB row keyed by UUIDv5(username) — the OIDC user must
      already exist in the IdP (Kratos in our single-tenant
      deploy). The interim "generate password once, return it
      in the 201 response, admin sends it out-of-band" flow
      ships now and lets the admin endpoint create the Kratos
      identity via the admin API. Follow-up replaces that with
      first-class invites:
        - new `user_invites` table (token_hash, user_id,
          expires_at, consumed_at, created_by);
        - `POST /api/admin/users` returns
          `{id, invite_url, expires_at}` instead of a password;
        - public `POST /api/invite/{token}/accept` validates
          token, prompts for password (with complexity rules),
          writes the Kratos identity via the admin API, marks
          invite consumed, enables the rivolt user;
        - admin "Regenerate invite" button for expired/lost
          links;
        - delete path also removes the Kratos identity.
      Concurrent admin operations are serialized at the Kratos
      admin API level (it uses Postgres row-level locks).

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

## Trip planner UX backlog

Smaller cuts queued up; not phase-gated, ship as they make sense.

- [ ] **Park bands on the drive timeline** — overlay a subtle grey
      band over the speed/battery panels for any contiguous parked
      window ≥ 5 min (speed ≤ 0.5 mph). A drive that includes a long
      mid-trip stop currently reads as one continuous event; the
      band makes the "drive → park → drive" structure visible at a
      glance without altering the time axis.
- [ ] **Tappable route-table rows synced to map** — tap a charging
      stop in the table → map pans + the marker pulses. Tap a
      charger marker on the map → its row highlights and scrolls
      into view. Bidirectional selection state.
- [ ] **Collapse settings grid into Advanced disclosure** — show
      summary line "80% → 20%, All-Purpose, no adapter" with an
      Edit toggle so the form opens with route fields, not knobs.
- [ ] **"Add as waypoint" delta preview** — tap a charger marker
      to preview the time / SoC / cost delta before committing
      the stop.
- [ ] **Distance scale + total trip distance on the map.**
- [ ] **Distance-from-previous-stop / distance-from-origin
      columns** in the route table.
- [ ] **Stream the AI advice response** instead of waiting for
      the full blob (5–15 s feels much faster as it types).
- [ ] **Mobile**: compact "Chargers: DCFC ▾" dropdown on narrow
      screens; sticky `#` + Stop columns on horizontal scroll.
- [ ] **"No reachable charging stations" banner** — promote the
      empty-state from a buried subtitle to a prominent CTA
      ("Add a custom via-stop", "Widen starting SoC").
- [ ] **Weather overlay on the trip planner map.** Toggle next to
      the charger filter:
      - *Radar* — RainViewer precipitation tiles (free, no API
        key, ~2 h forecast). Auto-disable when the picked
        departure is > 2 h out.
      - *Along-route markers* — temp / wind / precip icons at the
        origin, destination, and ~hourly midpoints, sourced from
        Open-Meteo `FetchRange`. Works for any departure time
        including multi-day-ahead plans.
      Ship the radar first, layer markers on top for far-future
      trips. Optional OpenWeatherMap tiles (cloud / wind /
      temperature) are a follow-up if anyone wants them and is
      willing to configure a key admin-side.

---

## Notifications follow-ups

Push delivery is wired end-to-end; what's missing is event sources
for the non-charge-done categories.

- [ ] **Plug-in reminder** — fire when the vehicle has been parked
      at the configured home location below a configurable SoC
      threshold for > N minutes without the charge port plugged
      in. Needs a per-user state-watcher loop or a recorder hook
      keyed on `chargePortStatus`. Wire `pushSvc.NotifyPlugInReminder`.
- [ ] **Anomaly detector** — define what counts (phantom drain
      spike, sudden range drop vs. rolling avg, BMS thermal
      events). Either a deterministic rule pass after each
      drive/charge close, or a recap-flagged-it path. Wire
      `pushSvc.NotifyAnomaly`.
- [ ] **iOS PWA reliability pass** — confirm push payload size,
      tag-collapse behaviour, and the home-screen-required UX
      gotcha against the current iOS version.

---

## Maintenance backlog

Routine upgrades + tooling that haven't blocked launch but need
to land eventually.

- [ ] **Vite 8 upgrade.** Closes 3 moderate `npm audit` advisories
      (esbuild → vite → @vitejs/plugin-legacy) that are
      dev-server-only — they let a malicious website read
      responses from `npm run dev` on a maintainer's laptop, but
      don't ship in the production bundle. Three plugin bumps in
      one PR: `vite@^8`, `@vitejs/plugin-react@latest`,
      `@vitejs/plugin-legacy@^8` (the latter is a 5→8 jump, the
      actual risk). Verify `modernPolyfills` + `renderLegacyChunks`
      still behave on the production build (Safari 15+ target).
      `npm audit` will keep flagging the moderates until this
      ships.
- [ ] **Dependabot / GH Security tab** — auto-PRs for npm + Go
      bumps so we don't accumulate audit debt by hand.
- [ ] **CI lint gate.** `gosec`, `staticcheck`, and frontend
      eslint (with `--max-warnings 0`) run on every push.
      Currently lint warnings accumulate silently.
- [ ] **Per-user AI cost cap.** No daily budget on
      `/api/trips/plan/advice` today; a Reddit beta tester
      auto-firing analysis on every plan can rack up tokens.
      Soft cap (e.g. 20 calls / user / day, configurable in
      Admin → AI providers) protects the operator's bill.
- [ ] **RLS enforcement (stage 2 of migration 0008).** Split the
      app role into an owner + a runtime role, drop BYPASSRLS
      from runtime, FORCE the policies, add a chi middleware
      that pins `app.user_id` per request. The application code
      is already fully scoped — this moves enforcement from
      "by convention" to "by the database."
- [ ] **Drop OSRM, migrate drive map-matching to Valhalla
      `/trace_route`.** OSRM is currently kept around only for the
      drive detail page's GPS snap-to-road via `/match`; the trip
      planner and route geometry already go through Valhalla.
      Valhalla supports `trace_route` and `trace_attributes` with
      a different request/response shape than OSRM's `/match` —
      `DriveMap.tsx` needs to be rewritten to emit Valhalla's
      `shape_match=map_snap` request and decode the polyline +
      matched leg array it returns. Worth it because OSRM is a
      second routing engine running on nuc11 with its own ~3.5 Gi
      working set and a Texas-only graph (the trip planner needs
      US-wide so Valhalla is already authoritative). Until this
      lands, the cluster OSRM Deployment + the SPA's public-demo
      fallback at `router.project-osrm.org` both stay wired.

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
