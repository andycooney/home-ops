# Media downloader PIA WireGuard notes

## Current state

SABnzbd and qBittorrent run behind Gluetun sidecars with custom PIA WireGuard configuration.

Current active endpoints:

```text
SABnzbd:      PIA Virginia / Ashburn
qBittorrent:  PIA CA Ontario / Toronto, with PIA port forwarding enabled
```

Bad or stale configs produced symptoms such as:

```text
public_ip empty
tun0 RX 0
DNS lookup timeouts
ping failures through the tunnel
```

A healthy tunnel has a populated public IP and non-zero tunnel counters.

## 1Password fields

The existing `pia` item in the `kubernetes` vault stores WireGuard configs as concealed fields.

SABnzbd fields:

```text
WG0_CONF_VA
WG0_CONF_DC
WG0_CONF_NY
```

qBittorrent fields:

```text
QBITTORRENT_WG0_CONF_CA_ONTARIO
```

Virginia is the active SABnzbd config. DC and NY are fallback configs. qBittorrent uses a separate WireGuard config because it has different operational requirements, including PIA port forwarding.

If a WireGuard config is printed to a terminal or otherwise exposed, regenerate that config and replace the matching concealed field in 1Password.

## Kubernetes Secrets

ExternalSecrets produce these WireGuard config Secrets:

```text
sabnzbd-pia-wg-va-secret
sabnzbd-pia-wg-dc-secret
sabnzbd-pia-wg-ny-secret
qbittorrent-pia-wg-ca-ontario-secret
```

Each WireGuard Secret contains:

```text
wg0.conf
```

qBittorrent also has a VPN credential Secret:

```text
qbittorrent-vpn-secret
```

That Secret provides PIA username/password fields used by Gluetun for the PIA port-forwarding API flow.

## Kill switch model

Download apps run with Gluetun as a sidecar. The app container shares the pod network namespace with Gluetun, and Gluetun's firewall is the primary leak-prevention mechanism.

If the WireGuard tunnel is unhealthy, app egress should fail instead of falling back to the normal WAN path.

A separate Kubernetes `vpn-guard` or pause controller is deferred unless Gluetun recovery proves insufficient or an app behaves poorly while network calls are blocked.

## qBittorrent port forwarding

qBittorrent uses PIA CA Ontario because that endpoint supports PIA port forwarding and was a low-latency option during testing.

Gluetun writes the current forwarded port to:

```text
/tmp/gluetun/forwarded_port
```

The forwarded port is dynamic. It can change when Gluetun restarts, reconnects, changes servers, or refreshes the port-forward lease.

The qBittorrent pod includes a `port-sync` sidecar. It shares `/tmp/gluetun` with Gluetun, watches the forwarded port file, and updates qBittorrent through the local Web API:

```text
http://127.0.0.1:80/api/v2/app/setPreferences
```

The sidecar sets:

```json
{"listen_port": <forwarded_port>, "random_port": false}
```

This keeps qBittorrent's listening port aligned with the active PIA forwarded port.

### Port forwarding route gotcha

Do not use a broad `10.0.0.0/8` in qBittorrent's `FIREWALL_OUTBOUND_SUBNETS`.

PIA's port-forwarding gateway can live at an address such as:

```text
10.11.128.1:19999
```

If `10.0.0.0/8` is listed as an allowed outbound subnet, Gluetun can treat that PIA gateway as a local/outbound subnet instead of routing it through the VPN. The observed symptom was Gluetun timing out while calling the PIA port-forwarding API.

Use narrower cluster/service exceptions instead:

```text
10.42.0.0/16,10.43.0.0/16,192.168.0.0/16,172.16.0.0/12
```

## Validation commands

### SABnzbd

Check Gluetun public IP:

```sh
kubectl -n default exec deploy/sabnzbd -c gluetun -- \
  wget -qO- http://127.0.0.1:8000/v1/publicip/ip || true
```

Check tunnel counters:

```sh
kubectl -n default exec deploy/sabnzbd -c gluetun -- \
  sh -c 'ip -s link show tun0 || true'
```

Check app egress:

```sh
kubectl -n default exec deploy/sabnzbd -c app -- \
  curl -fsS https://ipinfo.io/ip || true
```

Expected active region:

```text
Virginia / Ashburn
```

### qBittorrent

Check Gluetun public IP:

```sh
kubectl -n default exec deploy/qbittorrent -c gluetun -- \
  wget -qO- http://127.0.0.1:8000/v1/publicip/ip || true
```

Check app egress:

```sh
kubectl -n default exec deploy/qbittorrent -c app -- \
  curl -fsS https://ipinfo.io/json && echo
```

Check the active forwarded port:

```sh
kubectl -n default exec deploy/qbittorrent -c gluetun -- \
  cat /tmp/gluetun/forwarded_port
```

Check qBittorrent's configured listening port:

```sh
kubectl -n default exec deploy/qbittorrent -c app -- \
  wget -qO- http://127.0.0.1:80/api/v2/app/preferences |
  jq '{listen_port, random_port}'
```

The forwarded port and qBittorrent `listen_port` should match, and `random_port` should be `false`.

Check port-sync sidecar logs:

```sh
kubectl -n default logs deploy/qbittorrent -c port-sync --tail=80
```

## Troubleshooting notes

WireGuard setup completing in Gluetun only means the interface and config were applied. It does not prove traffic is flowing.

Bad tunnel signs:

```text
public_ip empty
DNS lookup timeouts
ping 1.1.1.1 fails
tun0 RX 0
```

If those symptoms appear, regenerate the PIA WireGuard config and update the matching 1Password field.

For qBittorrent port forwarding, useful log filters are:

```sh
kubectl -n default logs deploy/qbittorrent -c gluetun --tail=250 |
  grep -Ei 'port forward|forwarded|signature|getSignature|error|warn|wireguard|server names'
```

If Gluetun obtains a forwarded port but qBittorrent does not update, check:

```sh
kubectl -n default logs deploy/qbittorrent -c port-sync --tail=80
kubectl -n default exec deploy/qbittorrent -c gluetun -- cat /tmp/gluetun/forwarded_port
kubectl -n default exec deploy/qbittorrent -c app -- \
  wget -qO- http://127.0.0.1:80/api/v2/app/preferences |
  jq '{listen_port, random_port}'
```

## Speed finding

SABnzbd speed improved significantly after the FiOS ONT/NID uplink was fixed from 100 Mb Fast Ethernet negotiation to 1 Gb negotiation.

Observed post-fix speeds were roughly:

```text
35-55 MB/s
```

Virginia / Ashburn appeared faster than Washington DC during later SABnzbd testing and is now the active SABnzbd endpoint.

## Rotation notes

SABnzbd DC, NY, and VA WireGuard configs were regenerated and stored as concealed fields in 1Password after earlier private key material was printed during testing.

qBittorrent uses its own WireGuard config and should not reuse SABnzbd's configs.

After regenerating and updating 1Password, force-sync the matching ExternalSecret and restart the affected app.

For the active SABnzbd Virginia config:

```sh
kubectl -n default annotate externalsecret sabnzbd-pia-wg-va force-sync="$(date +%s)" --overwrite
kubectl -n default rollout restart deploy/sabnzbd
kubectl -n default rollout status deploy/sabnzbd --timeout=3m
```

For the active qBittorrent CA Ontario config:

```sh
kubectl -n default annotate externalsecret qbittorrent-pia-wg-ca-ontario force-sync="$(date +%s)" --overwrite
kubectl -n default rollout restart deploy/qbittorrent
kubectl -n default rollout status deploy/qbittorrent --timeout=3m
```
