# Networking and external access

## Domain model

```text
cooney.site   = internal only
cooney.online = external only
```

Internal records generally point at:

```text
internal.cooney.site
```

External records generally point at:

```text
external.cooney.online
```

Gateway records:

```text
internal.cooney.site -> 192.168.60.1
external.cooney.online -> Cloudflare Tunnel / external gateway target
```

## Gateways

Expected Gateway services:

```text
envoy-internal -> 192.168.60.1
envoy-external -> 192.168.60.2
```

Validate:

```sh
kubectl -n network get svc envoy-internal envoy-external -o wide
kubectl -n network get gateway envoy-internal envoy-external -o wide
```

TLS expectations:

```text
envoy-internal -> cooney-site-production-tls
envoy-external -> cooney-online-production-tls
```

Validate:

```sh
kubectl -n network get gateway envoy-internal envoy-external -o yaml \
  | grep -E "internal\.cooney\.site|external\.cooney\.online|cooney-site-production-tls|cooney-online-production-tls" -B2 -A2
```

## BGP / UDM routing

Cilium advertises Envoy Gateway LoadBalancer VIPs to the UDM using BGP.

Validate Cilium:

```sh
cilium bgp peers
```

On the UDM:

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

If BGP peers are established but no prefixes are received, check the UDM FRR inbound route-map allows:

```text
192.168.60.0/24 le 32
```

Useful file:

```text
kubernetes/apps/kube-system/cilium/app/udm-frr-bgp.conf
```

## Thunderbolt Ceph backend network

The Ceph storage backend is routed Layer 3 over the Thunderbolt ring. It is not bridged.

Management network:

```text
talos01 -> 192.168.42.11
talos02 -> 192.168.42.12
talos03 -> 192.168.42.13
```

Ceph backend identities:

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

Current routing is static. Future dynamic routing with FRR is tracked in issue `#191`.

Do not reintroduce a Linux bridge over Thunderbolt for Ceph. The previous bridge design caused severe TCP degradation for transit traffic even though direct Thunderbolt links were fast.

Runbook:

```text
docs/runbooks/ceph-thunderbolt-backend.md
```

## HTTPRoutes

List routes:

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

Route acceptance:

```sh
kubectl get httproute -A -o json | jq -r '
  .items[]
  | .metadata.namespace as $ns
  | .metadata.name as $name
  | .status.parents[]?
  | [$ns, $name, .parentRef.name, (.conditions[]? | select(.type=="Accepted") | .status)]
  | @tsv
'
```

Expected:

```text
Accepted=True
```

## DNS validation

Internal:

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
apps -> internal.cooney.site
```

External:

```sh
dig external.cooney.online +short
dig echo.cooney.online +short
dig flux-webhook.cooney.online +short
dig sabnzbd.cooney.online +short
```

Expected:

```text
apps -> external.cooney.online
```

## Cloudflare Tunnel

Current tunnel:

```text
name: kubernetes
id: e278fcc0-5e2d-4a62-9682-62d9cde718e7
```

Validate:

```sh
kubectl -n network get pods,deploy,svc,cm,secret,externalsecret | grep -iE "cloudflare|cloudflared|tunnel"
kubectl -n network logs deploy/cloudflare-tunnel --tail=120
cloudflared tunnel list
```

Short QUIC reconnects or DNS timeout messages can occur transiently. Investigate only if current tunnel connections are unstable.

## Cloudflare Access

All normal external apps under:

```text
*.cooney.online
```

require Cloudflare Access.

Current Access applications:

```text
protected-external-apps
  Destination: *.cooney.online
  Authentication: Google
  Policy: allow-andy

flux-webhook
  Destination: flux-webhook.cooney.online/<exact Flux receiver path>
  Policy: bypass-flux-webhook
```

The Flux webhook bypass must be exact-path scoped.

Get the current webhook path:

```sh
WEBHOOK_PATH="$(kubectl -n flux-system get receiver github-webhook -o jsonpath='{.status.webhookPath}')"
echo "https://flux-webhook.cooney.online${WEBHOOK_PATH}"
```

Validate Access behavior:

```sh
curl -I https://echo.cooney.online
curl -I "https://flux-webhook.cooney.online${WEBHOOK_PATH}"
curl -I https://flux-webhook.cooney.online
```

Expected:

```text
echo.cooney.online -> Cloudflare Access 302
flux-webhook.cooney.online/<exact hook path> -> no Access redirect
flux-webhook.cooney.online/ -> Cloudflare Access 302
```

A `400` from the exact webhook path is acceptable for manual curl testing.

Do not publicly paste Cloudflare Access redirect URLs, cookies, JWTs, `CF_Authorization` headers, or authenticated echo output.
