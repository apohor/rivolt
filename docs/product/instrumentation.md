# Funnel and success-metric instrumentation (P4)

North star: **Activated vehicle = a connected vehicle whose owner views a priced receipt in the Money tab within 7 days of connecting.**

## Privacy rules
- First-party only (own DB), no third-party analytics, no cookies beyond the session.
- No coordinates, addresses, emails, VINs or amounts in event properties. Counts and enums only.
- Visitor events use a random per-browser id in localStorage. That is a persistent identifier: disclose it in the Privacy Policy and honour DNT.
- `POST /api/events` is unauthenticated: per-IP rate limit, small max payload (e.g. 2 KB, 20 events per batch), reject unknown event names and property keys.
- Do not store IP address or User-Agent with events.
- Self-hosters: off by default; one env flag.

## Events
| Event | When | Properties |
|---|---|---|
| `landing_view` | `/` loaded signed out | referrer host, utm_source |
| `demo_view` | `/demo` first screen (demo.rivolt.dev lands here) | tab |
| `demo_tab` | tab changed in demo | tab |
| `signup_start` | signup form opened | method (oidc/email) |
| `signup_done` | account created | method |
| `rivian_connect_start` | Rivian sign-in shown | authorized_driver (bool) |
| `rivian_connect_done` | vehicles listed | vehicle_count |
| `rivian_connect_fail` | error | class (user-action/mfa/rate/other) |
| `first_data` | first sample stored | seconds_since_connect |
| `money_view` | Money tab opened | month_offset |
| `receipt_open` | receipt expanded | source (billed/est_home/est_network/user) |
| `price_edit` | user sets a price | from_source |
| `statement_share` | share/export used | kind (png/csv) |
| `plan_run` | trip planned | stops_bucket |
| `alert_shown` / `alert_dismiss` | Now card alert | kind |

Derived server-side: `activated` (north star), `week_return` (any event 7–14 days after connect).

## Funnel
landing_view → demo_view → signup_start → signup_done → rivian_connect_done → first_data → money_view → receipt_open (= activated).

## Targets (initial; revisit at 50 vehicles)
| Metric | Target |
|---|---|
| Landing → demo | 30%+ |
| Demo → signup_start | 15%+ |
| signup_done → rivian_connect_done | 50%+ (today unknown; baseline first) |
| Connected → activated in 7 days | 60%+ |
| Monthly active / connected vehicles | 60%+ |
| Money tab monthly opens per active user | 1+ |
| Connect failure rate | below 15% |

Stop rules: if connect completion stays under 25% for 30 days, fix onboarding (Authorized Driver default, clearer MFA) before building features.

## Implementation split
- **architect:** `POST /api/events` (batched, auth optional), `events` table (ts, anon_id, user_id nullable, name, props jsonb, 90-day retention), derived `activated` view, a read-only admin summary query. Drop invalid property keys (allowlist above).
- **ux:** small `track(name, props)` helper; fire the events above; no PII; respects the flag and DNT.
- **pm:** reads weekly summary, reports against targets.

## Rollout
Flag default off. Enable on preview first; check payloads contain no PII before prod.
