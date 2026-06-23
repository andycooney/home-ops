# Ceph Thunderbolt backend runbook

This runbook documents the current Rook/Ceph backend network design and the operational checks for the routed Thunderbolt storage fabric.

## Current design

The Ceph backend is a routed Layer 3 Thunderbolt network. Do not reintroduce the previous Linux bridge design.

The previous `ceph0` Linux bridge over the Thunderbolt interfaces was tested and rejected:

- direct Thunderbolt links were fast;
- routed Thunderbolt links were fast;
- Linux bridge transit over `thunderbolt-net` collapsed TCP forwarding to tens of Mbps with very high retransmits;
- qdisc/sysctl tuning did not fix bridge transit, and BBR made TCP behavior worse.

Current model:

```text
talos01 management: 192.168.42.11
talos02 management: 192.168.42.12
talos03 management: 192.168.42.13

Ceph backend identities:
talos01: 192.168.16.11/32
talos02: 192.168.16.12/32
talos03: 192.168.16.13/32

Thunderbolt point-to-point links:
talos01 <-> talos02: 192.168.16.0/31
talos01 <-> talos03: 192.168.16.2/31
talos02 <-> talos03: 192.168.16.4/31

Ceph public_network:  192.168.16.0/24
Ceph cluster_network: 192.168.16.0/24
```

Rook/Ceph uses host networking and advertises OSD public and cluster addresses on the `192.168.16.x` backend.

## Interface alias convention

Talos aliases the two Thunderbolt network interfaces so the generated per-node config can be stable across kernel interface names.

Physical convention:

```text
thunderbolt0 = physical left  = busPath 1-1.0
thunderbolt1 = physical right = busPath 0-1.0
```

Ring cabling convention:

```text
talos01 right -> talos02 left
talos02 right -> talos03 left
talos03 right -> talos01 left
```

Expected peer map:

```text
talos01 thunderbolt1 -> talos02
talos01 thunderbolt0 -> talos03

talos02 thunderbolt0 -> talos01
talos02 thunderbolt1 -> talos03

talos03 thunderbolt1 -> talos01
talos03 thunderbolt0 -> talos02
```

## Validate Talos backend addressing

Generate Talos config before applying changes:

```sh
just -f talos/mod.just generate-config
```

Validate live addresses and routes from Talos:

```sh
for node in 192.168.42.11 192.168.42.12 192.168.42.13; do
  echo
  echo "===== $node addresses ====="
  talosctl --endpoints "$node" --nodes "$node" get addresses | grep -E '192\.168\.16|thunderbolt|lo' || true
  echo
  echo "===== $node routes ====="
  talosctl --endpoints "$node" --nodes "$node" get routes | grep -E '192\.168\.16' || true
done
```

Validate direct backend reachability from a node shell or using temporary diagnostics tooling:

```sh
ping -c 3 192.168.16.11
ping -c 3 192.168.16.12
ping -c 3 192.168.16.13
```

## Rook/Ceph network configuration

The CephCluster HelmRelease should include:

```yaml
network:
  provider: host
  addressRanges:
    public:
      - 192.168.16.0/24
    cluster:
      - 192.168.16.0/24
  connections:
    requireMsgr2: true
```

Both `public` and `cluster` networks intentionally use the routed Thunderbolt backend. In this cluster, Kubernetes storage clients and OSD replication traffic are all expected to use the dedicated backend.

Relevant file:

```text
kubernetes/apps/rook-ceph/rook-ceph/cluster/helmrelease.yaml
```

Reconcile after changes:

```sh
flux reconcile source git flux-system -n flux-system
flux reconcile kustomization rook-ceph-cluster -n rook-ceph --with-source --timeout=10m
```

The Rook/Ceph reconcile can take several minutes while monitors, managers, exporters, crash collectors, MDS pods, and OSD pods rotate.

## Validate Ceph after backend changes

```sh
kubectl -n rook-ceph get helmrelease,cephcluster
kubectl -n rook-ceph get pods -o wide | egrep 'NAME|mon-|mgr-|osd-|mds-|rook-ceph-tools'
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph health detail
```

Expected healthy state:

```text
health: HEALTH_OK
mon: quorum present
osd: 3 osds: 3 up, 3 in
pgs: active+clean
```

If `noout` is intentionally set during maintenance, `HEALTH_WARN` for `noout` alone is acceptable until the migration or node work is complete.

## Confirm Ceph is using the backend network

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd dump | grep -E 'osd\.[0-9]+'
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph mon dump
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph config dump | grep -Ei 'public_network|cluster_network|ms_bind|mon_host'
```

Expected OSD addresses:

```text
osd.0 -> 192.168.16.11
osd.1 -> 192.168.16.12
osd.2 -> 192.168.16.13
```

Expected config values:

```text
cluster_network 192.168.16.0/24
public_network  192.168.16.0/24
```

Rook monitor names such as `mon.a`, `mon.b`, and `mon.c` are disposable. Rook may replace monitors and create later names such as `mon.j`, `mon.k`, and `mon.l`. Do not depend on MON letters mapping permanently to nodes. The important checks are quorum, reachability, and placement across all three nodes.

## Ceph maintenance safety

Before planned Talos node maintenance or backend network work:

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd set noout
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status
```

After the node or network work is complete and Ceph is stable except for `noout`:

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd unset noout
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status
```

Do not clear `noout` until:

```text
3 mons are in quorum
3 OSDs are up/in
PGs are active+clean
no OSD or MON pod is CrashLooping
Rook is no longer actively rotating core Ceph daemons
```

## Validate storage clients

```sh
kubectl get storageclass
kubectl get pvc -A | grep -Ei 'rook|ceph|rbd|cephfs|Bound'
kubectl get pods -A | grep -Ei 'ContainerCreating|CrashLoop|MountVolume|rook|ceph'
```

The `ceph-block` StorageClass is the default durable app PVC class.

## Optional RADOS benchmark

Only run this when Ceph is healthy. Confirm the pool name first:

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd pool ls
```

Current block pool:

```text
ceph-blockpool
```

Benchmark and cleanup:

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- rados bench -p ceph-blockpool 30 write --no-cleanup
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- rados bench -p ceph-blockpool 30 seq
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- rados cleanup -p ceph-blockpool
```

Known post-migration sanity result:

```text
write: ~701 MB/s average
seq read: ~4519 MB/s average
```

Treat these as rough smoke-test numbers, not formal performance targets.

## Future work: dynamic routing

The current routed backend uses static routes. Direct pairwise paths are fast and stable, but static routes do not automatically reconverge if one Thunderbolt link fails.

Track FRR/dynamic routing work here:

```text
https://github.com/andycooney/home-ops/issues/191
```

Goal for that future work:

- advertise each node's stable `/32` backend identity;
- peer over the Thunderbolt `/31` links;
- keep direct paths preferred;
- allow traffic to reconverge through the third node if one Thunderbolt link fails;
- verify Ceph remains healthy during a link-failure test.
