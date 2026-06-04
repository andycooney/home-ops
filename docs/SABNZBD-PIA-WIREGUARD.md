# SABnzbd PIA WireGuard notes

## Current state

SABnzbd runs behind Gluetun with custom PIA WireGuard configuration.

The active preferred endpoint is PIA Virginia / Ashburn. Washington DC and New York are retained as fallback configs.

Bad or stale configs produced symptoms such as:

```text
public_ip empty
tun0 RX 0
DNS lookup timeouts
ping failures through the tunnel
```

A healthy tunnel has a populated public IP and non-zero tunnel counters.

## 1Password fields

The existing `pia` item in the `kubernetes` vault stores WireGuard configs as concealed fields:

```text
WG0_CONF_VA
WG0_CONF_DC
WG0_CONF_NY
```

Virginia is the active SABnzbd config. DC and NY are fallback configs.

If a WireGuard config is printed to a terminal or otherwise exposed, regenerate that config and replace the matching concealed field in 1Password.

## Kubernetes Secrets

ExternalSecrets produce these Kubernetes Secrets:

```text
sabnzbd-pia-wg-va-secret
sabnzbd-pia-wg-dc-secret
sabnzbd-pia-wg-ny-secret
```

Each contains:

```text
wg0.conf
```

SABnzbd currently mounts the Virginia secret.

## Kill switch model

SABnzbd runs with Gluetun as a sidecar. The app container shares the pod network namespace with Gluetun, and Gluetun's firewall is the primary leak-prevention mechanism.

If the WireGuard tunnel is unhealthy, app egress should fail instead of falling back to the normal WAN path.

A separate Kubernetes `vpn-guard` or pause controller is deferred unless Gluetun recovery proves insufficient or the app behaves poorly while network calls are blocked.

## Validation commands

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

Check logs:

```sh
kubectl -n default logs deploy/sabnzbd -c gluetun --tail=200
kubectl -n default logs deploy/sabnzbd -c app --tail=200
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

## Speed finding

SABnzbd speed improved significantly after the FiOS ONT/NID uplink was fixed from 100 Mb Fast Ethernet negotiation to 1 Gb negotiation.

Observed post-fix speeds were roughly:

```text
35-55 MB/s
```

Virginia / Ashburn appeared faster than Washington DC during later testing and is now the active endpoint.

## Rotation notes

DC, NY, and VA WireGuard configs were regenerated and stored as concealed fields in 1Password after earlier private key material was printed during testing.

After regenerating and updating 1Password, force-sync the matching ExternalSecret and restart SABnzbd. For the active Virginia config:

```sh
kubectl -n default annotate externalsecret sabnzbd-pia-wg-va force-sync="$(date +%s)" --overwrite
kubectl -n default rollout restart deploy/sabnzbd
kubectl -n default rollout status deploy/sabnzbd --timeout=3m
```
