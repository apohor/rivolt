#!/usr/bin/env bash
# migration-immutable.sh BASE HEAD: a migration file that is on the base branch is never modified, renamed or deleted (migrations v2, step 1).
#
# Why: a file is applied the moment its commit reaches main (preview deploys main), so an edit afterwards is a schema that CI and fresh installs
# build but that preview, prod and demo never ran. 0108 was edited two hours after it merged, 0001 after release v0.38.18: both harmless, and
# nothing would have stopped a harmful one. The rule is "on the base branch", not "in the newest tag" (DA ruling M1).
#
# Adding a file is always fine (contract-check.sh judges the contract ones). Break-glass, for a file that failed on prod and has to be amended:
# the PR label `migration-amend` (env LABELS, comma separated) AND the devils-advocate's ack in the ledger; CI only sees the label.
#
# Exit 0 when nothing existing changed (or the label is present), 1 otherwise.
set -euo pipefail

base="${1:?base rev}"
head="${2:?head rev}"
dir="internal/db/migrations"

# --no-renames: a rename shows as a delete plus an add, so it is caught as the delete.
changed=$(git diff --name-only --no-renames --diff-filter=MD "$base" "$head" -- "$dir" || true)
if [ -z "$changed" ]; then
  echo "migration-immutable: no existing migration modified, renamed or deleted"
  exit 0
fi

echo "migration-immutable: these migrations are already on the base branch and were changed:" >&2
printf '  %s\n' $changed >&2
case ",${LABELS:-}," in
  *,migration-amend,*)
    echo "migration-immutable: label migration-amend present: allowed here, and the devils-advocate's ack must be in the ledger" >&2
    exit 0
    ;;
esac
echo "migration-immutable: an applied file must not change. Fix forward with a new migration. If a file failed on prod and must be amended, add the PR label migration-amend and get the devils-advocate's ack." >&2
exit 1
