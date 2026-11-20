# Release QA gate

The process QA follows for every release candidate (rc). The runner (`run.mjs`) does the capturing and the cheap checks; QA reads the sheet and judges.

## Rules

1. **Time budget.** The gate is set, PASS or FAIL, within 10 minutes of preview showing the rc (`/api/health` reports the rc's version). QA sends its lines as each section finishes (runner, diff re-check, sim drive, sim charge), never one report at the end.
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
| 5-10 | Watch the sim drive and charge (start, middle, end on Now, Live, the graphs); speed, distance, time and energy must agree. Send the lines, set the gate. |

Run the runner once more after the sim runs end if a state shown NOT SEEN (a sim run is what puts the truck into it).

## Runner

- Credentials: `RIVOLT_QA_USER` and `RIVOLT_QA_PASS`, else the Keychain item `rivolt-preview-login` (`--keychain` for another). Never print or write them.
- Needs `npm ci` in `web/` once (it uses the repo's Playwright). `--browser webkit|chromium`, default webkit.
- Lines come from `manifest.json`: add a `must` / `mustNot` regex to a screen, or a state with its own lines, to make a release check it. Expected-to-fail requests go in `failedIgnore`.
- Per capture: loaded, no console errors, no failed requests, no horizontal overflow, texts that must or must not show. Sign-out runs last because it ends the session.
- Output: `index.html` (contact sheet, failures first), `results.json` (`PASS` / `FAIL` / `NOT SEEN` per line), one PNG per capture. Exit code 1 on any FAIL.

## Sim runs

(Filled in with the short variants when they land; until then a drive takes about 24 minutes and a charge 22, one at a time: start them on main beforehand, never inside the gate window.)

## Release-tooling finding, Nov 20

`final-gate.sh` (tag-validate) ignores `rc-bypass.log` when the tagged commit is an rc's commit and then requires that rc's `qa/rc-gate` success; the dry run said WOULD TAG. `release-verdict.sh` now repeats that check. If a dry run and a real tag ever disagree, the dry run is the bug.
