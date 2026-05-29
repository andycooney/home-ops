# Home automation stack

The Home Assistant stack is managed from `kubernetes/apps/default`.

Current stack components:

| App | Purpose | Internal URL |
| --- | --- | --- |
| Home Assistant | Main home automation controller | `https://home-assistant.cooney.site`, `https://hass.cooney.site` |
| Mosquitto | Internal MQTT broker | cluster-internal only |
| Zigbee2MQTT | Zigbee coordinator bridge | `https://zigbee.cooney.site` |
| Z-Wave JS UI | Z-Wave controller UI and websocket server | `https://zwave.cooney.site` |

## Current rollout state

The stack was intentionally merged with each Flux Kustomization suspended:

```yaml
spec:
  suspend: true
```

This lets the manifests, PVCs, secrets, and appdata migration exist safely without starting the applications before their persistent data is ready.

Check suspension state:

```sh
kubectl -n default get kustomizations.kustomize.toolkit.fluxcd.io home-assistant mosquitto zigbee zwave
```

## Persistent data migration

Historical appdata was copied from:

| Source | Destination PVC / path |
| --- | --- |
| `/Volumes/appdata/homeassistant` | `home-assistant` mounted at `/config` |
| `/Volumes/appdata/mosquitto` | `config-mosquitto-0` mounted at `/config` |
| `/Volumes/appdata/zigbee2mqtt` | `zigbee` mounted at `/config` |
| `/Volumes/appdata/zwavejs2mqtt` | `zwave` mounted at `/usr/src/app/store` |

The temporary copy pods used for migration were deleted after the copy finished.

Useful verification commands:

```sh
kubectl -n default get pvc home-assistant home-assistant-cache config-mosquitto-0 zigbee zwave
kubectl -n default get pods | grep -E 'config-copy|copy' || true
```

## Startup order

Unsuspend one app at a time after confirming its PVC and secrets are ready.

Recommended order:

1. Mosquitto
2. Zigbee2MQTT
3. Z-Wave JS UI
4. Home Assistant

Mosquitto should come up first because Zigbee2MQTT publishes to it. Home Assistant should come up last so MQTT discovery and controller integrations are already available.

## Unsuspend workflow

For each app:

```sh
flux resume kustomization <app> -n default
flux reconcile kustomization <app> -n default --with-source
kubectl -n default get ks <app>
kubectl -n default get hr,pod,pvc,httproute | grep <app>
kubectl -n default logs deploy/<app> --tail=100
```

For Mosquitto, which is a StatefulSet, use:

```sh
kubectl -n default get sts,pod,pvc | grep mosquitto
kubectl -n default logs sts/mosquitto --tail=100
```

## Secrets

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

Do not commit Zigbee network keys, MQTT passwords, Home Assistant tokens, or controller credentials to Git.

## Zigbee coordinator

Zigbee2MQTT is currently configured for the existing network coordinator settings:

```text
serial port: tcp://172.16.1.75:6638
adapter: ember
channel: 25
base topic: zigbee2mqtt
permit_join: false
```

The network key and PAN ID are supplied through External Secrets from 1Password.

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

| PVC | Size |
| --- | --- |
| `home-assistant` | `10Gi` |
| `home-assistant-cache` | `5Gi` |
| `config-mosquitto-0` | `5Gi` |
| `zigbee` | `5Gi` |
| `zwave` | `5Gi` |

`ceph-block` supports expansion, so these can be grown later if needed. PVCs should not be shrunk.
