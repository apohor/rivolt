# Incident runbook: Rivian blocks or rate-limits Rivolt

Scope: hosted rivolt.dev. Owner: operator on call (one person). Last reviewed: DRAFT 2026-10-13. **[VERIFY dashboards and queries against Grafana before relying on them]**

## 1. Detect
| Signal | Where | Meaning |
|---|---|---|
| `rivolt_rivian_results_total{class="rate_limited"}` rising | Prometheus/Grafana; `class` is one of success, transient, outage, rate_limited, user_action, unknown (`internal/metrics`) | Rivian is throttling (429 or equivalent) |
| `class="unknown"` or `outage` spike across many `op` values | same | Gateway change or block, not a user problem |
| `class="user_action"` for many users at once | same | Sign-in or session scheme changed, or credentials mass-revoked |
| `rivolt_rivian_breaker_state` = open, `rivolt_rivian_breaker_trips_total{reason}` increasing | same (`internal/rivian.Breaker`: 60 s window) | We have already stopped ourselves |
| `rivolt_rivian_ratelimit_blocked_total{class}` rising | same | Our own token bucket is the limiter (local, not Rivian) |
| WebSocket closes beyond the normal ~3-minute TTL churn (code 4420) becoming failures to reconnect | logs (Loki) | Subscription path blocked |
| User reports: "No Cloud Connection" or remote commands delayed in the **official** Rivian app | email, forum thread, GitHub issues | **Highest severity**: we may be affecting owners' own app |
| Rivian communication: C&D, email, account suspension notice | inbox | Legal/partner event |

Alerts to add if missing **[DECIDE]**: rate_limited ratio > 5% for 10 min; breaker open > 5 min; any user report mentioning "No Cloud Connection".

## 2. Decide: pause or degrade
| Situation | Action |
|---|---|
| Any report that the **official app** is affected, or any Rivian contact/notice | **PAUSE immediately** (kill switch). Investigate after. |
| `rate_limited` sustained and not explained by a deploy or reconnect storm | **PAUSE**, then resume at reduced rate |
| Breaker open but errors are `transient` or one `op` only | **DEGRADE**: leave on, lower cadence (see below); watch 30 min |
| Only `user_action` for one user | Not an incident: user needs to re-auth |
| Only `outage` classes with other apps also failing (check Rivian status, forums) | Wait; show "Rivian unavailable" banner; no action on upstream |

Pause costs: no new data until resumed (history already stored remains, gaps appear). Degrading costs: staler data. When in doubt about owner-app impact, pause.

## 3. Actions
**Pause (all Rivian traffic):** `PUT /api/admin/kill-switch` with `{"paused": true, "reason": "...", "actor": "..."}` (`internal/api/api.go`, `internal/flags.KillSwitchState`). Every pod picks it up within about 10 s (flags poll interval). Every outbound Rivian call returns `ErrUpstreamPaused`. Confirm via `GET /api/admin/kill-switch` and a drop in `rivolt_rivian_results_total`.
**Resume:** same endpoint, `{"paused": false}`. Resume during a quiet hour; expect a reconnect burst, which the breaker and token bucket contain.
**Degrade (no deploy needed):**
1. Turn the sleep gate on for everyone: set flag `sleep_safe_vehicles` to `{"all": true}` (see `internal/flags/flags.go` comment for the SQL). This parks non-essential subscriptions while vehicles sleep.
2. Lower token-bucket rates via env/config and roll pods **[VERIFY how rates are configured]**.
3. Stop accepting new connections: set flag `signup_cap` to a low `limit` (0 blocks all new signups).
4. Turn off optional features that call Rivian per request (trip planner) via `trip_planner_enabled`.
**Preserve evidence:** save 30 minutes of logs and a metrics screenshot before changing anything.

## 4. Communicate
Post to: status page / signup banner / email beta list / forum thread. Keep it factual; never blame Rivian publicly without confirmation.

*Pause, internal-first notice (within 1 hour):*
> Rivolt has paused all connections to Rivian while we investigate unusual responses. Your stored history is safe. Nothing needs to be done on your side. Next update by [time].

*If any user reported official-app problems:*
> If you saw "No Cloud Connection" or delayed commands in the Rivian app, Rivolt may have been a factor. We have paused all access. Please sign out of Rivolt in Settings, and tell us the time it started. We're sorry. If the issue persists in the Rivian app, contact Rivian support and mention third-party access.

*Resume:*
> Rivolt is reconnecting. Data from [window] may have gaps. We have [lowered request rates / enabled sleep protection]. Tell us if you notice anything unusual in the Rivian app.

*Rivian blocks us (no ETA):*
> Rivian has changed how its app services respond and Rivolt can't currently read new data. Your history remains available and exportable (Settings → Data → Download). We're in contact with Rivian and will update when we know more. Self-hosters may be affected too.

## 5. After the incident
- Write a short post-mortem in docs/ (timeline, signal that fired, time to pause, user impact).
- If Rivian made contact, reply once, factually, and send the outreach letter's asks.
- Decide: resume, resume degraded, or stop hosted ingestion. Stopping triggers the devil's advocate "hard stop" list: pause signups, offer export, reassess.
- Update this runbook and alert rules.

## 6. Tabletop test (Gate C requirement)
Once, before charging: flip the kill switch on preview, confirm propagation within 15 s, confirm banner and gaps behave, practise sending the three comms templates. Record time to pause.
