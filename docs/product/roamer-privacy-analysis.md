# Rivian Roamer privacy policy: analysis and comparison (PM, 2026-10-15)

> Benchmarking only. Not legal advice. Roamer is a third-party Rivian companion app (operator named in its policy as "Velocity Studios, LLC"). We paraphrase and quote only short phrases; our policy text is written independently.

## Sources and confidence
| Source | URL | Fetched | Notes |
|---|---|---|---|
| Roamer privacy policy | https://rivianroamer.com/privacy-policy | 2026-10-15 | Page was read through an automated summarizer, so the short quotes below are **not verified word-for-word**; re-check against the live page before relying on any of them. The page showed an "effective date" of 2026-10-21, which is after the fetch date: treat the date as **unverified** (a scheduled revision, or a misread). |
| Roamer help center | https://rivianroamer.com/help | 2026-10-15 | Credentials, deletion, 6-month history rule, imports. |
| Roamer App Store "App Privacy" label | not found | 2026-10-15 | Searches returned only Rivian's own app listing. **Not verified.** Someone with an iPhone should read the Roamer listing's "Data Linked to You" section. |

## What Roamer says

| Topic | Roamer (paraphrased) |
|---|---|
| Operator and contact | An LLC named in the policy; one support email address. |
| Collected: account | Email, name, settings, linked Rivian account. |
| Collected: vehicle and location | Vehicles and configuration, status, location, charging and driving history; saved places, routes, charge locations. |
| Collected: payment | Subscription IDs from Stripe; says it never stores full card numbers. |
| Collected: analytics/device | Site and app usage, IP address, browser/app version, device type, error details. Names PostHog as the analytics tool. |
| Rivian credentials | Password is passed to Rivian to sign in and "never saves it"; keeps an encrypted key. Help center: only encrypted tokens received; revoke via the Rivian app or a password change; suggests a secondary driver account. |
| Retention | Account data while the account is active; subscription history 6 months after a Plus subscription ends; server/usage logs up to one year; payment records up to seven years; encrypted backups on a schedule; short post-deletion confirmation data (30 days). |
| Deletion and export | In-app account deletion (signs out, disables access, then deletes vehicle data, history, imports, uploads, analytics); can download drives and charges; imports from other trackers. |
| Third parties | Stripe, PostHog, AWS (servers, files, logs, email), Apple (notifications), database and map companies, AI assistants only when the user connects them. |
| Sale, sharing, ads | "never sell or rent"; sharing with service providers, user-controlled social features, consented AI assistants, and law enforcement when required; no cross-site tracking; does not respond to Do Not Track. |
| Sale on acquisition | Information would go to a new owner if Roamer is sold or merged. |
| Children | Adults 18+; deletes data if it finds a child under 13. |
| Jurisdictions | No dedicated CCPA/CPRA or GDPR section, no legal bases, no transfer mechanism, no named rights list. |
| Security | Encrypts in transit and at rest "wherever we can"; no breach-notice timeline. |
| Changes | Promises notice before a big change takes effect. |

## Side by side with our draft

| Area | Roamer | Rivolt draft (before this revision) | Verdict |
|---|---|---|---|
| Plain-language summary | Short, conversational sections | None; table-first | **Roamer clearer.** Closed: summary box at top. |
| What we collect, with where it lives | Prose list | Table with code citations | **We are stronger** (verifiable). |
| Credential handling | Password never saved, encrypted key | Same, plus exact token set, envelope encryption, 15-minute MFA expiry | **We are stronger**; make the "not stored" claim part of the summary. |
| Rivian-side disclosure | Not described | Discloses requests present as Rivian's iOS app | **We are stronger** (honesty stance). |
| Never sends vehicle commands | Not stated | Not stated | **Gap for both; we can say it** (only CSRF, Login, LoginWithOTP mutations in `internal/rivian/live.go`). Added. |
| Retention periods | Numbers given for logs, backups, history | "No automatic deletion" + DECIDE | **Roamer clearer.** We cannot honestly promise numbers yet; kept DECIDE with concrete options. |
| Log retention | "up to one year" | VERIFY | Open. Do not copy a number until Loki retention is confirmed. |
| Deletion | In-app, immediate sign-out | In-app, cascades, IdP removal, waitlist row | **Equal**; ours is more specific. |
| Export | Drives and charges download | JSON bundle (+ CSV in flight) | **Equal.** |
| Third parties | Named, with purpose | Named per recipient, with VERIFY marks | **We are stronger**, but our VERIFYs are visible weakness until closed. |
| Analytics | PostHog (third party) | First-party events only, DNT/GPC honored server-side | **We are stronger.** Roamer says it ignores Do Not Track. |
| Sale/ads | Never sell | Never sell, no ads | Equal. |
| Sale on acquisition | Stated | Absent | **Roamer more candid**; added as a DECIDE (we should say what happens). |
| Children | 18+ | "not for under-16s" in LAWYER note | Both need a decision; we leave it as DECIDE (see Terms). |
| CCPA/CPRA, GDPR | Absent | One sentence | **Both weak.** Closed: jurisdiction rights section with honest scope (no legal-basis claims until lawyer review). |
| Security | Generic | Specific (RLS, sealed secrets, sessions) | **We are stronger.** |
| Breach notice | None | "as required by law" | Equal; DECIDE a target. |
| Changes | Notice before big change | Notify signed-in users | Equal. |
| Operator identity | Named LLC | DECIDE | **Roamer clearer.** Open owner decision. |

## Things Roamer promises that we should not copy
1. **Specific retention numbers** ("up to one year" logs, "30 days" post-deletion, 6 months history). We have no automatic history deletion and unconfirmed log/backup retention. Promise only what the code or ops config enforces.
2. **"Immediately" deleted / durably deleted.** Our deletion cascades in the database, but encrypted backups persist until rotation and the phantom-repair undo table (`charges_deleted_backup`, 0049, no foreign key) is kept at least 90 days; we say so.
3. **"We encrypt your data ... wherever we can" as a blanket claim.** We state exactly what is sealed (Rivian session blobs) and what is not (vehicle history is protected by access control and disk/backup encryption as configured, not by per-user encryption).
4. **Acquisition clause** ("your information would go to the new owner"). Not a default we should adopt without an owner decision.
5. **Social features and AI assistant sharing.** Not applicable to us; do not add sharing language we do not have. Our LLM feature sends trip data to a provider only on use (details marked VERIFY).

## Where we lean on Roamer's approach
- Short summary first, then detail; a clear "what we do not do" list.
- Naming a supported secondary-driver path for credential safety (we already do via Authorized Driver; now in the summary).
- Stating retention for logs. We will once ops confirms the number.
