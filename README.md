# Rivolt

> A real Rivian companion. Live telemetry, per-trip analytics, a real
> charging cost ledger, and a route planner that tells you what a road
> trip will actually cost.

**Rivolt** is an open-source companion app for Rivian vehicles. It
streams live telemetry from your truck, records every drive and
charging session against your *real* electricity rate, and plans road
trips with cost / weather / efficiency analysis built in.

The official Rivian app shows you numbers; Rivolt turns them into a
ledger you own and a planner that actually accounts for what the trip
will cost.

---

## Why Rivolt

Rivolt sits between four kinds of tools and replaces all of them:

|                                | Official app | Roamer | Outpost | ElectraFi | **Rivolt** |
|---                             |:---:        |:---:   |:---:    |:---:      |:---:        |
| Live vehicle telemetry         | ✅           | ✅      | ✅       | ✅         | ✅           |
| Per-drive efficiency breakdown | ❌           | ⚠️      | ⚠️       | ✅         | ✅           |
| Real charging-cost ledger      | ❌           | ❌      | ❌       | ⚠️         | ✅           |
| Trip planner with cost/weather | ❌           | ❌      | ❌       | ❌         | ✅           |
| Multi-user / household         | ⚠️           | ❌      | ❌       | ❌         | ✅           |
| Open source, MIT               | ❌           | ❌      | ❌       | ❌         | ✅           |
| Optional AI commentary         | ❌           | ❌      | ❌       | ❌         | ✅           |

Rivolt's differentiators in one sentence each:

- **Costs use *your* rate.** Configure your home $/kWh; every
  charging session is priced against it. Trip planner shows DCFC
  spend + home-rate equivalent before you leave the driveway.
- **The trip planner is honest.** It surfaces total energy used,
  arrival time at your chosen departure datetime, charger detour
  cost, and weather impact for *this* corridor — not generic
  "consider Conserve mode" tips.
- **Per-drive analytics that explain the numbers.** Route maps,
  speed and elevation overlays, temperature, headwind, energy
  used vs. the rolling average, weather context. Not a wall of
  charts you have to interpret.
- **Read-only against Rivian.** Rivolt never sends commands to
  your truck. It can't unlock doors, start charging, or change
  drive mode. Telemetry only.
- **Open source and yours to inspect.** MIT-licensed core; full
  data export and one-click disconnect from the Settings page.

---

## What it does today

### Live + history

- **Live panel** — real-time SoC, range, charge state, gear, GPS
  via the Rivian WebSocket feed (1–5 s frames while driving).
- **Drive timeline** — per-drive maps with road-snapped polyline
  (OSRM / Valhalla), speed-coloured route, elevation, temperature,
  weather overlay, headwind, precipitation bands. GPS staleness
  detection with admin-tunable thresholds.
- **Charge sessions** — per-session curve, BMS thermal split when
  the Parallax feed is available, peak/avg kW, energy delivered,
  cost. Public / Home / DCFC bucketed automatically with no AI
  involved (DBSCAN clustering on coordinates).
- **ElectraFi CSV import** — backfill years of history in minutes.

### Trip planner

Plan a road trip end-to-end:

- **Pick origin, destination, and via-stops** with a custom
  date/time picker; departure defaults to "now" but anything on
  the calendar works.
- **Rivian's own planner picks charging stops** bounded by your
  starting SoC, target arrival SoC, and adapter config (Tesla
  NACS yes/no).
- **Charger overlay** along the actual route corridor (perpendicular
  distance to polyline, not bounding box), filterable DCFC / L2 /
  All, sourced from NREL AFDC.
- **Trip analysis** card surfaces: headline summary, DCFC spend in
  USD plus home-rate-equivalent energy cost, and optional LLM-written
  commentary across four named sections (cost framing, efficiency
  tips, weather impact, vehicle config). The dollar figures are
  computed deterministically from your home rate + stop SoC deltas;
  the LLM frames them, never invents them.
- **Last-trip persistence** — closing and reopening the page lands
  you back on your previous inputs.

### Notifications

- **Web Push** (VAPID) — per-device subscribe in Settings →
  Notifications. Charge-close events fire automatically with
  final SoC + kWh added in the body; per-event toggles let
  users opt in / out of charging-done, plug-in reminder, anomaly.
- **PWA** — installable on iOS / Android / desktop; offline-capable
  service worker; mobile-first chart cursor.

### Accounts

- **OIDC** sign-in via Ory Hydra (any compliant OIDC provider
  works). Invite-code signup flow for households + small fleets.
- **Multi-user from day one.** Every data-plane store carries
  `user_id`; Postgres Row-Level Security enforces isolation at
  the database, not the application layer. Rivian credentials
  are envelope-encrypted with the user ID bound as AES-GCM AAD
  so ciphertext can't be swapped across tenants.
- **Read-only** against the Rivian API. Rivolt sends commands to
  no Rivian endpoint, period.

---

## Sign up

See [`docs/SIGNUP.md`](docs/SIGNUP.md) for an end-to-end walkthrough
of:
- redeeming an invite code and creating your account,
- connecting your Rivian account (with the dedicated Authorized
  Driver account approach we recommend),
- importing your history from ElectraFi,
- configuring your home charging cost,
- planning your first trip.

---

## Data ownership

- Your Rivian credentials are AES-GCM sealed and decrypted only
  in-memory at request time. Disconnect Rivian in one click.
- AI features (when enabled by the operator) are powered by an
  install-wide provider — OpenAI, Anthropic, or Google Gemini —
  configured from the admin UI. Each user's drive / trip context
  is sent to that provider only at the moment a recap or trip
  analysis is generated; nothing is mirrored to a third-party
  service in between.
- All your drive, charge, and trip data is exportable as JSON from
  Settings → Data, and deletable from the same page.
- Read-only against the Rivian API.

---

## Stack

- **Single Go binary + embedded SPA.** Distroless multi-arch
  container image; the same binary serves API + static assets.
- **Go 1.25**, chi router, pgx-backed `database/sql`,
  coder/websocket for the Rivian feed, webpush-go for VAPID.
- **Postgres** with Row-Level Security, monthly partitioning on
  `vehicle_state`, envelope-encrypted secrets in `user_secrets`.
- **React 18 + TypeScript + Vite + Tailwind v3** for the SPA;
  TanStack Query for the data layer; uPlot for time-series
  charts; Leaflet + protomaps-leaflet for maps.
- **Optional AI providers** — OpenAI, Anthropic, or Google Gemini,
  selected install-wide by the operator from the admin UI.
  Hot-swappable; calls go to the chosen provider only when an AI
  feature actually runs (trip analysis, drive recap).

---

## Status

In production at [rivolt.dev](https://rivolt.dev). Preview at
[preview.rivolt.dev](https://preview.rivolt.dev) tracks `main`.

See [`docs/ROADMAP.md`](docs/ROADMAP.md) for what's queued and
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the load-bearing
design decisions.

---

## License

Core is MIT-licensed. Some future add-ons may ship under a separate
commercial license; details will be published alongside each add-on.

## Legal

"Rivian" is a trademark of Rivian Automotive, Inc. Rivolt is an
independent, community-built project with no affiliation to,
endorsement by, or partnership with Rivian. Reference to Rivian is
for descriptive purposes only.

Use at your own risk. Rivolt relies on unofficial access to Rivian's
APIs and may break at any time.
