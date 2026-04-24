#!/usr/bin/env bash
#
# cleanup-phantom-charges.sh — identify and (optionally) delete phantom
# charge sessions from the Rivolt SQLite database.
#
# Context: before v0.3.54 the live recorder could open a new charge
# session whenever charger_state briefly flickered into "charging_*"
# while the car was unplugged, inheriting the cached Parallax frame
# (25.7 kWh, etc). Those phantom rows show StartSoC == EndSoC (or a
# <1 pp delta) and clutter the charging analytics.
#
# v0.3.54 gates the recorder on isPluggedCS(charger_status) AND
# isChargingCS(charger_state) so no new phantoms get written. This
# script cleans up the existing ones.
#
# Usage (from the repo root on the Synology host or wherever the
# compose stack lives — the script shells into the stack's data volume,
# not the container, so it works even when the container is stopped):
#
#   ./scripts/cleanup-phantom-charges.sh preview   # show what would go
#   ./scripts/cleanup-phantom-charges.sh delete    # actually delete
#
# Set VOLUME=<docker-volume-name> if yours differs from the compose
# default ("rivolt_rivolt-data"). Set THRESHOLD_PP=<float> to override
# the 1-percentage-point SoC-delta cutoff.

set -euo pipefail

MODE="${1:-preview}"
VOLUME="${VOLUME:-rivolt_rivolt-data}"
THRESHOLD_PP="${THRESHOLD_PP:-1.0}"
DB_PATH="/data/rivolt.db"

if ! docker volume inspect "$VOLUME" >/dev/null 2>&1; then
  echo "error: docker volume '$VOLUME' not found" >&2
  echo "       set VOLUME=<name> or run 'docker volume ls | grep rivolt'" >&2
  exit 1
fi

# Use a transient sqlite container mounting the Rivolt volume. keinos/sqlite3
# is a small, well-maintained image that ships the sqlite3 CLI. We pipe SQL
# on stdin so the script is self-contained and survives variable expansion.
run_sqlite() {
  docker run --rm -i \
    -v "${VOLUME}:/data" \
    keinos/sqlite3 \
    sqlite3 "$DB_PATH"
}

case "$MODE" in
  preview)
    echo "== Phantom charges (EndSoC - StartSoC < ${THRESHOLD_PP} pp) =="
    run_sqlite <<SQL
.headers on
.mode column
SELECT
  substr(id, 1, 8)                    AS id,
  datetime(started_at, 'unixepoch')   AS started,
  round(start_soc_pct, 1)             AS start_pct,
  round(end_soc_pct, 1)               AS end_pct,
  round(end_soc_pct - start_soc_pct, 2) AS delta_pct,
  round(energy_added_kwh, 2)          AS kwh,
  final_state,
  source
FROM charges
WHERE (end_soc_pct - start_soc_pct) < ${THRESHOLD_PP}
ORDER BY started_at DESC;

SELECT
  count(*)                AS phantom_count,
  round(sum(energy_added_kwh), 1) AS phantom_kwh
FROM charges
WHERE (end_soc_pct - start_soc_pct) < ${THRESHOLD_PP};
SQL
    echo
    echo "re-run with 'delete' to remove them (a timestamped backup is made first)."
    ;;

  delete)
    stamp="$(date -u +%Y%m%dT%H%M%SZ)"
    backup="rivolt.db.${stamp}.bak"
    echo "== Backing up DB to /data/${backup} (inside the volume) =="
    docker run --rm \
      -v "${VOLUME}:/data" \
      keinos/sqlite3 \
      sh -c "cp ${DB_PATH} /data/${backup} && ls -la /data/${backup}"

    echo
    echo "== Deleting phantom charges =="
    run_sqlite <<SQL
BEGIN;
CREATE TEMP TABLE _doomed AS
  SELECT id FROM charges
  WHERE (end_soc_pct - start_soc_pct) < ${THRESHOLD_PP};

SELECT 'to delete: ' || count(*) FROM _doomed;

DELETE FROM charges WHERE id IN (SELECT id FROM _doomed);

SELECT 'deleted:   ' || changes();
COMMIT;
VACUUM;
SQL
    echo
    echo "done. backup kept at /data/${backup} inside the '${VOLUME}' volume."
    echo "to restore: docker run --rm -v ${VOLUME}:/data keinos/sqlite3 \\"
    echo "            sh -c 'cp /data/${backup} ${DB_PATH}'"
    ;;

  *)
    echo "usage: $0 {preview|delete}" >&2
    exit 2
    ;;
esac
