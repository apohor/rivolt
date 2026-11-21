# Release QA gate

The process QA follows for every release candidate (rc). The runner (`run.mjs`) does the capturing and the cheap checks; QA reads the sheet and judges.

## Rules

1. **Time budget.** The gate is set, PASS or FAIL, within 12 minutes of preview showing the rc (`/api/health` reports the rc's version). QA sends its lines as each section finishes (runner, diff re-check, sim drive, sim charge), never one report at the end.
2. **Validation on main counts.** QA validates continuously on main while PRs merge and records the validated SHA (a line in the QA notes: `validated <sha7> <date>`). On the rc, QA re-checks only the files and PRs listed by the diff summary (validated SHA to rc SHA), plus one smoke run of the runner. If only release notes and docs differ, the validation carries.
3. **Sim runs belong in the gate, and they are fast.** The gate includes one simulated drive and one simulated charge on the rc, using the short variants (see "Sim runs"). Start them before anything else, then run the runner while they play.
4. **Answer the lead between steps.** No single command runs longer than 2 minutes without a check of the inbox.
5. **NOT SEEN is written down, and does not block by itself.** Whatever the runner or QA did not see goes into the gate text as `NOT SEEN: <line>`. A FAIL blocks; a NOT SEEN is for the lead to weigh.

## Steps and expected minutes (from preview showing the rc)

| Min | Step |
| --- | --- |
| 0 | Confirm the version: the runner's `--expect-version vX.Y.Z-rc.N --wait-version 10`. Start the sim runs (below). |
| 0-1 | `node ops/release/qa-gate/run.mjs --base https://preview.rivolt.dev --out <dir> --expect-version <rc> --validated <sha> --rc <rc sha>`. About 15 s for 200+ lines, plus the diff summary. |
| 1-3 | Read `<dir>/index.html`: failures and NOT SEEN first, then the contact sheet. Send the runner's lines. |
| 3-5 | Re-check what the diff lists (screens the changed files touch), at 393 and 1440, light and dark. Send the lines. |
| 5-12 | Watch the sim drive and charge (start, middle, end on Now, Live, the graphs); speed, distance, time and energy must agree. Send the lines, set the gate. |

Run the runner once more after the sim runs end if a state shown NOT SEEN (a sim run is what puts the truck into it).

## Runner

- Credentials: `RIVOLT_QA_USER` and `RIVOLT_QA_PASS`, else the Keychain item `rivolt-preview-login` (`--keychain` for another). Never print or write them.
- Needs `npm ci` in `web/` once (it uses the repo's Playwright). `--browser webkit|chromium`, default webkit.
- Lines come from `manifest.json`: add a `must` / `mustNot` regex to a screen, or a state with its own lines, to make a release check it. Expected-to-fail requests go in `failedIgnore`.
- Per capture: loaded, no console errors, no failed requests, no horizontal overflow, texts that must or must not show. Sign-out runs last because it ends the session.
- Output: `index.html` (contact sheet, failures first), `results.json` (`PASS` / `FAIL` / `NOT SEEN` per line), one PNG per capture. Exit code 1 on any FAIL.
- **Start only when preview is stable on the rc.** The runner reads `/api/health` `--reads` times (default 8, at least twice the replicas) before and after the screens. A version change or a mix of versions in those reads (a roll under way, or a read with no answer) makes the run INVALID, not FAIL: the sheet says so, exit code 3, and the run is repeated once preview is stable. A page that loaded one build's index and another build's chunks otherwise shows up as a false lazy-chunk error.

## Sim runs

The sim exists on the preview admin site only (`RIVOLT_SIM_CONTROLS` with `RIVOLT_RIVIAN_LIVE_FORBIDDEN`); prod answers 404 on `/api/admin/sim`, and a router test holds that. A run is a stored document read by every pod, so any replica shows the same frames. One run at a time on the one sample truck.

| Call (admin session, POST) | Plays | Length from the request |
| --- | --- | --- |
| `/api/admin/sim/drive?short=1` | Pasadena City College and back, about 3.6 mi at the full drive's cruise speed (4.5 min of driving), 30 s parked before and after | about 6 min |
| `/api/admin/sim/charge?short=1&queue=1` | a DC fast slice: 160 kW peak, about 99 kW average, SoC +5.1 points in about 4 min, pack temperature up as at a fast stop, 30 s tails | starts when the drive ends, about 5 min more |
| `/api/admin/sim/drive`, `/charge` (no `short`) | the full runs: Santa Anita Park drive about 28 min, 7.6 kW home charge about 28 min | long: start them on main beforehand, never inside the gate window |

`queue=1` books one run behind the one under way (a second queued run is refused), so the gate books drive then charge in two calls and the pair ends about 11 minutes after the first request. Speed, distance, time and energy are consistent in every variant: the short drive is a nearer errand at the same speed, the short charge a DC fast slice whose power integrates to its SoC rise. The clock is never sped up.

Cards each run shows: the short drive shows the drive card (speed, distance, drive graph, navigation to the errand) then Activity as one drive of about 3.6 mi; the short charge shows the **fast** charging card (kW curve falling with the taper, pack temperature line moving). The long 7.6 kW run shows the **home** AC card (flat kW, plugged, "charging complete" tail), so the home card is only seen on the long run on main.

**Where the short pair books.** The short runs start wherever the routine has the sample truck: at home, away, asleep, or in the middle of its own scheduled charge. The run takes the truck's place for its window (the drive is an out and back of about 3.6 mi from where it stands; the charge happens where it stands) and the routine continues from there with no jump in place or odometer; the battery eases back to the schedule over two hours. Booking is refused only when another run is active (409 busy), when the truck is on a scheduled drive (409 with the time the drive ends: book after it), or when a scheduled drive would start within 10 minutes of the run's end (409 with that time). Both time refusals carry `free_at` (UTC, RFC 3339), the moment to book again. A scheduled charge inside the window does not refuse: the run pre-empts it. The full runs (no `short`) still need the truck at home.

**What the fast card says.** The card shows the car's charge limit as its target (80%), and the slice adds only about 5 points, so the run ends short of 80% (76.4% in a gate run) with the target still shown. That is the fixture, not a bug: check that the SoC, energy and kW agree with each other, not that the card reaches its target. Live's "waiting for first frame" clears once the first fast-charge frame carries a session (a few seconds into the charge).

**The short charge is a test fixture.** At a high starting SoC its power is more than a real truck would take there (the sample truck usually sits above 70%, so there is no cap). The gate checks that power, energy, rate, range and cost agree with each other and that the card and graph move, not that the numbers are what a real truck would show.

**The runner books the pair.** `--sim` signs in to the admin site (Cloudflare Access service token from the Keychain item `rivolt.qa-access-preview`, the same preview login), books `drive?short=1` then `charge?short=1&queue=1`, and adds PASS/FAIL lines: both kinds end in `_short`, the charge starts when the drive ends, the pair fits 11 minutes. It books only when `/api/health` shows one version on every read before the screens; otherwise a FAIL line says why. `STAGE` lines mark the bookings, and the sim start and end times are in the lines' details.

**Rollout trap.** The short variants are separate run kinds (`drive_short`, `charge_short`). A preview pod without that code does not know the kind, logs "run cannot be rebuilt" and shows the schedule instead of the run, so a half-rolled preview shows two different trucks. Before the gate books a short run, check that every preview pod reports the rc (`/api/health` three times in a row, as the runner's version gate does).

**Later: a second sample truck** would let a drive and a charge play at the same time, so the pair would take about 6 minutes instead of 11. It needs a second sample vehicle in the replay feed with its own place and schedule, its own sim document key, and the admin routes taking a truck id.

Stage timestamps: the runner prints `STAGE <time> <name>` per stage and keeps them in `results.json` (`meta.stages`). The sim stages (drive start/end, charge start/end) are recorded by QA in the same format.

## Release-tooling finding, Nov 20

`final-gate.sh` (tag-validate) ignores `rc-bypass.log` when the tagged commit is an rc's commit and then requires that rc's `qa/rc-gate` success; the dry run said WOULD TAG. `release-verdict.sh` now repeats that check. If a dry run and a real tag ever disagree, the dry run is the bug.
