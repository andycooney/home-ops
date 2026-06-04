# Plex operations

## Current desired state

Plex is managed by `kubernetes/apps/default/plex/app/helmrelease.yaml`.

```text
Canonical URL:      https://plex.cooney.site
Direct LAN URL:     http://192.168.60.40:32400
Cluster DNS URL:    http://plex.default.svc.cluster.local:32400
Namespace:          default
Service:            plex
Transcode path:     /media/transcode
```

Plex advertises the canonical HTTPS URL, the direct LAN endpoint, and the in-cluster service DNS name:

```text
PLEX_ADVERTISE_URL=https://plex.cooney.site:443,http://192.168.60.40:32400,http://plex.default.svc.cluster.local:32400
```

Plex also allows unauthenticated discovery/access only from the trusted internal client subnet:

```text
PLEX_NO_AUTH_NETWORKS=172.16.1.0/24
```

This is intentional. Plex Web and Apple TV were able to authenticate at the API level, but Plex Web initially tested the bundled local server connection without a token and received `406 Not Acceptable`. Allowing no-auth for the Apple TV/client subnet restored Plex Web libraries, server settings, and Apple TV library visibility.

The direct LAN endpoint is a Cilium-advertised LoadBalancer service. Native Plex clients should prefer it for local playback, which avoids the Envoy Gateway path and preserves local direct-play behavior.

## Network configuration

The Plex service is a direct LoadBalancer:

```yaml
service:
  app:
    type: LoadBalancer
    annotations:
      lbipam.cilium.io/ips: 192.168.60.40
    labels:
      bgp.cilium.io/advertise: "true"
    externalTrafficPolicy: Local
```

The `bgp.cilium.io/advertise=true` label is required so Cilium BGP exports the `192.168.60.40/32` route to the UDM. Without it, Cilium can allocate the VIP but the LAN will not route to it.

The HTTPS route remains useful for Plex Web and remote/browser access:

```text
https://plex.cooney.site -> envoy-internal -> Plex
```

Local native clients should be able to use:

```text
http://192.168.60.40:32400
```

The in-cluster DNS URL is also advertised because this mirrors the known-good containerized Plex pattern used as a reference and helps Plex keep a stable internal service identity:

```text
http://plex.default.svc.cluster.local:32400
```

### Discovery model notes

The working model intentionally does **not** use `hostNetwork` and does **not** use `ADVERTISE_IP`.

During troubleshooting, host networking moved Plex off a Cilium pod IP, but it introduced extra node/LAN candidates and did not eliminate stale Kubernetes-local addresses from Plex Online resources. The final working configuration returned Plex to normal pod networking and aligned with the reference pattern:

```text
LoadBalancer VIP + externalTrafficPolicy: Local
PLEX_ADVERTISE_URL with HTTPS hostname, LAN VIP, and cluster DNS URL
PLEX_NO_AUTH_NETWORKS limited to 172.16.1.0/24
```

Known bad/suspicious client candidates seen during troubleshooting included:

```text
10.42.x.x:32400       # Kubernetes pod IP; not routable from LAN clients
108.18.x.x:443        # auto-detected public WAN plex.direct candidate
```

The public WAN candidate was removed by disabling manual port mapping. The pod-IP candidate did not by itself prevent the final working configuration once the advertise URL and no-auth subnet were corrected.

## Validate routing

```sh
kubectl -n default get svc plex -o wide
kubectl -n default get svc plex -o yaml | grep -E "bgp.cilium.io/advertise|lbipam.cilium.io/ips|externalTrafficPolicy" -A2 -B2
curl -i --max-time 10 http://192.168.60.40:32400/identity
curl -Ik https://plex.cooney.site/identity
```

On the UDM, verify the BGP route exists:

```sh
ip route | grep 192.168.60.40
```

Expected route source is BGP, for example:

```text
192.168.60.40 via 192.168.42.12 dev br200 proto bgp metric 20
```

## Plex Preferences.xml expectations

Validate the active settings:

```sh
PLEX_POD="$(kubectl -n default get pod -l app.kubernetes.io/name=plex -o jsonpath='{.items[0].metadata.name}')"

kubectl -n default exec "$PLEX_POD" -- sh -c '
  PREF="/config/Library/Application Support/Plex Media Server/Preferences.xml"
  grep -oE "customConnections=\"[^\"]*\"|allowedNetworks=\"[^\"]*\"|LanNetworksBandwidth=\"[^\"]*\"|TranscoderTempDirectory=\"[^\"]*\"|ManualPortMappingMode=\"[^\"]*\"|ManualPortMappingPort=\"[^\"]*\"" "$PREF" || true
'
```

Expected:

```text
TranscoderTempDirectory="/media/transcode"
customConnections="https://plex.cooney.site:443,http://192.168.60.40:32400,http://plex.default.svc.cluster.local:32400"
allowedNetworks="172.16.1.0/24"
LanNetworksBandwidth="172.16.1.0/24,192.168.42.0/24,192.168.60.0/24"
ManualPortMappingMode="0"
```

`allowedNetworks` is the no-auth bypass list. Keep it limited to the trusted Apple TV/client subnet (`172.16.1.0/24`). Do not broaden it to the Kubernetes pod/service CIDRs.

`LanNetworksBandwidth` is different from `allowedNetworks`: it controls which networks Plex treats as local for bandwidth/streaming decisions and can include the other internal LAN/storage ranges.

## Validate Plex Online resources

Use a fresh Plex Web token from `https://app.plex.tv/desktop/` when checking Plex Online resources. Do not paste token values into chat or docs.

```sh
WEB_TOKEN='paste_fresh_token_here'

curl -fsS "https://plex.tv/api/resources?includeHttps=1&X-Plex-Token=$WEB_TOKEN" |
  xmllint --format - |
  sed -n '/<Device name="AndyPlex"/,/<\/Device>/p'
```

Good candidates include:

```text
192.168.60.40:32400
plex.cooney.site:443
```

A `10.42.x.x:32400` candidate may appear from Kubernetes pod networking. Treat it as suspicious for LAN clients, but do not chase it alone if Plex Web, server settings, and Apple TV libraries are working.

## Media mounts

Plex intentionally mounts media read-only, while the transcode directory is writable:

```text
storage.cooney.site:/media/tvshows    -> /media/tvshows     read-only
storage.cooney.site:/media/movies     -> /media/movies      read-only
storage.cooney.site:/media/transcode  -> /media/transcode   read-write
```

Do not mount all of `/media` read-only if Plex needs `/media/transcode` below it to be writable.

Validate:

```sh
kubectl -n default exec "$PLEX_POD" -- sh -c '
  for d in /media/tvshows /media/movies /media/transcode; do
    echo
    echo "Testing $d"
    TESTFILE="$d/.plex-write-test-$(date +%s)-$$"

    if touch "$TESTFILE" 2>/tmp/plex-write-test.err; then
      echo "touch OK"
      rm -f "$TESTFILE" && echo "cleanup OK" || echo "cleanup FAILED"
    else
      echo "touch FAILED:"
      cat /tmp/plex-write-test.err
    fi
  done
'
```

Expected:

```text
/media/tvshows      touch FAILED
/media/movies       touch FAILED
/media/transcode    touch OK
```

## Playback validation

While a local Apple TV is playing something, query sessions through the direct LAN endpoint:

```sh
PLEX_TOKEN="$(kubectl -n default exec "$PLEX_POD" -- sh -c '
  sed -n "s:.*PlexOnlineToken=\"\([^\"]*\)\".*:\1:p" "/config/Library/Application Support/Plex Media Server/Preferences.xml" | head -1
')"

curl -fsS "http://192.168.60.40:32400/status/sessions?X-Plex-Token=$PLEX_TOKEN" | xmllint --format -
```

Good local playback signs:

```text
Player address="172.16.1.x"
Player local="1"
Session location="lan"
Part decision="directplay"
Stream location="direct"
```

Plex request logs may still label some direct requests as `(WAN)`. Prefer the active session fields above for the playback decision.

## Web and Apple TV visibility troubleshooting

If Plex Web or Apple TV cannot see libraries, first confirm the server and token can access libraries:

```sh
curl -fsS "http://192.168.60.40:32400/library/sections?X-Plex-Token=$WEB_TOKEN" | xmllint --format -
curl -fsS "http://192.168.60.40:32400/media/providers?X-Plex-Token=$WEB_TOKEN" | xmllint --format -
```

If those work but Plex Web shows no libraries/settings, check the browser console for connection bootstrap failures such as:

```text
[Connections] [Bundled] is unauthorized at http://192.168.60.40:32400/media/providers (Status 406)
[Primary Server] Waiting for any eligible server
```

That symptom was fixed by the current `PLEX_ADVERTISE_URL` plus `PLEX_NO_AUTH_NETWORKS=172.16.1.0/24` model.

Avoid these false starts unless new evidence requires them:

```text
Do not reclaim/reset Plex just because Web cannot see libraries if API tokens still work.
Do not switch Plex to hostNetwork for this issue.
Do not add ADVERTISE_IP; it did not resolve the stale resource candidates.
Do not broaden allowedNetworks beyond the trusted client subnet.
```

## TV metadata migration notes

The successful TV repair path was:

```text
1. Normalize Sonarr folders/files with Plex-friendly `{tvdb-...}` identifiers.
2. Move all shows from Personal Media / none-agent to NFO Series.
3. Switch the TV library back to Plex TV Series.
4. Run full metadata refresh and wait for Plex to migrate shows, seasons, and episodes to `plex://...` GUIDs.
```

The final TV target state is:

```text
total_tv_shows             167
plex_series_guid_count     167
nfo_series_guid_count      0
personal_media_guid_count  0
legacy_tvdb_guid_count     0
```

Useful summary query:

```sh
kubectl -n default exec -i "$PLEX_POD" -- sh <<'EOF'
PMS="/config/Library/Application Support/Plex Media Server"
DB="$PMS/Plug-in Support/Databases/com.plexapp.plugins.library.db"
PSQL="/usr/lib/plexmediaserver/Plex SQLite"

"$PSQL" "$DB" <<'SQL'
.mode column
.headers on

WITH shows AS (
  SELECT *
  FROM metadata_items
  WHERE deleted_at IS NULL
    AND metadata_type = 2
)
SELECT 'total_tv_shows' AS metric, COUNT(*) AS count FROM shows
UNION ALL SELECT 'plex_series_guid_count', COUNT(*) FROM shows WHERE guid LIKE 'plex://show/%'
UNION ALL SELECT 'nfo_series_guid_count', COUNT(*) FROM shows WHERE guid LIKE 'tv.plex.agents.nfo.series://%'
UNION ALL SELECT 'personal_media_guid_count', COUNT(*) FROM shows WHERE guid LIKE 'tv.plex.agents.none://%'
UNION ALL SELECT 'legacy_tvdb_guid_count', COUNT(*) FROM shows WHERE guid LIKE 'com.plexapp.agents.thetvdb://%'
UNION ALL SELECT 'blank_guid_count', COUNT(*) FROM shows WHERE guid IS NULL OR guid = ''
UNION ALL SELECT 'other_guid_count', COUNT(*) FROM shows
  WHERE guid IS NOT NULL
    AND guid != ''
    AND guid NOT LIKE 'plex://show/%'
    AND guid NOT LIKE 'tv.plex.agents.nfo.series://%'
    AND guid NOT LIKE 'tv.plex.agents.none://%'
    AND guid NOT LIKE 'com.plexapp.agents.thetvdb://%';
SQL
EOF
```

Check duplicate active shows:

```sh
kubectl -n default exec -i "$PLEX_POD" -- sh <<'EOF'
PMS="/config/Library/Application Support/Plex Media Server"
DB="$PMS/Plug-in Support/Databases/com.plexapp.plugins.library.db"
PSQL="/usr/lib/plexmediaserver/Plex SQLite"

"$PSQL" "$DB" <<'SQL'
.mode column
.headers on

SELECT
  title,
  COUNT(*) AS active_rows
FROM metadata_items
WHERE deleted_at IS NULL
  AND metadata_type = 2
GROUP BY title
HAVING COUNT(*) > 1
ORDER BY active_rows DESC, title;
SQL
EOF
```

Expected: no rows.

## False-deleted metadata repair notes

After a restore/migration, some Plex metadata rows were incorrectly marked deleted while the files still existed. Repair notes:

```text
Use Plex's bundled SQLite binary:
  /usr/lib/plexmediaserver/Plex SQLite

Stop Plex before modifying the database.
Back up the database before changing rows.
Do not use generic sqlite3 for Plex DB repair.
Run helper pods as UID/GID 1000.
Avoid adding fsGroup to the Plex PVC unless necessary; it can trigger a large recursive permission walk.
```

## Artwork notes

Plex imports/caches local artwork under the Plex config directory and serves cached `metadata://...` or `upload://...` references to clients. Replacing a bad local file does not always update the selected cached artwork pointer automatically.

Known cleanup pattern:

```text
1. Replace or delete the bad source artwork file.
2. Refresh metadata or manually select the replacement poster in Plex.
3. Verify no active DB rows reference the old bad hash.
```

Zero-byte local poster files can create zero-byte cached posters and `UltraBlurProcessor` errors.

## Useful log tails

Playback and Apple TV checks:

```sh
kubectl -n default exec "$PLEX_POD" -- sh -c '
  PMS="/config/Library/Application Support/Plex Media Server"
  tail -F "$PMS/Logs/Plex Media Server.log" "$PMS/Logs/Plex Transcoder Statistics.log" 2>/dev/null
' | grep -Ei '172\.16\.|192\.168\.42\.|192\.168\.60\.40|10\.42\.|WAN|LAN|direct play|direct stream|transcod|Apple|tvOS|library/sections|401|403'
```

Metadata migration checks:

```sh
kubectl -n default exec "$PLEX_POD" -- sh -c '
  PMS="/config/Library/Application Support/Plex Media Server"
  tail -F "$PMS/Logs/Plex Media Server.log" "$PMS/Logs/Plex Media Scanner.log" "$PMS/Logs/Plex Match.log" 2>/dev/null
' | grep -Ei 'match|metadata|refresh|agents\.series|agents\.nfo|plex://show|tvdb|nfo|poster|thumb|error|warn|failed'
```
