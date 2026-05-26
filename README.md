# home-ops

`home-ops` is the source of truth for Andy Cooney's Kubernetes homelab.

It contains the Talos cluster configuration, Flux GitOps manifests, core platform applications, networking, DNS, TLS, storage, backup, observability, external access, and recovery notes needed to operate or rebuild the environment.

## Current platform summary

The base platform currently includes:

- Talos Linux Kubernetes cluster
- Flux GitOps from this repository
- 1Password-backed External Secrets
- Cilium networking with BGP VIP advertisement
- Envoy Gateway for internal and external HTTP routing
- Internal DNS under `cooney.site`
- External DNS under `cooney.online`
- cert-manager wildcard TLS certificates
- TLS certificate backup to 1Password
- Cloudflare Tunnel for external ingress
- Cloudflare Access protecting external apps by default
- Rook/Ceph block storage
- OpenEBS hostpath storage
- VolSync/Kopia backups to QNAP NFS
- Observability stack
- GitHub Actions Runner Controller
- Tuppr system upgrade controller with upgrades suspended
- Intel GPU DRA support
- Multus IoT VLAN groundwork
- Flux Operator UI
- Descheduler installed in dry-run mode

## Domain model

```text
cooney.site   = internal only
cooney.online = external only
```

Internal apps generally route through:

```text
envoy-internal -> internal.cooney.site -> 192.168.60.1
```

External apps generally route through:

```text
envoy-external -> external.cooney.online -> Cloudflare Tunnel
```

External apps under `*.cooney.online` must be protected by Cloudflare Access unless an exception is explicitly documented.

## Important URLs

Internal-only:

- `https://flux.cooney.site`
- `https://kopia.cooney.site`
- `https://rook.cooney.site`
- `https://sabnzbd.cooney.site`
- `https://grafana.cooney.site`
- `https://prometheus.cooney.site`
- `https://alertmanager.cooney.site`

External:

- `https://echo.cooney.online`
- `https://flux-webhook.cooney.online`

`echo.cooney.online` should require Cloudflare Access. The Flux webhook has an exact-path Cloudflare Access bypass so GitHub can deliver webhook events.

## Documentation map

Read these files instead of growing this README indefinitely.

| Area | File | Purpose |
| --- | --- | --- |
| Design decisions | [`docs/DECISIONS.md`](docs/DECISIONS.md) | Key platform decisions, conventions, and rationale |
| Bootstrap and recovery | [`docs/BOOTSTRAP-RECOVERY.md`](docs/BOOTSTRAP-RECOVERY.md) | Talos bootstrap, Flux recovery, rebuild flow |
| Core platform apps | [`docs/CORE-PLATFORM.md`](docs/CORE-PLATFORM.md) | Platform app inventory and validation commands |
| Networking and external access | [`docs/NETWORKING-ACCESS.md`](docs/NETWORKING-ACCESS.md) | DNS, gateways, BGP, Cloudflare Tunnel, Cloudflare Access |
| Storage and backups | [`docs/STORAGE-BACKUP.md`](docs/STORAGE-BACKUP.md) | Rook/Ceph, OpenEBS, VolSync/Kopia, restore notes |
| Secrets and credentials | [`docs/SECRETS.md`](docs/SECRETS.md) | 1Password, External Secrets, SOPS remnants, required items |
| Observability | [`docs/OBSERVABILITY.md`](docs/OBSERVABILITY.md) | Grafana, Prometheus, Alertmanager, probes |
| Application onboarding | [`docs/APP-ONBOARDING.md`](docs/APP-ONBOARDING.md) | Checklist for adding new apps |
| Security checks | [`docs/SECURITY-CHECKS.md`](docs/SECURITY-CHECKS.md) | Secret scanning, Cloudflare Access checks, safe output handling |
| Resource requests | [`docs/RESOURCE-REQUESTS.md`](docs/RESOURCE-REQUESTS.md) | Sane request/limit defaults and tuning notes |
| Restore drill | [`docs/RESTORE-DRILL.md`](docs/RESTORE-DRILL.md) | Periodic restore-test procedure |
| Base platform checklist | [`docs/BASE-PLATFORM-CHECKLIST.md`](docs/BASE-PLATFORM-CHECKLIST.md) | Current base-platform completion checklist |

## Common commands

Run a broad health sweep:

```sh
scripts/cluster-health.sh
```

Run repo validation:

```sh
scripts/validate-repo.sh
scripts/secret-scan.sh
```

Force Flux to fetch latest repo state:

```sh
flux reconcile source git flux-system -n flux-system
```

Check Flux health:

```sh
flux check
flux get sources git -A
flux get ks -A
flux get hr -A
```

## Base platform checkpoint

The current base platform checkpoint tag is:

```text
working-checkpoint/base-platform-complete
```

It marks the repository state after base platform completion and before broader application onboarding.
