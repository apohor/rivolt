# Rivolt Privacy Policy

> **DRAFT. Not legal advice. Confirm with a licensed attorney before publishing.** This is the short policy to publish now, while Rivolt is run by an individual. It replaces nothing in `privacy-policy.DRAFT.md`, which stays the detailed reference; every claim here must stay true to that draft and to the code (sources in the comment at the end). Replace the placeholders before publishing. **[ATTORNEY TO CONFIRM]** marks lines with legal consequences.

Last updated: [PUBLICATION DATE]

**Who runs Rivolt.** Rivolt (rivolt.dev) is run by **[OWNER LEGAL NAME], an individual** (not a company) in **[STATE]**, United States ("we"). Contact: anton@rivolt.dev. Rivolt is independent and not affiliated with Rivian. This policy covers the hosted service only, not copies of the open-source code that others run themselves. **[ATTORNEY TO CONFIRM]**

**Who it is for.** Adults (18 and over) in the United States who own or drive a Rivian. **[ATTORNEY TO CONFIRM]**

**What we collect, and why**
- **Your account:** email, display name and your sign-in provider's account id, to sign you in; for each sign-in, the IP address and browser it came from, to keep you signed in and spot stolen sessions; we delete these 30 days after the sign-in expires or you sign out. If you asked for access, your request (email and message): deleted after 90 days if it is not approved, and if it is approved, once you sign up or 30 days after the invite expires unused. **[CONFIRM #363 SHIPPED]**
- **Your Rivian sign-in:** your Rivian email and the session tokens Rivian issues, stored encrypted, so we can keep reading your vehicle. **Your Rivian password is never stored**; it is used once to sign in to Rivian.
- **Your vehicle's data**, read from Rivian: charge level, range, odometer, speed, temperatures, tire pressure, software version, trim and picture, and **GPS location**. We turn it into drives, charges and costs. Location is the most sensitive data we hold. It is never shown to other users. Rivolt records the vehicle, not the driver: if someone else drives it, their trips are recorded too.
- **Rivian charging receipts**, only if you turn them on in Settings (off by default; turning them off deletes the stored receipts) **[CONFIRM #363 SHIPPED]**: amount billed, currency, energy, times, vendor, city, and charger name (not for home chargers). We never ask for your payment card or address.
- **Your settings:** electricity rates, saved places and trips, notification and email preferences.
- **Notifications:** if you turn on notifications, your browser's push address and User-Agent.
- **Technical logs:** IP address, pages requested, errors, your account id, your vehicle's Rivian id and some raw vehicle data, to run and fix the service. Kept 90 days.
- **Product analytics:** our own simple usage events (no IP address, location, amounts or emails), with a random id stored in your browser. Kept 90 days. If your browser sends Do Not Track or Global Privacy Control, we record nothing.

**Who we share it with.** Only these, and only for the service:
- **Your sign-in provider (Google or GitHub)**, if you use it to sign in to Rivolt.
- **Rivian**, to read your vehicle's data. Rivolt's requests present themselves as Rivian's own mobile app (see the Terms, section 2).
- **Cloudflare**, which every request to rivolt.dev passes through (including your IP address).
- **Resend**, which sends our emails (your email address, and your statement summary if you turn on the monthly statement email).
- **Your browser's push service**, if you turn on notifications.
- **Open-Meteo**, for weather (trip coordinates rounded to about 11 km, plus the hour) and, sometimes, for place search (the place you typed).
- **An AI provider (Anthropic, OpenAI or Google Gemini), only if you turn it on.** Trip recaps send no coordinates or addresses. Trip advice sends the start and destination you type (which can be an address), charger names or locations (to about 110 m) and tire pressures.

We run our own servers in the United States, and database backups go to storage run inside the same cluster, not to a third-party cloud. As the operator, we can see stored data, and use that only to run, support and repair the service. We do not share your data with anyone else, except where the law requires it. **[ATTORNEY TO CONFIRM]** **We do not sell your data, and there is no advertising** and no third-party analytics script.

**Keeping, exporting and deleting**
- If you connect a vehicle, we keep your history until you delete it.
- If you never connect Rivian, your account is deleted 120 days after signup. We email a warning at day 90, and never delete less than 30 days after that warning. This starts only after this policy is published.
- In Settings you can export your data, delete your history, or delete your whole account. Signing Rivolt out of Rivian deletes the stored Rivian session; you can also revoke access in the Rivian app.
- After you delete your account, copies can remain in logs for up to 90 days and in backups for up to 365 days. Analytics events not linked to your account cannot be found, so they simply expire after 90 days.

**Security.** Your Rivian session is encrypted, every user's data is kept separate, and the code is open source. No system is perfectly secure. If a breach affects your data, we will email you without undue delay, and within 30 days of confirming it. **[ATTORNEY TO CONFIRM]**

**Your choices.** Wherever you live, you can see, export, correct and delete your data, in the app or by emailing us; we answer within 30 days. Your data is stored and processed in the United States. **[ATTORNEY TO CONFIRM]**

**Changes.** We will tell you about material changes by email and in the app at least 30 days before they take effect. We will ask for your consent before using data we already hold in a materially new way. If Rivolt moves to a company or a new owner, we will email you at least 30 days beforehand, so you can export or delete your data. **[ATTORNEY TO CONFIRM]**

<!--
Internal notes (not for publication):
- Mailing address: not published (owner decision, 2026-10-17); email is the only contact. No law found that requires a postal address in a privacy policy for a free US service run by an individual (CalOPPA, Delaware 6 Del. C. 1205C, Nevada NRS 603A.340, Google API Services User Data Policy). Optional later, when marketing email starts or an LLC is formed: a USPS PO box, or a private mailbox at a commercial mail receiving agency (USPS Form 1583); not a home address. Both count as a "valid physical postal address" under 16 CFR 316.2(p). Resend's Acceptable Use Policy also asks senders to include a valid address and company name in emails.
- Sources for each claim (main, plus PR #361 wording):
  account/sign-in: users row, internal/oidc; prod providers RIVOLT_OIDC_PROVIDERS=hydra,google,github (checked by team lead 2026-10-17; hydra is our own sign-in at auth.rivolt.dev, no third party); sessions IP + User-Agent: migration 0006, internal/sessions/store.go; 30-day purge after expiry/revocation and waitlist sweep (90 days unapproved; approved: on signup or 30 days after invite expiry) via internal/retention (PR #363). access requests: signup_requests, migration 0028. push: push_subscriptions (endpoint, keys, user_agent), internal/push/store.go.
  Rivian session sealed, password not stored: internal/secrets/rivian.go, internal/crypto, internal/rivian/live.go (Session has no password field).
  vehicle data: vehicle_state, drives, charges; software version internal/otadetails; trim/image vehicles table.
  receipts: rivian_receipts (migration 0055), internal/receipts; after PR #363 needs both the rivian_receipts operator flag (internal/flags/flags.go) and the user's opt-in (setting privacy.rivian_receipts, PUT /api/settings/rivian-receipts; opting out deletes stored receipts). backups: full draft section 4/5 (MinIO in-cluster, CNPG 365d).
  logs 90 days: Loki 2160 h; raw Parallax frames per full draft section 2.
  analytics: events table (migration 0046), DNT/GPC in internal/api/events.go, 90-day purge.
  sharing: full draft section 4; identity internal/rivian/live.go:25-52, ws_mux.go:114-121; Open-Meteo rounding 0.1 degree; AI: internal/ai, internal/recap, internal/tripadvice/advice.go.
  operator access: /api/admin/users routes, internal/api/api.go.
  retention: inactive purge internal/db/inactive_purge.go (flag inactive_account_purge, off until publication); backups CNPG retentionPolicy 365d, to in-cluster MinIO on an NFS-backed volume (checked by team lead 2026-10-17; encryption at rest not confirmed, so the text makes no encryption claim); logs survive deletion (full draft section 5, VERIFY).
  "except where the law requires": new in both drafts (PR #361), attorney to confirm.
-->
