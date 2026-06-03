# SABnzbd PIA WireGuard notes

## Current state

SABnzbd runs behind Gluetun with custom PIA WireGuard configuration.

The preferred tested endpoint was PIA Washington DC. New York and Washington DC both worked after regenerating fresh WireGuard configs.

Bad or stale configs produced symptoms such as:

```text
public_ip empty
tun0 RX 0
DNS lookup timeouts
ping failures through the tunnel
```

A healthy tunnel has a populated public IP and non-zero tunnel counters.

## 1Password fields

The existing `pia` item in the `kubernetes` vault stores WireGuard configs:

```text
WG0_CONF_DC
WG0_CONF_NY
```

## Kubernetes Secrets

ExternalSecrets produce these Kubernetes Secrets:

```text
sabnzbd-pia-wg-dc-secret
sabnzbd-pia-wg-ny-secret
```

Each contains:

```text
wg0.conf
```

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

## Follow-up

Regenerate the final DC WireGuard config once more because private key material was printed during testing.

After regenerating and updating 1Password:

```sh
kubectl -n default annotate externalsecret sabnzbd-pia-wg-dc force-sync="$(date +%s)" --overwrite
kubectl -n default rollout restart deploy/sabnzbd
kubectl -n default rollout status deploy/sabnzbd --timeout=3m
```
