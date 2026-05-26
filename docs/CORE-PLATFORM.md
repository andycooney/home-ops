# Core platform apps

This file describes the base platform apps installed in the cluster and how to validate them.

## Flux

Flux reconciles this repository from `main`.

Validate:

```sh
flux check
flux get sources git -A
flux get ks -A
flux get hr -A
```

## cert-manager

cert-manager issues wildcard certificates for:

```text
*.cooney.site
*.cooney.online
```

Validate:

```sh
kubectl -n network get certificate
kubectl -n network get secret cooney-site-production-tls cooney-online-production-tls
```

Expected certificates:

```text
cooney-site-production     True   cooney-site-production-tls
cooney-online-production   True   cooney-online-production-tls
```

Inspect SANs:

```sh
for secret in cooney-site-production-tls cooney-online-production-tls; do
  echo "===== $secret ====="
  kubectl -n network get secret "$secret" -o jsonpath='{.data.tls\.crt}' \
    | base64 -d \
    | openssl x509 -noout -subject -issuer -dates -ext subjectAltName
done
```

## TLS certificate backup

TLS secrets are backed up to 1Password using External Secrets `PushSecret` resources.

Active path:

```text
kubernetes/apps/network/certificates/export
```

Current exports:

```text
network/cooney-online-production-tls -> 1Password item cooney-online-production-tls
network/cooney-site-production-tls   -> 1Password item cooney-site-production-tls
```

Validate:

```sh
flux get ks certificates-export -n network
kubectl -n network get pushsecret
kubectl -n network describe pushsecret cooney-online-production-tls
kubectl -n network describe pushsecret cooney-site-production-tls
op item get cooney-online-production-tls --vault kubernetes
op item get cooney-site-production-tls --vault kubernetes
```

## Cilium

Cilium provides cluster networking and advertises Gateway VIPs to the UDM using BGP.

Validate:

```sh
cilium status
cilium bgp peers
```

Expected BGP peer state:

```text
talos01/talos02/talos03 -> 172.16.1.1 established
Advertised = 2
```

## Envoy Gateway

Gateway VIPs:

```text
envoy-internal -> 192.168.60.1
envoy-external -> 192.168.60.2
```

Validate:

```sh
kubectl -n network get svc envoy-internal envoy-external -o wide
kubectl -n network get gateway envoy-internal envoy-external -o wide
```

## Cloudflare Tunnel

Cloudflare Tunnel provides external ingress for `*.cooney.online`.

Validate:

```sh
kubectl -n network get pods,deploy,svc,cm,secret,externalsecret | grep -iE "cloudflare|cloudflared|tunnel"
kubectl -n network logs deploy/cloudflare-tunnel --tail=120
cloudflared tunnel list
```

Current tunnel:

```text
name: kubernetes
id: e278fcc0-5e2d-4a62-9682-62d9cde718e7
```

## External DNS

External-DNS manages records:

```text
unifi-dns      -> internal cooney.site records
cloudflare-dns -> external cooney.online records
```

Validate:

```sh
kubectl -n network get pods | grep external-dns
kubectl -n network logs deploy/cloudflare-dns --tail=120
```

## Rook/Ceph

Rook/Ceph provides block storage.

Validate:

```sh
flux get ks -n rook-ceph
flux get hr -n rook-ceph
kubectl -n rook-ceph get cephcluster
kubectl -n rook-ceph get pods
```

If toolbox is available:

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph health detail
```

Expected:

```text
HEALTH_OK
```

## VolSync/Kopia

VolSync/Kopia provides PVC backup support to QNAP NFS.

Validate:

```sh
flux get ks kopia volsync volsync-maintenance -n volsync-system
flux get hr kopia -n volsync-system
kubectl -n volsync-system get pods
kubectl -n volsync-system get replicationsource,replicationdestination
curl -Ik https://kopia.cooney.site
```

## Observability

See:

```text
docs/OBSERVABILITY.md
```

## GitHub Actions Runner Controller

A scoped runner is installed for this repo only:

```text
https://github.com/andycooney/home-ops
```

Validate:

```sh
flux get ks -A | grep -E "actions-runner|runner"
flux get hr -n actions-runner-system
kubectl -n actions-runner-system get externalsecret
kubectl -n actions-runner-system get pods
kubectl -n actions-runner-system get autoscalingrunnersets,autoscalinglisteners,ephemeralrunnersets
```

Expected baseline:

```text
minRunners: 0
maxRunners: 1
ephemeral runner set current replicas: 0 when idle
```

## Tuppr

Tuppr is installed for future controlled Talos/Kubernetes upgrades, but upgrade definitions are suspended.

Validate:

```sh
kubectl api-resources | grep -i talos
flux get ks -A | grep -E "system-upgrade|tuppr"
flux get hr -n system-upgrade
kubectl -n system-upgrade get pods
kubectl -n system-upgrade get serviceaccount.talos.dev
kubectl -n system-upgrade get kubernetesupgrades,talosupgrades
```

Expected:

```text
tuppr Ready=True
tuppr-upgrades Suspended=True
```

Do not enable `tuppr-upgrades` casually.

## Intel GPU DRA

Intel GPU DRA support is installed for future workloads that can use Intel Quick Sync/iGPU.

Validate:

```sh
flux get ks intel-gpu-resource-driver -n kube-system
flux get hr intel-gpu-resource-driver -n kube-system
kubectl -n kube-system get pods | grep -i intel
kubectl get resourceslices
```

Expected ResourceSlice characteristics:

```text
Driver: gpu.intel.com
Driver attribute: i915
Pci Address: 0000:00:02.0
Sriov: false
```

## Multus

Multus is installed for selected workloads that need a secondary IoT VLAN interface.

Validate:

```sh
flux get ks multus multus-networks -n kube-system
flux get hr multus -n kube-system
kubectl -n kube-system get pods | grep -i multus
kubectl get network-attachment-definitions -A
kubectl -n kube-system get network-attachment-definition iot -o yaml
```

Expected:

```text
kube-system/iot NetworkAttachmentDefinition
interface: eno1.777
```

## Descheduler

Descheduler is installed in dry-run mode.

Validate:

```sh
flux get ks descheduler -n kube-system
flux get hr descheduler -n kube-system
kubectl -n kube-system get deploy,svc,servicemonitor,pods | grep descheduler
kubectl -n kube-system logs deploy/descheduler
```

Expected:

```text
DryRun is set to True
totalEvicted=0
evictionRequests=0
```
