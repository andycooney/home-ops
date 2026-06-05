# Observability

The observability baseline lives under:

```text
kubernetes/apps/o11y
```

## Components

Current baseline components:

```text
blackbox-exporter-lan
gatus
grafana-operator
grafana-instance
kube-prometheus-stack
prometheus-adapter
snmp-exporter
```

Validated internal URLs:

```text
https://grafana.cooney.site
https://prometheus.cooney.site
https://alertmanager.cooney.site
https://gatus.cooney.site
https://status.cooney.site
```

## Validation

```sh
flux get ks -A | grep -E "o11y|blackbox|gatus|grafana|prometheus|alert|snmp"
flux get hr -n o11y
kubectl -n o11y get pods -o wide
kubectl -n o11y get httproute
kubectl -n o11y get servicemonitor,podmonitor,scrapeconfig,probe
```

Expected core pods:

```text
alertmanager-kube-prometheus-stack-0              2/2 Running
blackbox-exporter-lan                             1/1 Running
gatus                                             1/1 Running
grafana-deployment                                1/1 Running
grafana-operator                                  1/1 Running
kube-prometheus-stack-operator                    1/1 Running
kube-state-metrics                                1/1 Running
node-exporter                                     3/3 Running
prometheus-adapter                                1/1 Running
prometheus-kube-prometheus-stack-0                2/2 Running
snmp-exporter                                     1/1 Running
```

## HTTP checks

Grafana:

```sh
curl -Ik https://grafana.cooney.site
```

Expected:

```text
HTTP/2 200
```

Prometheus and Alertmanager may return `405` to `HEAD`. Use GET readiness endpoints:

```sh
curl -kL https://prometheus.cooney.site/-/ready
curl -kL https://alertmanager.cooney.site/-/ready
```

Expected:

```text
ready
```

Gatus:

```sh
curl -Ik https://gatus.cooney.site
curl -Ik https://status.cooney.site
```

Expected:

```text
HTTP/2 200
```

## Gatus

Gatus is the internal status page.

```text
namespace: o11y
urls:
  https://gatus.cooney.site
  https://status.cooney.site
storage: ceph-block StatefulSet volume claim template
config: kubernetes/apps/o11y/gatus/app/resources/config.yaml
```

Gatus has static checks for core endpoints and also uses `gatus-sidecar` to discover annotated HTTPRoutes and services.

For an HTTPRoute that should be discovered by Gatus, add an annotation such as:

```yaml
gatus.home-operations.com/endpoint: |-
  conditions: ["[STATUS] == 200"]
```

For routes that should not be auto-added, use:

```yaml
gatus.home-operations.com/enabled: "false"
```

Validate config and logs:

```sh
kubectl -n o11y exec statefulset/gatus -c app -- cat /config/config.yaml
kubectl -n o11y logs statefulset/gatus -c app --tail=120
```

## SNMP Exporter

SNMP Exporter is used for network/device SNMP scraping.

```text
namespace: o11y
app: snmp-exporter
current target: UDM/gateway at 172.16.1.1
module: if_mib
auth profile: home_v2
ServiceMonitor: snmp-exporter-udm
```

The SNMP community is stored in 1Password:

```text
vault: kubernetes
item: snmp-exporter
field: SNMP_COMMUNITY
```

The Kubernetes secret flow is:

```text
1Password snmp-exporter item
  -> ExternalSecret snmp-exporter-auth
  -> Kubernetes Secret snmp-exporter-auth
  -> /run/secrets/snmp-exporter/auth.yml
```

The `auth.yml` file is rendered by External Secrets with the actual community value. Do not depend on SNMP Exporter environment expansion for this field.

Validate objects:

```sh
kubectl -n o11y get hr,ocirepository,externalsecret,secret,pod,svc,servicemonitor | grep -E 'snmp|NAME'
kubectl -n o11y rollout status deploy/snmp-exporter --timeout=5m
```

Health endpoint:

```sh
kubectl -n o11y run "snmp-exporter-health-$(date +%s)" \
  --rm \
  --restart=Never \
  --image=curlimages/curl:8.11.1 \
  -- curl -sS http://snmp-exporter.o11y.svc.cluster.local:9116/-/healthy
```

SNMP scrape test:

```sh
kubectl -n o11y run "snmp-test-$(date +%s)" \
  --rm \
  --restart=Never \
  --image=curlimages/curl:8.11.1 \
  -- curl -sS 'http://snmp-exporter.o11y.svc.cluster.local:9116/snmp?target=172.16.1.1&module=if_mib&auth=home_v2' | head -20
```

Expected output starts with Prometheus metric text such as:

```text
# HELP ...
# TYPE ...
```

When piping `kubectl run` output to `head`, do not use `-it`; a pseudo-TTY can leave the local terminal in a bad state and print NUL/control characters.

If the community string is exposed in terminal output, rotate it in UniFi and 1Password, refresh the ExternalSecret, and restart the exporter:

```sh
kubectl -n o11y annotate externalsecret snmp-exporter-auth \
  force-sync="$(date +%s)" --overwrite
kubectl -n o11y rollout restart deploy/snmp-exporter
kubectl -n o11y rollout status deploy/snmp-exporter --timeout=5m
```

## Baseline scrape/probe targets

Current baseline targets:

```text
homebase.cooney.site:9100
storage.cooney.site
storage.cooney.site:2049
UDM/gateway SNMP at 172.16.1.1
```

## Prometheus resource note

Prometheus had previously been tuned down to a 1000Mi memory limit. After the kube-prometheus-stack update, Prometheus OOMKilled during WAL replay with roughly 1500+ WAL segments. Increasing only the memory limit allowed WAL replay to finish without increasing scheduled memory pressure.

Current expected values:

```text
requests: cpu=100m, memory=512Mi
limits: memory=4Gi
retention.time: 30d
retention.size: 50GiB
```

Validate live resources:

```sh
kubectl -n o11y get statefulset prometheus-kube-prometheus-stack \
  -o jsonpath='{range .spec.template.spec.containers[*]}{.name}{" requests="}{.resources.requests.memory}{" limits="}{.resources.limits.memory}{"\n"}{end}'
```

Expected:

```text
prometheus requests=512Mi limits=4Gi
```

If Prometheus is stuck at `1/2` or `CrashLoopBackOff`, check whether it is replaying WAL or being OOMKilled:

```sh
kubectl -n o11y get pod prometheus-kube-prometheus-stack-0 \
  -o jsonpath='{range .status.containerStatuses[*]}{.name}{" ready="}{.ready}{" restarts="}{.restartCount}{" state="}{.state}{" last="}{.lastState}{"\n"}{end}'

kubectl -n o11y logs prometheus-kube-prometheus-stack-0 -c prometheus --tail=120
kubectl -n o11y logs prometheus-kube-prometheus-stack-0 -c prometheus --previous --tail=120
```

Good recovery signs:

```text
WAL replay completed
TSDB started
Server is ready to receive web requests.
prometheus-kube-prometheus-stack-0 2/2 Running 0 restarts
```

After recovery, check TSDB/WAL size:

```sh
kubectl -n o11y exec prometheus-kube-prometheus-stack-0 -c prometheus -- \
  du -sh /prometheus /prometheus/wal
```

Do not reduce Prometheus below the current limit without reviewing actual usage, retention, and scrape/cardinality growth.
