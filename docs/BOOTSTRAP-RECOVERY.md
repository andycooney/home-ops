# Bootstrap and recovery

Use this when rebuilding or recovering the cluster from this repository.

## Before attempting a rebuild

From a workstation:

```sh
mise trust
mise install
op whoami
export OP_SERVICE_ACCOUNT_TOKEN="$(op read 'op://kubernetes/onepass_principal/credential')"
```

Confirm the repository renders locally:

```sh
kubectl kustomize kubernetes/apps/flux-system >/tmp/flux-system-render.yaml
kubectl kustomize kubernetes/apps/cert-manager >/tmp/cert-manager-render.yaml
kubectl kustomize kubernetes/apps/network >/tmp/network-render.yaml
kubectl kustomize kubernetes/apps/default >/tmp/default-render.yaml
kubectl kustomize kubernetes/apps/kube-system >/tmp/kube-system-render.yaml
```

Run repo validation:

```sh
scripts/validate-repo.sh
```

## Talos configuration

Talos machine configuration is generated from a shared machineconfig template and per-node patch files.

Review:

```sh
ls talos
ls talos/nodes
sed -n '1,220p' talos/machineconfig.yaml.j2
sed -n '1,220p' talos/nodes/talos01.yaml.j2
sed -n '1,220p' talos/nodes/talos02.yaml.j2
sed -n '1,220p' talos/nodes/talos03.yaml.j2
```

Confirm:

- node management IPs;
- install disks;
- install image/schematic;
- DNS;
- VIP/API endpoint;
- primary NIC/bond config;
- Thunderbolt interface aliases;
- routed Ceph backend addresses and routes;
- Kubernetes Talos API access for Tuppr.

Current node resolver target:

```text
172.16.1.1
```

Current Talos/Kubernetes API VIP:

```text
192.168.42.20
```

## Talos routed Thunderbolt backend

The Ceph backend uses routed Thunderbolt L3. It is not a Linux bridge.

Stable backend identities:

```text
talos01 -> 192.168.16.11/32
talos02 -> 192.168.16.12/32
talos03 -> 192.168.16.13/32
```

Thunderbolt point-to-point links:

```text
talos01 <-> talos02: 192.168.16.0/31
talos01 <-> talos03: 192.168.16.2/31
talos02 <-> talos03: 192.168.16.4/31
```

Interface alias convention:

```text
thunderbolt0 = physical left  = busPath 1-1.0
thunderbolt1 = physical right = busPath 0-1.0
```

Cabling convention:

```text
talos01 right -> talos02 left
talos02 right -> talos03 left
talos03 right -> talos01 left
```

Do not recreate the old `ceph0` bridge. The bridge design was tested and rejected because transit TCP over bridged `thunderbolt-net` was pathologically slow.

Detailed runbook:

```text
docs/runbooks/ceph-thunderbolt-backend.md
```

## IoT VLAN Talos patch

Current Talos network patch creates VLAN 777 on the primary node NIC:

```text
parent interface: eno1
VLAN interface:   eno1.777
VLAN ID:          777
Purpose:          IoT network for Multus/Home Assistant workloads
```

Expected patch:

```yaml
machine:
  network:
    interfaces:
      - interface: eno1
        vlans:
          - vlanId: 777
            dhcp: false
```

Talos should not receive a routable IPv4 address on this VLAN.

## Tuppr Talos API access

Current Talos feature patch enables Kubernetes Talos API access for the system-upgrade namespace:

```yaml
machine:
  features:
    kubernetesTalosAPIAccess:
      enabled: true
      allowedRoles:
        - os:admin
      allowedKubernetesNamespaces:
        - system-upgrade
```

Do not add additional namespaces unless a workload intentionally needs Talos API access.

## Regenerate and apply Talos config

Generate all node configs:

```sh
just -f talos/mod.just generate-config
```

Generate one node config:

```sh
just -f talos/mod.just generate-config talos01
just -f talos/mod.just generate-config 02
```

Verify generated configs:

```sh
grep -R "vlanId: 777\|interface: eno1" -n talos/clusterconfig
grep -R "kubernetesTalosAPIAccess\|allowedKubernetesNamespaces\|allowedRoles\|system-upgrade" -n talos/clusterconfig
grep -R "192.168.16\|thunderbolt0\|thunderbolt1\|busPath" -n talos/clusterconfig
```

Apply to nodes:

```sh
just -f talos/mod.just apply-node 192.168.42.11
just -f talos/mod.just apply-node 192.168.42.12
just -f talos/mod.just apply-node 192.168.42.13
```

Validate VLAN links:

```sh
for node in talos01.cooney.site talos02.cooney.site talos03.cooney.site; do
  echo "===== $node ====="
  talosctl -n "$node" get links | grep -E "eno1($|[[:space:]])|eno1\.777"
  talosctl -n "$node" get addresses | grep eno1.777 || true
done
```

Expected:

```text
eno1      up true
eno1.777  up true
```

The only expected address on `eno1.777` is IPv6 link-local.

Validate Thunderbolt backend addresses and routes:

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

## Bootstrap Talos

Only run when rebuilding from bare nodes or after a full reset:

```sh
just bootstrap talos
```

## Bootstrap Flux/apps

```sh
just bootstrap apps
```

Watch startup:

```sh
kubectl get pods -A --watch
```

## Flux recovery

Force Flux to fetch latest repo state:

```sh
flux reconcile source git flux-system -n flux-system
```

Webhook URL:

```text
https://flux-webhook.cooney.online/hook/<receiver-webhook-path>
```

Get the live receiver path:

```sh
kubectl -n flux-system get receiver github-webhook -o jsonpath='{.status.webhookPath}' && echo
```

The GitHub webhook should use:

```text
Content type: application/json
Events: push
SSL verification: enabled
Secret: value from flux-system/github-webhook-token-secret
```

Get webhook secret value:

```sh
kubectl -n flux-system get secret github-webhook-token-secret \
  -o jsonpath='{.data.token}' | base64 -d && echo
```

## Key reconciliation commands

```sh
flux reconcile kustomization flux-system -n flux-system --with-source --timeout=5m
flux reconcile kustomization flux-instance -n flux-system --with-source --timeout=5m
flux reconcile kustomization cert-manager -n cert-manager --with-source --timeout=5m
flux reconcile kustomization cloudflare-dns -n network --with-source --timeout=5m
flux reconcile kustomization cloudflare-tunnel -n network --with-source --timeout=5m
flux reconcile kustomization envoy-gateway -n network --with-source --timeout=10m
flux reconcile kustomization k8s-gateway -n network --with-source --timeout=5m
flux reconcile kustomization unifi-dns -n network --with-source --timeout=5m
flux reconcile kustomization echo -n default --with-source --timeout=5m
flux reconcile kustomization coredns -n kube-system --with-source --timeout=5m
flux reconcile kustomization cilium -n kube-system --with-source --timeout=10m
flux reconcile kustomization rook-ceph -n rook-ceph --with-source --timeout=10m
flux reconcile kustomization rook-ceph-cluster -n rook-ceph --with-source --timeout=10m
flux reconcile kustomization kopia -n volsync-system --with-source --timeout=5m
```

Rook/Ceph reconciles can take several minutes when host networking or daemon placement changes cause MON/MGR/MDS/OSD pods to rotate.

## Post-recovery Ceph checks

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd dump | grep -E 'osd\.[0-9]+'
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph config dump | grep -Ei 'public_network|cluster_network'
```

Expected:

```text
HEALTH_OK
OSDs advertise 192.168.16.11/12/13
cluster_network 192.168.16.0/24
public_network 192.168.16.0/24
```

## Post-recovery health

```sh
scripts/cluster-health.sh
```
