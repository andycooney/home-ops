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
victoria-logs
victoria-logs-collector
```

Validated internal URLs:

```text
https://grafana.cooney.site
https://prometheus.cooney.site
https://alertmanager.cooney.site
https://gatus.cooney.site
https://status.cooney.site
https://victoria-logs.cooney.site
https://logs.cooney.site
```

## Validation

```sh
flux get ks -A | grep -E "o11y|blackbox|gatus|grafana|prometheus|alert|snmp|victoria"
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
victoria-logs-0                                   1/1 Running
victoria-logs-collector                           1/1 Running on each node
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

VictoriaLogs may return `400` for `HEAD /`. Use `/health` for checks:

```sh
curl -kL https://victoria-logs.cooney.site/health
curl -kL https://logs.cooney.site/health
```

Expected:

```text
OK
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

## VictoriaLogs

VictoriaLogs is the centralized Kubernetes log search backend.

```text
namespace: o11y
server: victoria-logs
collector: victoria-logs-collector
routes:
  https://victoria-logs.cooney.site
  https://logs.cooney.site
storage: ceph-block PVC, 20Gi
retention: 14d / 15GiB
server resources: requests 10m/128Mi, limit 2Gi
collector resources: requests 5m/64Mi, limit 256Mi
```

The deployment follows the `onedr0p/home-ops` two-part pattern:

```text
kubernetes/apps/o11y/victoria-logs/app        # VictoriaLogs server
kubernetes/apps/o11y/victoria-logs/collector  # log collector
```

The collector remote-writes to:

```text
http://victoria-logs.o11y.svc.cluster.local:9428
```

The collector streams include these fields:

```text
kubernetes.pod_name
kubernetes.pod_namespace
kubernetes.container_name
kubernetes.pod_labels.app.kubernetes.io/name
```

Validate objects:

```sh
kubectl -n o11y get hr,ocirepository,pod,pvc,svc,httproute,podmonitor,servicemonitor | grep -E 'victoria|NAME'
kubectl -n o11y rollout status statefulset/victoria-logs --timeout=10m
kubectl -n o11y rollout status daemonset/victoria-logs-collector --timeout=10m
```

Validate collector reachability to the server:

```sh
COLLECTOR="$(kubectl -n o11y get pod -l app.kubernetes.io/name=victoria-logs-collector -o jsonpath='{.items[0].metadata.name}')"
kubectl -n o11y exec "$COLLECTOR" -- wget -qO- http://victoria-logs.o11y.svc.cluster.local:9428/health
```

Generate a smoke-test log line:

```sh
kubectl -n default run "log-test-$(date +%s)" \
  --restart=Never \
  --image=busybox:1.38.0 \
  -- sh -c 'echo "victoria logs smoke test $(date -Iseconds)"'
```

Query for that smoke-test log:

```sh
kubectl -n o11y run "vlogs-query-$(date +%s)" \
  --rm -i \
  --restart=Never \
  --image=curlimages/curl:8.11.1 \
  -- sh -c 'curl -G -sS "http://victoria-logs.o11y.svc.cluster.local:9428/select/logsql/query" \
    --data-urlencode "query=victoria logs smoke test" \
    --data-urlencode "limit=20"'
```

A broad recent query should also return live cluster logs:

```sh
kubectl -n o11y run "vlogs-query-$(date +%s)" \
  --rm -i \
  --restart=Never \
  --image=curlimages/curl:8.11.1 \
  -- sh -c 'curl -G -sS "http://victoria-logs.o11y.svc.cluster.local:9428/select/logsql/query" \
    --data-urlencode "query=*" \
    --data-urlencode "limit=20"'
```

Check collector log-file visibility on each node:

```sh
for pod in $(kubectl -n o11y get pod -l app.kubernetes.io/name=victoria-logs-collector -o name); do
  echo "===== $pod ====="
  kubectl -n o11y exec "$pod" -- sh -c 'find /var/log/pods -type f 2>/dev/null | wc -l; find /var/log/containers -type l 2>/dev/null | wc -l'
done
```

Check VictoriaLogs storage usage:

```sh
kubectl -n o11y exec victoria-logs-0 -- du -sh /storage
```

Successful sends from the collector are usually quiet. A quiet collector log does not mean ingestion is broken; prove ingestion with a query.

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
VictoriaLogs internal log search
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
