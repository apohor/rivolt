# Rivolt Helm chart

Single Deployment of [Rivolt](https://github.com/apohor/rivolt) plus an
optional [CloudNativePG](https://cloudnative-pg.io/) `Cluster` for the
database.

## TL;DR — homelab on k3s

```bash
# 1. Install the CNPG operator (once per cluster).
helm repo add cnpg https://cloudnative-pg.github.io/charts
helm install cnpg --namespace cnpg-system --create-namespace \
  cnpg/cloudnative-pg

# 2. Mint a KEK.
KEK="v1:$(openssl rand -base64 32)"

# 3. Install rivolt with the bundled CNPG cluster.
helm install rivolt ./deploy/helm/rivolt \
  --namespace rivolt --create-namespace \
  --set cnpg.enabled=true \
  --set secrets.kek="$KEK" \
  --set secrets.username=anton \
  --set secrets.password=change-me

# 4. Port-forward.
kubectl -n rivolt port-forward svc/rivolt 8080:80
# → http://localhost:8080
```

## Database wiring

Two modes, mutually exclusive:

| Mode                              | When to use                                    | How                                                                                                                                       |
| --------------------------------- | ---------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| `cnpg.enabled=true`               | Homelab quick-start, or full CNPG production   | Chart renders a `Cluster` CR. Operator must be installed separately. App reads `DB_USER` / `DB_PASSWORD` from the auto-generated `<cluster>-app` Secret. |
| `cnpg.enabled=false` (default)    | Existing Postgres (managed cloud, shared CNPG) | Set `externalDatabase.host` + either `password` or `existingSecret`.                                                                       |

### Pointing at an existing CNPG cluster shared with other apps

CNPG generates a `<cluster>-app` Secret per Cluster, with keys
`username` and `password`. Reuse it directly — no copying:

```yaml
cnpg:
  enabled: false
externalDatabase:
  host: my-shared-pg-rw.databases.svc.cluster.local
  database: rivolt
  sslmode: require
  existingSecret: my-shared-pg-app
  existingSecretUserKey: username
  existingSecretPasswordKey: password
```

(You'll need to provision the `rivolt` database + role separately;
CNPG `Database` / `Role` CRs are the clean way.)

### Production CNPG

```yaml
cnpg:
  enabled: true
  instances: 3
  storage:
    size: 50Gi
    storageClass: fast-ssd
  postgresql:
    parameters:
      max_connections: "200"
      shared_buffers: "1GB"
      work_mem: "16MB"
  backup:
    enabled: true
    retentionPolicy: "30d"
    barmanObjectStore:
      destinationPath: s3://my-pg-backups/rivolt
      s3Credentials:
        accessKeyId:
          name: pg-backup-creds
          key: ACCESS_KEY_ID
        secretAccessKey:
          name: pg-backup-creds
          key: SECRET_ACCESS_KEY
      wal:
        compression: gzip
```

## Secrets

Three layers, in priority order:

1. `secrets.existingSecret: <name>` — chart skips its own Secret;
   Deployment `envFrom` references the user-supplied object. Use
   this with ExternalSecrets / Vault / SOPS / sealed-secrets.
2. `secrets.<key>` inline values — fine for a homelab.
3. `extraEnv:` / `extraEnvFrom:` — final escape hatch for env vars
   the chart doesn't model directly (per-provider OIDC client IDs,
   etc.).

`secrets.kek` is **required**. Losing it bricks every encrypted row
(Rivian session, AI keys, VAPID private key). Back it up before
first boot. Rotate by setting `secrets.kek` to a new key (e.g.
`v2:...`) and `secrets.kekRotation` to the previous key so old
ciphertexts decrypt during the rewrap migration.

## OIDC

```yaml
config:
  baseUrl: https://rivolt.example.com
  oidc:
    providers: "google"
  extraEnv:
    RIVOLT_OIDC_GOOGLE_ISSUER: https://accounts.google.com
    RIVOLT_OIDC_GOOGLE_DISPLAY_NAME: Google
extraEnv:
  - name: RIVOLT_OIDC_GOOGLE_CLIENT_ID
    valueFrom:
      secretKeyRef: { name: oidc-google, key: client-id }
  - name: RIVOLT_OIDC_GOOGLE_CLIENT_SECRET
    valueFrom:
      secretKeyRef: { name: oidc-google, key: client-secret }
```

The redirect URL registered with the IdP must be exactly
`{baseUrl}/api/auth/oidc/google/callback`.

## Replica count

`replicaCount` defaults to **1** and `autoscaling.enabled` defaults
to **false**. This is a deliberate guardrail: Phase 2 prerequisites
(subscription lease reconciliation, Redis token bucket, reconnect-
storm controls) aren't built yet. Two replicas means two Rivian
websockets per vehicle, which gets the upstream gateway to ban us.

The HPA template ships pre-wired so flipping it on after Phase 2
is a one-line values change.

## What the chart does NOT do

- **Does not install the CNPG operator.** Cluster-scoped, one per
  cluster — outside this chart's scope.
- **Does not bundle Postgres directly.** No Bitnami subchart, no
  raw StatefulSet. CNPG or external is the choice.
- **Does not configure cert-manager.** Add the annotation +
  `tls:` block to `ingress` and install cert-manager separately.
- **Does not template OIDC client secrets** beyond the obvious
  inline path. Use `extraEnv` with `valueFrom: secretKeyRef` for
  anything you don't want in plaintext values.

## Uninstall

```bash
helm uninstall rivolt -n rivolt
```

The CNPG Cluster, the data PVC, and any user-managed Secret are
**preserved** by `helm.sh/resource-policy: keep`. Delete them by
hand if you actually want them gone:

```bash
kubectl -n rivolt delete cluster <cluster-name>
kubectl -n rivolt delete pvc <release>-rivolt-data
```
