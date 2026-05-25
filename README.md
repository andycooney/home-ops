# home-ops Recovery Notes

This repository is the source of truth for the Kubernetes homelab. It contains the Talos configuration, Flux GitOps configuration, application manifests, DNS/TLS routing, and operational notes needed to recover or rebuild the cluster.

This README is focused on **resurrecting the environment** if the cluster needs to be recovered, rebuilt, or bootstrapped again from this repo.

## Current cluster shape

- Kubernetes is deployed on Talos Linux.
- Flux reconciles this repository from the `main` branch.
- Secrets and cluster variables are sourced from 1Password through External Secrets.
- Internal DNS is under `cooney.site`.
- External DNS is under `cooney.online`.
- Internal apps route through `envoy-internal`.
- External apps route through `envoy-external` and Cloudflare Tunnel.
- Cert-manager issues short-lived Let's Encrypt wildcard certificates for both domains.
- External-DNS manages DNS records:
  - `unifi-dns` manages internal `cooney.site` records in UniFi.
  - `cloudflare-dns` manages external `cooney.online` records in Cloudflare.
- Intel GPU DRA support is installed through `intel-gpu-resource-driver`.
- Multus is installed for selected workloads that need a secondary interface.
- Talos nodes expose an IoT VLAN link on `eno1.777` for Multus-attached workloads such as Home Assistant.
- Talos nodes do not have routable IP addresses on the IoT VLAN.
- Production TLS certificate secrets are backed up to 1Password with External Secrets `PushSecret` resources.
- Tuppr is installed as the system upgrade controller, but the actual Talos/Kubernetes upgrade resources are suspended for manual enablement.
- A scoped GitHub Actions Runner Controller scale set is installed for `andycooney/home-ops` with scale-to-zero behavior.

## Important URLs

Internal-only:

- `https://kopia.cooney.site`
- `https://rook.cooney.site`
- `https://sabnzbd.cooney.site`
- `https://grafana.cooney.site`
- `https://prometheus.cooney.site`
- `https://alertmanager.cooney.site`

External:

- `https://echo.cooney.online`
- `https://flux-webhook.cooney.online`

Known external application notes:

- `https://sabnzbd.cooney.online` reaches the external Envoy Gateway, but SABnzbd currently blocks public internet access with `External internet access denied`. Do not treat this as a known-good external URL until Cloudflare Access and SABnzbd exposure settings are intentionally configured.

## Domain model

The domain split is intentional:

```text
cooney.site   = internal only
cooney.online = external only
```

Internal routes should generally create CNAME records pointing at:

```text
internal.cooney.site
```

External routes should generally create CNAME records pointing at:

```text
external.cooney.online
```

The gateway records are generated automatically:

```text
internal.cooney.site -> 192.168.60.1
external.cooney.online -> 192.168.60.2
```

Do not manually recreate app DNS records unless external-dns is unavailable and recovery requires a temporary manual workaround.

## 1Password dependencies

The repo expects these values to exist in 1Password.

### Cluster variable item

Vault: `Kubernetes`
Item: `home-ops-bootstrap`

Required fields:

```text
INTERNAL_DOMAIN = cooney.site
EXTERNAL_DOMAIN = cooney.online
```

### Cloudflare item

Vault: `Kubernetes`
Item: `cloudflare`

Required fields:

```text
CLOUDFLARE_API_TOKEN
CLOUDFLARE_TUNNEL_ID
```

The Cloudflare API token must be able to manage both `cooney.online` and `cooney.site` for cert-manager DNS-01 validation:

```text
Zone:Read
DNS:Edit
```

It also needs the required Cloudflare Tunnel permissions used by the tunnel/external DNS setup.

### OnePassword Connect / service account

Bootstrap/recovery requires access to the 1Password service account token used by External Secrets / OnePassword Connect.

Current local environment pattern:

```sh
export OP_SERVICE_ACCOUNT_TOKEN="$(op read 'op://kubernetes/onepass_principal/credential')"
```

If rebuilding from a new workstation, make sure the 1Password CLI is authenticated and this token can be read before attempting bootstrap.

### Flux webhook secret

Vault: `Kubernetes`
Item: `flux`
Field:

```text
GITHUB_WEBHOOK_TOKEN
```

The ExternalSecret creates:

```text
flux-system/github-webhook-token-secret
```

The Flux Receiver uses that Secret to validate GitHub webhook signatures.

Verify:

```sh
kubectl -n flux-system get externalsecret github-webhook-token
kubectl -n flux-system get secret github-webhook-token-secret
kubectl -n flux-system get receiver github-webhook
```

### GitHub Actions runner app

Vault: `Kubernetes`
Item: `actions-runner`

Required fields:

```text
ACTIONS_RUNNER_APP_ID
ACTIONS_RUNNER_INSTALLATION_ID
ACTIONS_RUNNER_PRIVATE_KEY
```

These values back the GitHub App used by GitHub Actions Runner Controller for the `andycooney/home-ops` repository.

## Secrets model

The cluster now uses External Secrets for shared cluster variables:

```text
kubernetes/components/externalsecret.yaml
```

This creates a namespace-local Secret named:

```text
cluster-secrets
```

in the app namespaces that include the shared component.

Expected keys:

```text
INTERNAL_DOMAIN
EXTERNAL_DOMAIN
CLOUDFLARE_TUNNEL_ID
```

Verify after recovery:

```sh
kubectl get externalsecret -A | grep cluster-secrets
kubectl get secret -A | grep cluster-secrets
```

Expected namespaces:

```text
cert-manager
default
flux-system
kube-system
network
```

## Storage model

The cluster currently uses multiple storage backends for different purposes.

### Rook/Ceph block storage

Most persistent application PVCs should use the Rook/Ceph block storage class:

```text
ceph-block
```

For example, the observability stack stores its persistent data on `ceph-block`:

```text
alertmanager-kube-prometheus-stack-db-alertmanager-kube-prometheus-stack-0   1Gi   ceph-block
prometheus-kube-prometheus-stack-db-prometheus-kube-prometheus-stack-0       50Gi  ceph-block
grafana-pvc                                                                  5Gi   ceph-block
```

These volumes are provisioned by the Rook/Ceph RBD CSI driver and are not stored directly on a Talos node filesystem path.

Useful checks:

```sh
kubectl get pvc -A
kubectl -n rook-ceph get cephcluster
kubectl -n rook-ceph get cephblockpool
```

### OpenEBS hostpath storage

OpenEBS hostpath storage is available for workloads that intentionally need local-node hostpath storage, scratch space, or cache-style PVCs.

On this Talos cluster, OpenEBS hostpath must use a kubelet-visible base path:

```text
/var/lib/kubelet/openebs/local
```

Do not use these older paths on this cluster:

```text
/var/openebs/local
/var/mnt/local-hostpath
```

Those paths may exist on the Talos host, but they are not reliably visible from inside Talos' containerized kubelet root filesystem.

Useful checks:

```sh
kubectl get storageclass
kubectl -n openebs-system get pods
kubectl get pvc -A | grep openebs
```

### VolSync / Kopia backups

VolSync backups are written to a Kopia filesystem repository backed by the QNAP NFS export:

```text
storage.cooney.site:/home-ops-backups
```

Each protected app should have its own repository directory:

```text
/home-ops-backups/<app>
```

Inside VolSync mover pods, the NFS export is mounted at:

```text
/mnt/repository
```

The per-app Kopia repository URL is stored in the app's 1Password item as:

```text
KOPIA_REPOSITORY = filesystem:///mnt/repository/<app>
```

Useful checks:

```sh
kubectl get replicationsource -A
kubectl get replicationdestination -A
kubectl get externalsecret -A | grep volsync
```

## Remaining SOPS usage

SOPS may still exist for bootstrap-level or legacy secrets. Do not delete these unless the bootstrap process has been updated to replace them with 1Password/External Secrets.

Check remaining SOPS files with:

```sh
find . -name "*.sops.yaml" -o -name "*.sops.yml"
```

Known historical files may include:

```text
talos/talsecret.sops.yaml
bootstrap/github-deploy-key.sops.yaml
bootstrap/sops-age.sops.yaml
kubernetes/apps/network/cloudflare-tunnel/app/secret.sops.yaml
```

The Flux GitHub webhook token was moved from:

```text
kubernetes/apps/flux-system/flux-instance/app/secret.sops.yaml
```

to the `flux` 1Password item field:

```text
GITHUB_WEBHOOK_TOKEN
```

The old `kubernetes/components/sops/cluster-secrets.sops.yaml` should not be used for domain substitution anymore.

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

Confirm no old domain substitution remains:

```sh
grep -R "SECRET_DOMAIN" -n kubernetes/apps kubernetes/components
```

Expected result: no active app references.

## Bootstrap / recovery flow

Use this when rebuilding the cluster from this repository.

### 1. Confirm Talos config

Review Talos node configuration and generated cluster config:

```sh
ls talos
ls talos/clusterconfig
```

Confirm node IPs, disks, install image, DNS, VIP/API endpoint, and patches are correct.

Current node resolver target should be the internal network resolver:

```text
172.16.1.1
```

Current Talos network patch creates VLAN 777 on the primary node NIC:

```text
parent interface: eno1
VLAN interface:   eno1.777
VLAN ID:          777
Purpose:          IoT network for Multus/Home Assistant workloads
```

The VLAN interface is intentionally created without DHCP or static IPv4 addressing. Talos/Kubernetes should not be reachable from the IoT VLAN through this interface.

Expected Talos network patch:

```yaml
machine:
  network:
    interfaces:
      - interface: eno1
        vlans:
          - vlanId: 777
            dhcp: false
```

Current Talos feature patch enables Kubernetes Talos API access for the system upgrade controller:

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

Do not add additional namespaces to `allowedKubernetesNamespaces` unless a workload intentionally needs Talos API access.

Regenerate generated Talos configs after editing patches:

```sh
just -f talos/mod.just generate-config
```

Verify the generated configs include VLAN 777 and Talos API access:

```sh
grep -R "vlanId: 777\|interface: eno1" -n talos/clusterconfig
grep -R "kubernetesTalosAPIAccess\|allowedKubernetesNamespaces\|allowedRoles\|system-upgrade" -n talos/clusterconfig
```

Apply updated config to each node:

```sh
just -f talos/mod.just apply-node 192.168.42.11
just -f talos/mod.just apply-node 192.168.42.12
just -f talos/mod.just apply-node 192.168.42.13
```

Validate the live Talos VLAN links:

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

The only expected address on `eno1.777` is an IPv6 link-local `fe80::/64` address. There should be no routable IPv4 address on VLAN 777.

Validate Talos API access is exposed to Kubernetes:

```sh
kubectl api-resources | grep -i talos
```

Expected:

```text
serviceaccounts   tsa   talos.dev/v1alpha1
talosupgrades           tuppr.home-operations.com/v1alpha1
```

### 2. Bootstrap Talos if needed

Only do this when rebuilding from bare nodes or after a full reset.

```sh
just bootstrap talos
```

If applying updated Talos config to existing nodes instead of rebuilding:

```sh
just -f talos/mod.just generate-config
just -f talos/mod.just apply-node <node-ip>
```

### 3. Bootstrap Flux/apps

```sh
just bootstrap apps
```

Then watch cluster startup:

```sh
kubectl get pods -A --watch
```

## Flux recovery commands

Force Flux to fetch the latest repo state:

```sh
flux reconcile source git flux-system -n flux-system
```

GitHub push webhooks should normally trigger Flux reconciliation automatically.

Webhook URL:

```text
https://flux-webhook.cooney.online/hook/<receiver-webhook-path>
```

Get the live receiver path:

```sh
kubectl -n flux-system get receiver github-webhook -o jsonpath='{.status.webhookPath}' && echo
```

The GitHub webhook should be configured with:

```text
Content type: application/json
Events: push
SSL verification: enabled
Secret: value from flux-system/github-webhook-token-secret
```

Get the webhook secret value for GitHub:

```sh
kubectl -n flux-system get secret github-webhook-token-secret \
  -o jsonpath='{.data.token}' | base64 -d && echo
```

List all Flux Kustomizations:

```sh
kubectl get kustomizations.kustomize.toolkit.fluxcd.io -A
```

Reconcile key pieces:

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

## Post-recovery validation

### Flux

```sh
flux check
flux get sources git -A
flux get ks -A
flux get hr -A
```

### External Secrets

```sh
kubectl get externalsecret -A
kubectl get secret -A | grep cluster-secrets
```

### Cert-manager

```sh
kubectl -n network get certificate
kubectl -n network get secret cooney-site-production-tls cooney-online-production-tls
```

Expected certificates:

```text
cooney-site-production     True   cooney-site-production-tls
cooney-online-production   True   cooney-online-production-tls
```

Inspect certificate SANs:

```sh
for secret in cooney-site-production-tls cooney-online-production-tls; do
  echo "===== $secret ====="
  kubectl -n network get secret "$secret" -o jsonpath='{.data.tls\.crt}' \
    | base64 -d \
    | openssl x509 -noout -subject -issuer -dates -ext subjectAltName
done
```

### Gateways

```sh
kubectl -n network get gateway envoy-internal envoy-external -o yaml \
  | grep -E "internal\.cooney\.site|external\.cooney\.online|cooney-site-production-tls|cooney-online-production-tls" -B2 -A2
```

Expected:

```text
envoy-internal -> internal.cooney.site -> cooney-site-production-tls
envoy-external -> external.cooney.online -> cooney-online-production-tls
```

### Cilium BGP / UDM routing

Cilium advertises Envoy Gateway LoadBalancer VIPs to the UniFi Dream Machine using BGP.

Expected VIPs:

```text
envoy-internal -> 192.168.60.1
envoy-external -> 192.168.60.2
```

Verify the Gateway services:

```sh
kubectl -n network get svc envoy-internal envoy-external -o wide
kubectl -n network get gateway envoy-internal envoy-external -o wide
```

Verify Cilium BGP peers:

```sh
cilium bgp peers
```

Expected peer state:

```text
talos01/talos02/talos03 -> 172.16.1.1 established
Advertised = 2
```

On the UDM, verify received prefixes and installed routes:

```sh
vtysh -c "show bgp summary"
vtysh -c "show bgp ipv4 unicast"
vtysh -c "show ip route bgp"
```

Expected UDM routes:

```text
B>* 192.168.60.1/32 via 192.168.42.11, br200
  *                    via 192.168.42.12, br200
  *                    via 192.168.42.13, br200
B>* 192.168.60.2/32 via 192.168.42.11, br200
  *                    via 192.168.42.12, br200
  *                    via 192.168.42.13, br200
```

If BGP peers are established but `State/PfxRcd` is `0`, reload/check the UDM FRR configuration. The inbound route-map must allow the Cilium LoadBalancer pool:

```text
192.168.60.0/24 le 32
```

Useful FRR config file:

```text
kubernetes/apps/kube-system/cilium/app/udm-frr-bgp.conf
```

### Routes

```sh
kubectl get httproute -A
```

Expected notable routes:

```text
echo                                echo.cooney.online
github-webhook                      flux-webhook.cooney.online
rook-ceph-dashboard                 rook.cooney.site
kopia                               kopia.cooney.site
sabnzbd-internal                    sabnzbd.cooney.site
sabnzbd-external                    sabnzbd.cooney.online
grafana-httproute                   grafana.cooney.site
kube-prometheus-stack-prometheus    prometheus.cooney.site
kube-prometheus-stack-alertmanager  alertmanager.cooney.site
```

Multus IoT is not an HTTPRoute. It is represented by:

```text
kube-system/iot                    NetworkAttachmentDefinition
```

### DNS

Internal DNS:

```sh
dig internal.cooney.site +short
dig kopia.cooney.site +short
dig rook.cooney.site +short
dig sabnzbd.cooney.site +short
dig grafana.cooney.site +short
dig prometheus.cooney.site +short
dig alertmanager.cooney.site +short
```

Expected:

```text
internal.cooney.site -> 192.168.60.1
kopia.cooney.site -> internal.cooney.site
rook.cooney.site -> internal.cooney.site
sabnzbd.cooney.site -> internal.cooney.site
grafana.cooney.site -> internal.cooney.site
prometheus.cooney.site -> internal.cooney.site
alertmanager.cooney.site -> internal.cooney.site
```

External DNS:

```sh
dig external.cooney.online +short
dig echo.cooney.online +short
dig flux-webhook.cooney.online +short
dig sabnzbd.cooney.online +short
```

Expected:

```text
external.cooney.online -> Cloudflare Tunnel / external gateway target
echo.cooney.online -> external.cooney.online
flux-webhook.cooney.online -> external.cooney.online
sabnzbd.cooney.online -> external.cooney.online
```

### HTTP/HTTPS checks

```sh
curl -I http://kopia.cooney.site
curl -Ik https://kopia.cooney.site
curl -I http://rook.cooney.site
curl -Ik https://rook.cooney.site
curl -I http://sabnzbd.cooney.site
curl -Ik https://sabnzbd.cooney.site
curl -Ik https://grafana.cooney.site
curl -kL https://prometheus.cooney.site/-/ready
curl -kL https://alertmanager.cooney.site/-/ready
```

Expected:

```text
HTTP  -> 301 redirect to HTTPS
HTTPS -> 200 or app-specific success response
```

For SABnzbd, a successful internal response may be an app-specific redirect such as:

```text
HTTP 303 -> /sabnzbd/wizard/
```

## Kube-system platform notes

### Intel GPU resource driver

Intel GPU DRA support is installed under:

```text
kubernetes/apps/kube-system/intel-gpu-resource-driver
```

This is useful for future workloads that can use Intel Quick Sync / iGPU acceleration, such as media or camera workloads.

Validated hardware exposure on each Talos node:

```text
/dev/dri/card0
/dev/dri/renderD128
```

Validate the Flux and Helm resources:

```sh
flux get ks intel-gpu-resource-driver -n kube-system
flux get hr intel-gpu-resource-driver -n kube-system
kubectl -n kube-system get pods | grep -i intel
```

Expected:

```text
intel-gpu-resource-driver                          Ready=True
intel-gpu-resource-driver-kubelet-plugin           3/3 pods Running
```

This driver uses Kubernetes Dynamic Resource Allocation rather than the older device-plugin allocatable-resource pattern. GPU availability is published through `ResourceSlice` objects, not classic node allocatable keys.

Validate DRA resources:

```sh
kubectl api-resources | grep -i resource
kubectl get resourceslices
kubectl describe resourceslice <slice-name>
```

Expected ResourceSlice characteristics:

```text
Driver: gpu.intel.com
Driver attribute: i915
Pci Address: 0000:00:02.0
Sriov: false
```

Current expected resource slices:

```text
talos01 -> gpu.intel.com -> i915 -> 0000:00:02.0
talos02 -> gpu.intel.com -> i915 -> 0000:00:02.0
talos03 -> gpu.intel.com -> i915 -> 0000:00:02.0
```

### Multus / IoT VLAN preparation

Multus is installed for workloads that need a secondary interface on the IoT VLAN, especially Home Assistant.

Current network preparation:

```text
IoT VLAN: 777
IoT subnet: 192.168.70.0/24
IoT gateway: 192.168.70.1
Talos parent NIC: eno1
Talos VLAN link: eno1.777
Talos IPv4 on IoT VLAN: none
```

This allows selected pods to attach directly to the IoT VLAN while keeping the Talos host itself off that routed network.

The live IoT `NetworkAttachmentDefinition` is:

```text
kube-system/iot
```

It uses the prepared VLAN interface:

```text
eno1.777
```

Validate Multus and the IoT network attachment:

```sh
flux get ks multus multus-networks -n kube-system
flux get hr multus -n kube-system
kubectl -n kube-system get pods | grep -i multus
kubectl get network-attachment-definitions -A
kubectl -n kube-system get network-attachment-definition iot -o yaml
```

Expected:

```text
multus             Ready=True
multus-networks    Ready=True
multus             HelmRelease Ready=True
multus DaemonSet   3/3 pods Running
kube-system/iot    NetworkAttachmentDefinition
```

Do not attach general workloads to the IoT VLAN. Only workloads that intentionally need L2/multicast access to IoT devices should receive this secondary interface.

Home Assistant should eventually use this network attachment with a static IoT VLAN IP outside the UniFi DHCP range.

## Network platform notes

### TLS certificate backup to 1Password

Existing cert-manager managed TLS secrets are backed up to 1Password using External Secrets `PushSecret` resources.

Active export path:

```text
kubernetes/apps/network/certificates/export
```

The active Flux Kustomization is:

```text
network/certificates-export
```

Current exported certificate secrets:

```text
network/cooney-online-production-tls -> 1Password item cooney-online-production-tls
network/cooney-site-production-tls   -> 1Password item cooney-site-production-tls
```

Normal operation keeps cert-manager as the source of truth. When cert-manager renews a live Kubernetes TLS secret, the `PushSecret` should update the matching 1Password item.

Validate:

```sh
flux get ks certificates-export -n network
kubectl -n network get pushsecret
kubectl -n network describe pushsecret cooney-online-production-tls
kubectl -n network describe pushsecret cooney-site-production-tls
op item get cooney-online-production-tls --vault kubernetes
op item get cooney-site-production-tls --vault kubernetes
```

Expected:

```text
certificates-export Ready=True
cooney-online-production-tls Synced
cooney-site-production-tls Synced
```

Recovery-only import manifests are kept under:

```text
kubernetes/apps/network/certificates/import
```

They are intentionally not referenced by the active `certificates-export` Kustomization. Use them only during recovery if cert-manager/DNS validation is unavailable and the TLS secrets need to be restored from 1Password.

## Platform automation notes

### System upgrade controller

Tuppr is installed under:

```text
kubernetes/apps/system-upgrade
```

Talos Kubernetes API access is enabled for the `system-upgrade` namespace so Tuppr can create its Talos service account:

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

The controller is active, but the upgrade definitions are intentionally suspended:

```text
system-upgrade/tuppr            active
system-upgrade/tuppr-upgrades   suspended
```

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
serviceaccounts.talos.dev exists
tuppr Ready=True
tuppr-upgrades Suspended=True
tuppr HelmRelease Ready=True
no KubernetesUpgrade or TalosUpgrade resources are applied while suspended
```

Manual enablement options for a future controlled upgrade:

```sh
flux resume ks tuppr-upgrades -n system-upgrade
```

or remove `suspend: true` from:

```text
kubernetes/apps/system-upgrade/tuppr/ks.yaml
```

Do not enable `tuppr-upgrades` casually. It includes Talos/Kubernetes upgrade resources and Talos `rebootMode: powercycle` behavior.

### GitHub Actions runner

A scoped GitHub Actions Runner Controller deployment is installed under:

```text
kubernetes/apps/actions-runner-system
```

The runner scale set is configured for this repo only:

```text
https://github.com/andycooney/home-ops
```

Current security posture:

```text
minRunners: 0
maxRunners: 1
no cluster-admin ClusterRoleBinding
no Talos os:admin ServiceAccount
no /var/run/secrets/talos.dev mount
```

The GitHub App credentials are stored in 1Password:

```text
vault: kubernetes
item: actions-runner
fields:
  ACTIONS_RUNNER_APP_ID
  ACTIONS_RUNNER_INSTALLATION_ID
  ACTIONS_RUNNER_PRIVATE_KEY
```

Validate controller and runner state:

```sh
flux get ks -A | grep -E "actions-runner|runner"
flux get hr -n actions-runner-system
kubectl -n actions-runner-system get externalsecret
kubectl -n actions-runner-system get pods
kubectl -n actions-runner-system get autoscalingrunnersets,autoscalinglisteners,ephemeralrunnersets
```

Expected baseline:

```text
actions-runner-controller Ready=True
actions-runner-controller-runners Ready=True
home-ops-runner ExternalSecret SecretSynced=True
home-ops-runner HelmRelease Ready=True
controller pod Running
listener pod Running
autoscaling runner set min 0 / max 1
ephemeral runner set current replicas 0
```

A smoke-test workflow exists at:

```text
.github/workflows/test-self-hosted-runner.yaml
```

Run it manually from GitHub Actions to verify the runner can scale up, complete a job, and clean itself up. The expected pod lifecycle is:

```text
Pending -> ContainerCreating -> Running -> Completed -> Terminating -> removed
```

## Observability notes

The observability baseline lives under:

```text
kubernetes/apps/o11y
```

Current baseline components:

```text
blackbox-exporter-lan
grafana-operator
grafana-instance
kube-prometheus-stack
prometheus-adapter
```

Validated internal URLs:

```text
https://grafana.cooney.site
https://prometheus.cooney.site
https://alertmanager.cooney.site
```

Expected live checks:

```sh
flux get ks -A | grep -E "o11y|blackbox|grafana|prometheus|alert"
flux get hr -n o11y
kubectl -n o11y get pods -o wide
kubectl -n o11y get httproute
kubectl -n o11y get servicemonitor,podmonitor,scrapeconfig,probe
```

Expected core pods:

```text
alertmanager-kube-prometheus-stack-0              2/2 Running
blackbox-exporter-lan                             1/1 Running
grafana-deployment                                1/1 Running
grafana-operator                                  1/1 Running
kube-prometheus-stack-operator                    1/1 Running
kube-state-metrics                                1/1 Running
node-exporter                                     3/3 Running
prometheus-adapter                                1/1 Running
prometheus-kube-prometheus-stack-0                2/2 Running
```

Grafana should return `HTTP/2 200` at:

```text
https://grafana.cooney.site
```

Prometheus and Alertmanager may return `HTTP/2 405` to `HEAD` requests. Use GET readiness endpoints instead:

```sh
curl -kL https://prometheus.cooney.site/-/ready
curl -kL https://alertmanager.cooney.site/-/ready
```

Baseline scrape/probe targets:

```text
homebase.cooney.site:9100
storage.cooney.site
storage.cooney.site:2049
```

Prometheus memory was reduced for this base cluster. Current expected values:

```text
requests: cpu=100m, memory=512Mi
limits: memory=1000Mi
```

## SABnzbd notes

SABnzbd is currently validated internally at:

```text
https://sabnzbd.cooney.site
```

Known-good internal behavior:

```text
HTTP 303 -> /sabnzbd/wizard/
```

The external route and external Envoy Gateway are functional when bypassing Cloudflare and targeting the external Gateway VIP directly:

```sh
curl -vk --resolve sabnzbd.cooney.online:443:192.168.60.2 https://sabnzbd.cooney.online
```

Expected direct-to-gateway response:

```text
HTTP 303 -> /sabnzbd/wizard/
```

The Cloudflare-proxied external URL currently reaches SABnzbd, but SABnzbd denies public internet access:

```text
External internet access denied - https://sabnzbd.org/access-denied
```

Do not expose SABnzbd externally without an intentional access-control layer such as Cloudflare Access and reviewed SABnzbd exposure settings.

## Kopia recovery notes

Kopia uses an NFS-backed filesystem repository mounted from:

```text
storage.cooney.site:/home-ops-backups
```

The repository path inside the Kopia UI pod is:

```text
/repository
```

VolSync mover pods use:

```text
/mnt/repository
```

If Kopia fails with:

```text
error getting kopia.repository blob: BLOB not found
```

check that the NFS mount resolves to the internal storage IP and that the repository has been initialized.

Useful checks:

```sh
kubectl -n volsync-system logs deploy/kopia --tail=100
kubectl -n volsync-system get pods
kubectl -n volsync-system get httproute kopia -o yaml
```

The Kopia UI should be available internally at:

```text
https://kopia.cooney.site
```

## Rook/Ceph recovery notes

Verify Rook/Ceph health:

```sh
kubectl -n rook-ceph get cephcluster
kubectl -n rook-ceph get pods
```

The Ceph dashboard should be available internally at:

```text
https://rook.cooney.site
```

Dashboard password:

```sh
kubectl -n rook-ceph get secret rook-ceph-dashboard-password \
  -o jsonpath='{.data.password}' | base64 -d && echo
```

Username is usually:

```text
admin
```

## Known-good tags

Useful restore points may include:

```text
post-internal-dns-tls-routing
post-onepassword-cluster-vars
post-udm-cilium-bgp-routing
post-observability-baseline
post-observability-and-webhook-baseline
post-intel-gpu-and-multus-iot-baseline
post-cert-backup-tuppr-actions-runner-baseline
```

List tags:

```sh
git tag --list
```

Push a new known-good tag:

```sh
git tag -a <tag-name> -m "<description>"
git push origin <tag-name>
```

## What not to do during recovery

- Do not manually create app DNS records unless using a temporary break-glass workaround.
- Do not point `cooney.site` records at the external gateway.
- Do not point `cooney.online` records at the internal gateway.
- Do not expose SABnzbd externally without Cloudflare Access or another intentional authentication layer.
- Do not delete SOPS bootstrap files unless the bootstrap process has been fully moved to 1Password.
- Do not reset Talos nodes unless you are intentionally rebuilding the cluster.
- Do not enable `tuppr-upgrades` unless intentionally performing a controlled Talos/Kubernetes upgrade.
- Do not give the GitHub Actions runner cluster-admin or Talos `os:admin` unless intentionally creating a separate privileged maintenance runner.

## Quick health snapshot

Run this when you think recovery is complete:

```sh
kubectl get nodes -o wide
kubectl get pods -A
flux get sources git -A
flux get ks -A
flux get hr -A
kubectl get certificate -A
kubectl get httproute -A
kubectl get externalsecret -A
kubectl -n o11y get pods -o wide
kubectl -n o11y get servicemonitor,podmonitor,scrapeconfig,probe
kubectl -n kube-system get pods | grep -E "reloader|spegel|intel|multus"
kubectl get resourceslices
kubectl get network-attachment-definitions -A
kubectl -n network get pushsecret
flux get ks -A | grep -E "system-upgrade|tuppr|actions-runner|runner"
kubectl -n system-upgrade get pods
kubectl -n system-upgrade get kubernetesupgrades,talosupgrades
kubectl -n actions-runner-system get pods
kubectl -n actions-runner-system get autoscalingrunnersets,autoscalinglisteners,ephemeralrunnersets
cilium bgp peers
```

On the UDM, also verify:

```sh
vtysh -c "show bgp summary"
vtysh -c "show ip route bgp"
```

Webhook reconciliation check:

```sh
kubectl -n flux-system get receiver github-webhook
kubectl -n flux-system get externalsecret github-webhook-token
kubectl -n flux-system get secret github-webhook-token-secret
```

Talos VLAN 777 check:

```sh
for node in talos01.cooney.site talos02.cooney.site talos03.cooney.site; do
  echo "===== $node ====="
  talosctl -n "$node" get links | grep -E "eno1($|[[:space:]])|eno1\.777"
  talosctl -n "$node" get addresses | grep eno1.777 || true
done
```

Expected: `eno1.777` exists on all three nodes and has no routable IPv4 address.

Multus IoT check:

```sh
flux get ks multus multus-networks -n kube-system
flux get hr multus -n kube-system
kubectl -n kube-system get pods | grep -i multus
kubectl get network-attachment-definitions -A
```

Expected: `multus` and `multus-networks` are Ready, three Multus pods are Running, and `kube-system/iot` exists.

Certificate backup check:

```sh
flux get ks certificates-export -n network
kubectl -n network get pushsecret
```

Expected: `certificates-export` is Ready and both certificate PushSecrets are synced.

System upgrade controller check:

```sh
flux get ks -A | grep -E "system-upgrade|tuppr"
flux get hr -n system-upgrade
kubectl -n system-upgrade get pods
kubectl -n system-upgrade get kubernetesupgrades,talosupgrades
```

Expected: Tuppr is Ready, `tuppr-upgrades` is suspended, and no upgrade resources are applied.

Actions runner check:

```sh
flux get ks -A | grep -E "actions-runner|runner"
flux get hr -n actions-runner-system
kubectl -n actions-runner-system get externalsecret
kubectl -n actions-runner-system get pods
kubectl -n actions-runner-system get autoscalingrunnersets,autoscalinglisteners,ephemeralrunnersets
```

Expected: controller/listener are Running, ExternalSecret is synced, max runners is 1, and current ephemeral runner replicas are 0 when no workflow is queued.
