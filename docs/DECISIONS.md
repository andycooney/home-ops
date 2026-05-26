# Platform decisions

This file captures the important choices behind the `home-ops` platform so future changes have context.

## GitOps model

This repository is the source of truth. Flux reconciles from the `main` branch.

Operational convention:

- Prefer Git commits over live manual changes.
- If a manual dashboard or cluster change is needed, document it.
- Keep runtime-impacting changes in separate commits when possible.
- Use conventional commit prefixes such as `feat:`, `fix:`, `docs:`, and `chore:`.

## Domain split

```text
cooney.site   = internal only
cooney.online = external only
```

This keeps internal admin services separate from internet-facing services.

Internal apps generally use:

```text
*.cooney.site -> internal.cooney.site -> envoy-internal
```

External apps generally use:

```text
*.cooney.online -> external.cooney.online -> Cloudflare Tunnel -> envoy-external
```

## External access

All normal external apps under `*.cooney.online` require Cloudflare Access.

Current Access model:

```text
protected-external-apps
  Destination: *.cooney.online
  Authentication: Google
  Policy: allow-andy

flux-webhook
  Destination: flux-webhook.cooney.online/<exact Flux receiver path>
  Policy: bypass-flux-webhook
```

The Flux webhook bypass is intentionally exact-path scoped so GitHub can deliver webhooks without interactive authentication.

Avoid broad bypasses such as:

```text
*.cooney.online bypass
flux-webhook.cooney.online bypass without a path
```

## Internal-only first

New apps should be internal-only by default.

External access should be added only when:

1. The app really needs internet access.
2. Cloudflare Access is configured first.
3. App-level authentication is still enabled when available.
4. Any bypass/public exception is documented.

## Storage defaults

Important persistent application data should use:

```text
ceph-block
```

Cache, scratch, or intentionally node-local data can use OpenEBS hostpath.

VolSync/Kopia should be configured for persistent apps that matter.

## Secrets

The preferred model is:

```text
1Password -> External Secrets -> Kubernetes Secret
```

SOPS may still exist for bootstrap-level or historical secrets, but new shared app secrets should prefer 1Password/External Secrets.

## Resource posture

The cluster is currently request-constrained more than usage-constrained.

Default app requests should be conservative:

```yaml
resources:
  requests:
    cpu: 25m
    memory: 128Mi
  limits:
    memory: 512Mi
```

Do not aggressively reduce Ceph, Cilium, control-plane, or critical platform resources without a specific reason.

## Upstream-derived manifests

Some manifests are copied from or inspired by `onedr0p/home-ops`.

When adding upstream-derived apps:

1. Prefer copying the upstream app folder first.
2. Make local changes as small diffs.
3. Keep local changes obvious.
4. Use the `onedr0p` git remote locally for future comparisons.

Example:

```sh
git remote add onedr0p https://github.com/onedr0p/home-ops.git 2>/dev/null || true
git fetch onedr0p main
git diff onedr0p/main -- kubernetes/apps/kube-system/descheduler
```

## Descheduler

Descheduler is installed in dry-run mode.

Do not disable dry-run until:

1. Logs have been reviewed.
2. Upstream `DefaultEvictor` settings are reviewed.
3. Protections for system-critical, PVC, and local-storage pods are understood.
