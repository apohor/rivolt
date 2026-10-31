#!/usr/bin/env bash
# The Monday readout: one command prints the whole thing. Read-only, counts only.
#
#   READOUT_DSN=postgres://grafana_ro:...@host/db  scripts/readout/run.sh [--week-start YYYY-MM-DD]
#   READOUT_OWNER_DSN=postgres://...               (optional) adds part 2, the numbers that need an owner-side read
#
# The week is Monday 00:00 UTC to the next Monday 00:00 UTC. Default: the week that just ended (run it on Monday morning).
# --week-start takes the Monday to start from (any other day is refused). Without READOUT_OWNER_DSN, part 2 is listed as skipped
# with the one command that runs it. Nothing here writes: every session is opened read-only.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

week_start=""
while [ $# -gt 0 ]; do
  case "$1" in
    --week-start) week_start="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,11p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# Date arithmetic with GNU date or BSD date.
if date -u -d "2026-01-05" +%u >/dev/null 2>&1; then
  dow() { date -u -d "$1" +%u; }
  addd() { date -u -d "$1 $2 days" +%F; }
else
  dow() { date -u -j -f %F "$1" +%u; }
  addd() { local n="$2"; case "$n" in -*|+*) ;; *) n="+$n" ;; esac; date -u -j -v"${n}"d -f %F "$1" +%F; }
fi
if [ -z "$week_start" ]; then
  today="$(date -u +%F)"
  back=$(( $(dow "$today") - 1 ))               # days since this Monday
  this_monday="$(addd "$today" "-$back")"
  week_start="$(addd "$this_monday" -7)"
fi
case "$week_start" in [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]) ;; *) echo "--week-start must be YYYY-MM-DD" >&2; exit 2 ;; esac
[ "$(dow "$week_start")" = 1 ] || { echo "--week-start $week_start is not a Monday" >&2; exit 2; }
week_end="$(addd "$week_start" 7)"

: "${READOUT_DSN:?set READOUT_DSN to the read-only (grafana_ro) connection string}"
psql_ro() { PGOPTIONS='-c default_transaction_read_only=on' psql -X -v ON_ERROR_STOP=1 -v from_ts="${week_start}T00:00:00Z" -v to_ts="${week_end}T00:00:00Z" "$@"; }

echo "Rivolt Monday readout, week ${week_start} to ${week_end} (UTC), counts only"
echo "######## Part 1: read-only role"
psql_ro "$READOUT_DSN" -f "$here/monday_ro.sql"

echo
echo "######## Part 2: needs an owner-side read (events, push, engagement tables)"
if [ -n "${READOUT_OWNER_DSN:-}" ]; then
  psql_ro "$READOUT_OWNER_DSN" -f "$here/monday_owner.sql"
else
  echo "SKIPPED (READOUT_OWNER_DSN not set). With the owner-side connection, either set it and rerun this command, or run just part 2:"
  echo "  psql \"\$OWNER_DSN\" -v from_ts=${week_start}T00:00:00Z -v to_ts=${week_end}T00:00:00Z -f scripts/readout/monday_owner.sql"
fi
