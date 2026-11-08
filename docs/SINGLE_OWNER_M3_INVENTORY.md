# Single-owner M3: where a request can reach Rivian, and what each becomes

Status: M3-0 deliverable. Checked against `origin/main` at 8f297176 (the plan was written at d9073f9a). Method: every
`clientFor(` / `monitorFor(` in non-test code, every other non-test user of an account's Rivian client, and the janitor and sync
jobs that reach Rivian without a request. "Owner" means the pod that holds the user's lease group.

## What changed since the Nov 1 plan

- The seven raw probe routes (state/debug, charging-schema, battery-temp, parallax-raw, supported-features, charging-field,
  charging-frames) are mounted only when `RIVOLT_ADMIN_DEBUG_PROBES` is on (preview, local), like the five admin probes. With the flag
  off (prod) they answer 404. M3-5 shrinks to: with the flag on, a non-owner answers 409.
- `/api/vehicles` stops calling Rivian per request: M3-5a, a separate PR (backend-3).
- The janitor's REST confirm (`confirmStillCharging` -> `restState`) is owner-only (see below).
- The 22 `clientFor`/`monitorFor` call sites are the same 22, now at the lines below.

## Call sites in `internal/api/api.go`

| # | Line | Route | Rivian? | End state | Step |
|---|---|---|---|---|---|
| 1 | 1048 | GET /vehicles | yes, every request | reads Postgres and published vehicle info; the upsert moves to the owner | M3-5a |
| 2 | 1074 | GET /vehicles/{id}/ota/notes | yes, per tap (a fresh signed URL) | forwarded kind `ota_notes` | M3-4b |
| 3 | 1103 | GET /vehicles/{id}/now | no (published frame time) | already owner-aware; test only | T20 |
| 4 | 1127 | GET /state/{id} | only on the `fetch` branch | that branch becomes forwarded `fresh_state` on a non-owner | M3-3 |
| 5 | 1134 | GET /state/{id}/debug | yes (raw) | mounted only with AdminDebugProbes; non-owner 409 | M3-5 |
| 6 | 1138 | GET /state/{id}/fresh | yes | forwarded `fresh_state` | M3-3 |
| 7 | 1141 | GET /live-session/{id} | only with no monitor (stub/dev) | unchanged (the monitor path uses the owner's published accumulator); test only | T20 |
| 8 | 1144 | GET /live-drive/{id} | no, but owner-local memory | reads the owner's published drive accumulator (not a forward) | M3-5b |
| 9-14 | 1148-1168 | GET charging-schema, battery-temp, parallax-raw, supported-features, charging-field, charging-frames | yes (two open a Parallax subscription) | mounted only with AdminDebugProbes; non-owner 409 | M3-5 |
| 15-19 | 1455-1480 | admin trips/plan/raw, gql/type, gql/enum, gql/raw, trips/plan/diag | yes | admin plus AdminDebugProbes; non-owner 409 | M3-5 |
| 20 | 1672 | POST /trips/plan | yes, per leg miss | forwarded `plan_legs` via `forwardPlanner`; cache and quota stay on the requester; Pro gate and quota are checked on the requester before forwarding (a 402 makes zero forwards) | M3-4 |
| 21 | 1695 | GET /trips/saved/{id}/active | no (monitor state) | published state; test only | T20 |
| 22 | 1751 | POST /trips/plan-multiday | yes, N legs | forwarded `plan_legs`, one forward per leg | M3-4 |

## Rivian reached without `clientFor`

| Path | Where | End state |
|---|---|---|
| Admin refresh of a user's Rivian session and admin vehicle sync | `internal/api/admin.go`: `POST /api/admin/users/{id}/refresh-rivian-session` (`RefreshSession`) and `POST /api/admin/users/{id}/sync-rivian` (`primeUserVehicles`), both behind `requireRivianActions` | **dead on prod by design, not forwarded** (DA): the admin deployment runs `RIVIAN_CLIENT=stub` and `RIVOLT_RIVIAN_LIVE_FORBIDDEN=true` and builds no Redis client, so both answer 409 "Rivian is disabled on this host", and the public hosts 404 `/admin`. Restoring them would give the admin host Redis credentials, the forward key and, through the owner, a Rivian capability it deliberately lacks: a feature request with its own design, not M3 |
| Login, MFA, logout under `/settings/rivian` | `d.Accounts` | any pod, throwaway client; the `adopt` nudge afterwards (M3-1) |
| Connection-state fallback | api.go `d.Accounts.For(uid).NeedsReauth()` | reads the local account object; moves to the Postgres `needs_reauth` column at M4 |
| Stale-charge janitor REST confirm | `internal/rivian/monitor.go` `sweepStaleCharges` -> `confirmStillCharging` -> `restState` | owner-only: the sweep skips a group this pod does not hold before it asks. Proved by `TestJanitorSkipIsNotAFence`, which now also asserts zero REST calls from a Lost or Warming pod. Stays owner-only under M4 |
| Monitor REST seed and periodic refresh, wake safety | `monitor.go` (`restState` at the seed and in `refreshOnce`), `wake_safety.go` | run inside the owner's subscription (the registry starts a subscription only for a group this pod holds: M2); the paths that ask `rivianCtx` (monitor.go 1630, 2113, wake_safety.go 322) refuse a group the pod does not hold. The seed and `refreshOnce` calls do not ask it themselves: **M4 adds an explicit owner guard on the Rivian call path** (refuse on a group this pod does not hold, with a counter) rather than relying on the subscription being the guard (DA) |
| Receipts and OTA-detail syncs | `internal/receipts/syncer.go`, `internal/otadetails/otadetails.go` (`accounts.For(uid)`) | owned groups only: M3-6 |
| Stripe account closure, billing | `internal/stripebill`, `internal/applebill`: no Rivian client (grep) | out of scope |
| Charge-time jobs (left-open alerts, tire-low, plug reminders, recaps) | read Postgres and the recorder's rows | no Rivian call; out of scope |

## Totals

Forwarded kinds: `plan_legs` (rows 20, 22), `fresh_state` (4, 6), `ota_notes` (2), `adopt`. Postgres or published read: 1, 8.
Already owner-aware, tests only: 3, 7, 21. Probes: 12 routes gated by `AdminDebugProbes`, 409 on a non-owner when on.

## Prerequisites verified on the cluster (read-only), Nov 8

- Valkey ACL (`apps/platform/redis/manifests/acl.yaml`, live in namespace `redis`): `default off`; user `rivolt` on with `~rivolt:*
  resetchannels -@all +@connection +@read +@write +@scripting +time -@dangerous`; `ops` by hand only; `metrics` with `+info +ping`. BLPOP
  is `@write @list @blocking`, so RPUSH, BLPOP, LLEN, SADD, SREM, EXPIRE, DEL, EVALSHA and TIME pass. No pub/sub (`resetchannels`).
  Every forwarder key therefore starts with `rivolt:`.
- NetworkPolicy `redis-master` in `redis`: ingress to 6379 only from pods named `rivolt` with instance `rivolt` or `rivolt-admin`
  in the `rivolt` namespace; port 9121 only from Prometheus in `monitoring`. (Preview has its own `redis-preview` namespace and
  policy.)

## C1 key (the forwarder HMAC)

32 random bytes, base64 (44 characters), distinct per environment, in Vault at `kv/rivolt/forward` (prod) and `kv/rivolt-preview/forward`
(preview), property `hmac_key`. Env name `RIVOLT_FORWARD_HMAC_KEY`, optional until `RIVOLT_FORWARD` is on. The ExternalSecret and env
wiring come with M3-1 (pattern: `apps/platform/external-secrets-resources/manifests/rivolt-preview.yaml`, `rivolt-oauth.yaml`). The
value is never printed or logged; `forward.ParseKey` decodes it and its error never echoes it.

## Forward outcome rule (DA, all kinds)

One rule for what a call does when the forward does not simply succeed. It is the same for `fresh_state`, `plan_legs` and `ota_notes`; `adopt` has no waiting caller.

| Situation | What happens | Outcome counted |
|---|---|---|
| This pod holds the group | runs here | `held` (left out of the local share) |
| No owner, owner not ready, Redis down, this pod is the owner | runs here | `local` |
| Owner still busy after its one retry (per-user cap) | runs here | `local_busy` |
| `moved` and the lease shows nobody new to ask | runs here | `local` |
| The vehicle's group is unknown to this pod | runs here | `no_group` |
| Timeout, after the one retry on an owner or epoch change | **never runs here**: `plan_legs` and `ota_notes` answer 503 with `Retry-After: 5`; `fresh_state` answers from the state this pod already has (its cache or the owner's published snapshot, no Rivian call) and only with none answers 503 with `Retry-After: 5` | `timeout` |
| Owner queue full (LLEN over 100) | 503 with `Retry-After: 5` | `unavailable` |
| `moved` twice (a new owner also says moved) | 503 with `Retry-After: 5` | `moved` |
| The owner's answer does not fit the reply cap | the kind's error | `too_big` |
| The owner's runner failed | the kind's error | `error` |

Why a timeout never runs here: most of the route deadline is gone, so a local call could not finish, and the owner is probably still inside a slow Rivian call, so a second call would double the load exactly then. Queue full is owner saturation for the same reason.

The local share used for "M3 is done" is `local + local_busy + no_group` over the forwarded traffic; `held` is the owner's own calls, and `timeout`, `unavailable`, `moved`, `too_big` and `error` are failures, outside the numerator. Tests: `TestPlanLegsSoftCasesPlanLocally`, `TestRequesterMovedWithNoNewOwnerIsServedLocally`, `TestRequesterMovedTwiceIs503`, `TestRequesterTimesOutCleanly`, `TestOwnerSilentPlanIs503RetryAfter5`, `TestForwardedStateTimeoutServesWhatThePodHas`.
