#!/usr/bin/env bash
# Tests for release-verdict.sh's final-gate mirror: a bypass final at a release candidate's commit is judged the way tag-validate (final-gate.sh)
# judges it, never WOULD TAG for a tag that gate refuses. git, gh and releasectl are stubbed in a temp dir.
set -uo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
t=$(mktemp -d)   # left for the runner to discard
fail=0
check() { # name want-substring got
  if grep -qF -- "$2" <<<"$3"; then echo "ok   $1"; else echo "FAIL $1: wanted '$2' in:"; echo "$3"; fail=1; fi
}
mkdir -p "$t/bin" "$t/infra/ops/release" "$t/infra/apps/platform/rivolt-preview" "$t/infra/apps/platform/rivolt-admin-preview" "$t/repo"
cat >"$t/bin/gh" <<'GH'
#!/usr/bin/env bash
case "$1" in run) echo 1;; api) echo "${RC_GATE_STATE:-none}";; esac
GH
cat >"$t/infra/ops/release/releasectl.py" <<'PY'
import sys
if sys.argv[1] == "get-tag":
    print(open(sys.argv[2]).read().strip())
PY
chmod +x "$t/bin/gh"
cd "$t/repo" && git init -q -b main && git config user.email t@t && git config user.name t && git config commit.gpgsign false
mkdir release-notes && echo x >a && git add . && git commit -qm base && git tag v0.9.0
echo v1 >release-notes/v1.0.0.md && git add . && git commit -qm "notes"
rc_sha=$(git rev-parse HEAD)
git tag -a v1.0.0-rc.1 -m rc
echo y >a && git add . && git commit -qm "later"
other_sha=$(git rev-parse HEAD)
run() { # sha rc-gate-state preview-tag
  echo "$3" >"$t/infra/apps/platform/rivolt-preview/values.yaml"; echo "$3" >"$t/infra/apps/platform/rivolt-admin-preview/values.yaml"
  PATH="$t/bin:$PATH" RC_GATE_STATE=$2 VERSION=v1.0.0 SHA=$1 FINAL_BY_WORKFLOW=true NO_RC_REASON="hotfix with a long enough reason" GH_REPO=x/y INFRA_DIR="$t/infra" \
    bash "$here/release-verdict.sh" 2>&1
}
check "bypass at an rc commit, gate not success: refused" "WOULD REFUSE" "$(run "$rc_sha" none v1.0.0-rc.1)"
check "  and says why" "bypass is not consulted" "$(run "$rc_sha" failure v1.0.0-rc.1)"
check "bypass at an rc commit, gate success, preview on the rc: would tag" "WOULD TAG" "$(run "$rc_sha" success v1.0.0-rc.1)"
check "bypass at an rc commit, gate success, preview elsewhere: refused" "not v1.0.0-rc.1" "$(run "$rc_sha" success v0.9.0)"
check "bypass at a commit that is no rc's: would tag, as before" "WOULD TAG" "$(run "$other_sha" none v0.9.0)"
exit $fail
