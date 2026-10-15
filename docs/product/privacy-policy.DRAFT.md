# Rivolt Privacy Policy (hosted service)

> **DRAFT. Not legal advice. Needs review by a qualified lawyer before publication.**
> Every data claim below cites where it comes from in the code. **[VERIFY]** items need a check against the deployed build before publishing. **[DECIDE]** items are owner decisions.
> Applies to the hosted service at rivolt.dev. For self-hosted installs, you are the data controller; none of this applies to us.
> Benchmark: compared with another Rivian companion app's published policy in `docs/product/roamer-privacy-analysis.md`. No wording was copied.

Last updated: DRAFT 2026-10-15

## The short version
- **What we do:** we read your Rivian's data through your Rivian account and turn it into drives, charges and costs.
- **Your Rivian password is never stored.** We keep an encrypted sign-in session so we can keep reading your vehicle. Logging out of Rivian in Rivolt deletes it, and you can revoke access from the Rivian app (use an Authorized Driver account if you want a separate, revocable login).
- **We never send commands to your vehicle.** Rivolt only reads. Its only write-style requests to Rivian are the sign-in steps (CSRF token, Login, LoginWithOTP).
- **Location is the most sensitive thing we hold.** It is never shown to other users, and our public demo uses generated data only.
- **We do not sell your data and do not run advertising.** No third-party analytics scripts; our product analytics are first-party and honor Do Not Track and Global Privacy Control.
- **You can export and delete.** Download your data and delete your history or your whole account in Settings.
- **What we cannot promise yet:** automatic deletion of old history, and some retention periods, are not decided or enforced (see sections 5 and 9).

## 1. Who we are
Rivolt is operated by **[DECIDE: individual or entity, contact address]**. Independent project; not affiliated with Rivian.

## 2. What we collect and why
| Data | Why | Where it lives (code ground truth) |
|---|---|---|
| Account: identity-provider subject, email, display name | sign in | `users` row; OIDC via `internal/oidc` |
| Rivian sign-in session: email and session/refresh tokens (`csrf_token`, `app_session_token`, `user_session_token`, `access_token`, `refresh_token`, timestamp) | read your vehicle data on your behalf | `user_secrets`, name `rivian.session`, sealed with AES-256-GCM per-blob keys under a master key, user id bound as authenticated data (`internal/secrets/rivian.go`, `internal/crypto`) |
| Rivian password | one-time sign-in only | **Not stored.** The `Session` struct has no password field (`internal/rivian/live.go`); the password is used for the login call only. **[VERIFY: logs never contain it]** |
| One-time code (MFA) in flight | complete sign-in | sealed `rivian.pending-mfa` secret, expires after 15 minutes, cleared on success or logout (`internal/secrets/rivian.go`) |
| Vehicle state samples: charge level, range, odometer, speed, gear, charging state and power, tire pressure, temperatures, **GPS location** | history, costs, drive and charge records | `vehicle_state` (about 3,200 rows per vehicle per day per the architecture report) |
| Drives and charges: start/end time, place, energy, cost, price source, route points | statements, receipts, planner | `drives`, `charges` tables |
| Locations you save, trips you plan | personalise | `locations`, saved trips |
| Settings: electricity rates, notification preferences | calculate costs | `user_settings` |
| Push subscription (endpoint and keys) if you enable notifications | send alerts | `push_subscriptions` |
| Monthly statement email preference (opt-in, default off) and send log (period, status, no content) | send your monthly statement | `statement_email_prefs`, `statement_email_sends` (migration 0048); gated by an operator flag **and** your opt-in (`internal/statement/job.go`, `internal/flags/flags.go`). Feature is off by default. |
| Session cookie (opaque token; only an HMAC of it is stored) | keep you signed in | `sessions` (`internal/sessions`) |
| Technical logs: IP address, request paths, errors, your user id | security and operations | Loki / cluster logs (`internal/logging`) **[VERIFY retention in days]** |
| Operational logs about our own requests to Rivian: which subscriptions we opened, which calls we made, and what happened around a vehicle waking from sleep, keyed by vehicle id | fix battery-drain and reliability problems | "wake attribution" and wake-safety log lines (`internal/rivian/actions.go`, `wake_safety.go`) **[VERIFY: no coordinates or emails in these lines; retention same as logs]** |
| Live-state cache: the in-progress drive/charge accumulator (including start coordinates) and the latest merged vehicle state | survive restarts and let any server instance answer | Redis, keys under `<prefix>:livestate` (`internal/rivian/livestate.go`). Accumulator expires 2 hours after last update; the shared state snapshot expires after 90 seconds. |
| Product analytics events (if enabled): event name plus counts and enums such as referrer host and utm_source; no location, amounts or emails; no IP address or User-Agent | improve onboarding | `events` table (migration 0046), see `docs/product/instrumentation.md` **[VERIFY at publication]** |

We do not sell your data and do not use it for advertising.

### Product analytics events (if enabled)
- **Random browser id.** Before you sign in, the browser stores a random id (`anon_id`) in localStorage and attaches it to events. This is a persistent identifier. It is not a cookie.
- **Linkable after sign-in.** Once you are signed in, each event row stores both `anon_id` and your `user_id`. Earlier events from the same browser (for example a landing-page view with its referrer host and utm_source) can therefore be linked to your account.
- **Retention.** Events are deleted after 90 days.
- **Do Not Track / Global Privacy Control.** If a request carries `DNT: 1` or `Sec-GPC: 1`, the server stores nothing (enforced server-side in `internal/api/events.go`).
- **Deletion.** Deleting your account removes events linked to your `user_id` (`ON DELETE CASCADE`). Events recorded only under `anon_id` have no `user_id`, so account deletion does not touch them and they cannot be tied to you. They are removed by an hourly purge of events older than 90 days, which runs at startup and every hour regardless of whether event collection is switched on. There is no other way to request deletion of anonymous-only events, because we cannot identify which are yours.

## 3. Location data
Location is the most sensitive data we hold. It is stored per vehicle sample and per drive/charge, and briefly in the live-state cache (section 2). It is used to classify charges as Home / Public / Fast and to show maps. It is never shown to other users. Rivolt's public demo uses generated data only (a sample truck; no real vehicle). Shared statement images contain no coordinates or addresses **[VERIFY once U3 ships]**. The monthly statement email contains totals and a summary, not a map **[VERIFY template]**.

## 4. Who we share with (processors)
| Recipient | What | Why |
|---|---|---|
| Rivian | your credentials and requests, as in your own app; Rivolt's requests identify themselves as Rivian's iOS app (see the Terms) | read your vehicle data |
| Cloudflare (CDN / proxy) | every request to rivolt.dev, including your IP address and request metadata, passes through it | delivery, caching, DDoS protection **[VERIFY: Cloudflare's logging/retention and whether TLS terminates there]** |
| Cloud hosting / backup storage **[DECIDE: provider names]** | encrypted at rest as configured | run the service |
| Open-Meteo (weather and geocoding) | coordinates and times of drives, place names **[VERIFY exact payload and rounding]** | weather in trip analysis, place lookup |
| Charger data (NREL AFDC) | queried against a local archive; **[VERIFY no user data sent]** | charger lookup |
| LLM provider(s) (Anthropic / OpenAI / Gemini are implemented in `internal/ai`) | trip or drive summaries for AI advice, only when you use the feature; capped per user per day (`internal/aibudget`) | explanations **[VERIFY exactly what fields are sent and which provider is enabled in prod. DECIDE: strip coordinates and addresses from LLM payloads before sending.]** |
| Resend (email) | your email address; for statement emails, your statement summary | signup links, notices, monthly statement if you opt in |
| AWS S3 (elevation tiles) | tile coordinates, which can reveal the area of your routes | elevation in trip analysis **[VERIFY and consider proxying or caching]** |
| Browser push services (Apple, Google, Mozilla) | an encrypted notification payload via your browser's push endpoint | notifications, only if you enable them |

**Maps and routing:** the basemap is self-hosted (Protomaps) and routes come from our own Valhalla instance, so map and route requests carry no data to a third-party map provider. The former third-party raster fallback (CARTO) has been removed, and a test asserts the content security policy no longer allows it (`internal/api/config_features_test.go`). **[VERIFY on the deployed build]**

**Analytics:** the Content-Security-Policy allows `static.cloudflareinsights.com` (Cloudflare Web Analytics), but no beacon is served today. **[VERIFY: if it is ever enabled it must be disclosed here and contradicts instrumentation.md's "no third-party analytics"; otherwise remove the CSP entry.]**

**Change of ownership.** **[DECIDE: what happens to user data if the service is transferred or shut down, e.g. notify and offer export/delete first.]**

## 5. Retention, export and deletion
- **Retention.** We keep vehicle samples, drives and charges until you delete them. There is currently **no automatic deletion** of history in the code; partition drops are manual. **[DECIDE: adopt a retention period, e.g. raw samples 24 months, and implement before promising it.]**
- **Logs.** Retention is set by our log stack, not the app. **[VERIFY: Loki retention in days; DECIDE: state it here once confirmed.]**
- **Export.** Signed-in users can download all their drives, charges and samples as one JSON bundle: `GET /api/data/backup` (`internal/api/data.go`). The bundle includes settings. **[VERIFY: saved places, receipts and price overrides; CSV export (#132) status.]**
- **Delete history only.** `DELETE /api/data/sessions` wipes samples, drives and charges, keeping your account and settings.
- **Delete everything.** `DELETE /api/data/account` deletes your account and cascades through per-user tables (drives, charges, samples, settings, user_secrets, push subscriptions, statement email preferences and send log, sessions, events), then asks the identity provider to remove your record (if that step fails it is recorded and an administrator retries it, so it is not silently lost). Last admin accounts are protected.
  - **Waitlist request.** Deleting your account also deletes your signup request (email and request message), matched by email or username, in the same transaction (A6, #81). **[VERIFY wording against `internal/api/data.go` at publication.]**
- **Disconnect Rivian.** Logging out of Rivian in Rivolt deletes the sealed session. You can also revoke an Authorized Driver from the Rivian app.
- **Repair undo copies.** When an operator repairs bad records (for example a phantom charge), the deleted rows are copied to an undo table (`charges_deleted_backup`, migration 0049) kept for at least 90 days. **[VERIFY: whether account deletion clears these rows; if not, add that or say they age out.]**
- **Caches.** Live-state cache entries in Redis expire on their own (2 hours / 90 seconds, section 2).
- **Backups.** Deleted data can persist in encrypted backups until they rotate. **[DECIDE/VERIFY: backup retention period.]**

## 6. Security
Per-user scoping on every query and row-level security policies; opaque server-side sessions; envelope encryption for Rivian credentials; a kill switch for the upstream; open-source code you can audit. Vehicle history is protected by access control and storage encryption as configured, not by per-user encryption. No system is perfectly secure. **[DECIDE: breach-notification commitment, e.g. notify affected users within a stated number of days; lawyer to confirm legal minimums.]**

## 7. Your choices and rights
You can, from inside the app: see and export your data, correct your settings and prices, delete your history, delete your account, turn off notifications and the statement email, and revoke Rivolt's access to your Rivian account from the Rivian app.

### Rights by region **[LAWYER: confirm every item below; nothing here is a compliance claim yet]**
- **California (CCPA/CPRA).** Rights to know, delete, correct, and opt out of sale or sharing. We do not sell or share personal information for cross-context advertising, so there is nothing to opt out of; we honor Global Privacy Control for analytics events. We do not use sensitive personal information (including precise location) to infer characteristics. **[VERIFY and LAWYER: the sensitive-data limitation language.]**
- **EU/UK (GDPR).** Rights of access, rectification, erasure, restriction, portability and objection, and to complain to your supervisory authority. **[DECIDE: whether we serve EU/UK users at all; if yes, name legal bases per purpose, the representative if required, and the international-transfer mechanism, since data is processed in the US.]** The roadmap lists subject-access endpoints as unchecked: do not claim GDPR compliance until confirmed.
- **Everywhere.** Email the contact in section 8 and we will respond within **[DECIDE: response time, e.g. 30 days]**. We cannot identify anonymous-only analytics events, so we cannot act on requests about them (section 2).
- **Children.** Rivolt is not directed to children and is for adults who own or drive a Rivian. **[DECIDE: age floor, align with the Terms (draft says under-16s excluded); LAWYER: COPPA/GDPR-K.]** If we learn we hold a child's account, we delete it.

## 8. Changes and contact
We will notify signed-in users of material changes before they take effect **[DECIDE: how, e.g. in-app banner plus email]**. Contact: **[DECIDE]**.

## 9. What is not decided or not yet true (so we do not overpromise)
Open items the owner or a lawyer must close before publication: operator identity and contact; retention periods for history, logs and backups; breach-notification commitment; LLM payload minimization; Cloudflare and hosting provider disclosure; EU/UK scope; change-of-ownership handling; age floor.
