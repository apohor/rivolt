# Rivolt Privacy Policy (hosted service)

> **DRAFT. Not legal advice. Needs review by a qualified lawyer before publication.**
> Every data claim below cites where it comes from in the code. **[VERIFY]** items need a check against the deployed build before publishing. **[DECIDE]** items are owner decisions.
> Applies to the hosted service at rivolt.dev. For self-hosted installs, you are the data controller; none of this applies to us.

Last updated: DRAFT 2026-10-13

## 1. Who we are
Rivolt is operated by **[DECIDE: individual or entity, contact address]**. Independent project; not affiliated with Rivian.

## 2. What we collect and why
| Data | Why | Where it lives (code ground truth) |
|---|---|---|
| Account: identity-provider subject, email, display name | sign in | `users` row; OIDC via `internal/oidc` |
| Rivian sign-in session: email and session/refresh tokens (`csrf_token`, `app_session_token`, `user_session_token`, `access_token`, `refresh_token`, timestamp) | read your vehicle data on your behalf | `user_secrets`, name `rivian.session`, sealed with AES-256-GCM per-blob keys under a master key, user id bound as authenticated data (`internal/secrets/rivian.go`, `internal/crypto`; ROADMAP Phase 1 "Credential envelope encryption") |
| Rivian password | one-time sign-in only | **Not stored.** The `Session` struct has no password field (`internal/rivian/live.go`); the password is used for the login call only. **[VERIFY: logs never contain it]** |
| One-time code (MFA) in flight | complete sign-in | sealed `rivian.pending-mfa` secret, expires after 15 minutes, cleared on success or logout (`internal/secrets/rivian.go`) |
| Vehicle state samples: charge level, range, odometer, speed, gear, charging state and power, tire pressure, temperatures, **GPS location** | history, costs, drive and charge records | `vehicle_state` (about 3,200 rows per vehicle per day per the architecture report) |
| Drives and charges: start/end time, place, energy, cost, price source, route points | statements, receipts, planner | `drives`, `charges` tables |
| Locations you save, trips you plan | personalise | `locations`, saved trips |
| Settings: electricity rates, notification preferences | calculate costs | `user_settings` |
| Push subscription (endpoint and keys) if you enable notifications | send alerts | `push_subscriptions` |
| Session cookie (opaque token; only an HMAC of it is stored) | keep you signed in | `sessions` (`internal/sessions`) |
| Technical logs: IP address, request paths, errors | security and operations | Loki / cluster logs **[VERIFY retention]** |
| Product analytics events (if enabled): event name plus counts and enums such as referrer host and utm_source; no location, amounts or emails; no IP address or User-Agent | improve onboarding | `events` table (migration 0046), see docs/product/instrumentation.md and #103 **[VERIFY at publication]** |


We do not sell your data and do not use it for advertising.

### Product analytics events (if enabled)
- **Random browser id.** Before you sign in, the browser stores a random id (`anon_id`) in localStorage and attaches it to events. This is a persistent identifier. It is not a cookie.
- **Linkable after sign-in.** Once you are signed in, each event row stores both `anon_id` and your `user_id`. Earlier events from the same browser (for example a landing-page view with its referrer host and utm_source) can therefore be linked to your account.
- **Retention.** Events are deleted after 90 days.
- **Do Not Track / Global Privacy Control.** If a request carries `DNT: 1` or `Sec-GPC: 1`, the server stores nothing (enforced server-side and covered by a test in `internal/api/events.go`).
- **Deletion.** Deleting your account removes events linked to your `user_id`. Events recorded only under `anon_id` (before you signed in, or from a browser you never signed in on) have no `user_id`, so account deletion does not touch them and they cannot be tied to you. They are removed by an hourly purge of events older than 90 days, which runs at startup and every hour regardless of whether event collection is switched on. There is no other way to request deletion of anonymous-only events, because we cannot identify which are yours.

## 3. Location data
Location is the most sensitive data we hold. It is stored per vehicle sample and per drive/charge. It is used to classify charges as Home / Public / Fast and to show maps. It is never shown to other users. Rivolt's public demo uses generated data only. Shared statement images contain no coordinates or addresses **[VERIFY once U3 ships]**.

## 4. Who we share with (processors)
| Recipient | What | Why |
|---|---|---|
| Rivian | your credentials and requests, as in your own app | read your vehicle data |
| Cloudflare (CDN / proxy) | every request to rivolt.dev, including your IP address and request metadata, passes through it | delivery, caching, DDoS protection **[VERIFY: Cloudflare's logging/retention and whether TLS terminates there]** |
| Cloud hosting / backup storage **[DECIDE: provider names]** | encrypted at rest as configured | run the service |
| Weather provider (Open-Meteo) | coordinates and times of drives **[VERIFY exact payload]** | weather in trip analysis |
| Charger data (NREL AFDC) | queried against a local archive; **[VERIFY no user data sent]** | charger lookup |
| LLM provider(s) (Anthropic / OpenAI / Gemini are implemented in `internal/ai`) | trip or drive summaries for AI advice, only when you use the feature; capped per user per day (`internal/aibudget`) | explanations **[VERIFY exactly what fields are sent and which provider is enabled in prod. DECIDE: strip coordinates and addresses from LLM payloads before sending.]** |
| Resend (email) | your email address | signup links and notices |
| AWS S3 (elevation tiles) | tile coordinates, which can reveal the area of your routes | elevation in trip analysis **[VERIFY and consider proxying or caching]** |
| Open-Meteo (geocoding and weather archive) | place names and coordinates | place lookup, weather |
| Browser push services (Apple, Google, Mozilla) | an encrypted notification payload via your browser's push endpoint | notifications, only if you enable them |
**Analytics:** the Content-Security-Policy allows `static.cloudflareinsights.com` (Cloudflare Web Analytics), but no beacon is served today. **[VERIFY: if it is ever enabled it must be disclosed here and contradicts instrumentation.md's "no third-party analytics"; otherwise remove the CSP entry.]**
Maps and routing are self-hosted (Protomaps basemap, Valhalla): no third-party map requests carry your data **[VERIFY]**.

## 5. Retention, export and deletion
- **Retention.** We keep vehicle samples, drives and charges until you delete them. There is currently **no automatic deletion** of history in the code; partition drops are manual (ROADMAP Phase 1 notes). **[DECIDE: adopt a retention period, e.g. raw samples 24 months, and implement before promising it.]**
- **Export.** Signed-in users can download all their drives, charges and samples as one JSON bundle: `GET /api/data/backup` (`internal/api/data.go`). The bundle includes settings (`internal/api/data.go`). **[VERIFY: saved places, receipts and price overrides; CSV statement export is planned, not shipped.]**
- **Delete history only.** `DELETE /api/data/sessions` wipes samples, drives and charges, keeping your account and settings.
- **Delete everything.** `DELETE /api/data/account` deletes your account and cascades through per-user tables (drives, charges, samples, settings, user_secrets, push subscriptions, sessions), then asks the identity provider to remove your record (if that step fails it is recorded and an administrator retries it, so it is not silently lost). Last admin accounts are protected.
  - **Waitlist request.** Deleting your account also deletes your signup request (email and request message), matched by email or username, in the same transaction (A6, #81). **[VERIFY wording against `internal/api/data.go` at publication.]**
- **Disconnect Rivian.** Logging out of Rivian in Rivolt deletes the sealed session. You can also revoke an Authorized Driver from the Rivian app.
- **Backups.** Deleted data can persist in encrypted backups until they rotate. **[DECIDE/VERIFY: backup retention period.]**
- **GDPR-style requests:** the endpoints above cover access and erasure for signed-in users; a self-service button in Settings → Data **[VERIFY exists]**. Roadmap lists "GDPR subject-access endpoints" as unchecked: confirm before claiming compliance.

## 6. Security
Per-user scoping on every query and row-level security policies; opaque server-side sessions; envelope encryption for Rivian credentials; kill switch for the upstream; open-source code you can audit. No system is perfectly secure; we will notify affected users of a breach as required by law.

## 7. Your choices and rights
Depending on where you live you may have rights to access, correct, delete, export and object. Use the in-app tools in section 5 or contact us. **[LAWYER: jurisdiction-specific wording, children's data (service is not for under-16s), international transfers.]**

## 8. Changes and contact
We will notify signed-in users of material changes. Contact: **[DECIDE]**.
