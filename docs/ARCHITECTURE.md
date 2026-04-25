# Rivolt architecture

**Status**: design doc. Target scale: **1000+ vehicles, single region**.
**Staged path**: (1) right decisions now → (2) self-hosted k8s → (3) managed cloud.

This document captures the architectural decisions that are expensive
to retrofit and therefore have to be right the first time. It is
deliberately narrow: decisions that shape the code and the schema,
not decisions that can be flipped via config.

Decisions marked **now** must be in place before Rivolt accepts a
second user. Decisions marked **self-host** are needed before the
service runs on a k8s cluster with more than one replica. Decisions
marked **cloud** are for the hosted-SaaS graduation and can wait.

---

## Scale target

The design target is **1000 vehicles, single region**, not "10k users
someday". Designing for 1000 concurrent Rivian websocket subscriptions
already forces most of the decisions that would matter at 10k; going
further adds cost without lessons.

Order-of-magnitude resource budget at 1000 vehicles:

| Resource                               | Steady state | Peak        | Notes                                      |
| -------------------------------------- | ------------ | ----------- | ------------------------------------------ |
| WS connections to Rivian               | ~1000        | ~1000       | One per vehicle; long-lived                |
| Goroutines for subs + reconnect        | ~3000        | ~6000       | ReadLoop + writer + reconnect timer each   |
| Outbound REST to Rivian                | 0.5 req/s    | 8 req/s     | Adaptive refresh; spikes during commute    |
| Inbound API req/s                      | ~3 req/s     | ~30 req/s   | Live panel viewers + periodic polling      |
| Sample rows written per day            | ~2M          | ~4M         | Partition by month from day one            |
| AI token spend / month                 | ~8M tokens   | ~12M tokens | Weekly digest + ad-hoc; ~$30/mo at mini    |
| Postgres steady CPU                    | <15%         | <50%        | `db.t4g.medium` headroom                   |

**The bottleneck is not your CPU or Postgres. It is the Rivian upstream
and your relationship with it.**

---

## Decision 1 — Monorepo, single binary, single container

**Status**: now.

- One git repo (`rivolt/`) contains server (`cmd/`, `internal/`), web
  (`web/`), iOS (`ios/`), Helm chart (`deploy/helm/rivolt/`), and
  docs.
- One Go binary serves the API **and** embeds the built web SPA via
  `embed.FS`. No separate web container.
- One container image per release: `ghcr.io/apohor/rivolt:vX.Y.Z`.

**Why not split?** Splitting server / web / iOS into separate repos or
containers pays off with multiple teams or a published public API.
Neither applies. The costs (CORS, version skew between web and API,
doubled CI, doubled observability, doubled YAML) start immediately
and are permanent; the benefits are zero at current scale. `git
subtree split` makes future extraction cheap; merging is painful —
so asymmetry favors staying together.

**The only future split worth planning for** is extracting the Helm
chart to its own release flow *if* Rivolt becomes something third
parties self-host at scale. That is a v1.x concern.

---

## Decision 2 — Multi-tenant from day one, enforced at the database

**Status**: now.

### Data model: one user owns many vehicles

The ownership graph is `user → vehicles → {drives, charges, samples,
subscriptions}`. Users always have 1+ vehicles; multi-vehicle
households are a first-class case from day one, not a post-MVP
upgrade:

- R1T + R1S in the same garage is the canonical Rivian-household
  configuration. Designing for it now costs nothing.
- Rivian's `GetVehicles` query returns an array; anything that
  treats it as a single-vehicle fetch is already wrong.
- The database schema already reflects this:
  `vehicles.user_id → users.id`, and every drive/charge/sample row
  references `vehicles.id` (not `user_id` directly) so a car
  transferring between accounts would rewire cleanly.

**Invariants**:

- Every user-scoped API route resolves `user_id` from the session
  and filters. Every vehicle-scoped route additionally verifies
  the target `vehicle_id` belongs to that `user_id` before doing
  anything else — otherwise `/api/state/:id` with a guessed UUID
  reads another tenant's car.
- The UI never assumes "the vehicle" is singular. There is always
  a vehicle picker (even if it collapses to a single tile when the
  user has one car). The iOS `HomeView` today picks `vehicles.first`
  as a stopgap; adding the picker is on the v0.9 checklist before
  shipping to anyone other than the operator.

### Row-level enforcement

Every row in every user-scoped table carries a `user_id UUID NOT NULL`.
Every store method takes a `userID` and filters by it. There are no
"global" caches, no background workers that operate on "all users"
without tenant awareness.

The `user_id` column is present on **every** table — not just on
`vehicles`. `drives.user_id` is denormalized from
`vehicles.user_id`, kept consistent by a trigger, because the
alternative (join through `vehicles` on every query) makes RLS
policies slower and more fragile. The denormalization is the price
of making tenant isolation cheap at query time.

As a safety net below application code, every user-scoped table has
**Postgres row-level security** enabled:

```sql
ALTER TABLE vehicles ENABLE ROW LEVEL SECURITY;
CREATE POLICY vehicles_tenant ON vehicles
    USING (user_id = current_setting('app.user_id')::uuid);
```

On every request, the Postgres session sets `app.user_id` from the
authenticated context. A missing filter in Go code cannot leak
another tenant's data — RLS is the last line of defence.

Introducing a `tenant_id` (distinct from `user_id`) today is not
necessary. The rename path is a find-and-replace; the hard part is
that every row already has a column to rename. So:

- **Today**: `user_id` everywhere. `tenant_id` is an alias if anything.
- **If/when organizations become a tier**: add a `tenants` table, a
  `user_tenants` join, and `tenant_id` joins `user_id` in the RLS
  policies. The schema migration is mechanical because the column
  already exists on every row.

**Anti-patterns to refuse in code review**:

- Any `SELECT ... FROM <tenant-scoped table>` without a `user_id = $1`
  predicate.
- Any goroutine launched in `main()` or `init()` that iterates over
  all users / vehicles / sessions.
- Any cache keyed by something other than `(user_id, ...)`.
- Any settings singleton. All settings live in a table keyed by user.

---

## Decision 3 — Credential envelope encryption with a pluggable sealer

**Status**: implemented (phase 1). `rivian.Session` is sealed today;
AI keys + per-user VAPID private keys are next in line.

Every third-party credential stored by Rivolt — Rivian tokens, OAuth
refresh tokens, AI provider API keys, web-push subscription secrets —
is encrypted at rest with envelope encryption.

```go
// internal/crypto/sealer.go
type Sealer interface {
    // Seal encrypts plaintext with a fresh per-blob DEK, wraps the
    // DEK with the KEK, and returns an opaque blob. userID is bound
    // as AES-GCM additional data on BOTH the DEK-wrap and the
    // payload, so a cross-user ciphertext swap fails to open.
    Seal(ctx context.Context, userID uuid.UUID, plaintext []byte) ([]byte, error)
    Open(ctx context.Context, userID uuid.UUID, blob []byte) ([]byte, error)
    KEKID() string
}
```

The on-wire blob layout is:

```
RVL\x01 | len(kekID) u8 | kekID | len(wrapNonce) u8 | wrapNonce |
len(wrappedDEK) u16 | wrappedDEK | len(payloadNonce) u8 | payloadNonce |
len(payloadCT) u32 | payloadCT
```

- Per-blob 256-bit **DEK**, generated fresh on every `Seal`, used to
  encrypt the plaintext with AES-256-GCM.
- A **KEK** wraps the DEK with AES-256-GCM. The wrapped DEK is
  length-prefixed and embedded in the same blob as the ciphertext.
- `kekID` is stamped in the blob header *and* mirrored to
  `user_secrets.kek_id` so a rotation job can find every row that
  still uses an old KEK via a single indexed query.

Rotation model: `NewEnvSealerFromEnv("RIVOLT_KEK", "RIVOLT_KEK_ROTATION"...)`
accepts a primary key (used for every new `Seal`) plus a list of
retained old keys (consulted only on `Open`, by matching `kekID` in
the blob header). Cutover is overlap-then-sweep — deploy with both
keys, run a background re-seal job, then drop the old key.

The threat model is database-at-rest: a DB dump, an offsite backup,
a compromised replica. Plaintext in process memory is **out of
scope** — a root-on-the-host attacker wins regardless.

Implementations:

- **phase 1**: `EnvSealer` — KEK read from `RIVOLT_KEK` env var
  (format `<kekID>:<base64-of-32-bytes>`). A `RIVOLT_ALLOW_NOOP_SEALER=1`
  escape hatch exists for local dev; the env var is deliberately
  ugly so it can never slip into a helm chart or compose file.
- **phase 2 (k8s)**: same `EnvSealer`, KEK delivered via
  SealedSecrets so the plaintext KEK lives only in the pod env.
- **phase 3 (cloud)**: `KMSSealer` — KEK lives in AWS KMS / GCP KMS
  / Vault Transit. `Seal`/`Open` hit the KMS API to wrap/unwrap the
  tiny (32-byte) DEK; ciphertext still lives in Postgres.
  One-line swap — no caller changes.

Plaintext credentials only exist in memory at the edge of request
handling. They are never logged, never written to disk, never sent
to observability vendors. A separate `user_secrets` table (rather
than sealed columns on `users`) keeps the hot row small, enables a
future replica-role split that can read `users` but not
`user_secrets`, and lets GDPR-delete cascade through a single
foreign key.

Inspired in part by Rivian Roamer's published credential policy
("passwords never stored; tokens stored encrypted; 6-month forced
re-link") — except Rivolt documents the mechanism, not just the
policy.

---

## Decision 4 — Server-side opaque sessions, not JWTs

**Status**: implemented. `internal/sessions` + migration 0006 +
`auth.Service.WithSessionStore` adapter.

Session cookies carry an **opaque random 32-byte token**, not
signed claims. The server stores `HMAC-SHA256(pepper, token)` —
never the token itself — so a DB dump alone can't forge sessions
without also owning the host-local pepper (reuses the existing
`RIVOLT_COOKIE_SECRET` env var).

```sql
CREATE TABLE sessions (
    id              UUID PRIMARY KEY,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash      BYTEA NOT NULL,     -- HMAC(pepper, raw_token)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ,        -- soft-delete; janitor hard-deletes after grace
    user_agent      TEXT,
    ip_address      INET,
    device_label    TEXT
);
CREATE UNIQUE INDEX sessions_token_hash_idx ON sessions(token_hash);
CREATE INDEX sessions_user_id_idx ON sessions(user_id);
CREATE INDEX sessions_expires_at_idx ON sessions(expires_at);
```

Consequences:

- Revocation is `UPDATE sessions SET revoked_at = now()`. Instant,
  forensic-friendly (support can see a revoked row for a grace
  window before the janitor hard-deletes it).
- "Sign out all other devices" is `RevokeAllExcept(userID, keepID)`.
  Table-stakes for any multi-user product.
- `last_seen_at` writes are throttled to once per minute per
  session — avoids a DB write storm from live-reload tabs while
  keeping hijack-detection accurate to the minute.
- Session hijack detection: compare `ip_address` / `user_agent` on
  each request and flag drift. Not automated enforcement day one —
  just emit a metric, learn what normal looks like.
- Privilege-sensitive actions (password change, credential reset)
  should rotate the session ID.
- Proxy-header auth (`X-Forwarded-User` from oauth2-proxy etc.)
  stays **stateless** — skips the sessions table entirely, because
  the upstream IdP already owns session lifecycle. Only the
  built-in cookie login path creates rows.
- `ErrInvalidToken` is a single opaque sentinel covering
  bad-format / not-found / revoked / expired. Prevents side-channel
  on state.

**Why not JWT?** Stateless revocation is unsolved. Every "JWT with a
revocation list" design degenerates into a server-side session
lookup on every request, at which point JWT is paying complexity
costs for no benefit. The only JWT wins are in stateless-server
architectures, which Rivolt is not.

**Why HMAC+pepper and not bcrypt/argon2?** Session tokens are 32
bytes of cryptographic randomness, not passwords. Offline attack
resistance is dominated by entropy (256 bits) rather than work
factor. HMAC-SHA256 is cheap (no login latency cost), constant
time, and defeats the only realistic attack — a DB reader who
doesn't also own the host env.

---

## Decision 5 — Subscription ownership is sharded, leased, and reconciled

**Status**: self-host (becomes critical when N > 1 pods).

The central correctness challenge at 1000 vehicles: each Rivian
websocket subscription must be owned by **exactly one** pod at a
time. Not zero (no state updates), not two (duplicate upstream
load, duplicate writes, Rivian notices and throttles).

Design: **consistent hashing with Postgres-backed leases.** The lease
unit is the **vehicle**, not the user, because one user with two
cars can (and should) have those subscriptions on different pods:

- Sharding by `vehicle_id` spreads load evenly across pods when
  vehicle count per user is uneven (one household has 4 cars, most
  have 1).
- WS subscriptions are per-vehicle at the Rivian API level; there
  is no natural reason to colocate a user's two cars.
- The reconciliation math is simpler: the pod set and the vehicle
  set are independent inputs to the hash ring.

```sql
CREATE TABLE subscription_leases (
    vehicle_id      UUID PRIMARY KEY REFERENCES vehicles(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    pod_id          TEXT NOT NULL,
    acquired_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX ON subscription_leases (pod_id);
CREATE INDEX ON subscription_leases (expires_at);
```

`user_id` is denormalized onto the lease row (it's also on the
parent `vehicles` row) so the reconciliation loop can apply the
per-user `needs_reauth` kill switch (decision 8) without a join.

Every pod runs a **reconciliation loop** every ~30s:

1. Read the current pod set from k8s (via the downward API + a
   Service that publishes pod IPs, or via the k8s API with minimal
   RBAC). Sort deterministically; this is the hash ring.
2. For each vehicle this pod *should* own (per consistent hash),
   acquire or renew its lease:
   - `INSERT ... ON CONFLICT (vehicle_id) DO UPDATE SET pod_id = $me,
     expires_at = now() + interval '2 minutes' WHERE
     subscription_leases.pod_id = $me OR subscription_leases.expires_at < now()`
   - On success, ensure a WS subscription goroutine is running.
3. For each lease this pod *no longer* should own, release it
   (`DELETE ... WHERE pod_id = $me AND vehicle_id = $v`) and stop
   the goroutine.
4. On SIGTERM, release all leases synchronously before exit.

Properties:

- Pod death → leases expire in ≤2 min → another pod reconciles and
  takes over. During that window the vehicle has no live WS; REST
  fallback covers it.
- Rolling deploys → graceful shutdown releases leases, other pods
  pick them up within one reconciliation cycle (~30s).
- Hash-ring changes don't require coordination: every pod recomputes
  the ring independently from the (authoritative) pod set, and the
  leases serialize the result.

**Anti-patterns to refuse**:

- Every pod subscribing to every vehicle. Doubles upstream load per
  replica; Rivian will rate-limit you.
- Leader election via `k8s.io/client-go/tools/leaderelection` to make
  exactly one pod do subscriptions. Single point of failure for all
  vehicles, doesn't scale past one pod's memory.
- Lease TTLs shorter than the reconciliation interval (thrashing) or
  longer than 5 min (slow failover).

---

## Decision 6 — Reconnect storms are managed, not prevented

**Status**: self-host.

When a pod restarts, a Rivian endpoint blips, or a network partition
heals, naïve reconnect behaviour produces a thundering herd against
Rivian — exactly the traffic pattern that gets third-party clients
banned.

Controls:

- **Jittered exponential backoff** on every subscription, per-connection:
  `delay = min(base × 2^attempts, cap) × (0.5 + rand())`.
  `base=1s`, `cap=5min`. The `rand()` de-synchronizes the herd
  across vehicles; pure exponential alone doesn't.
- **Startup stagger**: when a pod boots and acquires N leases,
  spread initial WS connections over `N × 50ms` so 333 vehicles
  don't all hit Rivian in the first 100ms.
- **Circuit breaker on the upstream as a whole**: if Rivian returns
  5xx or 429 for > 10 requests in a 60s window, trip the breaker
  for 30s. Every subscription backs off; new subscriptions don't
  attempt at all. Emit a loud metric and structured log.
- **Kill switch**: a feature flag (row in a `flags` table or
  `config` table, read every ~10s) that stops all outbound Rivian
  traffic immediately. Togglable without a deploy. First thing to
  reach for if Rivian contacts you about traffic patterns.

---

## Decision 7 — Global upstream rate limit in Redis

**Status**: self-host.

Per-vehicle adaptive refresh is fine at small scale. At 1000 vehicles
with 200 simultaneously in `go` state during evening commute, the
outbound REST rate to Rivian can spike past 100 req/s. That's too
much.

Design: a **global token bucket** in Redis.

```
KEY rivolt:rivian:rest            → { tokens, last_refill_at }
KEY rivolt:rivian:rest:priority   → { tokens, last_refill_at }
```

- Main bucket: capacity 60, refill 30 tokens/s (tuned below safe
  ceiling, determined empirically).
- Priority bucket: capacity 20, refill 10 tokens/s. Reserved for
  user-initiated requests (live panel actively open, explicit
  refresh button).
- Check-and-decrement via a Lua script so the test-and-take is atomic
  across pods.

Why not Postgres? Row-level contention on a single bucket row is
poor across many pods; Redis' single-threaded nature gives you the
atomicity naturally. This is the one coordination primitive that
earns Redis a place in the stack.

Redis is **only used for transient coordination state**: token
buckets, ephemeral rate-limit counters, soft locks. Any Redis data
loss is survivable without user impact. Therefore: no Redis
persistence, no Redis clustering, a `cache.t4g.micro` survives
1000 vehicles with headroom.

---

## Decision 8 — Rivian errors are classified, not propagated

**Status**: now (the wrapper), self-host (the lifecycle automation).

Every Rivian API call goes through a single wrapper that classifies
errors into a closed set:

```go
type UpstreamErrorClass int

const (
    // Transient — retry with backoff.
    ErrClassTransient UpstreamErrorClass = iota
    // Upstream outage — circuit-break and retry later.
    ErrClassOutage
    // User action required — invalid token, expired password, MFA
    // reset. Stop calling Rivian for this user. Mark credentials
    // as "needs re-auth". Notify via push / email. Resume on
    // successful re-auth.
    ErrClassUserAction
    // Rate limited by Rivian. Back off globally.
    ErrClassRateLimited
    // Unknown — treat as transient but log loudly.
    ErrClassUnknown
)
```

The credential-failure workflow for `ErrClassUserAction` is the
single biggest determinant of your support load at 1000 vehicles.
Without it, one user with a revoked token causes thousands of
failed requests per day on your shared Rivian IP, which gets
*everyone* throttled.

Application code never sees raw HTTP errors from the Rivian client —
only the classified enum. Classification rules are centralized and
unit-tested against canned responses.

---

## Decision 9 — Time-series data is partitioned from day one

**Status**: now.

`samples` is the big table: ~2M rows/day at 1000 vehicles, ~700M
rows/year. Two options:

**Option A — Native Postgres partitioning** (picked):

```sql
CREATE TABLE samples (
    vehicle_id UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL,
    at         TIMESTAMPTZ NOT NULL,
    -- ... payload columns
    PRIMARY KEY (vehicle_id, at)
) PARTITION BY RANGE (at);

CREATE TABLE samples_2026_04 PARTITION OF samples
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
```

A monthly cron (or `pg_partman` extension) creates next month's
partition before month-end. Dropping old data:
`DROP TABLE samples_2025_01` — instant, no lock on live partitions.

**Option B — TimescaleDB**:

Adopt when the first of these is true:
- Storage cost of vanilla Postgres samples > $50/mo.
- Query latency on multi-month aggregations > 500ms.
- Team is comfortable with a non-vanilla Postgres extension.

At 1000 vehicles, option A is enough. Option B becomes attractive
at ~10× that scale because of columnar compression (~10× size
reduction on samples-style data).

**Same treatment** for `drive_samples`, `charge_samples` — anything
with sample-row cardinality. `drives` and `charges` (one row per
session) stay unpartitioned; they're orders of magnitude smaller.

---

## Decision 10 — Observability is vendored, not self-hosted

**Status**: self-host (stub now, real in self-host phase).

- **Logs**: structured JSON (`slog`), with `user_id`, `vehicle_id`,
  `request_id`, `trace_id` injected from context. Shipped to a
  vendor (Grafana Loki, Datadog, Honeycomb — pick one, don't run
  your own).
- **Metrics**: Prometheus `/metrics` endpoint. Histogram buckets for
  request latency, counters for Rivian API results per class,
  gauges for subscription lease counts per pod, AI token spend per
  user. Scraped by the vendor's collector.
- **Traces**: OpenTelemetry SDK with the OTLP exporter. Spans for
  every HTTP handler, every Rivian upstream call, every AI
  provider call. Vendor-neutral at the SDK layer; switching
  vendors is config.

Self-hosting Prometheus + Grafana + Loki for a user-facing service
is a full-time job. Grafana Cloud's free tier (10k series, 50GB
logs/mo) handles 1000 vehicles with headroom. Pay-as-you-go beyond.

**What gets a metric**: every outbound Rivian call (classified),
every AI completion (provider + token spend), every subscription
lease acquire / release, every session created / revoked, every
authentication failure.

---

## Decision 11 — Secrets via the operator, not the repo

**Status**: self-host.

No secret ever lives in git, not even encrypted. `values.yaml` has
only non-secret configuration. Secrets arrive at runtime via:

- **self-host**: `kubernetes.io/tls` Secrets + `kubernetes.io/generic`
  Secrets, managed by the operator out-of-band (`kubectl create
  secret` or SealedSecrets for GitOps).
- **cloud**: **External Secrets Operator** (ESO) pulls from AWS
  Secrets Manager / GCP Secret Manager / Vault. Secret names
  referenced in Helm values; actual values fetched at pod start.

The KEK (decision 3) is itself a secret delivered by this path.

---

## Decision 12 — Identity moves to OIDC before cloud

**Status**: self-host (username/password still works; OIDC as the
recommended path). cloud (OIDC is the only path; password is
off).

Username/password is fine for a single self-hosted operator. It is
not fine for a hosted SaaS with 1000 users:

- Password reset flows are a support burden.
- Breach response (rotate every password) is operationally nasty.
- MFA bolt-on is more code than it should be.

Path: add **OIDC login** alongside username/password in self-host
phase. Users sign in with Google or GitHub. The OIDC client stores
only the `sub` claim mapped to a `users.id`. Password login stays
working so self-hosters don't need an IdP.

In cloud phase: password login is disabled by default. Self-hosters
flip a flag to re-enable it; hosted deployment runs OIDC-only.

---

## Decision 13 — The Rivian upstream relationship is managed

**Status**: self-host (before accepting users who aren't you).

Non-code but architecturally important:

- **User-agent**: outbound requests to Rivian impersonate the iOS
  Rivian Owner App (`User-Agent: RivianApp/4400 ...`,
  `apollographql-client-name: com.rivian.ios.consumer`,
  `apollographql-client-version: 3.6.0-4400`) because an
  unallowlisted UA is the single easiest signal for Rivian to
  block us on. We ship an `X-Rivolt-Version` trailer so our own
  identity still travels with every request — operators and
  Rivian's on-call can tell Rivolt traffic apart without us
  advertising \"not the iOS app\" to their bot-detection first.
  Flipping to an honest `Rivolt/<version>` UA is a phase-3
  conversation to have *with* Rivian dev-rels (see below), not a
  unilateral decision from us.
- **Dev-relations outreach**: before running a public instance,
  email Rivian's developer/API contact and introduce yourself.
  Precedents for EV telematics third-parties: Teslafi, Teslamate,
  Roamer. An unofficial client Rivian's on-call knows about is
  tolerated; one that surprises them in an incident is killed.
- **Kill switch**: decision 6. Must be togglable in seconds,
  without a deploy.
- **Aggressive back-off on 429/5xx**: global circuit, not
  per-subscription.

---

## What we are explicitly *not* doing

To keep the design honest, the following are non-goals and will be
pushed back on if they come up in code review or planning:

- **Microservices.** One binary, one image, at every scale target
  here. A "subscription worker" split *might* make sense at 100k
  vehicles; it is net-negative at 1k.
- **Frontend/backend container split.** No CDN bill to cut, no
  separate team, no SSR requirement. All cost, no benefit.
- **Separate repos per app.** Monorepo wins on atomic cross-cuts,
  single docs surface, single CI story.
- **Multi-region.** 1000 vehicles fit in one region. Latency to
  Rivian upstream dominates; co-locate with them.
- **Read replicas.** Vanilla Postgres on a `db.t4g.medium` handles
  the write/read mix at this scale. Add replicas when
  `pg_stat_statements` shows read contention.
- **Kafka / NATS / any event bus.** `LISTEN/NOTIFY` + periodic
  reconciliation covers every distribution need here.
- **General-purpose caching (Redis) for data.** Redis is scoped to
  coordination primitives (rate limit buckets). Data caching is
  in-process LRU where it's worth anything at all.
- **Self-hosted Postgres in k8s** for cloud phase. Managed
  service, always. In self-host phase a k8s-native operator
  (CloudNativePG) is acceptable.
- **JWTs**. See decision 4.

---

## Phased path

### Phase 1 — Right decisions now (pre-k8s)

Code-level work that can land today, no infra dependencies.

- [ ] Every store method takes `userID`. Every query filters by it.
      No singletons.
- [ ] Enable RLS on every user-scoped table. Pod sets
      `app.user_id` on every request.
- [ ] `Sealer` interface + `EnvSealer` implementation. Migrate
      every existing credential column to sealed storage.
- [ ] Server-side sessions table. Replace any signed-cookie or JWT
      paths with opaque IDs.
- [x] Rivian upstream wrapper with error classification + per-user
      credential-failure flag. Plumb the flag through every call
      site.
- [ ] Partition `samples` by month. `pg_partman` or a one-off cron.
- [x] Kill-switch flag (DB-backed, polled every ~10s). Single
      bool, global scope.
- [x] User-agent on all outbound Rivian calls.

### Phase 2 — Self-hosted k8s cluster

Deploy on the operator's own k8s (Synology + k3s today, a dedicated
cluster later). Multi-replica. All correctness plumbing is real.

- [ ] Helm chart at `deploy/helm/rivolt/`. Single Deployment, HPA
      3–8 on CPU. ConfigMap + Secret references.
- [ ] Subscription lease reconciliation loop + schema.
- [ ] Global upstream token bucket in Redis. One Redis Deployment
      in-cluster; no persistence.
- [ ] Jittered reconnect + startup stagger + circuit breaker.
- [ ] Structured logs shipped to Grafana Cloud. `/metrics` scraped
      by kube-prometheus-stack. OTLP traces to Grafana Tempo.
- [ ] OIDC login (Google + GitHub) added alongside password
      login. Password login still default for self-hosters.
- [ ] CloudNativePG (or an external managed Postgres) as the
      database. Never a single-pod StatefulSet with local PVC.
- [ ] SealedSecrets for secret delivery. KEK sealed the same way.
- [ ] CI: GitHub Actions builds multi-arch image on tag, pushes
      to ghcr, packages Helm chart, publishes to GitHub Pages
      chart repo.

### Phase 3 — Managed cloud (production SaaS)

Public deployment. Architecture shape is the same — only the infra
primitives and the auth posture change.

- [ ] Managed k8s (EKS / GKE / DOKS). One cluster, one region.
- [ ] Managed Postgres (RDS / Cloud SQL / Neon). Automated backups,
      PITR on. TimescaleDB extension if adopted.
- [ ] Managed Redis (ElastiCache / Memorystore). `t4g.micro` class.
- [ ] External Secrets Operator + cloud secret manager. KEK lives
      in cloud KMS; `Sealer` swapped to `KMSSealer`.
- [ ] cert-manager + Let's Encrypt for TLS. Ingress behind a
      managed L7 load balancer.
- [ ] CloudFlare in front for DDoS + WAF.
- [ ] OIDC-only authentication. Password login disabled.
- [ ] Dev-relations outreach to Rivian completed.
- [ ] Terms of Service + Privacy Policy. GDPR delete + export
      endpoints.
- [ ] Billing (Plus-tier) if applicable.

Expected managed-infra cost at 1000 vehicles: **$100–150 / month**.

---

## Change control

This doc is authoritative. Changes to it require a PR with a
rationale; "code says X, doc says Y" should always be resolved by
updating the doc or updating the code, not by ignoring the
mismatch.
