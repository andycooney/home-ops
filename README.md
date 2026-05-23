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

## Important URLs

Internal-only:

- `https://kopia.cooney.site`
- `https://rook.cooney.site`
- `https://sabnzbd.cooney.site`

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
kubernetes/apps/flux-system/flux-instance/app/secret.sops.yaml
kubernetes/apps/network/cloudflare-tunnel/app/secret.sops.yaml
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

### 2. Bootstrap Talos if needed

Only do this when rebuilding from bare nodes or after a full reset.

```sh
just bootstrap talos
```

If applying updated Talos config to existing nodes instead of rebuilding:

```sh
just talos generate-config
just talos apply-node <node-ip>
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
echo                  echo.cooney.online
github-webhook        flux-webhook.cooney.online
rook-ceph-dashboard   rook.cooney.site
kopia                 kopia.cooney.site
sabnzbd-internal      sabnzbd.cooney.site
sabnzbd-external      sabnzbd.cooney.online
```

### DNS

Internal DNS:

```sh
dig internal.cooney.site +short
dig kopia.cooney.site +short
dig rook.cooney.site +short
dig sabnzbd.cooney.site +short
```

Expected:

```text
internal.cooney.site -> 192.168.60.1
kopia.cooney.site -> internal.cooney.site
rook.cooney.site -> internal.cooney.site
sabnzbd.cooney.site -> internal.cooney.site
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

The repository path inside the pod is:

```text
/repository
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

## Quick health snapshot

Run this when you think recovery is complete:

```sh
kubectl get nodes -o wide
kubectl get pods -A
flux get ks -A
flux get hr -A
kubectl get certificate -A
kubectl get httproute -A
kubectl get externalsecret -A
cilium bgp peers
```

On the UDM, also verify:

```sh
vtysh -c "show bgp summary"
vtysh -c "show ip route bgp"
```
