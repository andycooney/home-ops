# VolSync-managed PVCs

The standard persistent-app pattern in this repo is now:

```text
components/volsync = app config PVC + VolSync backup/restore resources
```

For normal protected apps, the shared component owns all of the following resources:

```text
kubernetes/components/volsync/
  externalsecret.yaml
  persistentvolumeclaim.yaml
  replicationdestination.yaml
  replicationsource.yaml
```

This is intentionally close to the onedr0p-style component model: if an app uses the shared VolSync component, it also gets the standard app config PVC from that component.

## Standard component behavior

The shared component creates a restore-aware PVC:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${VOLSYNC_CLAIM:=${APP}}
spec:
  accessModes:
    - ${VOLSYNC_ACCESSMODES:=ReadWriteOnce}
  dataSourceRef:
    kind: ReplicationDestination
    apiGroup: volsync.backube
    name: ${APP}-dst
  resources:
    requests:
      storage: ${VOLSYNC_CAPACITY:=5Gi}
  storageClassName: ${VOLSYNC_STORAGECLASS:=ceph-block}
```

The durable application PVC defaults to `ceph-block`. VolSync/Kopia temporary cache and mover PVCs default to OpenEBS hostpath because they are disposable scratch space:

```text
VOLSYNC_STORAGECLASS default: ceph-block
VOLSYNC_CACHE_STORAGECLASS default: openebs-hostpath
```

The common substitutions default to the app name, so most apps only need:

```yaml
postBuild:
  substitute:
    APP: bazarr
```

Only specify capacity when the standard `5Gi` default is wrong:

```yaml
postBuild:
  substitute:
    APP: home-assistant
    VOLSYNC_CAPACITY: 10Gi
```

Do not add these unless the app really needs nonstandard names:

```yaml
VOLSYNC_CLAIM: <app>
VOLSYNC_SECRET_KEY: <app>
```

They default to `${APP}`.

## App convention

For a normal app with a standard config PVC:

```yaml
spec:
  components:
    - ../../../../components/volsync
  dependsOn:
    - name: rook-ceph-cluster
      namespace: rook-ceph
  postBuild:
    substitute:
      APP: <app>
```

For an app that also depends on the NFS media share, include zeroscaler too:

```yaml
spec:
  components:
    - ../../../../components/volsync
    - ../../../../components/zeroscaler
```

Special or additional PVCs stay app-local. The shared VolSync component owns the standard app config PVC only.

Current example:

```text
home-assistant
  home-assistant       -> standard /config PVC, managed by components/volsync
  home-assistant-cache -> /config/.venv cache PVC, app-local in app/pvc-cache.yaml
```

Do not use the shared component for workloads whose storage is owned by a higher-level data service or operator. For example, CloudNativePG-managed database PVCs should use database-aware backup/restore patterns, not this generic app PVC component.

## Existing PVC migration rule

Kubernetes does not allow adding `dataSourceRef` to an existing bound PVC.

Trying to change an existing PVC from:

```yaml
dataSourceRef: null
```

to:

```yaml
dataSourceRef:
  kind: ReplicationDestination
  apiGroup: volsync.backube
  name: <app>-dst
```

fails with:

```text
PersistentVolumeClaim "<app>" is invalid:
spec is immutable after creation except resources.requests and volumeAttributesClassName for bound claims
```

Therefore, migrating an existing app to the shared restore-aware PVC is an intentional maintenance operation:

1. Stop the app.
2. Confirm the PVC is not in use.
3. Confirm/select a good Kopia snapshot.
4. Delete the old PVC.
5. Reconcile the app so the shared VolSync component recreates the PVC.
6. Restore the selected Kopia snapshot into the new PVC before first app startup.
7. Review the restored file listing.
8. Start the app.
9. Validate the UI/config.
10. Resume the app in Git and let a fresh VolSync backup complete.

## Migration helpers

Use the `just` recipes:

```sh
just volsync-migrate <app>
just volsync-resume <app>
```

They wrap:

```text
scripts/volsync-pvc-migrate-app.sh
scripts/volsync-pvc-resume-app-git.sh
```

`just volsync-migrate <app>` performs the live maintenance flow for one app:

```text
scale deployment to 0
patch HelmRelease suspend=true
show PVC usage
list Kopia snapshots in local time
prompt for snapshot ID
delete and recreate the PVC
restore selected snapshot into /config
print restored file sample and size
prompt before starting the app
patch HelmRelease suspend=false live
reconcile the HelmRelease
```

`just volsync-resume <app>` updates Git after the app validates:

```text
remove spec.suspend from app/helmrelease.yaml
re-add zeroscaler for the known NFS-dependent apps
run scripts/validate-repo.sh
show the diff
```

Commit the resume diff per app, for example:

```sh
just volsync-resume radarr

git add \
  kubernetes/apps/default/radarr/app/helmrelease.yaml \
  kubernetes/apps/default/radarr/ks.yaml

git commit -m "fix(radarr): resume after volsync pvc migration"
git push origin main

flux reconcile source git flux-system -n flux-system
flux reconcile ks radarr -n default --with-source
```

## Snapshot selection

The migration helper prints snapshot rows with local time, snapshot ID, size, and Kopia retention tags.

Prefer the latest `latest-1` snapshot unless the app had already been started against bad/empty config. If a bad config state was started, restore an older known-good snapshot instead.

The Prowlarr canary proved why this matters: the PVC recreation succeeded mechanically, but the initially selected restore state contained a minimal database. We recovered by listing Kopia snapshots directly, restoring the older known-good snapshot into the PVC, verifying the SQLite counts, and only then starting the app.

## Completed migration batch

The following apps were migrated to the managed-PVC pattern and validated:

```text
bazarr
prowlarr
qbittorrent
qui
radarr
recyclarr
sabnzbd
seerr
sonarr
stash
tautulli
whisparr
zigbee
zwave
plex
home-assistant
```

`home-assistant-cache` remains app-local by design.

## Validation commands

Broad health check:

```sh
flux get ks -A | awk 'NR==1 || $4!="True"'
flux get hr -A | awk 'NR==1 || $4!="True"'
kubectl get pods -A | grep -v Running | grep -v Completed
```

Check app backup/restore resources:

```sh
APP=<app>

kubectl -n default get pvc "$APP"
kubectl -n default get externalsecret "${APP}-volsync"
kubectl -n default get secret "${APP}-volsync-secret"
kubectl -n default get replicationsource "$APP"
kubectl -n default get replicationdestination "${APP}-dst"
kubectl -n default describe replicationsource "$APP" | sed -n '/Status:/,$p'
```

For NFS-dependent apps, confirm the HPA returned after resume:

```sh
kubectl -n default get hpa bazarr qbittorrent radarr sabnzbd sonarr qui stash plex whisparr
```

## Known exclusions and special cases

`postgres-cnpg` and CloudNativePG-created data PVCs are not covered by this component. Use database-aware backup/restore for CNPG.

`database/postgres` was explicitly excluded from the shared VolSync-managed PVC component because it has special/local storage behavior.

`plex-tools` should not use zeroscaler. It is a helper Kustomization under `plex/tools`, not the main Plex workload.

`home-assistant-cache` is intentionally app-local. The shared VolSync component manages only `pvc/home-assistant`.
