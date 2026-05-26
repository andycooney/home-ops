# Observability

The observability baseline lives under:

```text
kubernetes/apps/o11y
```

## Components

Current baseline components:

```text
blackbox-exporter-lan
grafana-operator
grafana-instance
kube-prometheus-stack
prometheus-adapter
```

Validated internal URLs:

```text
https://grafana.cooney.site
https://prometheus.cooney.site
https://alertmanager.cooney.site
```

## Validation

```sh
flux get ks -A | grep -E "o11y|blackbox|grafana|prometheus|alert"
flux get hr -n o11y
kubectl -n o11y get pods -o wide
kubectl -n o11y get httproute
kubectl -n o11y get servicemonitor,podmonitor,scrapeconfig,probe
```

Expected core pods:

```text
alertmanager-kube-prometheus-stack-0              2/2 Running
blackbox-exporter-lan                             1/1 Running
grafana-deployment                                1/1 Running
grafana-operator                                  1/1 Running
kube-prometheus-stack-operator                    1/1 Running
kube-state-metrics                                1/1 Running
node-exporter                                     3/3 Running
prometheus-adapter                                1/1 Running
prometheus-kube-prometheus-stack-0                2/2 Running
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

## Baseline scrape/probe targets

Current baseline targets:

```text
homebase.cooney.site:9100
storage.cooney.site
storage.cooney.site:2049
```

## Resource note

Prometheus memory was reduced for this base cluster.

Expected current values:

```text
requests: cpu=100m, memory=512Mi
limits: memory=1000Mi
```

Do not reduce Prometheus further without reviewing actual usage and retention requirements.
