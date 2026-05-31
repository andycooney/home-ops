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
| Whisparr | Adult media automation | `https://whisparr.cooney.site` |
| SABnzbd | Usenet downloader | `https://sabnzbd.cooney.site` |
| qBittorrent | Torrent downloader | `https://qbittorrent.cooney.site` |
| Qui | qBittorrent management UI | `https://qui.cooney.site` |
| Stash | Adult media library | `https://stash.cooney.site` |

## External Seerr aliases

```text
https://seerr.cooney.online
https://media.cooney.online
https://requests.cooney.online
```

External routes should remain protected by Cloudflare Access unless a specific bypass is documented.

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

Seerr now has the following hostnames:

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

Validate after merge:

```sh
curl -Ik -X GET https://ha.cooney.site
```

## Plex PVC cleanup

The Plex PVC should be checked for bloat after the route/app changes are merged.

Recommended inventory commands:

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
