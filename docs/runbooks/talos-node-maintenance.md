# Talos Node Maintenance Runbook

This runbook covers planned Talos node maintenance in the home Kubernetes cluster, especially when the node also participates in Rook/Ceph storage. Use it for Talos upgrades, node reboots, node-level troubleshooting, and recovery from a partially completed Tuppr upgrade.

The goal is to keep the cluster schedulable, keep Ceph healthy, and avoid unattended upgrade retries while maintenance is in progress.

## Cluster assumptions

Current Talos node management addresses:

| Node | Talos endpoint |
| --- | --- |
| talos01 | 192.168.42.11 |
| talos02 | 192.168.42.12 |
| talos03 | 192.168.42.13 |

Important namespaces:

| Namespace | Purpose |
| --- | --- |
| rook-ceph | Rook/Ceph storage |
| system-upgrade | Tuppr upgrade operator |
| o11y | Observability, Alertmanager, Prometheus, VictoriaLogs |
| flux-system | Flux controllers |

## Ceph backend assumptions

Ceph uses a routed Thunderbolt backend, not a Linux bridge.

Stable backend identities:

| Node | Ceph backend identity |
| --- | --- |
| talos01 | 192.168.16.11/32 |
| talos02 | 192.168.16.12/32 |
| talos03 | 192.168.16.13/32 |

Point-to-point Thunderbolt links:

```text
talos01 <-> talos02: 192.168.16.0/31
talos01 <-> talos03: 192.168.16.2/31
talos02 <-> talos03: 192.168.16.4/31
```

Ceph should use the backend network for both public and cluster networks:

```text
public_network:  192.168.16.0/24
cluster_network: 192.168.16.0/24
```

Detailed runbook:

```text
docs/runbooks/ceph-thunderbolt-backend.md
```

## Before touching a node

Check cluster health first.

```sh
kubectl get nodes -o wide
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status
kubectl get pods -A --field-selector=status.phase!=Running,status.phase!=Succeeded
```

Proceed only when the cluster is already healthy or when the maintenance is intended to repair the unhealthy node.

Expected healthy Ceph state:

```text
health: HEALTH_OK
mon: quorum present
osd: 3 osds: 3 up, 3 in
pgs: active+clean
```

Also confirm Ceph is still using the routed backend:

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

## Check Talos versions

Use explicit endpoints and nodes so `talosctl` does not wander onto Cilium or pod network addresses that are not in the Talos certificate SANs.

```sh
for ip in 192.168.42.11 192.168.42.12 192.168.42.13; do
  echo
  echo "=== $ip ==="
  talosctl --endpoints "$ip" --nodes "$ip" version
 done
```

For a compact node view:

```sh
kubectl get nodes -o wide
```

## Pause Tuppr during manual maintenance

Tuppr can create Talos or Kubernetes upgrade jobs. If manual node work is being done, pause the operator so it does not retry or start new upgrade work unattended.

```sh
kubectl -n system-upgrade scale deploy/tuppr --replicas=0
kubectl -n system-upgrade get deploy tuppr
kubectl -n system-upgrade get pods
```

While Tuppr is intentionally paused, Alertmanager may fire:

```text
TupprOperatorAbsent
```

That alert is expected while the Tuppr deployment is scaled to zero.

Bring Tuppr back only after the upgrade automation policy is understood and the cluster is healthy:

```sh
kubectl -n system-upgrade scale deploy/tuppr --replicas=1
```

## Inspect Tuppr upgrade status

Talos upgrade status is stored on the Tuppr custom resource. This is often more useful than pod logs after a failed upgrade because upgrade job pods may already be gone.

```sh
kubectl get talosupgrades.tuppr.home-operations.com talos -o json | jq '.spec, .status'
```

Kubernetes upgrade status:

```sh
kubectl get kubernetesupgrades.tuppr.home-operations.com kubernetes -o json | jq '.spec, .status'
```

Useful fields to inspect:

```text
.status.phase
.status.completedNodes
.status.failedNodes
.status.history
.status.message
```

If the CR reports a node failed with `Job failed permanently`, finish diagnosis before letting Tuppr retry unattended.

## Prepare Ceph for one-node maintenance

For a planned Talos reboot or upgrade on a node with a Ceph OSD, set `noout` before taking the node down. This prevents Ceph from treating the OSD outage as a reason to rebalance immediately.

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd set noout
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status
```

Only work on one node at a time.

Do not intentionally take down an additional node or Thunderbolt link while `noout` is set for another maintenance action.

## Cordon the node

Cordon prevents new pods from scheduling onto the node during maintenance.

```sh
kubectl cordon talos01
kubectl get nodes
```

A cordoned node will show:

```text
Ready,SchedulingDisabled
```

Do not leave the node cordoned after maintenance unless that is intentional.

## Upgrade a single Talos node manually

Example: upgrade `talos01` to Talos `v1.13.4`.

```sh
talosctl \
  --endpoints 192.168.42.11 \
  --nodes 192.168.42.11 \
  upgrade \
  --image ghcr.io/siderolabs/installer:v1.13.4 \
  --wait=false
```

Use the corresponding node IP for `talos02` or `talos03`.

If the command syntax changes, check:

```sh
talosctl upgrade --help
```

## Watch recovery

Watch Kubernetes, Talos, and Ceph together.

```sh
watch -n 5 '
date
kubectl get nodes -o wide
echo
talosctl --endpoints 192.168.42.11 --nodes 192.168.42.11 version 2>/dev/null | grep -E "NODE:|Tag:" || true
echo
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status 2>/dev/null | grep -E "health:|mon:|osd:|pgs:" || true
'
```

A healthy result after maintenance:

```text
node is Ready
node reports the expected Talos version
Ceph is HEALTH_OK or HEALTH_WARN only because noout is intentionally set
3 osds up / 3 in
PGs active+clean
```

## Validate routed backend after node recovery

After a node reboot, Talos network config should restore the Thunderbolt `/31` links, loopback `/32` identity, and static routes.

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

Confirm Ceph still advertises OSDs on the backend identities:

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd dump | grep -E 'osd\.[0-9]+'
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph config dump | grep -Ei 'public_network|cluster_network'
```

## Finish maintenance

Uncordon the node after it is back and healthy:

```sh
kubectl uncordon talos01
kubectl get nodes
```

Unset Ceph `noout` only after Ceph is stable except for the intentional `noout` warning:

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd unset noout
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status
```

Final checks:

```sh
kubectl get nodes -o wide
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd dump | grep -E 'osd\.[0-9]+'
kubectl get pods -A --field-selector=status.phase!=Running,status.phase!=Succeeded
```

## Confirm no node is still cordoned

```sh
kubectl get nodes
```

If a node is still cordoned, it will show `SchedulingDisabled`.

Uncordon intentionally:

```sh
kubectl uncordon talos01
```

## Pod placement report

Show all pods and the node each pod is running on:

```sh
kubectl get pods -A -o wide
```

Cleaner, sorted by node:

```sh
kubectl get pods -A \
  -o custom-columns='NODE:.spec.nodeName,NS:.metadata.namespace,POD:.metadata.name,STATUS:.status.phase,IP:.status.podIP' \
  --sort-by=.spec.nodeName
```

Count running pods per node:

```sh
kubectl get pods -A --field-selector=status.phase=Running \
  -o custom-columns='NODE:.spec.nodeName' --no-headers \
  | sort | uniq -c
```

## Alertmanager checks after maintenance

If Alertmanager is port-forwarded locally on port 9093:

```sh
curl -s http://localhost:9093/api/v2/alerts \
  | jq -r '.[] | select(.status.state=="active") |
    [.labels.alertname, (.labels.severity // "-"), (.labels.node // .labels.instance // .labels.pod // .labels.namespace // "-"), .startsAt] | @tsv' \
  | sort
```

Expected or common alerts during maintenance:

| Alert | Meaning |
| --- | --- |
| Watchdog | Expected always-on alert |
| TupprOperatorAbsent | Expected if Tuppr is intentionally scaled to zero |
| CephOSDNooutSet or similar | Expected while Ceph `noout` is intentionally set |
| KubeJobFailed | A failed Job object still exists, even if a later CronJob run succeeded |

## CronJob failure cleanup

A successful later CronJob run does not clear an older failed Job. Each CronJob execution creates a separate Job object. `KubeJobFailed` remains active while the failed Job object still exists.

List jobs:

```sh
kubectl -n default get jobs | grep -E 'plex-off-deck|recyclarr|NAME'
```

Delete the exact failed Job names, not the CronJob base names:

```sh
kubectl -n default delete job <failed-job-name> --ignore-not-found
```

Example:

```sh
kubectl -n default delete job plex-off-deck-29690820 recyclarr-29691360 --ignore-not-found
```

Then recheck Alertmanager after the next Prometheus evaluation.

## VictoriaLogs notes

VictoriaLogs components live in `o11y`.

Check collector and server pods:

```sh
kubectl -n o11y get pods -o wide | grep -Ei 'victoria|collector'
kubectl -n o11y get daemonset,deploy | grep -Ei 'victoria|collector'
```

Expected collector state:

```text
victoria-logs-collector   3/3
victoria-logs-0           Running
```

The collector streams these Kubernetes fields:

```text
kubernetes.pod_name
kubernetes.pod_namespace
kubernetes.container_name
kubernetes.pod_labels.app.kubernetes.io/name
```

Useful UI searches depend on the exact LogsQL syntax for the deployed version. Start broad with pod, namespace, or node names, then inspect the JSON for the exact label names.

If a namespace returns no historical logs, use Kubernetes events, custom resource status, and controller status as the source of truth. For Tuppr upgrade failures, the `TalosUpgrade` CR status is usually the most durable record.

## Troubleshooting a stuck or failed node upgrade

If an upgrade appears stuck:

1. Check whether the node is Ready or cordoned.
2. Check Talos version directly with explicit endpoint and node.
3. Check Ceph quorum and OSD state.
4. Check Tuppr CR status if the upgrade was started by Tuppr.
5. Avoid rebooting additional nodes until Ceph is healthy again.

Useful commands:

```sh
kubectl get nodes -o wide
talosctl --endpoints 192.168.42.11 --nodes 192.168.42.11 version
talosctl --endpoints 192.168.42.11 --nodes 192.168.42.11 service kubelet
talosctl --endpoints 192.168.42.11 --nodes 192.168.42.11 get machinestatus
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd tree
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd dump | grep -E 'osd\.[0-9]+'
```

If a node is healthy but pods are Pending, verify it is not still cordoned:

```sh
kubectl get nodes
```

If `SchedulingDisabled` is present and maintenance is complete:

```sh
kubectl uncordon <node>
```

## End-state checklist

Before considering maintenance complete:

```text
all Talos nodes report the intended version
all Kubernetes nodes are Ready
no node unexpectedly shows SchedulingDisabled
Ceph is HEALTH_OK
all OSDs are up/in
OSDs advertise 192.168.16.11/12/13 backend addresses
cluster_network and public_network are 192.168.16.0/24
PGs are active+clean
no unexpected not-ready pods
Tuppr is either intentionally paused or intentionally running
only expected Alertmanager alerts remain
```
