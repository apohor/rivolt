# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repo pair

This is the **application** repo. The companion **infra** repo lives at `../rivolt-infra` (GitOps for the k3s cluster that runs `rivolt.dev`). Releases cross both repos — see "Ship it" below.

## Common commands

```bash
make dev-api      # Go server (needs DATABASE_URL pointing at a running Postgres)
make dev-web      # Vite only — useful when iterating on the SPA against a remote API
make web          # build SPA into internal/web/dist (the embed.FS source)
make build        # CGO_ENABLED=0 binary at bin/rivolt with embedded SPA + version stamp
make test         # go test ./...
make fmt tidy
make clean        # also recreates internal/web/dist with a placeholder so the binary still builds
```

Single-test runs: `go test ./internal/leases/ -run TestCoordinator -v`.

Frontend lint/typecheck (run from `web/`): `npm run lint`, `npm run typecheck`.

## Ship it (cross-repo release flow)

Two cadences:

**Preview (continuous, no tag).** Push to `main`. CI publishes
`ghcr.io/apohor/rivolt:sha-<short>` plus mutable `:main`, then the
`bump-preview` job in `.github/workflows/build.yml` rewrites
`image.tag` in `rivolt-infra/apps/rivolt-preview/values.yaml`. ArgoCD
re-reads the chart at `main` and rolls preview within ~2 min. No
chart-version bump required for preview iteration.

**Prod (tag, manual promote).** Once the work has soaked on
`preview.rivolt.dev` and you want to ship it:

1. Bump `deploy/helm/rivolt/Chart.yaml` to the next `vX.Y.Z` (both
   `version` and `appVersion`).
2. Commit, tag `vX.Y.Z`, push the tag (`git push origin vX.Y.Z` —
   `--follow-tags` skips lightweight tags).
3. CI publishes `vX.Y.Z` / `X.Y` / `latest`. **Preview is not touched.**
4. Bump `rivolt-infra/apps/rivolt/app.yaml` `chart.version: vX.Y.Z`
   and push. ArgoCD reconciles prod.

The chart version on the prod app **must equal** a pushed git tag,
otherwise ArgoCD fails with `unable to resolve 'vX.Y.Z' to a commit
SHA`. Preview is exempt (it tracks the `main` ref directly).

## Architecture invariants (load-bearing — read `docs/ARCHITECTURE.md` before changing)

**One git repo, one Go binary, one container image.** `cmd/rivolt` serves the API and embeds the built SPA via `embed.FS` (`internal/web/Assets()`). No separate web container. iOS app and Helm chart live alongside.

**Multi-tenant from day one, enforced at the database.** Every user-scoped table carries `user_id UUID NOT NULL` and has Postgres RLS enabled. Every store method takes a `userID`. `cmd/rivolt/main.go` constructs **factories** (`drives.NewFactory`, `charges.NewFactory`, `samples.NewFactory`, `settings.NewFactory`, `push.NewFactory`); handlers resolve `factory.For(uid)` per request. There is no singleton "current user", no global cache that isn't keyed on `(user_id, ...)`, no goroutine that iterates all users. Adding a new data-plane store means adding a factory and threading `uid` through.

**Rivian client is per-user too.** `rivian.AccountRegistry.For(uid)` returns that user's `*LiveClient`. The shared `Breaker`, `RateLimit`, kill-switch, and reauth sink are wired in at construction (see `buildLive` in `main.go`). The boot-time hydrate sweep (`db.ListUsersWithRivianSession`) pre-warms clients + monitors **before** the HTTP server accepts traffic — pod restarts must be invisible to the data plane.

**Subscription ownership is leased, not elected.** `internal/leases.Coordinator` polls Postgres every ~30s and reconciles which vehicles this pod owns based on a consistent hash. `monitorRegistry.EnsureSubscribed`/`Unsubscribe` are the side-effect callbacks. Lease TTL is 2 min; SIGTERM releases all leases synchronously **before** HTTP shutdown so peers can pick up vehicles while we drain. Never write a goroutine in `main()` that subscribes to "all vehicles" — that's the multi-replica thundering-herd anti-pattern.

**Credentials are envelope-encrypted via `crypto.Sealer`.** `Sealer.Seal(ctx, userID, plaintext)` binds `userID` as AES-GCM AAD on both DEK-wrap and payload, so cross-tenant ciphertext swap fails. `RIVOLT_KEK` is required (`<kekID>:<base64-32-bytes>`); `RIVOLT_KEK_ROTATION` is the comma-separated retain list for rolling rotations. `RIVOLT_ALLOW_NOOP_SEALER=1` is dev-only — deliberately ugly so it can never slip into a helm chart. The `secrets.Store` (table `user_secrets`) is the canonical sink; new credential types should go through it, not back-fill into other tables.

**Sessions are server-side opaque tokens, never JWT.** `internal/sessions` HMACs the raw token with `RIVOLT_COOKIE_SECRET` as pepper before storing. Revocation is a row update. "Sign out all other devices" is a single store call. Don't reach for JWTs — see ARCHITECTURE decision 4 for why.

**Three auth issuers.** Static cookie creds, trusted-proxy header (`RIVOLT_TRUSTED_PROXY_CIDR` allowlist), OIDC (`RIVOLT_OIDC_PROVIDERS`). When none are configured the API stays open (legacy single-tenant docker-compose UX); presence of any flips `authEnforced`. Hydra (`HYDRA_ADMIN_URL`) + Kratos (`KRATOS_ADMIN_URL`) drive the federated sign-in bridge mounted at `/api/auth/hydra/*`. `RIVOLT_AUTH_BYPASS_USER` is debug-only and refuses to start with `RIVOLT_SECURE_COOKIE!=false`.

**Time-series tables (`samples`, `vehicle_state` and similar) are partitioned by month.** A `samples.PartitionJanitor` goroutine creates next-month partitions ahead of time; without it pods that run past the last pre-created partition reject writes with `no partition of relation … found for row`.

**Errors from Rivian go through one classifier.** `rivian.UpstreamErrorClass` (`Transient | Outage | UserAction | RateLimited | Unknown`). `UserAction` flips `needs_reauth` for that user (persisted via `db.SetNeedsReauth`) and stops calling Rivian on their behalf. Application code never sees raw HTTP errors from the client.

**Operational kill switch.** `internal/flags` polls a DB-backed flag every ~10s. Every outbound Rivian call gates on it (`WithUpstreamGate` returning `ErrUpstreamPaused`). First lever to pull if Rivian contacts us about traffic patterns — togglable without a deploy.

## Frontend

`web/` is React 18 + TypeScript + Vite + Tailwind v3 + TanStack Query + uPlot. Tailwind only (no CSS-in-JS). Browserslist deliberately tight — Safari 15+, Chrome/Firefox/Edge 100+. `npm run icons` regenerates the PWA icon set.

## Telemetry

Structured `slog` JSON by default (`RIVOLT_LOG_LEVEL`, `RIVOLT_LOG_FORMAT`). `logging.NewContextHandler` wraps the inner handler so every log line emitted while serving a request automatically gets `request_id`/`user_id`/`vehicle_id`/`trace_id` from context — don't pass these as explicit log fields, put them in context. OTel + Prometheus `/metrics` are always wired; `RIVOLT_OTEL_ENABLED` toggles the OTLP exporter.

## Don'ts (architectural code-review reflexes)

- `SELECT ... FROM <tenant-scoped table>` without a `user_id = $1` predicate.
- Goroutines launched in `main()`/`init()` that iterate all users / vehicles / sessions.
- Caches keyed by anything other than `(user_id, ...)`.
- Settings singletons. Settings live in tables keyed by user.
- New JWT paths. Use the opaque sessions store.
- Frontend/backend container split, microservice splits, multi-region, Kafka, Redis-as-data-cache. See ARCHITECTURE "What we are explicitly *not* doing".
