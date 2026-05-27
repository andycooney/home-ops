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

## App import workflow from onedr0p/home-ops

Prefer importing a single app from `onedr0p/home-ops` into a short-lived PR branch, then adapting it to this repo before merge.

Start from a clean `main`:

```sh
git checkout main
git pull origin main
git checkout -b <app>-onboarding
git fetch onedr0p main
git checkout onedr0p/main -- kubernetes/apps/default/<app>
```

Adapt the imported app before enabling or merging:

- Replace upstream domains with `{{ .Release.Name }}.cooney.site` for internal apps.
- Default to internal-only routing through `envoy-internal` with `sectionName: https`.
- Do not copy upstream secrets, domains, storage paths, node selectors, hardware assumptions, or environment-specific values blindly.
- Preserve useful schema comments from the upstream manifests.
- Keep files ending with a trailing newline.
- Prefer `ceph-block` PVCs for important app config/state.
- Add the VolSync component and substitutions for persistent app PVCs when backups are expected.
- Prefer 1Password and External Secrets for app secrets.
- Enable the app from `kubernetes/apps/default/kustomization.yaml` only after the manifests have been adapted.

Validate and open a PR:

```sh
scripts/validate-repo.sh
git status
git add kubernetes/apps/default/<app> kubernetes/apps/default/kustomization.yaml
git commit -m "feat: configure <app>"
git push -u origin <app>-onboarding

gh pr create \
  --title "feat: configure <app>" \
  --base main \
  --head <app>-onboarding
```

After checks pass, review the GitHub diff and squash merge:

```sh
gh pr merge <pr-number> --squash --delete-branch
```

After merge, rely on the GitHub webhook to trigger Flux, then validate live state:

```sh
git checkout main
git pull origin main

kubectl -n default get ks <app>
kubectl -n default get externalsecret,secret,pvc,hr,pod | grep <app>
kubectl -n default get httproute | grep <app>
kubectl -n default logs deploy/<app> --tail=100
just sanity-check
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
