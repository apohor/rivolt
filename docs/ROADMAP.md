# Rivolt — roadmap

## 2-week MVP slice

Goal: a self-hostable binary that logs in with Rivian credentials, shows live vehicle state, drive history, and a weekly AI summary. Enough to dogfood on your own Synology and demo in the Rivian Discord.

### Week 1 — plumbing

- [ ] **Day 1-2: Rivian API client**
  - Fork / vendor an existing community Go or Node Rivian API wrapper (e.g. python-rivian port)
  - Implement `auth.Login()`, `auth.Refresh()`, `vehicle.State()`, `vehicle.History()`
  - Token persistence in SQLite with encryption at rest
- [ ] **Day 3: Storage schema**
  - `vehicles` — id, VIN, name, model, added_at
  - `vehicle_state` — rolling snapshot log (SoC, range, odometer, gear, lat/lng, ts)
  - `drives` — derived from state deltas (start/end ts, start/end odometer, route, energy used, efficiency)
  - `charges` — derived from charge state (start/end SoC, start/end ts, kWh added, max_power, location)
- [ ] **Day 4: Scaffolding**
  - Clone Caffeine's skeleton: `cmd/rivolt/main.go`, `internal/web`, `internal/api`, `internal/push`, AI adapters
  - Rename all identifiers; strip coffee-specific routes
  - `Dockerfile` (distroless multi-arch), `docker-compose.yml`, `.github/workflows/build.yml`
- [ ] **Day 5: Basic SPA**
  - Vite + React + Tailwind home page: current SoC, range, last drive card, last charge card
  - TanStack Query polling `/api/vehicle/state` every 30s
  - Dark mode from Caffeine carried over

### Week 2 — insight

- [ ] **Day 6-7: Drive detail page**
  - Drive list with map thumbnail, date, distance, efficiency, cost
  - Drive detail with full route trace, elevation, speed plot
  - Electricity rate settings (flat / TOU windows)
- [ ] **Day 8: Charge detail page**
  - Charge list with power curve thumbnail
  - Charge detail with power/SoC/temperature curves (uPlot)
- [ ] **Day 9: AI weekly summary**
  - Reuse Caffeine's `internal/ai` adapters verbatim (OpenAI / Anthropic / Gemini)
  - New prompt: "Summarize this week's driving and charging in 3 paragraphs. Call out anomalies."
  - BYO-key settings page
- [ ] **Day 10: Web push**
  - VAPID keypair from Caffeine's `internal/push/vapid.go` verbatim
  - Subscription: "notify me when charging completes"
- [ ] **Day 11-12: Polish + docs**
  - `INSTALL.md`, `DEVELOPMENT.md`
  - Screenshots (fullPage + Pillow trim from Caffeine playbook)
  - First public release → tag `v0.1.0`
- [ ] **Day 13-14: Dogfood + Discord**
  - Deploy to Synology alongside Caffeine
  - Announce in r/Rivian, Rivian Discord, Rivian Owners Forum

## Post-MVP

Ordered by expected value, not by time.

### v0.2 — home-energy foundation

- [ ] Enphase Envoy local API adapter
- [ ] Tesla Powerwall local API adapter
- [ ] "Schedule charge to solar peak" scheduler
- [ ] "Effective cost per kWh after solar offset" in charge detail

### v0.3 — overland mode

- [ ] GPX export per drive
- [ ] Photo attachment per drive waypoint
- [ ] Offline OSM tile caching (pre-download a bounding box)
- [ ] Trail logbook export (GPX + photos + markdown)

### v0.4 — multi-vehicle household

- [ ] Support > 1 vehicle per account
- [ ] "Which vehicle for this trip" recommendation
- [ ] Shared home charger queue (if 2 vehicles, 1 wall connector)

### v0.5 — Plus tier

- [ ] License-key validation flow
- [ ] Gate home-energy + overland + multi-vehicle behind Plus
- [ ] Stripe integration for Plus tier
- [ ] Privacy Policy + Terms of Service

### v0.6+ — fleet

- [ ] Mileage reports (IRS-grade CSV export)
- [ ] Per-driver attribution (who was driving when)
- [ ] SSO (Google Workspace, Microsoft Entra)

## Non-roadmap

These are explicit "not now, probably never":

- Social / leaderboards (Roamer owns this)
- Native iOS app (PWA is sufficient)
- Android Auto / CarPlay integration (requires OEM partnership we won't get)
- Inventory scraping
