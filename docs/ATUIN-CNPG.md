# Atuin and CloudNativePG

This runbook documents the Atuin deployment and the shared CloudNativePG PostgreSQL platform introduced for it.

## Current state

Atuin is deployed as an internal-only app:

```text
https://atuin.cooney.site
```

Current confirmed state:

```text
Atuin version: 18.16.1
Atuin route: internal only, envoy-internal, atuin.cooney.site
Atuin user: andycooney
Registration: disabled
Durable state: PostgreSQL database `atuin` on `postgres-cnpg`
Runtime config: Git-managed server.toml rendered through a ConfigMap
Atuin app PVC: none
```

Health check:

```sh
curl -Ik https://atuin.cooney.site/healthz
```

Expected:

```text
HTTP/2 200
atuin-version: 18.16.1
```

## Repository layout

CloudNativePG is organized under one platform folder, with the shared PostgreSQL cluster named explicitly so additional CNPG clusters can be added later.

```text
kubernetes/apps/database/cloudnative-pg/
  ks.yaml                         # CloudNativePG operator Flux Kustomization
  app/
    helmrelease.yaml              # CloudNativePG operator HelmRelease
    ocirepository.yaml            # CloudNativePG chart source
    kustomization.yaml
  postgres-cnpg/
    ks.yaml                       # shared Postgres cluster Flux Kustomization
    app/
      cluster.yaml                # CNPG Cluster: postgres-cnpg
      externalsecret-atuin-db.yaml
      job-atuin-db-bootstrap.yaml
      kustomization.yaml
```

Atuin lives under:

```text
kubernetes/apps/default/atuin/
  ks.yaml
  app/
    externalsecret.yaml
    helmrelease.yaml
    ocirepository.yaml
    kustomization.yaml
    resources/server.toml
```

## Shared PostgreSQL cluster

The shared CNPG cluster is:

```text
Cluster: postgres-cnpg
Namespace: database
Primary service: postgres-cnpg-rw.database.svc.cluster.local
Storage: ceph-block, managed by CNPG from spec.storage
```

Validate:

```sh
kubectl -n database get cluster postgres-cnpg
kubectl -n database get pods -l cnpg.io/cluster=postgres-cnpg
kubectl -n database get pvc -l cnpg.io/cluster=postgres-cnpg
```

Expected:

```text
postgres-cnpg   Cluster in healthy state
postgres-cnpg-1 1/1 Running
```

CNPG creates its own PVCs from the `Cluster` spec. There is intentionally no standalone `persistentvolumeclaim.yaml` for `postgres-cnpg`.

## Atuin database

Atuin uses a database and user created by an idempotent bootstrap Job:

```text
Database: atuin
User: atuin
Secret source: 1Password item `atuin` in the `kubernetes` vault
Kubernetes DB secret: database/atuin-db-secret
Runtime app secret: default/atuin-secret
```

The bootstrap Job is:

```text
database/atuin-db-bootstrap
```

Validate:

```sh
kubectl -n database get externalsecret atuin-db
kubectl -n database get secret atuin-db-secret
kubectl -n database get job atuin-db-bootstrap
kubectl -n default get externalsecret atuin
kubectl -n default get secret atuin-secret
```

## 1Password fields

The `atuin` item in the `kubernetes` vault contains both DB fields and the user sync key.

Required database fields:

```text
ATUIN_DB_NAME
ATUIN_DB_USER
ATUIN_DB_PASSWORD
ATUIN_DB_HOST
ATUIN_DB_PORT
ATUIN_DB_URI
```

User/client fields currently stored there as well:

```text
ATUIN_KEY
username / ATUIN_USERNAME
password / ATUIN_PASSWORD
ATUIN_EMAIL
```

The Atuin sync encryption key is required to restore/sync history on another workstation. Atuin cannot recover this key.

Show non-secret fields:

```sh
op item get atuin --vault kubernetes \
  --fields label=ATUIN_DB_HOST,label=ATUIN_DB_NAME,label=ATUIN_DB_USER,label=ATUIN_DB_PORT
```

Show the local client key:

```sh
atuin key
```

## Atuin configuration file

Atuin needs `/config/server.toml`, but durable state is in Postgres. The repo uses the same ConfigMap-file pattern used by Recyclarr.

Source file:

```text
kubernetes/apps/default/atuin/app/resources/server.toml
```

Current contents:

```toml
host = "0.0.0.0"
port = 80
open_registration = false
```

Kustomize renders it as:

```yaml
configMapGenerator:
  - name: atuin-configmap
    files:
      - server.toml=./resources/server.toml
generatorOptions:
  disableNameSuffixHash: true
```

The HelmRelease mounts it as:

```yaml
persistence:
  config-file:
    type: configMap
    name: "{{ .Release.Name }}-configmap"
    globalMounts:
      - path: /config/server.toml
        subPath: server.toml
  tmp:
    type: emptyDir
    globalMounts:
      - path: /tmp
```

Do not reintroduce an Atuin PVC unless Atuin gains durable local state that is not in Postgres.

## Client setup

Install locally on macOS:

```sh
brew install atuin
atuin init zsh >> ~/.zshrc
exec zsh
```

Client config should include:

```toml
sync_address = "https://atuin.cooney.site"
```

Config path:

```text
~/.config/atuin/config.toml
```

Register was completed for:

```text
username: andycooney
```

After registration, open registration was disabled in `server.toml`.

Validate sync:

```sh
atuin sync
atuin status
```

Expected remote:

```text
Address: https://atuin.cooney.site
Username: andycooney
```

## Database inspection

Open a temporary psql shell using the app DB secret:

```sh
kubectl -n database run psql-atuin \
  --rm -it \
  --restart=Never \
  --image=ghcr.io/cloudnative-pg/postgresql:17 \
  --env="PGHOST=postgres-cnpg-rw.database.svc.cluster.local" \
  --env="PGDATABASE=atuin" \
  --env="PGUSER=atuin" \
  --env="PGPASSWORD=$(kubectl -n database get secret atuin-db-secret -o jsonpath='{.data.ATUIN_DB_PASSWORD}' | base64 -d)" \
  -- psql
```

Useful psql commands:

```sql
\dt
SELECT current_database(), current_user;
SELECT COUNT(*) FROM store;
SELECT COUNT(*) FROM users;
SELECT username FROM users;
```

For Atuin v18, synced encrypted records are expected in `store`. The older/plain `history` table may be `0` even when sync is working.

## Reconciliation order

After changing CloudNativePG or Atuin manifests:

```sh
flux reconcile source git flux-system -n flux-system
flux reconcile ks cloudnative-pg -n database --with-source
flux reconcile ks postgres-cnpg -n database --with-source
flux reconcile hr atuin -n default --force
```

Validate:

```sh
kubectl -n database get cluster postgres-cnpg
kubectl -n database get job atuin-db-bootstrap
kubectl -n default rollout status deploy/atuin --timeout=5m
kubectl -n default logs deploy/atuin -c app --tail=120
curl -Ik https://atuin.cooney.site/healthz
```

## Helm stuck-state recovery

During initial rollout, Atuin hit the same class of Helm stuck-state seen previously with qBittorrent: Helm release history was stuck in a pending/failed state while Flux continued to wait.

Safe recovery pattern for a newly onboarded app with no useful live state yet:

```sh
flux suspend hr atuin -n default

kubectl -n default get secret -l owner=helm,name=atuin \
  -o custom-columns=NAME:.metadata.name,STATUS:.metadata.labels.status,VERSION:.metadata.labels.version

kubectl -n default delete secret -l owner=helm,name=atuin,status=pending-install --ignore-not-found
kubectl -n default delete secret -l owner=helm,name=atuin,status=pending-upgrade --ignore-not-found

flux resume hr atuin -n default
flux reconcile hr atuin -n default --force --timeout=10m
```

If a Helm release exists but is not useful yet, a full uninstall can also be acceptable for brand-new apps:

```sh
flux suspend hr atuin -n default
helm -n default uninstall atuin --wait --timeout 2m
flux resume hr atuin -n default
flux reconcile hr atuin -n default --force --timeout=10m
```

Do not use this casually for stateful apps with existing production data.

## Backup note

`postgres-cnpg` does not currently use VolSync. CNPG-generated PVCs are not automatically covered by the repo's per-app VolSync component.

Preferred next step is to add CNPG-native database-aware backups for `postgres-cnpg` rather than raw PVC VolSync backups.
