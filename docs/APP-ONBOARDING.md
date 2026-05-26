# Application onboarding guide

Use this checklist for each new application added to `home-ops`.

## Exposure

Default to internal-only.

| Exposure | Route | DNS |
| --- | --- | --- |
| Internal | `envoy-internal` | `*.cooney.site` |
| External | `envoy-external` / Cloudflare Tunnel | `*.cooney.online` |

Only add an external route when the app has an intentional access-control story.

## Persistence

Prefer these defaults:

| Data type | Storage |
| --- | --- |
| Important app data | `ceph-block` PVC |
| Cache/scratch | OpenEBS local hostpath |
| No persistent data | no PVC |

## VolSync/Kopia

For persistent apps:

```sh
scripts/volsync-app-bootstrap.sh <app-name>
```

Expected NFS path:

```text
/home-ops-backups/<app-name>
```

Expected 1Password item:

```text
vault: kubernetes
item: <app-name>
fields:
  KOPIA_REPOSITORY=filesystem:///mnt/repository/<app-name>
  KOPIA_PASSWORD=<generated>
```

## Required validation

Before commit:

```sh
kubectl kustomize kubernetes/apps/<namespace>/<app>/app >/tmp/<app>-render.yaml
scripts/validate-repo.sh
```

After Flux applies:

```sh
flux get ks <app> -n <namespace>
flux get hr <app> -n <namespace>
kubectl -n <namespace> get pods,svc,pvc,httproute | grep <app>
curl -Ik https://<app>.cooney.site
```
