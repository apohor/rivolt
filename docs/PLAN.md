# Rivolt — project plan

## One-line pitch

A self-hosted, AI-assisted, home-energy-aware companion for Rivian owners. Your truck. Your data. Your rules.

## Positioning

| Product | Positioning |
|---|---|
| Rivian Roamer | "Data analytics for Rivian owners" (SaaS, social, leaderboards) |
| Outpost | "Personal tracker app" (iOS-only, minimal) |
| ElectraFi | "TeslaFi for Rivian" (legacy UI, power-user) |
| **Rivolt** | **"Your Rivian, on your own terms"** (OSS, self-hosted, AI, home-integrated) |

## Target user

Rivian owners who also:
- Run a home lab (NAS, Home Assistant, Synology, Unraid, Proxmox)
- Have solar + a Powerwall / Enphase / SolarEdge / Span panel
- Prefer open-source / BYO-key over SaaS lock-in
- Want deeper data than the official Rivian app

Adjacent markets: small fleets (overland guides, film crews, contractors) once the core is stable.

## Differentiators

1. **Self-hosted** — single Go binary + embedded web bundle, SQLite persistence, distroless multi-arch Docker image. Credentials never leave the LAN.
2. **AI coach (BYO key)** — OpenAI / Anthropic / Gemini adapters, hot-swappable. Plain-language explanations grounded in the user's own data.
3. **Home-energy aware** — native adapters for Enphase Envoy, Tesla Powerwall, SolarEdge, Span panel. TOU rate imports. Schedule charges to solar peak or off-peak windows. Report "effective cost per kWh" after solar offset.
4. **Overland mode** — GPX traces, offline topo tiles (MapTiler / OSM), photo-per-waypoint, trail log export.
5. **Household fleet** — multi-vehicle aware. "Best vehicle for this trip" given SoC + weather + charger availability.

## Deliberately not doing (v1)

- Social / leaderboards (Roamer owns this)
- Native iOS app (PWA covers 95% of use cases without App Store friction)
- Inventory scraping (legal risk; Roamer already does it)
- Paid hosting at launch (self-hosted only; cloud tier later)

## Monetization

| Tier | Price | What's included |
|---|---|---|
| Core (OSS) | Free | Self-host forever, basic analytics, trip log, charge tracking, Web Push, AI with your own key |
| Cloud | $6/mo · $60/yr | Hosted instance, automatic backups, HTTPS |
| Plus *(license key — works on both self-hosted & cloud)* | $9/mo · $90/yr | Home-energy integrations, overland mode, multi-vehicle, 100 bundled AI calls/mo |
| Fleet | $25/mo per vehicle | Mileage/tax reports, per-driver accountability, SSO |

Dual-licensing (MIT core + commercial premium plugins) — same model as GitLab, Cal.com, Plausible, Sentry.

## Risks

- **Rivian API access is unofficial.** Mitigate with a swappable integration layer; track upstream breakage; publicly commit to not abusing the API.
- **Rivian TM exposure** from the "Riv" prefix in the name. Mitigate with clear disclaimer footer, explicit "unaffiliated" language, and no Rivian logos / brand assets.
- **Smaller TAM than Tesla.** This is a lifestyle-business opportunity; ceiling likely 20-50k paying users over 3 years with good execution.
- **Legal / privacy** — never sell user data, never ship telemetry, ship a clear Privacy Policy before any paid tier.

## Architecture (inherited from Caffeine)

```
cmd/rivolt/main.go            # Entrypoint
internal/
  rivian/                     # Rivian API client (reverse-engineered)
  shots → trips/              # Drive & charge session store
  ai/                         # OpenAI / Anthropic / Gemini adapters
  home/                       # Enphase, Powerwall, SolarEdge, Span
  overland/                   # GPX + tile + photo store
  push/                       # VAPID Web Push
  api/                        # chi HTTP router
  web/                        # Embedded SPA bundle
web/                          # React + Vite + Tailwind SPA
docs/                         # This folder
deploy/                       # docker-compose examples
```

Reuse ~40% of Caffeine scaffolding verbatim: AI provider adapters, VAPID push store, SQLite helpers, PWA shell, embedded web.go pattern, distroless Dockerfile, GitHub Actions multi-arch build.

## First 2-week slice

See [`ROADMAP.md`](ROADMAP.md).
