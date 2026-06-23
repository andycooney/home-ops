# Storage and backup

## Rook/Ceph block storage

Most persistent application PVCs should use:

```text
ceph-block
```

Validate:

```sh
kubectl get pvc -A
kubectl -n rook-ceph get cephcluster
kubectl -n rook-ceph get cephblockpool
kubectl -n rook-ceph get pods
```

If the toolbox exists:

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph health detail
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph df
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd tree
```

Expected:

```text
HEALTH_OK
```

### Ceph Thunderbolt backend

Rook/Ceph runs on host networking and uses the routed Thunderbolt backend for both Ceph public and cluster networks:

```text
public_network:  192.168.16.0/24
cluster_network: 192.168.16.0/24
```

Stable backend node identities:

| Node | Backend identity |
| --- | --- |
| talos01 | 192.168.16.11/32 |
| talos02 | 192.168.16.12/32 |
| talos03 | 192.168.16.13/32 |

Thunderbolt point-to-point links:

```text
talos01 <-> talos02: 192.168.16.0/31
talos01 <-> talos03: 192.168.16.2/31
talos02 <-> talos03: 192.168.16.4/31
```

Confirm Ceph is advertising OSDs on the backend network:

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd dump | grep -E 'osd\.[0-9]+'
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph config dump | grep -Ei 'public_network|cluster_network'
```

Expected OSD addresses:

```text
osd.0 -> 192.168.16.11
osd.1 -> 192.168.16.12
osd.2 -> 192.168.16.13
```

Rook monitor letters are disposable. Do not depend on `mon.a`, `mon.b`, and `mon.c` mapping permanently to specific nodes. Verify quorum and node spread instead.

Full backend runbook:

```text
docs/runbooks/ceph-thunderbolt-backend.md
```

## OpenEBS hostpath storage

OpenEBS hostpath is available for workloads that intentionally need node-local storage, scratch space, or cache-style PVCs.

On this Talos cluster, OpenEBS hostpath must use:

```text
/var/lib/kubelet/openebs/local
```

Do not use:

```text
/var/openebs/local
/var/mnt/local-hostpath
```

Those paths may exist on the Talos host but are not reliably visible from inside Talos' containerized kubelet root filesystem.

Validate:

```sh
kubectl get storageclass
kubectl -n openebs-system get pods
kubectl get pvc -A | grep openebs
```

## CloudNativePG storage

The shared PostgreSQL platform is managed by CloudNativePG:

```text
namespace: database
cluster: postgres-cnpg
service: postgres-cnpg-rw.database.svc.cluster.local
```

CNPG creates PostgreSQL data PVCs from the `Cluster` resource's `spec.storage` field. Do not add a standalone `persistentvolumeclaim.yaml` for CNPG-managed database data.

Validate:

```sh
kubectl -n database get cluster postgres-cnpg
kubectl -n database get pods -l cnpg.io/cluster=postgres-cnpg
kubectl -n database get pvc -l cnpg.io/cluster=postgres-cnpg
```

Current important consumer:

```text
Atuin -> database `atuin` on postgres-cnpg
```

See:

```text
docs/ATUIN-CNPG.md
```

## VolSync / Kopia backups

VolSync backups are written to a Kopia filesystem repository backed by the QNAP NFS export:

```text
storage.cooney.site:/home-ops-backups
```

Each protected app should have its own repository directory:

```text
/home-ops-backups/<app>
```

Inside VolSync mover pods, the export is mounted at:

```text
/mnt/repository
```

Per-app Kopia repository URL:

```text
KOPIA_REPOSITORY = filesystem:///mnt/repository/<app>
```

Validate:

```sh
kubectl get replicationsource -A
kubectl get replicationdestination -A
kubectl get externalsecret -A | grep volsync
```

`postgres-cnpg` does not currently use VolSync. CNPG-generated PVCs are not automatically covered by the repo's per-app VolSync component. Prefer adding CNPG-native database-aware backups for `postgres-cnpg` instead of raw PVC snapshots.

## VolSync-managed standard app PVCs

The normal persistent-app pattern is now managed by the shared component:

```text
kubernetes/components/volsync
```

That component owns the standard app config PVC and the VolSync backup/restore resources:

```text
ExternalSecret/<app>-volsync
PersistentVolumeClaim/<app>
ReplicationDestination/<app>-dst
ReplicationSource/<app>
```

The standard durable PVC uses Ceph by default:

```text
VOLSYNC_STORAGECLASS=ceph-block
VOLSYNC_CAPACITY=5Gi
```

The common names default to the app name:

```text
VOLSYNC_CLAIM=${APP}
VOLSYNC_SECRET_KEY=${APP}
```

Apps only need to override `VOLSYNC_CAPACITY` when the default `5Gi` is not correct.

VolSync/Kopia cache and temporary mover PVCs are disposable scratch space and should use OpenEBS hostpath by default:

```text
VOLSYNC_CACHE_STORAGECLASS=openebs-hostpath
```

Do not move special/additional PVCs into the shared component. Keep them app-local. Example:

```text
home-assistant         -> standard /config PVC managed by components/volsync
home-assistant-cache   -> /config/.venv cache PVC kept app-local
```

For the full pattern and production restore workflow, see:

```text
docs/VOLSYNC-MANAGED-PVCS.md
```

## Kopia UI

Kopia UI:

```text
https://kopia.cooney.site
```

Validate:

```sh
curl -Ik https://kopia.cooney.site
kubectl -n volsync-system get cm kopia -o yaml \
  | grep -n -A8 -B4 'repository.config\|"path"'
```

Expected live ConfigMap repository path:

```text
/repository/kopia
```

## VolSync maintenance repository

Validate the maintenance repository secret:

```sh
kubectl -n volsync-system get secret volsync-maintenance-secret -o json \
  | jq -r '.data.KOPIA_REPOSITORY | @base64d'
```

Expected:

```text
filesystem:///mnt/repository/volsync-maintenance
```

## Restore drill

Use a small test PVC for the first restore drill, not production app data.

See:

```text
docs/RESTORE-DRILL.md
```

Basic approach:

1. Create a small test PVC.
2. Write known test data.
3. Back it up with VolSync.
4. Restore into a new PVC name.
5. Verify the data.
6. Delete the test resources.

Do not use a production PVC for the first drill.
