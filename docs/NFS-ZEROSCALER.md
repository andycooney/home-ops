# NFS zeroscaler

This cluster uses a shared NFS health probe to scale NFS-dependent media apps down to zero when the QNAP/NFS export is unavailable, then back to one replica when NFS recovers.

## Why this exists

Several media apps mount NFS paths from `storage.cooney.site`. During QNAP/NFS maintenance or outage windows, leaving those apps running causes noisy errors and can leave workloads stuck against missing media paths.

The zeroscaler pattern makes those apps depend on the blackbox NFS probe instead:

```text
NFS healthy:     probe_success{job="nfs_probe"}=1 -> apps stay at 1 replica
NFS unavailable: probe_success{job="nfs_probe"}=0 -> apps scale to 0 replicas
NFS recovered:   probe_success{job="nfs_probe"}=1 -> apps scale back to 1 replica
```

## Control-plane requirement

Kubernetes HPA scale-to-zero requires the `HPAScaleToZero` feature gate on the API server and controller manager.

The Talos controller patch is:

```text
talos/patches/controller/cluster.yaml
```

Expected patch content:

```yaml
cluster:
  apiServer:
    extraArgs:
      feature-gates: HPAScaleToZero=true
  controllerManager:
    extraArgs:
      feature-gates: HPAScaleToZero=true
```

After changing Talos patches, regenerate and apply the control-plane configs:

```sh
just talos::generate-config

grep -R "HPAScaleToZero" -n talos/clusterconfig

just -f talos/mod.just apply-node 192.168.42.11
just -f talos/mod.just apply-node 192.168.42.12
just -f talos/mod.just apply-node 192.168.42.13
```

Validate that the API server accepts `minReplicas: 0`:

```sh
cat <<'EOF' | kubectl apply --dry-run=server -f -
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: hpa-scale-to-zero-test
  namespace: default
spec:
  minReplicas: 0
  maxReplicas: 1
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: does-not-matter
  metrics:
    - type: External
      external:
        metric:
          name: probe_success
          selector:
            matchLabels:
              job: nfs_probe
        target:
          type: Value
          value: "1"
EOF
```

Expected:

```text
horizontalpodautoscaler.autoscaling/hpa-scale-to-zero-test created (server dry run)
```

## Components

The reusable component is:

```text
kubernetes/components/zeroscaler
```

It creates an HPA with:

```yaml
minReplicas: 0
maxReplicas: 1
metrics:
  - type: External
    external:
      metric:
        name: probe_success
        selector:
          matchLabels:
            job: nfs_probe
      target:
        type: Value
        value: "1"
```

Important: the HPA selector currently matches only `job: nfs_probe`. Do not add `instance: storage.cooney.site:2049` directly to the HPA selector; the external metrics API rejects that value because of the colon in the label value.

## Protected apps

The current NFS-dependent apps protected by zeroscaler are:

```text
bazarr
qbittorrent
radarr
sabnzbd
sonarr
qui
stash
plex
whisparr
```

Tautulli was checked and does not currently mount `storage.cooney.site`, so it is not zeroscaled for NFS.

## Flux Kustomization convention

Cross-cutting components belong in the Flux `ks.yaml` layer:

```yaml
spec:
  components:
    - ../../../../components/volsync
    - ../../../../components/zeroscaler
  dependsOn:
    - name: rook-ceph-cluster
      namespace: rook-ceph
```

Use the app-level `app/kustomization.yaml` only for concrete app resources such as `helmrelease.yaml`, `ocirepository.yaml`, PVCs, ExternalSecrets, and config files.

Do not include `../../../../components/volsync` in both `ks.yaml` and `app/kustomization.yaml`; doing so double-renders resources such as `ExternalSecret/${APP}-volsync` and `ReplicationSource/${APP}`.

The broader VolSync consistency audit is tracked in issue #92.

## Validate healthy state

Check the external metric:

```sh
kubectl get --raw '/apis/external.metrics.k8s.io/v1beta1/namespaces/default/probe_success' | jq
```

Expected metric labels include:

```text
job=nfs_probe
instance=storage.cooney.site:2049
```

Check HPAs:

```sh
kubectl -n default get hpa \
  bazarr qbittorrent radarr sabnzbd sonarr \
  qui stash plex whisparr
```

Expected while NFS is healthy:

```text
TARGETS   1/1
MINPODS   0
MAXPODS   1
REPLICAS  1
```

Check HPA conditions:

```sh
for app in bazarr qbittorrent radarr sabnzbd sonarr qui stash plex whisparr; do
  echo "===== $app ====="
  kubectl -n default describe hpa "$app" | sed -n '/Conditions:/,/Events:/p'
done
```

Expected condition:

```text
ScalingActive=True
ValidMetricFound
```

## NFS outage test procedure

Capture a baseline before touching NFS:

```sh
mkdir -p /tmp/nfs-failover-test
TS="$(date +%Y%m%d-%H%M%S)"
OUT="/tmp/nfs-failover-test/baseline-${TS}.txt"

{
  echo "===== timestamp ====="
  date -Iseconds

  echo
  echo "===== nfs external metric ====="
  kubectl get --raw '/apis/external.metrics.k8s.io/v1beta1/namespaces/default/probe_success' | jq

  echo
  echo "===== hpa ====="
  kubectl -n default get hpa bazarr qbittorrent radarr sabnzbd sonarr qui stash plex whisparr -o wide

  echo
  echo "===== deployments ====="
  kubectl -n default get deploy bazarr qbittorrent radarr sabnzbd sonarr qui stash plex whisparr -o wide

  echo
  echo "===== pods ====="
  kubectl -n default get pod -o wide | grep -E 'bazarr|qbittorrent|radarr|sabnzbd|sonarr|qui|stash|plex|whisparr' || true
} | tee "$OUT"

echo "Saved baseline to $OUT"
```

Watch the transition during maintenance:

```sh
watch -n2 '
date
echo
kubectl get --raw /apis/external.metrics.k8s.io/v1beta1/namespaces/default/probe_success 2>/dev/null | jq -r ".items[]? | [.metricLabels.job, .metricLabels.instance, .value] | @tsv" || true
echo
kubectl -n default get hpa bazarr qbittorrent radarr sabnzbd sonarr qui stash plex whisparr
echo
kubectl -n default get deploy bazarr qbittorrent radarr sabnzbd sonarr qui stash plex whisparr
echo
kubectl -n default get pod | grep -E "bazarr|qbittorrent|radarr|sabnzbd|sonarr|qui|stash|plex|whisparr" || true
'
```

Expected when NFS goes down:

```text
probe_success changes from 1 to 0 or becomes unavailable
HPAs calculate desired replicas as 0
Deployments move to desired replicas 0
Pods terminate
```

Expected when NFS comes back:

```text
probe_success returns to 1
HPAs return to 1/1
Deployments return to 1/1
Pods restart cleanly
```

The initial end-to-end test during QNAP/NFS maintenance worked as expected: protected apps scaled down when NFS was unavailable and scaled back to one replica after the probe recovered.

## Plex note

Plex may take longer than other apps to become ready after an NFS outage because it initializes the media server and library state. Verify it separately after recovery:

```sh
kubectl -n default get deploy plex
kubectl -n default get pod -l app.kubernetes.io/name=plex
curl -s http://192.168.60.40:32400/identity
```

A Plex log line like this is expected noise in the container and is not currently treated as a health issue:

```text
Critical: libusb_init failed
```

The legacy `Sub-Zero.bundle` plugin was moved aside from the Plex config PVC after it logged framework startup errors. `Services.bundle` is a normal Plex bundle and should not be removed.
