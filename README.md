# Rivolt

> A quiet rivolt against closed apps. Self-hosted Rivian companion with AI, home-energy integration, and an overland logbook.

**Rivolt** is an open-source, self-hosted companion for Rivian vehicles. It runs on your own hardware, keeps your credentials on your LAN, and gives you deeper insight into your drives, charging, efficiency, and routes than the official app.

---

## Why Rivolt

Current Rivian companion apps (Rivian Roamer, Outpost, ElectraFi) are closed SaaS products that hold your Rivian credentials on their servers. They're good at what they do, but they all share four limitations:

1. **Your data lives on someone else's server.** Disconnect anytime ≠ data sovereignty.
2. **No AI.** They show you charts. They don't explain what the charts mean.
3. **No home-energy awareness.** They don't know about your solar, your Powerwall, your TOU rate windows, or your Gen-2 V2H.
4. **No overland / trip-journal primitive.** Closest is a drive history list.

Rivolt is built to address all four.

## What Rivolt does

Core (free, open-source, self-hosted):

- **Live vehicle dashboard** — SoC, range, charge state, last drive, last charge
- **Drive analytics** — route maps, efficiency breakdowns, cost-per-mile using your actual electricity rate
- **Charging analytics** — curves, temperature impact, session cost, BMS effects
- **AI coach (bring your own key)** — plain-language weekly summaries, trip planning, "why did my efficiency drop" explanations grounded in *your* data, using your own OpenAI / Anthropic / Gemini key
- **Installable PWA** — works on any browser; service-worker offline; web push for plug-in reminders, update alerts, departure prep

Planned add-ons (not in the initial release):

- **Home energy integration** — Enphase Envoy, Tesla Powerwall, SolarEdge, Span panel adapters; schedule charges to solar peak or TOU off-peak
- **Overland mode** — GPX traces, offline topo tiles, photo attachments per waypoint, trail logbook export
- **Household fleet** — multi-vehicle aware, "best vehicle for this trip" recommendation, shared charger queue planning
- **Managed hosting** — optional hosted instance for users who prefer not to self-host

## Data sovereignty

- Credentials stored locally in SQLite, never transmitted outside your LAN on self-hosted deployments
- BYO AI key (OpenAI / Anthropic / Gemini) — calls go directly from your server to the provider you chose
- Full export any time
- Disconnect your Rivian account in one click
- Read-only against the Rivian API

## Stack

Same DNA as [Caffeine](https://github.com/apohor/caffeine):

- **Single Go binary + embedded web bundle** — distroless multi-arch Docker image
- **Go 1.25** — chi, pure-Go SQLite, coder/websocket, webpush-go
- **React 18 + TypeScript + Vite + Tailwind v3** — TanStack Query, uPlot for charts
- **PWA** — Web Push (VAPID), offline-capable service worker

## Status

**Pre-release.** This repo is the starting point — architecture proposal in [`docs/PLAN.md`](docs/PLAN.md). See [`docs/ROADMAP.md`](docs/ROADMAP.md) for the 2-week MVP slice.

## License

The core is MIT-licensed. Some future add-ons may ship under a separate commercial license; licensing details will be published alongside each add-on.

## Legal

"Rivian" is a trademark of Rivian Automotive, Inc. Rivolt is an independent, community-built project with no affiliation to, endorsement by, or partnership with Rivian. Reference to Rivian is for descriptive purposes only.

Use at your own risk. Rivolt relies on unofficial access to Rivian's APIs and may break at any time.
