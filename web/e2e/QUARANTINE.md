# e2e quarantine and the tag gate

Owner: QA. Reviewer for changes to this file: backend.

## Quarantine (max 3 entries)

A spec goes here only when it is flaky or blocked on a known bug and the team has agreed to stop it blocking merges. Quarantined specs stay in the repo, marked with `test.fixme(...)` that links back to this file; they are not deleted and not silently skipped.

Rules:
- At most **3** entries. A fourth means something is not being fixed: fix or delete one first.
- Every entry has an owner, a tracking issue or PR, a reason, and the date it was added.
- An entry older than 14 days is a bug to be closed, not a resting place.
- Snapshot or baseline updates (`*-snapshots/`, `__screenshots__` expectations) need a reviewer who is not the author.

| Spec | Owner | Tracking | Why | Added |
|------|-------|----------|-----|-------|
| _none_ | | | | |

## Tag gate (every release tag)

CI runs Chromium shards on web PRs; the WebKit shard (the iPhone Safari engine) is not required per PR. At tag time QA runs the gate below on the **exact preview version** that will be tagged; `ship-release` waits for a PASS naming that version.

1. Read the preview footer or `/api/health`: record the full version string (`vX.Y.Z-dev.N+sha-…`). Hold preview (`preview-hold`) while testing; release it after.
2. iPhone WebKit, signed in as the QA owner, read-only (non-GET blocked), dark and light:
   - `?ui=off` equals today's UI on Now, Activity, Money, Plan, Truck.
   - With the flag on: Now, Activity, Money, Plan, Truck render with no overflow and no console errors.
   - Cold Truck load shows the skeleton, then the data; Money for an empty month; Plan with the sheet at peek and at half.
   - Footer version matches step 1; `/metrics` returns 403; sign-in works and survives a reload.
3. Public demo, signed out: every `/demo` route, the six presets with road-following lines, Plan vs actual, no non-GET, no `/api/maps/valhalla`.
4. Screenshots are opened and read, not just taken. Mechanics (HTTP 200, "element present") are never a PASS on their own.
5. The report ends with a **Not verified** list and one of: `PASS <version>` / `FAIL <version>`.
