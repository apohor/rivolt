#!/usr/bin/env bash
# The per-role secret matrix (S19 9f; zero-downtime design §2.6): with roles.enabled, each role's pods mount exactly that role's row of
# roles.secrets, through envFrom, env or volumes, and no other Secret. The expected rows come from the merged values (the chart's
# defaults plus the case), the mounted ones from the render. Then the cases the chart must refuse to render.
# Usage: chart-role-secrets.sh <chart dir>   (needs helm and yq v4)
set -euo pipefail
chart=${1:?chart dir}
case=$chart/ci/roles-values.yaml
out=$(helm template rivolt "$chart" -f "$case")
# The merged matrix: the case's keys over the chart's (yq's *+ merges maps; the case lists replace the default lists).
matrix=$(yq eval-all 'select(fileIndex == 0) * select(fileIndex == 1) | .roles.secrets' "$chart/values.yaml" "$case")
app=$(yq eval-all 'select(fileIndex == 0) * select(fileIndex == 1) | .secrets.existingSecret' "$chart/values.yaml" "$case")
db=$(echo "$out" | yq 'select(.kind == "Deployment" and .metadata.labels["rivolt.dev/role"] == "api") | .spec.template.spec.containers[0].env[] | select(.name == "DB_PASSWORD") | .valueFrom.secretKeyRef.name')
fail=0
for role in ingest api jobs; do
  dep=$(echo "$out" | yq "select(.kind == \"Deployment\" and .metadata.name == \"rivolt-$role\")")
  [ -n "$dep" ] || { echo "::error::no Deployment rivolt-$role"; exit 1; }
  [ "$(echo "$dep" | yq '.spec.template.spec.containers[0].env[] | select(.name == "RIVOLT_ROLE") | .value')" = "$role" ] \
    || { echo "::error::rivolt-$role does not set RIVOLT_ROLE=$role"; fail=1; }
  want=$( {
    echo "$matrix" | yq ".appSecretKeys | to_entries | .[] | select(.value[] == \"$role\") | \"app:\" + .key"
    echo "$matrix" | yq ".secretRefs | to_entries | .[] | select(.value[] == \"$role\") | \"ref:\" + .key"
    echo "$matrix" | yq ".env | to_entries | .[] | select(.value[] == \"$role\") | \"env:\" + .key"
  } | sort -u)
  # Every Secret the pod can read: envFrom secretRefs, env secretKeyRefs (the app Secret's keys as app:, the database credentials left
  # out: every role connects), Secret and projected volumes.
  got=$(echo "$dep" | yq '.spec.template.spec |
      ([.containers[].envFrom[]? | select(.secretRef) | "ref:" + .secretRef.name] +
       [.containers[].env[]? | select(.valueFrom.secretKeyRef) | select(.valueFrom.secretKeyRef.name != "'"$db"'") |
         (if .valueFrom.secretKeyRef.name == "'"$app"'" then "app:" + .valueFrom.secretKeyRef.key else "env:" + .name end)] +
       [.volumes[]? | select(.secret) | "vol:" + .secret.secretName] +
       [.volumes[]? | select(.projected) | .projected.sources[] | select(.secret) | "vol:" + .secret.name]) | .[]' | sort -u)
  if [ "$want" != "$got" ]; then
    echo "::error::rivolt-$role mounts other secrets than its row of roles.secrets"
    diff <(echo "$want") <(echo "$got") || true
    fail=1
  else
    echo "$role: exactly its row ($(echo "$got" | wc -l | tr -d ' ') secrets)"
  fi
done
# The row, spelled out for the cells that matter most, so a matrix edit that empties a row cannot pass by itself.
row() { echo "$out" | yq "select(.kind == \"Deployment\" and .metadata.name == \"rivolt-$1\") | .spec.template.spec.containers[0] | ((.envFrom[]? | select(.secretRef) | .secretRef.name), (.env[]? | .name))"; }
for c in "ingest -RIVOLT_STRIPE_SECRET_KEY" "ingest -archive-credentials" "ingest -RIVOLT_OIDC_EXAMPLE_CLIENT_SECRET" "ingest +RIVOLT_KEK" \
         "ingest +forward-key" "api +RIVOLT_STRIPE_WEBHOOK_SECRET" "api +RIVOLT_KEK" "api +archive-credentials" "jobs +RIVOLT_STRIPE_SECRET_KEY" \
         "jobs -RIVOLT_STRIPE_WEBHOOK_SECRET" "jobs -RIVOLT_OIDC_EXAMPLE_CLIENT_SECRET" "jobs +archive-credentials" "ingest +RIVOLT_EXAMPLE_PLAIN"; do
  role=${c%% *}; cell=${c#* }; name=${cell:1}
  if row "$role" | grep -x "$name" >/dev/null; then has=+; else has=-; fi
  [ "$has" = "${cell:0:1}" ] || { echo "::error::rivolt-$role: want $cell"; fail=1; }
done
# The whole app Secret never reaches a role pod through envFrom, and the default render keeps it (one Deployment, every secret).
if echo "$out" | yq "select(.kind == \"Deployment\") | .spec.template.spec.containers[].envFrom[]?.secretRef.name" | grep -x "$app" >/dev/null; then
  echo "::error::a role pod mounts the whole app Secret"; fail=1
fi
helm template rivolt "$chart" -f "$chart/ci/existing-secret-values.yaml" | yq 'select(.kind == "Deployment") | .spec.template.spec.containers[0].envFrom[].secretRef.name' | grep -x rivolt-app >/dev/null \
  || { echo "::error::the default render lost the app Secret's envFrom"; fail=1; }
# Refused renders: a Secret no row assigns, an unknown role, a chart-held key with no row, and what the cutover brings.
refuse() {
  local want=$1; shift
  if msg=$(helm template rivolt "$chart" -f "$case" "$@" 2>&1); then echo "::error::rendered with $*"; fail=1; return; fi
  echo "$msg" | grep "$want" >/dev/null || { echo "::error::$* failed for another reason:"; echo "$msg"; fail=1; }
}
refuse 'extraEnvFrom secret "unassigned" has no roles.secrets.secretRefs entry' --set 'extraEnvFrom[0].secretRef.name=unassigned'
refuse 'extraEnv RIVOLT_UNASSIGNED comes from a Secret' --set 'extraEnv[0].name=RIVOLT_UNASSIGNED'
refuse 'unknown role "worker"' --set 'roles.secrets.env.RIVOLT_OIDC_EXAMPLE_CLIENT_SECRET[0]=worker'
refuse 'holds VAPID_PRIVATE_KEY and roles.secrets.appSecretKeys does not assign it' --set secrets.existingSecret= --set 'secrets.kek=v1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=' \
  --set secrets.vapidPrivateKey=x --set roles.secrets.appSecretKeys.VAPID_PRIVATE_KEY=null
refuse 'set roles.ingest.dbMaxConns' --set dbConnections.systemMaxConns=
refuse 'set roles.jobs.replicaDivisor' --set config.replicaDivisor= --set roles.ingest.replicaDivisor=3 --set roles.api.replicaDivisor=4
# With rollout.enabled the api role is the only Rollout, and the Rollout mounts the same row as the api Deployment did.
rr=$(helm template rivolt "$chart" -f "$case" --set rollout.enabled=true --set rollout.strategy.canary.steps[0].setWeight=25)
[ "$(echo "$rr" | yq 'select(.kind == "Rollout") | .metadata.name' | grep '^rivolt')" = rivolt-api ] || { echo "::error::rollout.enabled with roles: want the api role as the only Rollout"; fail=1; }
[ "$(echo "$rr" | yq 'select(.kind == "Deployment") | .metadata.name' | grep '^rivolt' | sort | tr '\n' ' ')" = "rivolt-ingest rivolt-jobs " ] || { echo "::error::rollout.enabled with roles: ingest and jobs must stay Deployments"; fail=1; }
[ "$(echo "$out" | yq 'select(.kind == "Deployment" and .metadata.name == "rivolt-api") | .spec.template.spec.containers[0] | [(.envFrom[]? | select(.secretRef) | .secretRef.name), (.env[]? | .name)] | sort | join(",")' | grep -v -e '^---$' -e '^$')" \
  = "$(echo "$rr" | yq 'select(.kind == "Rollout") | .spec.template.spec.containers[0] | [(.envFrom[]? | select(.secretRef) | .secretRef.name), (.env[]? | .name)] | sort | join(",")' | grep -v -e '^---$' -e '^$')" ] \
  || { echo "::error::the api Rollout mounts a different row than the api Deployment"; fail=1; }
exit $fail
