# Running a repair CLI as the operator (single-owner M4-4)

The repair CLIs (`repair-charge-stretched`, `repair-charge-close-stale`, `repair-charge-blips`, `repair-charge-energy`, `repair-charge-fragments`,
`repair-phantom-charges`, `repair-r2-history`, `repair-route-gaps`, `repair-route-points`, `reprice`) write rows the owner also writes, from outside
any lease. Fencing cannot cover them, so the database keeps a record of every mutating run and refuses the repair role's writes without one.

## What exists

- Database role `rivolt_repair` (created by the infra repo, LOGIN and BYPASSRLS; a repair reaches every user's rows). The server grants it, when it
  runs with `RIVOLT_REPAIR_ROLE=1`: SELECT on every table except the secrets and system tables (`user_secrets`, `sessions`, `webauthn_credentials`,
  `archive_keys`, `invite_codes`, the app-denied tables, `operator_actions`); INSERT, UPDATE and DELETE on `charges`, `drives`, `charges_capped_backup`,
  `charges_deleted_backup` and `vehicles` only; EXECUTE on `rivolt_operator_begin` and `rivolt_operator_end`.
- Table `operator_actions` (migration 0108): one row per mutating run: tool, role label (`lead` or `owner`, never a name), git sha, reason, start and
  end, outcome (`ok` or `failed`) and `rows_written`, counted by the guard itself for every row the run inserts, updates or deletes in a guarded table.
- A trigger on those five tables, for the repair role only: every write needs the session setting `rivolt.operator_action` to name an open action less
  than 12 hours old. Other roles are untouched.

## Running a repair

Dry runs read only and need nothing new. A mutating run (`--apply`, and an undo with `--apply`) needs, in the environment:

```
RIVOLT_REPAIR_DATABASE_URL=postgres://rivolt_repair:...@host/db   # the role's DSN (Vault; the operator only)
RIVOLT_OPERATOR_ROLE=lead                                          # or owner
RIVOLT_REPAIR_REASON="cap the 12 stretched rows approved in the dry run (sha ...)"
```

The CLI opens the action, works on connections that carry it, and closes it with the outcome from its exit code. The action id and role are printed
on stderr. To see what a run did: `SELECT * FROM operator_actions ORDER BY started_at DESC` (as the owner role; the repair role cannot read it).

## Switch-over (the existing path keeps working)

1. Land the migration and the code (no behaviour change: without `RIVOLT_REPAIR_DATABASE_URL` every CLI works exactly as today, with a one-line
   warning on a mutating run: "legacy path: no repair role, no audit row").
2. Infra creates the role (`rivolt_repair`, password in Vault at `kv/rivolt/pg-repair` and `kv/rivolt-preview/pg-repair`, property `password`; read by
   the operator only, never by agents) and sets `RIVOLT_REPAIR_ROLE=1` on the server so the grants are applied.
3. Try it on preview: a dry run, then a harmless apply, and read the `operator_actions` row.
4. Prod: use the new path for the next real run. Keep the legacy path until one run has gone through cleanly.
5. Then set `RIVOLT_REPAIR_REQUIRE_ROLE=1` in the operator's environment: a mutating run without the role's DSN is refused. (This is a guard against a
   slip, not a database rule: anyone holding the owner DSN can still write. The database rule applies to the repair role, which is the one that is
   audited.)

## Not covered

Writes by the app's owner role are not audited by this (full database-enforced fencing of owner tables is a later step, once the per-row cost of a
trigger on `vehicle_state` is measured).
