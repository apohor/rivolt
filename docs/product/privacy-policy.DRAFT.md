# Rivolt Privacy Policy (hosted service)

> **DRAFT. Not legal advice. Needs review by a qualified lawyer before publication.**
> Every data claim below cites where it comes from in the code. **[VERIFY]** items need a check against the deployed build before publishing. **[DECIDE]** items are owner decisions. Where a default is filled in, it is marked "(default, owner may change)".
> Applies to the hosted service at rivolt.dev. For self-hosted installs, you are the data controller; none of this applies to us.
> Benchmark: compared with another Rivian companion app's published policy in `docs/product/roamer-privacy-analysis.md`. No wording was copied.

Last updated: DRAFT 2026-10-17

## Open items before this can be published (read this first)
**Only two things need the owner (everything else below has a default):**
1. **Operator identity:** who runs Rivolt: an individual or an entity, and a mailing address (section 1).
2. **[LAWYER] review** of the whole draft, in particular:
   - Rights by region (section 7): California CCPA/CPRA wording and the sensitive-data limitation; EU/UK rights, legal bases, representative and international transfers (data is processed in the US).
   - Children and the age floor (the Terms draft says under-16s are excluded); COPPA / GDPR-K.
   - The breach-notification commitment and legal minimums (section 6).
   - Whether a one-time service-update email to existing account holders needs further consent.
   - Whether the policy can be published before the Terms, and the Rivian-identity disclosure wording (shared with the Terms).

**Defaults filled in (owner may change any):** contact = the sender address; history kept until you delete it for accounts with a vehicle, and accounts that never connected a vehicle deleted 120 days after signup with an email warning at day 90 (owner decision); logs 90 days; backups 365 days; hosting on self-operated US servers; 30 days' notice for material changes and for a change of ownership; rights requests answered in 30 days; service aimed at US Rivian owners; addresses stripped from trip-advice LLM payloads (build pending).

**[VERIFY] (team, on the deployed build, before publishing):** Loki re-scan for emails, tokens and VINs (pod-log scan passed 2026-10-16); Cloudflare logging/retention and TLS termination; Cloudflare Web Analytics: confirm it is off in the dashboard (the beacon is auto-injected and blocked by our CSP), then confirm no beacon is injected (the CSP entry is already removed); shared statement image has no coordinates; statement email template; export contents (receipts are in the export, checked in `internal/api/export.go` on main: `rivian_receipts.csv` and `receipt_*` columns in `charges.csv`; saved places and price overrides still to check); account deletion clears the repair undo rows (checked 2026-10-17: #207 is in the v0.33.0 tag and later, so it is deployed); waitlist-deletion wording against `internal/api/data.go`; whether the backup storage volume is encrypted at rest and where it physically sits; trip-advice address stripping once built.

---

## The short version
- **What we do:** we read your Rivian's data through your Rivian account and turn it into drives, charges and costs.
- **Your Rivian password is never stored.** We keep an encrypted sign-in session so we can keep reading your vehicle. Logging out of Rivian in Rivolt deletes it, and you can revoke access from the Rivian app (use an Authorized Driver account if you want a separate, revocable login).
- **We never send commands to your vehicle.** Rivolt only reads. Its only write-style requests to Rivian are the sign-in steps (CSRF token, Login, LoginWithOTP).
- **Location is the most sensitive thing we hold.** It is never shown to other users, and our public demo uses generated data only.
- **We do not sell your data and do not run advertising.** No third-party analytics scripts; our product analytics are first-party and honor Do Not Track and Global Privacy Control.
- **You can export and delete.** Download your data and delete your history or your whole account in Settings.
- **Retention:** if you connect a vehicle, we keep your history until you delete it. An account that never connected a vehicle is deleted 120 days after signup, with an email warning at day 90 (30 days before). Deleted data can stay in backups for up to 365 days (sections 5 and 9). Automatic deletion of unused accounts starts only after this policy is published.

## 1. Who we are
Rivolt is operated by **[DECIDE (owner): individual or entity, and mailing address]**. Contact: anton@rivolt.dev (default, owner may change). Independent project; not affiliated with Rivian.

## 2. What we collect and why
| Data | Why | Where it lives (code ground truth) |
|---|---|---|
| Account: identity-provider subject, email, display name | sign in | `users` row; OIDC via `internal/oidc` |
| Rivian sign-in session: email and session/refresh tokens (`csrf_token`, `app_session_token`, `user_session_token`, `access_token`, `refresh_token`, timestamp) | read your vehicle data on your behalf | `user_secrets`, name `rivian.session`, sealed with AES-256-GCM per-blob keys under a master key, user id bound as authenticated data (`internal/secrets/rivian.go`, `internal/crypto`) |
| Rivian password | one-time sign-in only | **Not stored.** The `Session` struct has no password field (`internal/rivian/live.go`); the password is used for the login call only. **[VERIFY: logs never contain it]** |
| One-time code (MFA) in flight | complete sign-in | sealed `rivian.pending-mfa` secret, expires after 15 minutes, cleared on success or logout (`internal/secrets/rivian.go`) |
| Vehicle state samples: charge level, range, odometer, speed, gear, charging state and power, tire pressure, temperatures, **GPS location** | history, costs, drive and charge records | `vehicle_state` (about 3,200 rows per vehicle per day per the architecture report) |
| Drives and charges: start/end time, place, energy, cost, price source, route points | statements, receipts, planner | `drives`, `charges` tables |
| Charging schedule windows: start, length, weekdays, amperage, on/off (read from Rivian; the schedule's location is used only to tell whether the truck is at it, and is never stored or shown) | explain a car that is plugged in and not charging | in memory and the short-lived live-state cache; not in the database (`internal/rivian/charging_schedule.go`) |
| Rivian charging receipts (only if the operator has turned receipts on for your account): billed amount, currency, energy, session start and end, vendor, city, charger name (not kept for home chargers) and whether it is a home charger. The payment card, order details and street address are never requested | show what Rivian actually billed next to our estimate | `rivian_receipts` (migration 0055) and `charges.receipt_*` columns; a summary request after each charge and once a day, plus one detail request per new receipt (`internal/receipts`, `internal/rivian/receipts.go`). Rows are deleted with your account (cascade) and are in your export (`rivian_receipts.csv`, `receipt_*` columns in `charges.csv`). |
| Software versions: installed and available version and the link to Rivian's release notes | show your software version on the Truck page | `vehicles.metadata`, refreshed about once a day per vehicle (`internal/otadetails`); the link is only an outbound link, we do not fetch or store the notes |
| Vehicle trim and the vehicle image URL | label the vehicle and show its picture | `vehicles` (`trim`, `image_url`), read from the vehicle info the monitor already fetches (no extra Rivian calls) |
| Locations you save, trips you plan | personalise | `locations`, saved trips |
| Settings: electricity rates, notification preferences | calculate costs | `user_settings` |
| Push subscription (endpoint and keys) if you enable notifications | send alerts | `push_subscriptions` |
| Monthly statement email preference (opt-in, default off) and send log (period, status, no content) | send your monthly statement | `statement_email_prefs`, `statement_email_sends` (migration 0048); gated by an operator flag **and** your opt-in (`internal/statement/job.go`, `internal/flags/flags.go`). Feature is off by default. |
| Session cookie (opaque token; only an HMAC of it is stored) | keep you signed in | `sessions` (`internal/sessions`) |
| Technical logs: IP address, request paths, errors, your user id (a UUID), Rivian's vehicle id, and some raw vehicle telemetry frames (Parallax shadow capture and WebSocket charging payloads; the charging payloads carry no location) | security, operations and debugging | Loki / cluster logs (`internal/logging`, `internal/rivian`). Retention is 90 days (Loki 2160 h). Email addresses were removed from log lines in #205/#206; a production scan of the last 9 hours of pod logs (2026-10-16, kubectl only, Loki not checked) found no tokens, cookies, credentials or VINs, and the only email was the system sender address. Parallax frames are logged in three ways. (1) Long-running raw capture: small status values for power state, cabin precondition, cabin climate hold and the over-the-air update topics (software-update frames are now decoded, so the version appears as text, plus a build hash); no location, VIN or email (cabin precondition can carry a schedule time). (2) Two short, time-boxed captures, with their code removed afterwards: until 2026-10-19 the charging session status (plug and display status) is decoded for every vehicle and logged raw for the two R2s only, and the pack's battery characteristics (capacity, chemistry, cell and module type) are logged raw for every vehicle; until 2026-10-20, 19 more Parallax topics are logged as numbers only (text is never logged; on the location and smart-charging topics, decimals are rounded to about 1 km and large integers are masked). (3) GPS position is decoded and never logged raw. **[VERIFY: re-scan Loki and after each release.]** |
| Operational logs about our own requests to Rivian: which subscriptions we opened, which calls we made, and what happened around a vehicle waking from sleep, keyed by vehicle id | fix battery-drain and reliability problems | "wake attribution" and wake-safety log lines (`internal/rivian/actions.go`, `wake_safety.go`). They carry Rivian's vehicle id (not the VIN) and action names, no coordinates. Retention is the same 90 days as other logs. |
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
| Hosting and backups: self-operated servers in the United States (default, owner may change); database backups go to storage run inside the same cluster (S3-compatible MinIO on a network volume), not to a third-party cloud | all service data, including encrypted backups | run the service. **[VERIFY: encryption at rest and physical location of the backup volume.]** |
| Open-Meteo (weather) | drive coordinates rounded to 0.1 degree (about 11 km) plus the hour, per request | weather in trip analysis |
| Open-Meteo (geocoding fallback) | the place text you typed, only when our own geocoder (Photon) fails or finds nothing (`internal/api/planner.go`) | place lookup in the trip planner |
| Charger data (NREL AFDC) | nothing: never queried live; data comes from a local archive | charger lookup |
| LLM provider (Anthropic / OpenAI / Gemini are implemented in `internal/ai`; a per-user setting, off unless you add a key) | only if you turn it on: recaps send date/time, drive stats, weather and nearby charge totals, no coordinates or addresses (`internal/recap`); trip advice sends the origin and destination text you typed (which can be an address), charger names or coordinates at 3 decimals (about 110 m), and tire pressures (`internal/tripadvice/advice.go`). Capped per user per day (`internal/aibudget`). | explanations. No production user has enabled it. We will strip addresses and coordinates from trip-advice payloads before sending (default, owner may change); until that ships, the origin and destination text you type is sent if you enable the feature. |
| Resend (email) | your email address; for statement emails, your statement summary | signup links, notices, monthly statement if you opt in |
| Browser push services (Apple, Google, Mozilla) | an encrypted notification payload via your browser's push endpoint | notifications, only if you enable them |

**Maps, routing and elevation:** in production the basemap is self-hosted (Protomaps), routes come from our own Valhalla instance, and elevation and place search (Photon) run inside our cluster, so these requests carry no data to a third-party provider (the geocoding fallback above is the one exception). The code can fetch elevation tiles from a public AWS S3 bucket when a self-hoster configures no local source. The former third-party raster fallback (CARTO) has been removed, and a test asserts the content security policy no longer allows it (`internal/api/config_features_test.go`). **[VERIFY on the deployed build]**

**Analytics:** our Content-Security-Policy no longer allows `static.cloudflareinsights.com` (`script-src 'self'`, checked on rivolt.dev and demo.rivolt.dev, 2026-10-17), so the Cloudflare Web Analytics beacon cannot run. QA saw Cloudflare inject the beacon on demo.rivolt.dev (2026-10-16) and the CSP refuse it, so automatic injection may still be switched on for the rivolt.dev zone. **[DECIDE (owner): turn Web Analytics off in the Cloudflare dashboard so nothing is injected at all, since it would contradict "no third-party analytics". If it is ever kept on, it must be disclosed here and in instrumentation.md.]**

**Change of ownership.** If the service is transferred or shut down, we will email users at least 30 days beforehand, with the chance to export and delete their data (default, owner may change).

## 5. Retention, export and deletion
- **Retention.** **Accounts with a vehicle:** we keep vehicle samples, drives and charges until you delete them; there is no automatic deletion of history (owner decision), and partition drops are manual. Raw-sample retention (for example 24 months) may be added later and will be announced first. **Accounts that never connected a vehicle** (owner decision, 2026-10-17): the account is deleted 120 days after signup, with a warning email 30 days before (at day 90). Enforcement starts only after this policy is published; until then nothing is deleted automatically.
- **Logs.** Logs are kept for 90 days (Loki, 2160 h), set by our log stack rather than the app (default, owner may change).
- **Export.** Signed-in users can download all their drives, charges and samples as one JSON bundle: `GET /api/data/backup` (`internal/api/data.go`), or a zip of CSVs (charges with effective cost source, drives, trips) plus settings from `GET /api/export` (Settings > Data > Export my data). The bundle includes settings. **[VERIFY: whether the exports include saved places and price overrides; receipts are included (`rivian_receipts.csv`).]**
- **Delete history only.** `DELETE /api/data/sessions` wipes samples, drives and charges, keeping your account and settings.
- **Delete everything.** `DELETE /api/data/account` deletes your account and cascades through per-user tables (drives, charges, samples, settings, user_secrets, push subscriptions, statement email preferences and send log, sessions, events), then asks the identity provider to remove your record (if that step fails it is recorded and an administrator retries it, so it is not silently lost). Last admin accounts are protected.
  - **Waitlist request.** Deleting your account also deletes your signup request (email and request message), matched by email or username, in the same transaction (A6, #81). **[VERIFY wording against `internal/api/data.go` at publication.]**
- **Disconnect Rivian.** Logging out of Rivian in Rivolt deletes the sealed session. You can also revoke an Authorized Driver from the Rivian app.
- **Repair undo copies.** When an operator repairs bad records (for example a phantom charge), the deleted rows are copied to an undo table (`charges_deleted_backup`, migration 0049) kept for at least 90 days. Deleting your account removes your rows from that table (#207, deployed since v0.33.0).
- **Caches.** Redis live-state entries are keyed per vehicle and are not cleared by account deletion; they expire on their own (2 hours / 90 seconds, section 2).
- **Backups.** The database is backed up continuously (write-ahead log) with a daily base backup at 02:30 UTC, and backups are kept for **365 days** (CNPG `retentionPolicy: 365d`, `rivolt-infra/apps/platform/rivolt/values.yaml`). Data you delete can therefore remain in backups for up to a year; we do not edit backups. **[VERIFY: whether a restore procedure re-applies earlier deletions; if not, say so here.]** Whole-year retention is a default; the owner may shorten it (default, owner may change).

## 6. Security
Per-user scoping on every query and row-level security policies; opaque server-side sessions; envelope encryption for Rivian credentials; a kill switch for the upstream; open-source code you can audit. Vehicle history is protected by access control and storage encryption as configured, not by per-user encryption. No system is perfectly secure. If a breach affects your data we will tell you without undue delay and within the time the law requires. **[LAWYER: confirm the commitment and legal minimums.]**

## 7. Your choices and rights
You can, from inside the app: see and export your data, correct your settings and prices, delete your history, delete your account, turn off notifications and the statement email, and revoke Rivolt's access to your Rivian account from the Rivian app.

### Rights by region **[LAWYER: confirm every item below; nothing here is a compliance claim yet]**
- **California (CCPA/CPRA).** Rights to know, delete, correct, and opt out of sale or sharing. We do not sell or share personal information for cross-context advertising, so there is nothing to opt out of; we honor Global Privacy Control for analytics events. We do not use sensitive personal information (including precise location) to infer characteristics. **[VERIFY and LAWYER: the sensitive-data limitation language.]**
- **EU/UK (GDPR).** Rights of access, rectification, erasure, restriction, portability and objection, and to complain to your supervisory authority. Rivolt is aimed at US Rivian owners (default, owner may change); export and deletion are available to everyone, wherever they live. **[LAWYER: if EU/UK users are in scope, legal bases per purpose, the representative if required, and the transfer mechanism, since data is processed in the US.]** The roadmap lists subject-access endpoints as unchecked: do not claim GDPR compliance until confirmed.
- **Everywhere.** Email the contact in section 8 and we will respond within 30 days (default, owner may change). We cannot identify anonymous-only analytics events, so we cannot act on requests about them (section 2).
- **Children.** Rivolt is not directed to children and is for adults who own or drive a Rivian. **[LAWYER: age floor, aligned with the Terms (the draft says under-16s excluded); COPPA/GDPR-K.]** If we learn we hold a child's account, we delete it.

## 8. Changes and contact
We will tell you about material changes by email and with an in-app notice at least 30 days before they take effect (default, owner may change). Contact: anton@rivolt.dev (default, owner may change).

## 9. What is not decided or not yet true (so we do not overpromise)
Still needed from the owner: operator identity and mailing address, and the lawyer review (see the top of this page). Not yet true: addresses are not yet stripped from trip-advice payloads (build pending; feature off for all production users); there is no automatic deletion of history for accounts with a vehicle (owner decision), and the 120-day cleanup of accounts that never connected a vehicle is decided but not yet built or enforced; backups hold deleted data for up to 365 days; email addresses were removed from logs in #205/#206 (verify on the deployed build). Everything else marked "(default, owner may change)" is a recommended default.
