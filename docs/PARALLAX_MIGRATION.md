# Parallax migration

Plan to move Rivolt's telemetry from Rivian's legacy `vehicleState` JSON
WebSocket to the modern **Parallax** RVM feed, topic by topic, measure-first
and additive - never a big-bang cutover.

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
| gnssLocation/Speed/Bearing/Altitude | `dynamics.vehicle.gnss` | **shipped (preview)** |
| pack temperature (none in legacy) | `energy.high_voltage.battery_state` | **recording** |
| charge live data | `energy_edge_compute.graphs.charge_session_breakdown` | **shipped** |
| gearStatus | `dynamics.vehicle.gear` | **authoritative (open) on preview** — enum 1P/2R/3N/4D; opens drives early via RIVOLT_PARALLAX_GEAR, close stays on vehicleState |
| driveMode | `dynamics.vehicle.drive_mode` | **RE'd** — `{1:varint}` (proto `g70/*`); shadow only — enum→name table (All-Purpose/Sport/…) still needed before authoritative |
| vehicleMileage (odometer) | `dynamics.vehicle.odometer` | **authoritative (stall-bridge) on preview** — `{1:varint}` whole **km**; monotonic, only advances cache when higher, so vehicleState's 0.01-mi wins normally (`RIVOLT_PARALLAX_ODOMETER`) |
| distanceToEmpty | `dynamics.vehicle.range` | todo |
| tirePressure{FL,FR,RL,RR} | `dynamics.tires.state` | todo |
| batteryLevel / batteryCapacity | `energy.high_voltage.battery_state` / `battery_characteristics` | todo |
| twelveVoltBatteryHealth | `energy.low_voltage.battery_state` | todo |
| cabinClimateInteriorTemperature | `comfort.cabin.cabin_temperatures` | todo |
| cabinPreconditioningStatus | `comfort.cabin.cabin_preconditioning_status` | todo |
| cabinHoldStatus | `comfort.cabin.climate_hold_status` | todo |
| petModeStatus | `comfort.cabin.pet_mode_status` | todo |
| doors/closures/locks/windows | `body.{closures,locks,windows}.states` | todo |
| trailer | `body.trailer.state` | todo |
| ota{Current,Available}Version/Status/InstallProgress | `ota.{ota_state,deployment,install}.*` | todo |
| alarmSoundStatus / gearGuardLocked | `security.alarm.state` / `security.access.*` | todo |
| powerState | `vehicle.power.state` | **authoritative on preview** — `{1:varint}`, `3=ready`/`4=go` confirmed; applied to cache (`RIVOLT_PARALLAX_POWER_STATE`); sleep enum TBD |

Coverage is effectively complete - every legacy field has a Parallax home.

## Phases

### Phase 0 - foundation (done)
Env master `RIVOLT_PARALLAX_GPS`, `SupportedFeatures` capability query +
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

`power.state` enum: `4 = go` (powered/driving), `3 = ready` (awake idle) —
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
(`RIVOLT_PARALLAX_DRIVE_DYNAMICS`, `StateMonitor.driveDynamicsShadow`) logs the
raw Parallax enum next to the concurrent vehicleState gear so the mapping is
observable over a real drive. `decodeSingleVarint` + `gearFromParallax` in
`internal/rivian/parallax.go`. Nothing is authoritative yet — measure first.

### Phase 3 - energy / charging
`energy.high_voltage.battery_state` (extend the temp decoder to SoC + capacity),
`energy.high_voltage.battery_characteristics`, `energy.low_voltage.battery_state`
(12V). Cross-check SoC/capacity against the existing charge-breakdown pipeline
and `chargeKwh` (pack-health signal). Reconcile the two energy sources before
either goes authoritative.

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
