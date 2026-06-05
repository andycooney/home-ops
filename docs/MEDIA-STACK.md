# Media stack

The media stack is managed from `kubernetes/apps/default`.

## Current apps

| App | Purpose | Internal URL |
| --- | --- | --- |
| Plex | Media server | `https://plex.cooney.site` |
| Seerr | Media request portal | `https://seerr.cooney.site`, `https://media.cooney.site`, `https://requests.cooney.site` |
| Tautulli | Plex monitoring/statistics | `https://tautulli.cooney.site` |
| Prowlarr | Indexer management | `https://prowlarr.cooney.site` |
| Radarr | Movie automation | `https://radarr.cooney.site` |
| Sonarr | TV automation | `https://sonarr.cooney.site` |
| Bazarr | Subtitle automation | `https://bazarr.cooney.site` |
| SABnzbd | Usenet downloader | `https://sabnzbd.cooney.site` |
| qBittorrent | Torrent downloader | `https://qbittorrent.cooney.site` |
| Qui | qBittorrent management UI | `https://qui.cooney.site` |

## External Seerr aliases

```text
https://seerr.cooney.online
https://media.cooney.online
https://requests.cooney.online
```

External routes should remain protected by Cloudflare Access unless a specific bypass is documented.

## Plex

Plex has two intentional access paths:

```text
https://plex.cooney.site          # canonical HTTPS URL through Envoy Gateway
http://192.168.60.40:32400       # direct LAN LoadBalancer for native Plex clients
```

The direct LAN endpoint is advertised to Plex clients so Apple TV and other local clients can avoid the HTTP gateway/proxy path for discovery and playback. Details are in [`PLEX-OPERATIONS.md`](PLEX-OPERATIONS.md).

In-cluster service URL:

```text
http://plex.default.svc.cluster.local:32400
```

## Sonarr and Radarr

Sonarr and Radarr own media renames. Recyclarr should not manage media naming. Details are in [`SONARR-OPERATIONS.md`](SONARR-OPERATIONS.md).

In-cluster service URLs:

```text
http://sonarr.default.svc.cluster.local
http://radarr.default.svc.cluster.local
```

When another app asks for host and port separately:

```text
Host: sonarr.default.svc.cluster.local
Port: 80
SSL: off
Base URL: blank

Host: radarr.default.svc.cluster.local
Port: 80
SSL: off
Base URL: blank
```

## Bazarr

Bazarr is deployed for subtitle automation.

```text
namespace: default
url: https://bazarr.cooney.site
pvc: bazarr
storage class: ceph-block
size: 2Gi
backup: VolSync/Kopia enabled
```

Bazarr intentionally mounts only the library paths it needs:

```text
/media/movies
/media/tvshows
```

It does not mount the full `/media` tree and does not mount `/unprocessed`.

Validation:

```sh
kubectl -n default rollout status deploy/bazarr --timeout=5m
kubectl -n default get pod,pvc,svc,httproute -l app.kubernetes.io/name=bazarr
curl -Ik https://bazarr.cooney.site/api/system/ping
```

Expected:

```text
HTTP/2 200
```

UI settings:

```text
Address: *
Port: 6767
Base URL: /
Instance Name: Bazarr
Hostname: bazarr.cooney.site
Authentication: No Authentication
CORS: disabled
```

Radarr integration:

```text
Address/Host: radarr.default.svc.cluster.local
Port: 80
SSL: off
Base URL: blank
API Key: from Radarr
```

Sonarr integration:

```text
Address/Host: sonarr.default.svc.cluster.local
Port: 80
SSL: off
Base URL: blank
API Key: from Sonarr
```

Path mappings should normally be unnecessary because Bazarr sees the same library paths as Radarr/Sonarr. If needed, use explicit same-to-same mappings:

```text
Movies:
Radarr path: /media/movies
Bazarr path: /media/movies

TV:
Sonarr path: /media/tvshows
Bazarr path: /media/tvshows
```

Providers should be enabled conservatively. Start with English-only subtitles and a small set of providers such as OpenSubtitles.com, Podnapisi, and Addic7ed for TV.

Notifications are intentionally disabled in Bazarr for now. Alertmanager should remain the central notification aggregator. Bazarr does not currently provide a clean native Alertmanager notification target. Gatus/Prometheus/Alertmanager should cover Bazarr health instead of Bazarr sending event notifications directly.

## SABnzbd

SABnzbd runs behind Gluetun with PIA WireGuard configuration stored in 1Password-backed secrets. Details are in [`SABNZBD-PIA-WIREGUARD.md`](SABNZBD-PIA-WIREGUARD.md).

In-cluster service URL:

```text
http://sabnzbd.default.svc.cluster.local
```

When another app asks for host and port separately:

```text
Host: sabnzbd.default.svc.cluster.local
Port: 80
SSL: off
Base URL: blank
```

## qBittorrent

qBittorrent runs with Gluetun as the VPN sidecar and a `port-sync` sidecar that reads the PIA forwarded port from Gluetun and updates qBittorrent's listening port.

```text
namespace: default
url: https://qbittorrent.cooney.site
pvc: qbittorrent
storage class: ceph-block
vpn: Gluetun + PIA WireGuard
forwarded port file: /tmp/gluetun/forwarded_port
port sync script: kubernetes/apps/default/qbittorrent/app/resources/port-sync.sh
```

The `port-sync` script is stored outside the HelmRelease and mounted from a generated ConfigMap:

```text
kubernetes/apps/default/qbittorrent/app/resources/port-sync.sh
```

The generated script ConfigMap must disable Flux/Kustomize substitution, otherwise shell variables such as `${PORT_FILE}` are rendered as empty strings:

```yaml
generatorOptions:
  disableNameSuffixHash: true
  annotations:
    kustomize.toolkit.fluxcd.io/substitute: disabled
```

The current resource requests are intentionally low because the cluster is request-saturated:

```text
app:       requests 25m / 256Mi, limits 2Gi
gluetun:   requests 5m / 64Mi,   limits 512Mi
port-sync: requests 1m / 32Mi,   limits 128Mi
```

The HelmRelease timeout is set to `15m` to avoid rollback churn while the pod waits for scheduling or RWO PVC attachment.

Validate qBittorrent:

```sh
kubectl -n default get pod -l app.kubernetes.io/name=qbittorrent
kubectl -n default rollout status deploy/qbittorrent --timeout=15m
kubectl -n default logs deploy/qbittorrent -c port-sync --tail=80
kubectl -n default logs deploy/qbittorrent -c gluetun --tail=120
```

Expected `port-sync` startup lines:

```text
Starting qBittorrent forwarded-port sync
Port file: /tmp/gluetun/forwarded_port
qBittorrent URL: http://127.0.0.1:80
```

Expected after Gluetun obtains a forwarded port:

```text
Updating qBittorrent listening port from <old_port> to <forwarded_port>
```

Check the current forwarded port:

```sh
kubectl -n default exec deploy/qbittorrent -c gluetun -- cat /tmp/gluetun/forwarded_port
```

If a rollout gets stuck with an old pod holding the RWO PVC, suspend Helm and cleanly scale down before retrying:

```sh
flux suspend hr qbittorrent -n default
kubectl -n default scale deploy/qbittorrent --replicas=0
kubectl -n default delete pod -l app.kubernetes.io/name=qbittorrent --force --grace-period=0
kubectl -n default describe pvc qbittorrent | grep -A5 "Used By"
```

Expected safe paused state:

```text
No qBittorrent pods
PVC qbittorrent Bound
Used By: <none>
```

Bring it back up:

```sh
flux resume hr qbittorrent -n default
flux reconcile source git flux-system -n flux-system
flux reconcile hr qbittorrent -n default --force --timeout=15m
```

Known cleanup item: issue #90 tracks replacing deprecated Gluetun DNS env vars (`DOT` and `DOT_IPV6`).

## Tautulli

Tautulli was imported from `onedr0p/home-ops` and adapted to this cluster.

```text
namespace: default
url: https://tautulli.cooney.site
pvc: tautulli
storage class: ceph-block
size: 5Gi
```

The app is internal-only. Initial configuration should be completed from the UI by connecting it to Plex.

After the PR is merged, bootstrap the VolSync/Kopia item before relying on backups:

```sh
scripts/volsync-app-bootstrap.sh tautulli
```

Then reconcile:

```sh
flux reconcile source git flux-system -n flux-system
flux reconcile kustomization tautulli -n default --with-source
kubectl -n default get pod,svc,pvc,httproute | grep tautulli
```

## Seerr aliases

Seerr has these hostnames:

```text
seerr.cooney.site
media.cooney.site
requests.cooney.site
seerr.cooney.online
media.cooney.online
requests.cooney.online
```

## Home Assistant alias

Home Assistant also has:

```text
https://ha.cooney.site
```

Validate:

```sh
curl -Ik -X GET https://ha.cooney.site
```

## Plex PVC cleanup

The Plex PVC should be checked for bloat periodically.

```sh
PLEX_POD="$(kubectl -n default get pod -l app.kubernetes.io/name=plex -o jsonpath='{.items[0].metadata.name}')"

kubectl -n default exec "$PLEX_POD" -- sh -c '
  echo "Top-level config usage:"
  du -xh -d 1 /config 2>/dev/null | sort -h

  echo
  echo "Plex Library usage:"
  du -xh -d 2 "/config/Library/Application Support/Plex Media Server" 2>/dev/null | sort -h | tail -50
'
```

Common bloat candidates to review before deleting:

```text
/config/Library/Application Support/Plex Media Server/Cache
/config/Library/Application Support/Plex Media Server/Crash Reports
/config/Library/Application Support/Plex Media Server/Logs
```

Do not blindly delete `Metadata`, `Media/localhost`, or `Plug-in Support/Databases`; those can be large but are core Plex library state.
