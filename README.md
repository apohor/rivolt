# Rivolt

> A Rivian companion. Live status, per-trip analytics, a charging
> cost ledger, and a map-first route planner that estimates what a road
> trip will cost.

**Rivolt** is an open-source companion app for Rivian vehicles. It
shows your truck's live state, records every drive and
charging session against your own electricity rate, and plans road
trips with cost / weather / efficiency analysis built in.

Rivolt turns your Rivian's charging and driving into a priced ledger
you own, and a planner that accounts for what a trip will cost.

---

## Try the demo

**[demo.rivolt.dev](https://demo.rivolt.dev)** is a stable, read-only demo
on a *generated* sample truck: no sign-in, no Rivian account, and no
real vehicle data. Browse every tab below the way you would on your
own truck, and open the planner's sample trips to see a route with
charging stops.

- The numbers are made up (a home-charging month at $0.16/kWh);
  yours will differ.
- To use it with your own truck, sign up directly — no invite needed; see [`docs/SIGNUP.md`](docs/SIGNUP.md).

---

## Five tabs

Rivolt is organised around five tabs: **Now**, **Activity**,
**Money**, **Plan** and **Truck**. Now, Activity and
Truck share one calendar scope (Week, Month, Year, All or a custom
range), Money steps month by month, and the app follows your system
light / dark setting (or the Appearance card in Settings).

### Now — the truck at a glance

The current state on one card: battery as an arc with the charge limit
marked on it, range, and whether the truck is parked, plugged in,
charging or driving (live frames while it moves, slower when parked).
Under it, the numbers for the scope you picked: miles since the last
charge, miles, energy added, efficiency, cost, and an Idle awake tile
that links to Truck. Below those, miles per day and a battery trend
with charging and driving shaded behind the line, so you can see why
the percentage moved.

![Now](docs/screenshots/now.png)

### Activity — every drive and charge

One day-grouped **Feed** of drives and charges, newest first, with
**Table** and **Map** views of the same rows (the map shows Drives or
Charges one layer at a time). Open any row for its detail page.

**Drives.** Distance, duration, average speed, energy used and cost.
The detail page draws the road-snapped route (self-hosted Valhalla over
a Protomaps basemap) coloured by speed, with time-aligned charts for
speed, SoC, elevation, temperature, headwind and precipitation, and
weather context for the drive's start hour from Open-Meteo when
enabled. A "Low GPS accuracy" pill appears when the modem returned
stale fixes or implausible jumps; its thresholds are admin-tunable.

**Charges.** Date, location, energy delivered, peak kW and cost. The
detail page has the power curve, peak and average kW, session
duration and, when the Parallax feed is available, the thermal split
between energy into the pack and energy spent on battery conditioning.
Sessions are bucketed Home / Public / Fast by clustering on coordinates
and peak kW; home sessions price against your configured home rate,
others against the cost Rivian reports.

![Activity](docs/screenshots/activity.png)

### Money — what charging actually cost

A monthly statement: spend for the month, how it compares with the
last one, and where the energy came from (Home, Away, DCFC) in dollars
or kWh. Receipts list every session with its price and where that price
came from, and anything estimated or unpriced is flagged rather than
guessed (with a one-tap way to add the price). "About these numbers"
spells out what is and is not included in the total.

![Money](docs/screenshots/money.png)

### Plan — a map-first trip planner

This is the feature the app was built for. On a computer the map fills
the right of the page and stays put while you work on the left; on a
phone the map is on top and the trip sits in a sheet you drag up.

- **Route bar.** From and To (with a swap button), departure, starting
  SoC on a slider, and a Plan button. Via-stops, target arrival SoC,
  drive mode and the adapter switch live under "More options".
- **Result.** Rivolt asks Rivian's own planner for a route with
  charging stops and shows arrival SoC, trip cost, time, distance and
  charging time, with alternatives as a compact chooser.
- **Itinerary.** Start, every charging stop and arrival as a vertical
  timeline. Open a stop for the charger and network, SoC in and out,
  charge time, energy and cost, and see it highlighted on the map (and
  the reverse).
- **Map.** Self-hosted Protomaps basemap with the route, plus a charger
  overlay along the route corridor (distance to the polyline, not a
  bounding box) with DCFC / L2 / Hotels filters from NREL AFDC data.
- **Cost.** Dollar figures come from your home rate, stop SoC deltas
  and each matched network's rate (your member rate where set), not
  from an LLM. When the operator wires an AI provider, short
  commentary on cost, efficiency, weather and the vehicle is added.
- **Saved and Plan vs actual.** Planner, Saved and Plan vs actual are
  tabs at the top; after you drive a saved trip the app compares plan
  and reality (cost, energy, stops, arrival battery, duration).

![Plan](docs/screenshots/plan.png)

### Truck — health, sleep and drain

Parked drain, pack capacity and the lowest tire pressure and software
version the truck reports. The sleep chart shows idle-awake time per
day (the car awake while parked and not charging), with a warning when
it keeps waking up again within a minute.

![Truck](docs/screenshots/truck.png)

### On a phone, and in light

The same tabs work at phone width, and the main tabs have a light theme.

![Plan on a phone](docs/screenshots/plan-phone.png) ![Now on a phone](docs/screenshots/now-phone.png)

![Now in light](docs/screenshots/now-light.png)

---

## Settings — everything in one place

A five-tab layout for the per-user controls:

- **Account** — Rivian connection (with the optional dedicated
  Authorized Driver setup documented in
  [`docs/SIGNUP.md`](docs/SIGNUP.md)).
- **Vehicle** — vehicle profile (pack capacity), display units,
  home location.
- **Charging** — home $/kWh rate + currency, preferred public
  networks, trip planner defaults.
- **Notifications** — per-device push subscription with per-event
  toggles (see below).
- **Data** — ElectraFi CSV import + danger zone (full JSON
  backup, reset).

---

## Notifications — web push

Web Push (VAPID), per-device subscribe. Enable once on each
device you want pinged, pick which events you care about, and
Rivolt delivers an OS-level notification on the next matching
event. Currently wired: **charging session completes** (with the
final SoC + kWh added in the body). Plug-in reminders and
anomaly alerts have the plumbing — event sources land next.

---

## Sign up

See [`docs/SIGNUP.md`](docs/SIGNUP.md) for an end-to-end walkthrough
of:
- signing up directly,
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
- **Read-only against the Rivian API.** Rivolt sends commands to
  no Rivian endpoint, period. It can't unlock doors, start
  charging, or change drive mode. Telemetry only.

---

## Stack

- **Single Go binary + embedded SPA.** Distroless multi-arch
  container image; the same binary serves API + static assets.
- **Go 1.25**, chi router, pgx-backed `database/sql`,
  coder/websocket for the Rivian feed, webpush-go for VAPID.
- **Postgres** with Row-Level Security, monthly partitioning on
  `vehicle_state`, envelope-encrypted secrets in `user_secrets`.
  Multi-tenant from day one: every data-plane store carries
  `user_id`, every query filters on it, every request pins
  `app.user_id` so RLS does the final enforcement at the DB.
- **React 19 + TypeScript + Vite + Tailwind v4** for the SPA;
  TanStack Query for the data layer; uPlot for time-series
  charts; Leaflet + protomaps-leaflet for maps.
- **Optional AI providers** — OpenAI, Anthropic, or Google Gemini,
  selected install-wide by the operator from the admin UI.
  Hot-swappable; calls go to the chosen provider only when an AI
  feature actually runs (trip analysis, drive recap).

---

## Status

In production at [rivolt.dev](https://rivolt.dev). The demo at
[demo.rivolt.dev](https://demo.rivolt.dev) runs on generated data, and
a preview at [preview.rivolt.dev](https://preview.rivolt.dev) tracks
`main`.

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
