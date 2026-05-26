# Resource request policy

This cluster currently runs on 16GB Talos nodes. Kubernetes scheduling is constrained by **requests**, not actual usage.

## Defaults

Small app:

```yaml
resources:
  requests:
    cpu: 25m
    memory: 128Mi
  limits:
    memory: 512Mi
```

Medium app:

```yaml
resources:
  requests:
    cpu: 50m
    memory: 256Mi
  limits:
    memory: 1Gi
```

Heavy app:

```yaml
resources:
  requests:
    cpu: 100m
    memory: 512Mi
  limits:
    memory: 2Gi
```

## Avoid aggressive tuning

Do not reduce these casually:

```text
Rook/Ceph OSDs, MONs, MDS
Ceph CSI
Cilium
Control-plane static pods
Prometheus below 512Mi request
```
