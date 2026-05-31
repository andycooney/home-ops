# Home automation stack

The Home Assistant stack is managed primarily from `kubernetes/apps/default`, with PostgreSQL managed from the dedicated `database` namespace.

Current stack components:

| App | Purpose | Internal URL |
| --- | --- | --- |
| Home Assistant | Main home automation controller | `https://home-assistant.cooney.site`, `https://hass.cooney.site` |
| PostgreSQL | Home Assistant recorder database | cluster-internal only, `postgres.database.svc.cluster.local:5432` |
| Mosquitto | Internal MQTT broker | cluster-internal only |
| Zigbee2MQTT | Zigbee coordinator bridge | `https://zigbee.cooney.site` |
| Z-Wave JS UI | Z-Wave controller UI and websocket server | `https://zwave.cooney.site` |

## Current rollout state

The stack has been migrated and started.

Current expected state:

```text
mosquitto:       running
zigbee:          running
zwave:           running
postgres:        running in namespace database
home-assistant:  running
```

Check live state:

```sh
kubectl -n default get ks home-assistant mosquitto zigbee zwave
kubectl -n database get ks postgres
kubectl -n default get pods | grep -E 'home-assistant|mosquitto|zigbee|zwave'
kubectl -n database get pods | grep postgres
```

The stack was originally merged suspended for safe appdata migration, but the active services should no longer have `spec.suspend: true` in their Flux Kustomizations.

## Persistent data migration

Historical appdata was copied from:

| Source | Destination PVC / path |
| --- | --- |
| `/Volumes/appdata/homeassistant` | `home-assistant` mounted at `/config` |
| `/Volumes/appdata/mosquitto` | `config-mosquitto-0` mounted at `/config` |
| `/Volumes/appdata/zigbee2mqtt` | `zigbee` mounted at `/config` |
| `/Volumes/appdata/zwavejs2mqtt` | `zwave` mounted at `/usr/src/app/store` |
| `/Volumes/appdata/postgresql/15/data` | `postgres` in namespace `database`, mounted at `/var/lib/postgresql/data` |

The temporary copy pods used for migration were deleted after the copy finished.

Useful verification commands:

```sh
kubectl -n default get pvc home-assistant home-assistant-cache config-mosquitto-0 zigbee zwave
kubectl -n database get pvc postgres
kubectl -A get pods | grep -E 'config-copy|data-copy|hacs-install|inspect' || true
```

## Startup order

For a clean restart or rebuild, bring dependencies up before Home Assistant:

1. PostgreSQL
2. Mosquitto
3. Zigbee2MQTT
4. Z-Wave JS UI
5. Home Assistant

Mosquitto should come up before Zigbee2MQTT because Zigbee2MQTT publishes to it. Home Assistant should come up last so the recorder database, MQTT discovery, and controller integrations are already available.

## PostgreSQL recorder database

PostgreSQL is managed from:

```text
kubernetes/apps/database/postgres
```

Runtime namespace:

```text
database
```

Internal service name:

```text
postgres.database.svc.cluster.local
```

The migrated database was PostgreSQL 15, so the app starts with the PostgreSQL 15 image. Do not point a newer major PostgreSQL image directly at the copied raw data directory. Upgrade later with a planned `pg_upgrade` or dump/restore.

Expected 1Password item:

```text
vault: kubernetes
item: postgres
fields:
  KOPIA_REPOSITORY
  KOPIA_PASSWORD
  HASS_DB
  HASS_USER
  HASS_USER_PASSWORD
```

Home Assistant does not store the recorder URL statically in `secrets.yaml`. The `home-assistant` ExternalSecret templates it dynamically from the `postgres` item:

```text
HASS_RECORDER_DB_URL=postgresql://<HASS_USER>:<HASS_USER_PASSWORD>@postgres.database.svc.cluster.local:5432/<HASS_DB>
```

Home Assistant config uses:

```yaml
recorder:
  db_url: !env_var HASS_RECORDER_DB_URL
```

Verify the rendered secret without printing the password:

```sh
kubectl -n default get secret home-assistant-secret -o jsonpath='{.data.HASS_RECORDER_DB_URL}' \
  | base64 -d \
  | sed 's#://[^:]*:[^@]*@#://<user>:<password>@#'
echo
```

Useful PostgreSQL checks:

```sh
kubectl -n database logs pod/postgres-0 --tail=100

kubectl -n database exec -it postgres-0 -- sh -c '
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "\l"
  psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "\du"
'
```

The migrated recorder database was pruned from Home Assistant and compacted with `VACUUM FULL ANALYZE` while Home Assistant was stopped.

To check database size:

```sh
kubectl -n database exec -it postgres-0 -- sh -c '
  psql -U "$POSTGRES_USER" -d homeassistant -c "
    SELECT pg_size_pretty(pg_database_size('\''homeassistant'\'')) AS database_size;
  "
'
```

## Home Assistant secrets

The Home Assistant 1Password item is `kubernetes/home-assistant`.

Expected active fields:

```text
KOPIA_REPOSITORY
KOPIA_PASSWORD
HASS_LATITUDE
HASS_LONGITUDE
HASS_ELEVATION
HASS_LUTRON_USER
HASS_LUTRON_PASSWORD
HASS_TWILIO_ACCOUNT_SID
HASS_TWILIO_AUTH_TOKEN
HACS_GITHUB_TOKEN
```

`HACS_GITHUB_TOKEN` is stored for reference/secret hygiene, but current HACS setup uses the normal HACS/GitHub login flow from the Home Assistant UI.

InfluxDB was present in the migrated Home Assistant config but is currently disabled/commented out because InfluxDB is not deployed in this repo yet. Re-enable it only after adding/migrating an InfluxDB service.

## HACS reset and reinstall notes

HACS was fully reset during migration because old HACS state/config did not survive cleanly and the old custom component code was incompatible with the current Home Assistant API.

Clean reset actions performed:

```text
removed stale /config/.storage/hacs* state files
removed /config/custom_components/hacs
reinstalled HACS 2.0.5 into /config/custom_components/hacs
```

Verify HACS files:

```sh
HA_POD="$(kubectl -n default get pod -l app.kubernetes.io/name=home-assistant -o jsonpath='{.items[0].metadata.name}')"

kubectl -n default exec "$HA_POD" -- sh -c '
  ls -la /config/custom_components/hacs/manifest.json
  python - <<PY
import json
m=json.load(open("/config/custom_components/hacs/manifest.json"))
print(m.get("domain"), m.get("version"), m.get("config_flow"))
PY
'
```

Previously installed HACS repositories captured before reset:

```text
alandtse/alexa_media_player          integration
dahlb/ha_carrier                     integration
dlarrick/hass-kumo                   integration
finity69x2/fan-percent-button-row    plugin
iMicknl/ha-nest-protect              integration
simbaja/ha_gehome                    integration
vigonotion/hass-simpleicons          integration
```

Reinstall only the integrations/plugins that are still needed, one at a time, from HACS.

## Zigbee coordinator

Zigbee2MQTT is currently configured for the existing SLZB coordinator settings:

```text
serial port: tcp://172.16.1.37:6638
adapter: ember
baudrate: 115200
channel: 25
base topic: zigbee2mqtt
permit_join: false
```

The previous endpoint was `tcp://172.16.1.75:6638`, but the coordinator moved to `.37`. Keep the coordinator on a DHCP reservation or stable IoT-network address before changing this again.

The network key and PAN ID are supplied through External Secrets from 1Password.

The Zigbee2MQTT 1Password item is `kubernetes/zigbee`.

Expected fields:

```text
KOPIA_REPOSITORY
KOPIA_PASSWORD
ZIGBEE2MQTT_CONFIG_ADVANCED_PAN_ID
ZIGBEE2MQTT_CONFIG_ADVANCED_NETWORK_KEY
ZIGBEE2MQTT_CONFIG_MQTT_USER
ZIGBEE2MQTT_CONFIG_MQTT_PASSWORD
```

## Z-Wave data notes

The Z-Wave JS UI store is mounted at:

```text
/usr/src/app/store
```

Important migrated files include:

```text
settings.json
users.json
nodes.json
c57b4b68.json
c57b4b68.jsonl
c57b4b68.metadata.jsonl
c57b4b68.values.jsonl
```

Old `zwavejs_*.log` files are not required for startup and can be pruned later if desired.

## PVC sizing

Current PVC sizes:

| PVC | Namespace | Size |
| --- | --- | --- |
| `home-assistant` | `default` | `10Gi` |
| `home-assistant-cache` | `default` | `5Gi` |
| `config-mosquitto-0` | `default` | `5Gi` |
| `zigbee` | `default` | `5Gi` |
| `zwave` | `default` | `5Gi` |
| `postgres` | `database` | `60Gi` |

`ceph-block` supports expansion, so these can be grown later if needed. PVCs should not be shrunk.
