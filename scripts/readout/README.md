# The Monday readout

One command prints the weekly numbers. Read-only (every session is opened with `default_transaction_read_only=on`), counts only: no ids,
emails, names, IPs, places or amounts come out, and nothing is written. The demo account (`rivolt-public-demo`) is left out of every number.

```sh
READOUT_SERVICE=readout_ro scripts/readout/run.sh                                  # the week that just ended
READOUT_SERVICE=readout_ro READOUT_OWNER_SERVICE=readout_owner scripts/readout/run.sh --week-start 2026-10-19
```

**Connection.** Name the connections in `~/.pg_service.conf` (host, port, dbname, user) and put the passwords in `~/.pgpass` (mode 0600). `run.sh`
connects with `PGSERVICE`, so no password and no connection string is ever on a command line, where `ps` shows it to everyone on the machine. A DSN
(`READOUT_DSN` / `READOUT_OWNER_DSN`) is still accepted if it has no password in it (the password then comes from `~/.pgpass` or `PGPASSWORD`);
one with a password is refused.

The week is Monday 00:00 UTC to the next Monday 00:00 UTC. With no `--week-start` it is the week that ended this morning, so run it on Monday.
`psql` must be on the PATH. Reach the database the way the other read-only checks do (a port-forward to the CNPG read service; the service entry then points at localhost).

## Part 1: the read-only role (`monday_ro.sql`)

| Section | What it counts |
|---|---|
| Sign-ups | accounts created this week, last week, and all accounts to date |
| Connected | accounts whose first live vehicle appeared this week / last week; accounts holding a Rivian session now; how many of this week's sign-ups have connected by now |
| First data | accounts whose first drive, charge or recorded state fell this week / last week; how many of this week's sign-ups have data by now |
| Weekly active | accounts with a drive or charge started this week; last week; active in both |

"Connected" does not count imported (`electrafi_`) vehicles. "First data" is the earliest of the first drive, the first charge and the first
`vehicle_state` row (one index probe per vehicle).

## Part 2: needs an owner-side read (`monday_owner.sql`)

These tables are not on the read-only role's allowlist (`internal/db/readonly.go`), so grafana_ro gets *permission denied*; part 2 needs the lead's
owner-side connection (`READOUT_OWNER_DSN`). Without it `run.sh` prints SKIPPED and the one command that runs it.

| Section | Table(s) |
|---|---|
| `landing_view` by referrer host and utm_source (top 25, plus totals and distinct browsers) | `events` |
| `plan_run` and `statement_share` counts (and statement_share by kind) | `events` |
| Accounts with any alert on: any notification switch on, opted into an extra alert (plug-in reminder, milestones, left-open, live charging), on web, on iPhone; weekly recap, trip recap, statement email, R2 arrival reminder | `push_subscriptions`, `push_apns_devices`, `engagement_prefs`, `statement_email_prefs` |

`events` holds a random per-browser id and allowlisted props only (referrer host and utm_source are tokens, never URLs). "Any switch on" includes
switches that are on by default the moment a device subscribes (charging done, anomaly, software update), so read `opted_in_extra` as the
deliberate ones.

## If you want these in part 1

Adding `events` (or a counts-only view over it) to the read-only allowlist would let the whole readout run with grafana_ro. That is a privacy
decision for DA, not part of this change.

## How it is tested

`internal/integration/readout_test.go` (CI integration job) seeds fixtures (a demo account that must count nowhere, sign-ups this week and last
week, an imported vehicle, drives, charges, events, push and engagement switches) and runs the real statements from these files: part 1 as a
read-only role with the production allowlist and its expected counts; part 2 shown to be denied to that role (which is why it is marked
owner-side) and then run as the owner with its expected counts. It also fails if a statement selects an identifying column. The runner's date
logic (default week, explicit week, year rollover, a non-Monday refused) was checked with a stub `psql`; the first run on production is the lead's.
