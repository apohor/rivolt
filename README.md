# Rivolt

> The first Rivian companion with a real AI copilot. Self-hosted. Your data, your keys, your rules.

**Rivolt** is an open-source, self-hosted companion for Rivian vehicles with an **AI copilot at its core**. It runs on your own hardware, uses your own AI API key, and turns your drive and charge data into plain-language insight — not just another dashboard of charts.

---

## Why Rivolt

Current Rivian companion apps (Rivian Roamer, Outpost, ElectraFi) and the official app itself are **dashboards**. They show you numbers. None of them *explain* them. And all of them are closed SaaS.

| | Official app | Roamer | Outpost | ElectraFi | **Rivolt** |
|---|---|---|---|---|---|
| AI copilot | ❌ | ❌ | ❌ | ❌ | ✅ |
| Self-hosted | ❌ | ❌ | ❌ | ❌ | ✅ |
| Credentials stay on your LAN | ❌ | ❌ | ❌ | ❌ | ✅ |
| Open source | ❌ | ❌ | ❌ | ❌ | ✅ |
| Home-energy aware | ❌ | ❌ | ❌ | ❌ | 🛣️ |
| Overland logbook | ❌ | ❌ | ❌ | ❌ | 🛣️ |

Rivolt is the first Rivian app to treat AI as a first-class primitive rather than a marketing afterthought.

## 🤖 AI copilot (the differentiator)

Every other Rivian app hands you a chart and leaves you to interpret it. Rivolt turns your own vehicle data into a conversation.

**Hot-swappable providers.** OpenAI, Anthropic, and Google Gemini adapters ship in the box. Use the model you trust — or swap mid-week without losing history. Bring your own key; costs are yours and Rivolt never proxies calls.

**Grounded in *your* data, not generic advice.** Every prompt is hydrated with your real drive traces, charge curves, battery temperatures, tire pressures, weather at the time, and electricity rate schedule. No hallucinated "try hypermiling!" tips — the coach points at the specific drive, the specific charge, the specific hour.

**What it does today (v0.1):**

- **Weekly driving digest** — 3-paragraph summary of your week: efficiency trend, cost, notable drives, anomalies
- **"Why did my efficiency drop?"** — root-cause analysis across weather, payload, HVAC, tire pressure, route
- **Trip planning** — given a destination, your historical curves, predicted weather, and home-charger schedule, recommend departure SoC + charging stops
- **Charging strategy coach** — multi-leg road trip plans with fallbacks, tuned to *your* vehicle's observed DC fast-charge curve
- **Anomaly alerts** — notifies when something in your data looks off (sudden range drop, phantom drain spike, unexpected BMS behavior)

**What's coming (v0.2+):**

- **Natural-language queries** — "what was my most efficient drive this month?", "how much did I spend charging in April?"
- **Voice-in via Web Speech API** — ask questions from the truck before a trip
- **Photo understanding** — attach a charger screen photo; AI logs cost/kWh/session ID automatically
- **Pre-departure brief** — a 30-second AI summary pushed as a notification before you leave the garage
- **Self-learning trip model** — AI improves its efficiency predictions the more you drive

**On privacy.** Your AI calls go *directly* from your Rivolt server to the provider you chose. Rivolt operates no AI proxy, no analytics, no telemetry. Caching happens on your disk.

## What else Rivolt does

Core (free, open-source, self-hosted):

- **Live vehicle dashboard** — SoC, range, charge state, last drive, last charge
- **Drive analytics** — route maps, efficiency breakdowns, cost-per-mile using your actual electricity rate
- **Charging analytics** — curves, temperature impact, session cost, BMS effects
- **Home vs. public detection** — charges are clustered by location (DBSCAN on lat/lon); the largest cluster is tagged `Home`, the second is `Work`, everything else is `Public`. No LLM involved; fully local.
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
