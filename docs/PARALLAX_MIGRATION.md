# Parallax migration

Plan to move Rivolt's telemetry from Rivian's legacy `vehicleState` JSON
WebSocket to the modern **Parallax** RVM feed, topic by topic, measure-first
and additive - never a big-bang cutover.

> **Status refreshed 2026-10-16 (QA, read-only evidence).** The section below
> is verified against `main` and 24 h of production logs. The design notes
> further down (Why, Two modes, Topic map, Phases) are the original July plan
> and are kept for the reasoning; where they disagree with this section, this
> section wins.

## Operator switch

One flag: **`RIVOLT_PARALLAX`**. It turns on Parallax GPS, the drive-dynamics
subscription and the capture set together (see `monitor.go`, "RIVOLT_PARALLAX
is the single operator switch"). The older `RIVOLT_PARALLAX_GPS`,
`RIVOLT_PARALLAX_DRIVE_DYNAMICS` and `RIVOLT_PARALLAX_CAPTURE` variables are
still read and still work, but are retired from operations: set the single
flag. Per vehicle, Parallax is used only when Rivian reports
`VEHICLE_CONNECTIVITY_PARALLAX=AVAILABLE`.

Production runs it on: every topic that only the capture set subscribes to
(alarm, trailer, 12V, OTA, pet mode) logs frames in prod today.

## What we subscribe to, and what each topic does today (21 topics)

Status words: **authoritative** (the live state follows it; vehicleState is the
fallback), **applied** (decoded and written to the live state), **decode-log**
(decoded and logged, not applied), **raw** (payload logged for offline RE only),
**silent** (subscribed, no frame arrived). "24 h frames" is the number of
`parallax ...` log lines in production Loki over the last 24 h, all vehicles
(9 vehicles, R1S/R1T/R2); it is a liveness check, not a data-quality check.

| # | Topic (RVM) | Status | 24 h frames | Notes |
|---|-------------|--------|-------------|-------|
| 1 | `dynamics.vehicle.gnss` | authoritative (GPS backbone) | 1,090 `gps covering vehicleState stall` lines | Own subscriber. Fills vehicleState stalls; ~60 s cadence |
| 2 | `energy.high_voltage.battery_state` | authoritative (SoC, pack kWh, pack temp) | 8,162 | Logs `px_soc` vs `vehicleState_soc`; also feeds pack health |
| 3 | `energy_edge_compute.graphs.charge_session_breakdown` | applied (live charge data) | 4,098 (+82 first frames) | Own subscriber, per charging session |
| 4 | `dynamics.vehicle.gear` | authoritative to open a drive; P applied when fresh | 914 | Enum 1 P / 2 R / 3 N / 4 D. A fresh P also clears a stale R/D (since v0.33.1; R2s never send P on vehicleState, only here). Drive close still follows vehicleState |
| 5 | `dynamics.vehicle.drive_mode` | applied | 3,936 | APK enum, mapped to vehicleState names |
| 6 | `dynamics.vehicle.odometer` | applied (stall bridge) | 4,353 | Whole km; only advances the cache when higher |
| 7 | `vehicle.power.state` | applied | 4,106 | 1 sleep, 2 standby, 3 ready, 4 go; 5 reset, 6 OTA update, 7 shutdown mapped on main (#323, next release; the R2's only power source - see R2 notes) |
| 8 | `dynamics.vehicle.range` | applied | 5,326 | Whole km |
| 9 | `dynamics.tires.state` | applied | 7,392 | Four pressures, bar |
| 10 | `comfort.cabin.cabin_temperatures` | applied | 5,083 | Interior temperature |
| 11 | `body.closures.states` | applied | 5,107 | Doors, frunk, liftgate, side bins, tonneau, windows (instances 12-15, 16 = rear window on R2). See the closure enum below |
| 12 | `body.locks.states` | applied | 4,208 | Door locks |
| 13 | `body.trailer.state` | decode-log | 3,928 | No State destination yet |
| 14 | `energy.low_voltage.battery_state` | applied | 3,928 | 12V health |
| 15 | `ota.ota_state.vehicle_ota_state` | raw | 3,928 | Not decoded |
| 16 | `ota.deployment.state` | raw | 3,981 | Version strings in a nested message; not decoded |
| 17 | `security.alarm.state` | applied | 3,928 | Alarm sound status |
| 18 | `comfort.cabin.cabin_preconditioning_status` | decode-log | 241 | Field number unconfirmed; log only |
| 19 | `comfort.cabin.pet_mode_status` | applied | 3,639 | |
| 20 | `comfort.cabin.climate_hold_status` | raw | 4,660 | Captured, not decoded |
| 21 | `security.access.immobilizer_state` | silent | 0 | Subscribed; no frame seen in 24 h on any vehicle |

How this was checked: topic list and handling from `internal/rivian/parallax.go`
and `StateMonitor.onDriveDynamicsFrame`; frame counts from Loki
(`{namespace="rivolt", app="rivolt"} |= "parallax drive-dynamics shadow"`, grouped by `topic`,
plus the `parallax battery_state charge`, `rivian parallax charge-breakdown frame`
and `parallax gps covering vehicleState stall` messages), 2026-10-16.

### Closure status enum (wire values)
`0 UNSPECIFIED, 1 OPEN, 2 CLOSE, 3 AJAR, 4 OPENING, 5 CLOSING`. Instances:
1-4 doors (row 1 L/R, row 2 L/R), 5 frunk, 6 tailgate, 7 liftgate, 8/9 side
bins, 10 charge port (R1 only), 11 tonneau, 12-15 windows FL/FR/BL/BR, 16 rear
window, 10000 the window group. Today anything that is not CLOSE reads as
"open"; mapping AJAR to "vented" and OPENING/CLOSING to "moving" is a small
open item (R2 gaps #3). (An earlier QA note read these values from the app's
Kotlin enum, which has a different order; the wire values above are the ones
that match live data.)

## R2 notes

- **Gear and park come from Parallax.** R2s never send a park gear on
  vehicleState (the field is empty or stuck on the last R/D). Parallax
  `dynamics.vehicle.gear` does send P (enum 1), and a fresh P is applied to the
  live state, so a parked R2 reads parked. Before v0.33.1 the stale R/D stayed
  for hours; that history was re-segmented.
- **Power state is Parallax-only.** R2s report power through
  `vehicle.power.state`; enums 5-7 (vehicle reset, OTA update, shutdown) are mapped on
  main (#323, ships in the next release).
- **Plug state needs `charging.session.status`.** vehicleState `chargerStatus`
  is NULL on every R2 row, and none of the topics we subscribe to carries a
  plugged signal. The app reads `charging.session.status` (not subscribed
  today): field 1 `plug_connection_status` (0 INIT, 1 NOT_CONNECTED,
  2 CONNECTED, 3 ERROR, 4 V2L_CONNECTED), field 2 `display_status` (ready,
  connecting, active, complete, scheduled, errors...), field 3 `evse_type`.
  Until it is implemented, R2 plugged is inferred from `charging_active` with
  kW > 0, which cannot see a plugged-in car that is not charging.
- **Pack capacity.** `energy.high_voltage.battery_characteristics` (not
  subscribed) carries pack capacity and chemistry for every model; it is cleared
  for a one-day capture.
- **Closures arrive for R2s** (window and door events seen in prod) but are not
  stored: `vehicle_state.raw` is NULL by design, so only the shadow log records
  them.

## Phase 5 / `PX_STATE_ALL` status

Not started, and now has a real first candidate. `PX_STATE_ALL=AVAILABLE`
(the gate for replacing legacy `vehicleState` wholesale) is reported by **both
R2s** in prod today and is `absent` for every R1 (24 h of `supported features`
log lines: 24 AVAILABLE, 72 absent, all vehicles
`VEHICLE_CONNECTIVITY_PARALLAX=AVAILABLE`). `GET /api/supported-features` is
the on-demand probe. We still use legacy `vehicleState` for every vehicle:
drive close, the sleep watchdog, and the fallback for every field above. The
preconditions in Phase 5 stand: a Parallax-liveness watchdog before the
fallback is removed, and the missing topics below (plug state, closure
persistence) first.

## App topics we do not subscribe to

The app's `zff` topic table holds 75 names (decompile of Rivian Android 3.17.0,
extracted by the lead into the team scratchpad). 21 are subscribed (above). The
other 54 split into **22 command topics** and **32 state or settings topics**.
Rivolt is read-only, so commands are out of scope.

**Notable state topics (candidates, in priority order):**

| Topic | Why it matters |
|-------|----------------|
| `charging.session.status` | R2 plug state (above); also display status (scheduled, complete, errors) |
| `energy.high_voltage.battery_characteristics` | Real pack capacity, chemistry and user energy limits for every model |
| `energy_edge_compute.graphs.parked_energy_distributions` | Rivian's own breakdown of parked (vampire) energy: a direct comparison for our drain card |
| `charging.session.time_estimation` | Rivian's time-to-full/limit estimate for a live charge |
| `charging.smart_charging.settings` / `smart_charging_info` / `weighted_charging_forecast` | Smart/clean-energy charging state, which explains "plugged but not charging" |
| `comfort.user_modes.state` | Camp/pet/car-wash mode, a good overnight-drain explainer |

**Other state or settings topics (not subscribed):**
`body.windows.states` (never emits on the R1S; windows ride on closures),
`charging.schedule.time_window`, `charging.session.notification`,
`charging.session.soc_slider`, `charging.session.trip_target`,
`comfort.cabin.cabin_ventilation_setting`, `comfort.cabin.climate_hold_setting`,
`comfort.cabin.defrost_defog_status`, `comfort.cabin.hvac_settings_status`,
`comfort.cabin.seat_conditioning_status`, `dynamics.vehicle.location`,
`energy_edge_compute.graphs.charging_graph_global`,
`energy_edge_compute.graphs.cold_weather_soc`,
`navigation.navigation_service.trip_info`, `navigation.navigation_service.trip_progress`,
`ota.user_schedule.ota_config`, `security.access.btm`,
`security.access.passive_entry`, `security.access.passive_entry_debug`,
`security.access.vas_fault`, `security.video_monitoring.state`,
`vehicle.network.state`, `vehicle.profiles.active_user`,
`vehicle.wheels.vehicle_wheels`.

**Command topics (out of scope, 22):** `body.closures.{open,close}`,
`body.locks.{lock,unlock}`, `charging.energy.{set_charge_limit,start_charging,stop_charging}`,
`charging.session.remote_command`, `comfort.cabin.{enable,disable}_preconditioning`,
`comfort.cabin.set_{cabin_surface,climate_hold,defrost_defog,pet_mode,preconditioning_temperature}`,
`ota.install.install_now`, `security.alarm.{enable,disable}_panic`,
`security.video_monitoring.{enable,disable}_video_monitoring`,
`security.video_monitoring.start_{live_streaming,video_download}`.

---

## Original plan (July 2026, design notes)

The sections below are the original reasoning and phase plan. Their status
columns and flag names are from July; use the status section above for the
current state.

## Why

Legacy `vehicleState` is a single subscription pushing a ~50-field JSON delta.
Parallax splits the same surface into independent protobuf **RVM topics**
(`parallaxMessages(vehicleId, rvms)` on the WS endpoint), each with its own
cadence and fidelity. Observed wins so far:

- **GPS** (`dynamics.vehicle.gnss`): steady ~60s cadence with no long stalls,
  vs vehicleState's ~3s-median-but-up-to-5-min-gaps that render as
  straight-line jumps. Shipped to preview.
- **Battery pack temperature** (`energy.high_voltage.battery_state`): not
  exposed by legacy `vehicleState` at all.
- **Actual pack capacity** (`chargeKwh`): direct degradation signal legacy
  only approximates.

## Two migration modes (this is the crux)

Rivian gates Parallax two ways (see `reference_parallax_gating` memory /
APK 3.13.x):

| Gate | Meaning | User R1S status |
|------|---------|-----------------|
| `VEHICLE_CONNECTIVITY_PARALLAX=AVAILABLE` | vehicle streams Parallax topics at all | AVAILABLE |
| `PX_STATE_ALL=AVAILABLE` (`PARALLAX_VEHICLE_STATE`) | vehicle may **replace** vehicleState wholesale | absent |

So there are two distinct moves:

1. **Cherry-pick** individual topics (gate: `VEHICLE_CONNECTIVITY_PARALLAX`).
   Works on the R1S **today**. This is Phases 1-4 below - Parallax and
   vehicleState run side by side, Parallax authoritative per-field once proven,
   vehicleState the fallback.
2. **Drop legacy vehicleState** entirely (gate: `PX_STATE_ALL`). Not enabled
   for the R1S yet. This is Phase 5, and only per-vehicle once the flag flips.

The plan reaches full Parallax coverage via mode 1, then does mode 2 opportunistically per vehicle.

## Topic map (legacy field group -> Parallax RVM)

| Legacy vehicleState | Parallax RVM topic | Status |
|---------------------|--------------------|--------|
| gnssLocation/Speed/Bearing/Altitude | `dynamics.vehicle.gnss` | **PRIMARY GPS backbone** — every gnss frame applies; vehicleState's ~3s fixes demoted to detail-fill between gnss ticks via fix-timestamp monotonicity (they stall for minutes / replay stale fixes; 5-min blackout observed 2026-07-14) |
| pack temperature (none in legacy) | `energy.high_voltage.battery_state` | **recording** |
| charge live data | `energy_edge_compute.graphs.charge_session_breakdown` | **shipped** |
| gearStatus | `dynamics.vehicle.gear` | **authoritative (open) — PROD** — enum 1P/2R/3N/4D; opens drives early, close stays on vehicleState |
| driveMode | `dynamics.vehicle.drive_mode` | **authoritative — PROD** — enum from APK 3.14.0 (2=everyday … 15=winter; anchored by live 2=EVERYDAY); mapped to vehicleState's lowercase names |
| vehicleMileage (odometer) | `dynamics.vehicle.odometer` | **authoritative (stall-bridge) — PROD** — `{1:varint}` whole **km**; monotonic, only advances cache when higher, so vehicleState's 0.01-mi wins normally |
| distanceToEmpty | `dynamics.vehicle.range` | **authoritative — PROD** — `{1:varint km}` (308 km = vehicleState 191.4 mi); applied to DistanceToEmpty (already km) |
| tirePressure{FL,FR,RL,RR} | `dynamics.tires.state` | **authoritative — PROD** — repeated TireState{pos 1-4, pressure double bar}; 3.25/3.28/3.25/3.25 bar matched vehicleState |
| batteryLevel / batteryCapacity | `energy.high_voltage.battery_state` | **authoritative on preview** — charge_state (field 1) → field 1 (double) = SoC %, field 2 (double) = pack capacity kWh. RE'd from a live frame (SoC 67.4 vs vehicleState 68.7; capacity 123.4 kWh) — note doubles, not the float32 the APK enum hinted |
| twelveVoltBatteryHealth | `energy.low_voltage.battery_state` | **applied on preview** — field 1 = LOW_VOLTAGE_BATTERY_HEALTH_STATUS (APK: 0 UNSPEC, 1 NORMAL, 2 LOW); mapped to normal/low → TwelveVoltBatteryHealth. Live capture: 1 (normal). Logged next to vehicleState for verify before prod |
| cabinClimateInteriorTemperature | `comfort.cabin.cabin_temperatures` | **authoritative — PROD** — field 3 (float32 °C) = interior temp; 26.0 matched vehicleState inside_temp_c (field 4 = driver setpoint) |
| cabinPreconditioningStatus | `comfort.cabin.cabin_preconditioning_status` | **decode-logged (preview)** — CABIN_PRECONDITIONING_STATE enum mapped (APK: 1-3 preconditioning, 4 preconditioned, 8 not_preconditioning), but the topic's field number is unconfirmed (never emitted during RE — car wasn't preconditioning). Log-only until a live capture verifies, then apply |
| cabinHoldStatus | `comfort.cabin.climate_hold_status` | **captured (preview)** — live wire `08 02 10 01 18 01 22 00` (field1=2 + 3 more); field-1 enum has a generic APK name not yet pinned, so raw-captured pending decode |
| petModeStatus | `comfort.cabin.pet_mode_status` | **applied on preview** — field 1 = PET_MODE_STATE (APK: 0 OFF, 1 ON, 2 DISABLED, 3 FAULTY); live capture 2 (disabled) → PetModeStatus. Logged next to vehicleState |
| immobilizer / OTA | `security.access.immobilizer_state` / `ota.deployment.state` | **captured (preview)** — OTA deployment.state carries version strings ("2026.15.01", build 5877a36f) in a repeated nested message; immobilizer pending first frame. Both raw-captured pending decode |
| doors/closures/locks/windows | `body.{closures,locks}.states` | **authoritative — PROD** — repeated {instance, status}; APK enum maps instances→doors/frunk/liftgate/tailgate/tonneau + door locks→Locked. CLOSE=2, LOCKED=1. **Windows ride on the closures topic** as instances 12=FL / 13=FR / 14=RL / 15=RR (mapping confirmed live on the R1S 2026-07-17, one window at a time). Graduated from CaptureRVMs into the base drive-dynamics subscription |
| windows (standalone) | `body.windows.states` | **dead — never emits** — the R1S reports window state on `body.closures.states` (above), not this topic; dropped from CaptureRVMs |
| trailer | `body.trailer.state` | **decode-logged (preview)** — field 1 = TRAILER_PRESENCE_STATUS (APK: 0 UNSPEC, 1 NOT_PRESENT, 2 PRESENT, 3 PRESENT_WITH_BRAKES, 4 INVALID). Live capture: 1 (not present). No State destination field yet, so log-only |
| ota{Current,Available}Version/Status/InstallProgress | `ota.{ota_state,deployment,install}.*` | todo |
| alarmSoundStatus | `security.alarm.state` | **applied on preview** — field 1 = SOUND_ALARM_STATUS (APK: 0 UNSPEC, 1 FALSE, 2 TRUE, 3 SNA); mapped to false/true → AlarmSoundStatus. Live capture: 1 (false). Logged next to vehicleState for verify before prod |
| gearGuardLocked | `security.access.*` | todo — no clean gear-guard-locked enum found in APK 3.14.0 security topics |
| powerState | `vehicle.power.state` | **authoritative — PROD** — `{1:varint}`, `3=ready`/`4=go` confirmed; applied to cache; sleep enum TBD |

Coverage is effectively complete - every legacy field has a Parallax home.

## Phases

### Phase 0 - foundation (done)
Env master `RIVOLT_PARALLAX_GPS` (now `RIVOLT_PARALLAX`), `SupportedFeatures` capability query +
per-vehicle gating, GPS + pack-temp + charge-breakdown on Parallax, first-frame
engage so an advertised-but-silent topic never blanks vehicleState.

### Phase 1 - consolidate the transport
`parallaxMessages` takes a **list** of rvms, so subscribe to all wanted topics
over **one** multiplexed subscription per vehicle instead of one-per-topic
(today gnss, battery_state, charging each open their own). Add a
`topic -> decoder` registry and a single fan-out loop. Keeps WS/subscription
count flat as topics grow (matters at 60 vehicles x N topics vs Rivian rate
limits). Foundational; no behaviour change.

### Phase 2 - drive dynamics
`dynamics.vehicle.{gear,drive_mode,odometer,range}`, `dynamics.tires.state`.
Highest-value, lowest-risk (drive lifecycle already keys off gear). Per topic:
RE the protobuf from APK + live frames, decode, **measure cadence vs
vehicleState over a real drive**, then make Parallax authoritative for that
field with vehicleState fallback.

**Capture 2026-07-13** (parked R1S — first three via `/api/parallax-raw`,
power.state via the shadow recorder). All four are single-field messages
carrying one varint in field 1:

| RVM | payload (b64 / hex) | field 1 | meaning |
|-----|---------------------|---------|---------|
| `dynamics.vehicle.gear` | `CAE=` / `08 01` | 1 | **P** (Park) |
| `dynamics.vehicle.drive_mode` | `CAI=` / `08 02` | 2 | driveMode enum = 2 |
| `dynamics.vehicle.odometer` | `CMPXAw==` / `08 c3 d7 03` | 60355 | **60355 km** |
| `vehicle.power.state` | `CAM=` / `08 03` | 3 | **ready** (== vehicleState "ready") |

**Drive capture 2026-07-14** (short R1S drive, shadow recorder). Gear enum
pinned by aligning each Parallax frame's `ts_ms` to the vehicleState shift
transition it precedes:

| gear enum | maps to | evidence |
|-----------|---------|----------|
| 1 | **P** | steady while parked |
| 2 | **R** | fired 2.2 s before vehicleState `R` |
| 3 | **N** (inferred) | not shifted through; P/R/N/D = 1/2/3/4 |
| 4 | **D** | fired 2.2 s before vehicleState `D` |

**drive_mode enum** (from the Rivian APK 3.14.0 `DRIVE_MODE_*_VALUE` proto
constants, extracted from `classes*.dex`; anchored by the live capture where
`2` = the default mode):

```
 0 unspecified   4 off_road_sport_auto   8 sport        12 off_road_sand
 1 init_mode     5 off_road_sport_drift  9 distance     13 off_road_rocks
 2 everyday      6 sport_launch         10 towing       14 off_road_mud
 3 off_road_snow_ice  7 fault           11 off_road_auto 15 winter
```

Mapped to vehicleState's lowercase driveMode names in `driveModeFromParallax`;
0/1/7 fall back to vehicleState. `power.state` enum: `4 = go` (powered/driving), `3 = ready` (awake idle) —
`3→4` led drive-start, `4→3` led drive-end; `sleep` enum TBD. Odometer
ticked 60355→60357 km over ~1 mi (whole-km resolution confirmed). **Parallax
gear led vehicleState by ~2.2 s** on both transitions on a drive with no
vehicleState stall — the lead grows to tens of seconds when vehicleState
stalls (the late-recording-start case). Enough to build the authoritative
gear decoder; still want a stalled-drive capture before the cut.

Odometer unit **settled: whole kilometers** — 60355 km = 37502.9 mi vs
vehicleState's 37503.06 mi at capture time (so ~1 km / 0.62 mi resolution,
coarser than vehicleState's 0.01 mi). Gear enum: only `P=1` is confirmed;
`R/N/D` are pinned from a drive capture — the shadow recorder
(`RIVOLT_PARALLAX_DRIVE_DYNAMICS`, `StateMonitor.driveDynamicsSubscriber`) logs the
raw Parallax enum next to the concurrent vehicleState gear so the mapping is
observable over a real drive. `decodeSingleVarint` + `gearFromParallax` in
`internal/rivian/parallax.go`.

**Status (2026-07-17): Phase 2 shipped to prod.** `RIVOLT_PARALLAX_DRIVE_DYNAMICS`
is enabled in prod, so gear (open), drive_mode, odometer (stall-bridge),
power.state, range, tires, and cabin interior temp all apply authoritatively
there, plus body closures + door locks (graduated from the preview capture set
into the base subscription). vehicleState remains the drive-close authority and
the fallback for every field. Still preview-only under `RIVOLT_PARALLAX_CAPTURE`:
windows (decoded to structured shadow logs, not yet applied), trailer, 12V, OTA,
alarm, and preconditioning (raw shadow capture).

### Phase 3 - energy / charging
`energy.high_voltage.battery_state` (extend the temp decoder to SoC + capacity),
`energy.high_voltage.battery_characteristics`, `energy.low_voltage.battery_state`
(12V). Cross-check SoC/capacity against the existing charge-breakdown pipeline
and `chargeKwh` (pack-health signal). Reconcile the two energy sources before
either goes authoritative.

**Status (2026-07-17):** SoC + pack capacity already decode from
`battery_state` and apply under `RIVOLT_PARALLAX_DRIVE_DYNAMICS` (so
Parallax SoC currently feeds BatteryLevelPct → drive/charge deltas in
prod). Capacity is cross-checked via `observeBatteryCapacity` → the
pack-health card (documented vs vehicle-reported). **Reconciliation
instrument added:** the battery_state subscriber now logs
`px_soc` / `vehicleState_soc` / `soc_delta` (vehicleState SoC stashed in
`lastVehStateSoC` before Parallax overwrites the cache), so the ≈1%
Parallax-vs-vehicleState offset can be quantified over real sessions
before deciding whether Parallax SoC should stay the energy-math source
or fall back to vehicleState. 12V health is applied on preview (Phase 4).

### Phase 4 - comfort / body / status
`comfort.cabin.*` (temps, preconditioning, pet mode, hold),
`body.{closures,locks,windows,trailer}.states`, `ota.*`, `security.alarm.state`.
These feed LivePanel and the status UI. Map Parallax enums to the string values
the UI expects (per-topic mapping tables - Parallax enums differ from
vehicleState strings, e.g. gear).

### Phase 5 - the flip (per vehicle, opportunistic)
Once Parallax covers the full surface **and** a vehicle reports
`PX_STATE_ALL=AVAILABLE`: stop the legacy vehicleState WS for that vehicle
(keep the REST seed for cold-start), gated on `PX_STATE_ALL`. vehicleState stays
the feed for vehicles without the flag. **Precondition:** a Parallax-liveness
watchdog analogous to `wsStaleThreshold` - dropping the vehicleState fallback
removes today's safety net, so we must detect a silent Parallax feed and
resubscribe/alert, or we lose all telemetry invisibly.

## Cross-cutting rules

- **Measure-first, per topic.** Prove cadence/fidelity over a real drive before
  making any field authoritative. GPS is the template (we nearly shipped a
  regression on a hunch; the data said otherwise).
- **Additive with fallback.** Legacy stays until Phase 5. Each field flips to
  Parallax only on real frame delivery (`parallaxGPSFor`-style first-frame gate).
- **Watchdog hygiene.** An authoritative Parallax topic must NOT advance
  `m.stamp` (that cursor gates the vehicleState WS watchdog); Phase 5 needs its
  own Parallax watchdog before the fallback is removed.
- **Protobuf RE per topic** is the real cost. Ground truth: APK decompile
  (`reference_rivian_apk_decompile`) + `/api/parallax-raw` live-frame capture.
- **Auth reality.** No gateway session refresh (`reference_rivian_no_gateway_refresh`);
  a dead session kills every topic at once. More topics don't worsen auth, but
  Phase 5 loses the fallback when it dies - another reason the watchdog gates it.

## Out of scope (separate track)

Parallax **commands** (`PVS_*_CMD`, `energy.set_charge_limit`,
`comfort.cabin.set_*`, `body.locks.{lock,unlock}`, `ota.install.install_now`).
This is the write path - remote control - distinct from telemetry recording.
Worth a separate plan; it's where session/reauth limits bite hardest.
