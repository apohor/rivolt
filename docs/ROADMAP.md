# Rivolt — roadmap

## 2-week MVP slice

Goal: a self-hostable binary that logs in with Rivian credentials, shows live vehicle state, drive history, and a AI integration. Enough to dogfood on your own Synology and test it locally.

## Post-MVP

Ordered by expected value, not by time.

### v0.8 — Kubernetes backend + server-side auth

Today Rivolt is a single-tenant binary: one Postgres instance, one
operator, credentials stored locally, sessions verified against a
single-user table. That's great for self-hosters and bad for anything
else. This milestone is the minimum cut to run Rivolt as a real
service.

- [ ] **Helm chart** (`deploy/helm/rivolt/`) — Deployment + Service +
      Ingress, ConfigMap for non-secret settings, Secret for VAPID /
      AI keys. Postgres is expected to be operator-provided (Bitnami
      subchart, CloudNativePG, or an external managed instance) — the
      chart takes a DSN, it doesn't ship the database.
- [ ] **Container hardening** — readiness/liveness probes on
      `/healthz`, non-root user, read-only rootfs, resource
      requests/limits, `PodDisruptionBudget`.
- [ ] **Horizontal scale-out** — now that the store is Postgres, audit
      the app for single-replica assumptions: in-process caches,
      background workers that assume a singleton, the Rivian websocket
      subscription (needs leader election via Postgres advisory locks
      or a lightweight coordinator pod).
- [ ] **Server-side auth rewrite**
  - Replace the single-user local table with a proper identity layer:
    session tokens in a server-side store (not just signed cookies),
    with revocation + device listing.
  - OIDC login (Google + GitHub first) so users don't manage a
    password at all. Keep username/password as a fallback for
    self-hosters who don't want an IdP.
  - Per-user Rivian credentials encrypted with a server-held KEK that
    itself comes from an env var / KMS reference — no plaintext keys
    on disk.
  - Rate limiting on `/api/auth/*` (chi middleware + in-memory bucket,
    or Redis if Postgres is selected).
- [ ] **Observability** — structured logs with request IDs, Prometheus
      `/metrics` endpoint (request latency, Rivian API errors, AI
      token spend per user), OpenTelemetry traces behind a flag.
- [ ] **CI → registry** — GitHub Actions builds multi-arch images to
      `ghcr.io/apohor/rivolt`, Helm chart published to GitHub Pages
      chart repo, SBOM + cosign signature attached to every release.

### v0.9 — native iOS app

The PWA gets most of the way there, but push notifications on iOS are
still second-class (no live activities, no background refresh, badge
count limitations) and the live panel wants CarPlay integration that
a web app can't touch. This milestone is a thin native client that
talks to the same backend.

- [ ] **SwiftUI app** — shared models generated from an OpenAPI spec
      the Go server now emits (one source of truth; removes
      type-drift risk). Target iOS 17+ so we can use the modern
      Observation framework and `@Observable`.
- [ ] **Auth** — OIDC via ASWebAuthenticationSession, tokens in the
      iOS Keychain. No Rivian credentials ever touch the device —
      the server holds them under the same per-user KEK as v0.8.
- [ ] **Live panel** — same data as the web `/live` page, rendered
      natively. WebSocket subscription with background reconnect.
- [ ] **Live Activities + Dynamic Island** — "Charging to 80%, 42
      min left" on the lock screen during an active charge session.
- [ ] **Push notifications** — swap VAPID/web-push for APNs on iOS;
      server sends to both channels based on which the user
      registered. Reuse the existing `internal/push` abstraction.
- [ ] **Widgets** — small/medium home-screen widget with SoC, range,
      and last-known location. StandBy mode variant.
- [ ] **CarPlay** — "Next charger on route" + "Remaining range" cards.
      Read-only in v0.9; writing commands (precondition, lock/unlock)
      waits until the Rivian API surface is stable enough to trust.
- [ ] **Install path: Xcode Run on a tethered iPhone** — single-device
      scope for v0.9. Paid Apple Developer Program membership ($99/yr)
      from day one so push, CarPlay, Live Activities, and App Groups
      entitlements all work; a free Personal Team would block most of
      the milestone. Archive/Ad-hoc/TestFlight are explicit non-goals
      until the app is worth sharing — keeps signing, provisioning,
      and Beta App Review off the critical path.

### Post-iOS — deferred product work

Once v0.8 (k8s + server-side auth) and v0.9 (native iOS) are in
place, the product surface opens up. These were originally planned
as v0.2–v0.5 on the single-binary track, but none of them make sense
to ship before Rivolt is a real service with a real mobile client.
Ordered by expected value, not by time.

- [ ] **Home-energy foundation** — Enphase Envoy + Tesla Powerwall
      local API adapters; "schedule charge to solar peak" scheduler;
      "effective cost per kWh after solar offset" line in charge
      detail.
- [ ] **Overland mode** — GPX export per drive, photo attachment per
      waypoint, offline OSM tile caching (pre-downloaded bounding
      box), trail logbook export (GPX + photos + markdown).
- [ ] **Multi-vehicle household** — support > 1 vehicle per account,
      "which vehicle for this trip" recommendation, shared home
      charger queue (2 vehicles, 1 wall connector).
- [ ] **Plus tier** — license-key validation, gate home-energy +
      overland + multi-vehicle behind Plus, Stripe integration,
      Privacy Policy + Terms of Service. Pricing decision feeds back
      into the iOS App Store listing model (paid vs free-with-Plus)
      once v0.9 graduates from single-device install to a public
      release.

### v0.6+ — fleet

- [ ] Mileage reports (IRS-grade CSV export)
- [ ] Per-driver attribution (who was driving when)
- [ ] SSO (Google Workspace, Microsoft Entra)

## Non-roadmap

These are explicit "not now, probably never":

- Social / leaderboards (Roamer owns this)
- Android Auto integration (requires OEM partnership we won't get —
  CarPlay is on the roadmap because Apple lets third parties ship
  EV/charging categories without OEM sign-off)
- Inventory scraping
