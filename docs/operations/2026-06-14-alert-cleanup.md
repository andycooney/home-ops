# 2026-06-14 Alert cleanup and monitoring rule changes

## Summary

After the Rook/Ceph CSI recovery and VolSync repair, Alertmanager was reduced to only the expected `Watchdog` alert.

Cleared or addressed alerts:

- `VolSyncVolumeOutOfSync`
- `KubePodCrashLooping`
- `KubeDeploymentReplicasMismatch`
- `KubeHpaMaxedOut`
- `NodeSystemdServiceFailed`
- `KubeCPUOvercommit`
- `KubeMemoryOvercommit`

`Watchdog` remains active by design.

## VolSync / storage recovery notes

VolSync source snapshots were stuck after the Rook/Ceph CSI issue. The recovery path was:

1. Delete stale not-ready `volsync-*-src` `VolumeSnapshot` objects.
2. Repair RBD CSI controller access needed for snapshot and temporary PVC provisioning.
3. Restart the RBD CSI controller.
4. Delete stuck pending `volsync-*-src` temporary PVCs.
5. Let VolSync recreate temp PVCs and mover jobs.
6. Confirm `ReplicationSource` `lastSyncTime` updated and mover pods/jobs cleaned up.

Expected post-recovery state:

- No stale or errored `ReplicationSource` objects.
- No leftover `volsync-*-src` temporary PVCs.
- No leftover VolSync mover pods/jobs.

## Zigbee alert

`KubePodCrashLooping` for Zigbee was caused by the Zigbee coordinator being unplugged.

The deployment was scaled down while the coordinator is offline, and the Zigbee Flux Kustomization was suspended.

When the coordinator is back online:

1. Re-enable the Zigbee Kustomization.
2. Scale/reconcile Zigbee back to normal.
3. Confirm the pod starts cleanly.

## HPA alert rule override

The built-in `KubeHpaMaxedOut` rule was noisy for singleton media apps using HPAs with `minReplicas: 0` and `maxReplicas: 1`.

Those apps intentionally cannot scale above one replica because they are PVC-backed or otherwise single-writer workloads.

Change made:

- Disabled the built-in `KubeHpaMaxedOut` rule via kube-prometheus-stack `defaultRules.disabled`.
- Added a custom replacement rule under `additionalPrometheusRulesMap`.
- Replacement rule only fires when an HPA is at max replicas and `maxReplicas > 1`.

This prevents singleton HPAs from alerting while preserving the alert for real multi-replica autoscaling ceilings.

## Overcommit alert rule override

The `KubeCPUOvercommit` and `KubeMemoryOvercommit` alerts were firing because requested resources were high relative to allocatable capacity, not because runtime usage was high.

Observed runtime usage was healthy:

- CPU: approximately 13-17%
- Memory: approximately 47-55%

Requested resources were close to capacity:

- CPU requests: approximately 86-95%
- Memory requests: approximately 92-94%

The default kube-prometheus-stack overcommit rules also check whether the cluster can tolerate losing a node. On the current 3-node cluster, the requests are high enough that the default HA overcommit rules stayed noisy.

Temporary change made:

- Disabled built-in `KubeCPUOvercommit`.
- Disabled built-in `KubeMemoryOvercommit`.
- Added custom replacement rules that fire only when requests exceed 99% of allocatable cluster capacity.

Follow-up issue:

- https://github.com/andycooney/home-ops/issues/142

After new hardware is deployed, revisit these custom thresholds and either restore upstream behavior or choose a better home-cluster policy.

## Node systemd alert

`NodeSystemdServiceFailed` was for `openipmi.service` on `homebase`.

On the host, `systemctl --failed` showed no failed units, and `openipmi.service` was inactive/dead rather than failed. The alert cleared after the next monitoring evaluation.

## Alertmanager final state

The expected steady-state active alert list is only:

```text
Watchdog
```

## Flux overview command

The top-level `just flux-overview` shortcut now runs the Kubernetes Flux overview.

The overview includes Ceph status from `rook-ceph-tools`, so it gives a quick post-change sanity view of:

- Flux Kustomizations
- Flux HelmReleases
- Not-ready pods
- Ceph health/status
