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

## Route patterns

Internal-only apps should use one route attached only to `envoy-internal` and a `*.cooney.site` hostname.

Apps that need both internal and external access should use separate route entries:

```yaml
route:
  internal:
    hostnames:
      - "{{ .Release.Name }}.cooney.site"
    parentRefs:
      - name: envoy-internal
        namespace: network
  external:
    hostnames:
      - "{{ .Release.Name }}.cooney.online"
    parentRefs:
      - name: envoy-external
        namespace: network
```

Do not put `.site` and `.online` hostnames on the same route with both Gateway parent refs. That can cause external-dns to publish the internal `.site` name through the external gateway.

## Persistence

Prefer these defaults:

| Data type | Storage |
| --- | --- |
| Important app data | `ceph-block` PVC |
| Cache/scratch | OpenEBS local hostpath |
| No persistent data | no PVC |

## Container image notes

Prefer images that work with the repo's default rootless security posture:

```yaml
runAsNonRoot: true
runAsUser: 1000
runAsGroup: 1000
capabilities:
  drop: ["ALL"]
```

Some LinuxServer/hotio-style images use an init layer that must start as root and then use `PUID`/`PGID` for app file ownership. Treat those as explicit exceptions, document them in the app manifest, and keep the exception scoped to that app.

Some otherwise-rootless images need explicit user environment variables when the application expects a username or home directory. Prefer keeping the app rootless and setting app-specific values such as `HOME`, `USER`, and an explicit config file path mounted on the app PVC.

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

## Media app mounts

Media apps that need library access should use the standard in-container paths:

```text
/media
/unprocessed
```

For read-only library access, mount the NFS shares at those same paths with `readOnly: true`. Avoid introducing aliases such as `/data` unless the app requires them.

## VPN-bound downloader apps

Use the SABnzbd/qBittorrent Gluetun sidecar pattern for downloader apps that must egress through PIA.

Defaults:

- Keep the downloader WebUI internal-only on `*.cooney.site`.
- Reuse the shared `pia` 1Password item, but render an app-scoped VPN secret.
- Keep `/media` and `/unprocessed` mounted at the standard paths.
- Do not expose a BitTorrent LoadBalancer or inbound listener until inbound port handling is explicitly designed.
- Verify egress from the app container and the Gluetun sidecar before considering the app done.

Example VPN egress check:

```sh
POD="$(kubectl -n default get pod -l app.kubernetes.io/name=qbittorrent -o jsonpath='{.items[0].metadata.name}')"

kubectl -n default exec "$POD" -c app -- wget -qO- https://ifconfig.co
kubectl -n default exec "$POD" -c gluetun -- wget -qO- https://ifconfig.co
kubectl -n default exec "$POD" -c app -- ip route
```

Expected result: the app container and Gluetun sidecar report the same public IP, and the app container has split default routes through `tun0`.

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
- Default to internal-only routing through `envoy-internal`.
- For dual-exposure apps, use separate internal and external route entries.
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
