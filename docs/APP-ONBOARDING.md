# Application onboarding guide

Use this checklist for each new application added to `home-ops`.

## Exposure

Default to internal-only.

| Exposure | Route | DNS | Access requirement |
| --- | --- | --- | --- |
| Internal | `envoy-internal` | `*.cooney.site` | Private/internal network only |
| External | `envoy-external` / Cloudflare Tunnel | `*.cooney.online` | Cloudflare Access required by default |

Only add an external route when the app has an intentional access-control story.

External apps under `*.cooney.online` must be protected by Cloudflare Access unless the exception is explicit and documented. See:

```text
docs/NETWORKING-ACCESS.md
```

## External application checklist

Before exposing a new app externally:

1. Confirm that external access is required.
2. Create or update the Cloudflare Access application/policy first.
3. Prefer Google authentication with explicit allowed users/groups.
4. Keep app-level authentication enabled when available.
5. Add the external HTTPRoute only after Access is ready.
6. Validate the external hostname with `curl -I`.
7. Document any bypass/public exception.

Expected protected external response before login:

```text
HTTP/2 302
location: https://cooney-home.cloudflareaccess.com/...
www-authenticate: Cloudflare-Access ...
```

## Persistence

Prefer these defaults:

| Data type | Storage |
| --- | --- |
| Important app data | `ceph-block` PVC |
| Cache/scratch | OpenEBS local hostpath |
| No persistent data | no PVC |

## Resource defaults

Start small and adjust from real usage.

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

## Folder pattern

Typical structure:

```text
kubernetes/apps/<namespace>/<app>/
  ks.yaml
  app/
    kustomization.yaml
    helmrelease.yaml
    httproute.yaml
    externalsecret.yaml        # optional
    persistentvolumeclaim.yaml # optional
  volsync/
    kustomization.yaml         # optional
    replicationsource.yaml     # optional
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

For external apps:

```sh
curl -I https://<app>.cooney.online
```
