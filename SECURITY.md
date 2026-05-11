# Security

If you discover a security vulnerability in Rivolt, please report it privately.

## Reporting

Two channels — pick whichever is easier:

- **GitHub Security Advisory** (preferred):
  <https://github.com/apohor/rivolt/security/advisories/new> —
  opens a private channel with the maintainer.
- **Email:** `pogiant@gmail.com` (subject line prefix:
  `[rivolt-security]`).

**Do not** open a public GitHub issue for security reports.

Please include:

- A description of the vulnerability
- Steps to reproduce (URLs, payloads, curl commands)
- The branch or release tag where you observed it
- Potential impact
- Any suggested fix (optional)

I'll acknowledge within 72 hours and aim to ship a fix within
14 days for confirmed issues, faster for anything that exposes
user data or vehicle credentials.

## Scope

In scope:

- **rivolt.dev** and **preview.rivolt.dev** (the hosted instances)
- The `apohor/rivolt` and `apohor/rivolt-infra` repositories
- Any container image published to `ghcr.io/apohor/rivolt`

Out of scope:

- The Rivian API itself, and other third-party services Rivolt
  depends on (OpenAI / Anthropic / Gemini, Open-Meteo, NREL AFDC,
  Ory Hydra / Kratos, Cloudflare). Report those to the relevant
  vendor.
- Self-hosted deployments where the operator weakened the
  default posture (e.g. `RIVOLT_ALLOW_NOOP_SEALER=1`, exposed
  Postgres without RLS, plain HTTP without a reverse proxy).
- Social-engineering attacks against the maintainer.

## What Rivolt promises

- **Read-only against Rivian.** The Rivian client makes no
  outbound calls that mutate vehicle state. It can't unlock
  doors, start charging, or change drive mode.
- **User isolation at the database.** Every tenant-scoped table
  carries `user_id`; Postgres Row-Level Security is enabled on
  all of them. See "Known limitations" below for the dormant /
  forced distinction.
- **Envelope-encrypted credentials.** Rivian credentials are
  sealed with AES-GCM using a KEK + per-user DEK, with `user_id`
  bound as AAD so ciphertext can't be swapped across tenants.
- **AI calls go directly to the configured provider.** No
  Rivolt-operated AI proxy, no analytics on prompts. Each
  request is a one-shot HTTPS call from the Rivolt pod to
  OpenAI / Anthropic / Gemini.
- **HTTPS-only cookies in production.** `HttpOnly`, `Secure`,
  `SameSite=Lax`.
- **No JWTs.** Sessions are opaque server-side tokens; revocation
  is a row update.
- **Defaults that fail closed.** Auth-on by default, RLS-on,
  secure-cookie-on, kill-switch wired, sealed credentials —
  weakening any of these is an explicit operator decision.

## What Rivolt does NOT promise

- Resistance against an attacker with **root on the Rivolt pod
  or DB host**. The KEK lives in memory; any actor with that
  access can decrypt sealed credentials.
- Hardening against **operator misconfiguration**. Running with
  `RIVOLT_ALLOW_NOOP_SEALER=1`, exposing the API without TLS, or
  granting DB superuser to the app role bypasses the intended
  posture by design.
- **Defense against a compromised Rivian gateway itself.** If
  Rivian's upstream is compromised, the data they push us is
  whatever they push; the classifier rejects malformed payloads
  but doesn't re-authenticate truthfulness.

## Known limitations

- **Row-Level Security policies are declarative-dormant.** They
  are enabled on every tenant table, but the app role currently
  retains `BYPASSRLS`. The application code is fully scoped per
  `user_id`; the RLS layer becomes load-bearing once stage 2 of
  migration 0008 lands. See
  `internal/db/migrations/0008_row_level_security.sql` for the
  rollout plan.
- **`'unsafe-inline'` on style-src.** The Leaflet basemap layer
  paints map markers with inline `style=` attributes; the
  shipped CSP includes `style-src 'self' 'unsafe-inline'` to
  allow this. XSS surface is bounded — operator-supplied chart
  strings flow through `escapeHTML` before reaching `innerHTML`.
  A nonce-based CSP is on the roadmap.
- **No automated dependency scanning gate in CI yet.**
  `npm audit` + `gosec` / `staticcheck` run by hand; Dependabot
  PRs are reviewed manually.

## Disclosure

After a fix ships I'll credit the reporter in the release notes
unless they prefer to remain anonymous.
