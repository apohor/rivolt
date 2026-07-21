# Recorder replay harness — design

> **Status (2026-07-20):** Phases 1-2 landed.
> - `internal/rivian/replay.go` — `Replayer` (persistence-free monitor, feed
>   full-snapshot frames, collect closed drives via the close hook) +
>   `FramesFromJSONL` loader.
> - `internal/rivian/replay_test.go` — hand-authored fixtures (one clean
>   drive; two drives across a park gap; a **missed-park phantom** that's the
>   regression target for the close hardening) + a **real DB-exported drive**
>   fixture (`testdata/clean_drive.jsonl`).
> - `cmd/replay run <fixture.jsonl>` — replay a stream and print the drives,
>   for hand-debugging.
>
> **DB export (until `cmd/replay export` exists), one JSONL line per row:**
> ```sql
> select row_to_json(t) from (
>   select vs.at, vs.shift_state, vs.battery_level_pct, vs.odometer_mi,
>          vs.range_mi, vs.speed_mph, vs.lat, vs.lon, vs.charging_state,
>          vs.charger_power_kw, vs.power_state, vs.location_fix_at
>   from vehicle_state vs join vehicles v on v.id = vs.vehicle_id
>   where v.rivian_vehicle_id = :vin and vs.at between :since and :until
>   order by vs.at
> ) t;
> ```
> The column names match the JSONL frame tags; the loader converts mi→km,
> mph→kph. Next: the fake-clock refactor for deterministic timing, then a
> raw-frame capture flag for Parallax decode fidelity.

## Why

Live-recording logic (drive/charge lifecycle, sleep detection, Parallax
decode, phantom-drive prevention) can only be exercised end-to-end against
a real Rivian feed — and we just learned we **can't** run a second live
feed on the same VIN without corrupting prod (the prod+preview same-vehicle
collision). Unit tests cover individual functions, but the failures that
bite (a 150 mi phantom drive from missed P-frames + sub-threshold gaps) are
**emergent** across a whole frame sequence.

A replay harness closes that gap: take a **real** captured frame stream,
push it through the **same** ingest + recorder path the live WS uses, and
assert on the drives/charges/samples it produces — deterministically, in
CI, with no network and no vehicle. Every real incident becomes a permanent
regression fixture.

## Non-goals

- Not a load test, not a Rivian API mock. It replays *frames*, not the WS
  protocol.
- Not a substitute for the live path in prod; it's a test/debug tool.

## Frame format (`testdata/*.jsonl`)

Newline-delimited JSON, one ingested frame per line, in arrival order:

```json
{"ts_ms":1784312116349,"src":"vehicleState","state":{"Gear":"D","BatteryLevelPct":45.0,"Latitude":37.77,"Longitude":-122.41,"OdometerKm":60355, ...}}
{"ts_ms":1784312116947,"src":"parallax","rvm":"dynamics.vehicle.gnss","payload_b64":"CAE..."}
```

- `vehicleState` frames carry a decoded `State` delta (what the WS callback
  already receives).
- `parallax` frames carry the raw RVM topic + base64 payload (what the
  Parallax subscribers receive), so decode logic is exercised too.
- `ts_ms` is the frame's arrival time; the harness advances a **fake clock**
  to it before ingesting, so debounce/stale-gap/reopen windows fire exactly
  as they would live, with zero real waiting.

## Two capture sources

**(1) DB export — start here.** `vehicle_state` rows *are* recorded State
snapshots. A small exporter (`cmd/replay export --vehicle VIN --since … --until …`)
turns a window of rows into a `vehicleState` frame stream. Zero new capture
infra, and **the data already exists** — including the 2026-07-18 phantom
incident, which becomes fixture #1. Caveat: these are post-merge snapshots,
not raw deltas, and lack exact WS gap timing; good enough for lifecycle
(gear transitions, sleep, odometer) regression.

**(2) Raw capture flag — later, for fidelity.** `RIVOLT_CAPTURE_FRAMES=/path.jsonl`
writes every raw frame (vehicleState delta + Parallax payload) as it's
ingested, preserving true arrival timing and gaps. Needed for
high-fidelity Parallax decode replay and gap-driven bugs.

## Harness shape

```
internal/rivian/replay.go        // Replayer: reads frames, drives a StateMonitor
internal/rivian/replay_test.go   // table tests over testdata/*.jsonl
internal/rivian/testdata/*.jsonl // captured/exported fixtures
cmd/replay/                      // CLI: export + run (manual incident debugging)
```

- **Recording sink:** an in-memory implementation of the samples/drives/
  charges stores (or the existing stores against an ephemeral SQLite/pgtest)
  that captures what the recorder writes, so assertions read the *output*.
- **Fake clock:** the one real refactor. Lifecycle code that calls
  `time.Now()` (debounce, reopen window, stale-session guard, flap window)
  must read from an injected clock instead, so replay is deterministic and
  instant. Most lifecycle math already keys off `curr.At` (frame time); the
  remaining `time.Now()` sites (`recorder.go`, `noteSubEnd`, watchdog) get a
  `clock` field defaulting to a real clock in prod, a manual clock in tests.

## What we assert

Per fixture, the produced output:
- drive count + each drive's distance/duration/start-end (no 150 mi
  phantom; no 0.x mi stub storms),
- sleep/awake/idle-awake minutes,
- charge sessions + energy,
- Parallax fields decoded correctly (gear/soc/pack-temp/etc.).

## Phasing

1. **DB-export replay + in-memory sink + assertions** on drive/sleep output.
   First fixture: the phantom-drive incident → lock in "a gappy feed with
   missed P-frames must not spawn a multi-drive phantom" once the
   no-movement / power_state close hardening lands.
2. **Fake-clock refactor** so timing-dependent lifecycle is deterministic.
3. **Raw capture flag + Parallax replay** for decode-level fidelity.
4. **`cmd/replay` CLI** to run a captured stream and print resulting
   drives/charges — for debugging the next weird incident by hand.

## Payoff

- Deterministic regression tests against **real** data, in CI, no vehicle.
- New incidents get captured once and guarded forever.
- Lifecycle changes (no-movement close, power_state close signal) can be
  validated against the exact stream that broke before shipping — replacing
  the "watch preview and hope" loop we no longer have.
