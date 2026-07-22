# qBittorrent runtime PIA WireGuard integration

## Scope and immutable image

This integration consumes the PR 1 supervisor image without changing its runtime source. The qBittorrent pod uses the repository-plus-tag convention with this exact linux/amd64 OCI index reference:

```text
ghcr.io/andycooney/qbittorrent-pia-runtime:sha-57cb9074d7f5@sha256:85a993530216302f4a04f507e936241c1d1c6468840b4e692980c92b50deb243
```

The digest is the OCI index digest published for merge commit `57cb9074d7f5621f0ec7c38173b85a8f1fb5632b`. It is not the attestation-manifest digest. The runtime image defines the supervisor, dynamic endpoint discovery and refresh, firewall, state machine, and session contract in [`QBITTORRENT-PIA-RUNTIME-IMAGE.md`](QBITTORRENT-PIA-RUNTIME-IMAGE.md); this change integrates that contract with the existing application.

## Startup ordering and identities

The app-template controller runs `firewall-init` as a regular Kubernetes init container before any application or sidecar starts. It uses the same immutable runtime image and executes `/usr/local/bin/pia-runtime firewall-init` as UID/GID 0. It is non-privileged, disallows privilege escalation, drops every capability, and adds only `NET_ADMIN`. It receives only non-secret firewall configuration.

The existing `gluetun` container name is retained to keep the chart diff focused, but the container now starts the runtime image's default `/usr/local/bin/pia-runtime supervise` process. It also runs as UID/GID 0, is non-privileged, disallows privilege escalation, drops every capability, and adds only `NET_ADMIN`, `CHOWN`, and `DAC_OVERRIDE`. `NET_ADMIN` is required for the supervisor-owned firewall. The immutable PR 1 filesystem contract requires `CHOWN` while publishing generation ownership and `DAC_OVERRIDE` to create files after the PF directory becomes UID/GID 65532 and to read the helper-owned mode-0600 port record. These filesystem capabilities are not granted to the init container, qBittorrent, or either helper. The supervisor alone receives the PIA credential Secret and mounts `/dev/net/tun`.

qBittorrent remains UID/GID 1000, non-root, read-only-root, and capability-free. The PF helper and port-sync run explicitly as UID/GID 65532, are non-root and read-only-root, disallow privilege escalation, and have no capabilities. Neither helper receives credentials or `NET_ADMIN`.

## Runtime tmpfs and credentials

`/run/pia` is a 16 MiB `emptyDir` with `medium: Memory`. It is mounted read-write into the supervisor and PF helper and read-only into port-sync. The init container does not require it, and qBittorrent cannot read session material. The supervisor remains isolated from the qBittorrent config, media, and unprocessed volumes. Although the pod retains `fsGroup: 1000`, the root supervisor creates and repairs `/run/pia` as `root:65532` using the modes defined by PR 1. The rendered pod specification is checked to confirm the expected mounts and identities.

The `qbittorrent-vpn-secret` ExternalSecret uses `mergePolicy: Replace` and emits only `PIA_USERNAME` and `PIA_PASSWORD` from the existing `pia` item's `username` and `password` fields. The old static WireGuard Secret, static PF hostname/gateway, Gluetun WireGuard mount, and `/tmp/gluetun/forwarded_port` handoff are removed. No 1Password values are changed.

Discovery prefers the stable PIA region IDs for Montreal, Ontario, Toronto, and Vancouver, in that order. Only the region IDs are configured: the supervisor still refetches PIA's current metadata and dynamically selects and registers current WireGuard endpoints at runtime. If those preferred regions cannot produce a session, the bounded candidate list falls back to other eligible non-US port-forwarding regions.

## Generation-bound port forwarding

The unprivileged PF helper waits for `/run/pia/ready`, snapshots its `sessions/<generation>` target, and then reads `generation`, `pia.token`, `tls-hostname`, and `pf-gateway` from that immutable generation path. It never requests another token and never receives the username or password. Curl is bound to `tun0`, preserves TLS hostname verification with the reviewed PIA CA, and connects only to the generation's PF gateway on TCP 19999.

After validating a new PIA response and decoded port range, the helper publishes `payload`, `signature`, `expires-at`, and finally `port` through same-directory mode-0600 temporary files and atomic renames, then binds that allocation. The port record is strict JSON containing the exact generation and a numeric port in `1..65535`. The same generation allocation is rebound every 900 seconds without requesting another signature. On restart, an existing allocation is reused only when its payload, signature, expiry, port, and generation all validate and it remains unexpired. Generation changes or absent, malformed, expired, or unusable allocation data require a fresh signature.

Token, payload, and signature curl parameters are supplied through helper-owned mode-0600 request files, never process arguments. The token request file strips only trailing CR/LF from the runtime token and rejects embedded line breaks; all request files contain the exact value without a trailing newline and are removed after every attempt and on exit. Readiness is polled every five seconds so invalidation or failover interrupts waits promptly. Tokens, payloads, signatures, response bodies, and authorization material are never logged.

Port-sync reads `/run/pia/ready/pf/port` through its read-only runtime mount, requires exactly the `generation` and `port` JSON keys, verifies the embedded generation against the snapshotted ready generation, validates the port range, and rechecks readiness around qBittorrent API access. It queries qBittorrent on every valid poll and updates `http://127.0.0.1:80` only when the current listen port differs, so a qBittorrent restart or configuration drift is repaired without duplicate POSTs while the same generation remains active. It follows generation changes on the five-second poll. It uses the existing PF helper image, pinned as `sha-2ce6208d13b2@sha256:b0c572e124abbc1ba5bf061c9d6359febdb848b6cbe276d4e56524deee7c2937`, because that image already supplies the reviewed shell, curl, and jq toolchain.

## Probes and validation

The supervisor uses exec probes only:

- liveness: `/usr/local/bin/pia-runtime healthcheck` every 15 seconds, with four failures allowed;
- readiness: `/usr/local/bin/pia-runtime readycheck` every five seconds, with one failure removing readiness.

Recoverable PIA outages and failover therefore remain live while pod readiness follows the verified generation. qBittorrent's existing liveness, readiness, and startup probes are unchanged.

The deployment sets each candidate's tunnel-verification timeout to 30 seconds. PIA's public inventory returns a changing subset from a larger endpoint pool, so a dead address must be cooled promptly before the supervisor refetches and evaluates another current subset. The Helm action timeout remains 30 minutes, comfortably covering the pod's five-minute graceful shutdown window and multiple complete six-candidate batches without rolling back active endpoint rotation.

Offline validation includes shell syntax, deterministic stubbed PF and qBittorrent API flows, secret-redaction assertions, generation race rejection, strict/malformed/mismatched/out-of-range port records, renewal configuration, duplicate-update suppression, and generation changes. Kustomize and app-template renders are inspected for the exact image, init ordering, capabilities, identities, tmpfs volume, credential isolation, `/dev/net/tun`, exec probes, and removal of static session inputs. Repository validation, schema validation, Flux Local, and redacted secret scanning remain required before review.

## Live validation still required

This draft makes no claim of live Talos or Kubernetes validation. Before merge or rollout, validate:

- the runtime reaches `HEALTHY`;
- pre-tunnel qBittorrent egress is blocked;
- qBittorrent's public IP differs from the WAN IP;
- DNS and HTTPS use `tun0`;
- the WebUI remains reachable through the internal route;
- the PF helper obtains and binds a port;
- the supervisor accepts only the active generation's port record;
- port-sync updates qBittorrent;
- the first health failure removes readiness and qBittorrent/PF access;
- endpoint failover creates a fresh token, keypair, and generation;
- stale PF data is rejected;
- pod restart leaves no session material on persistent storage;
- graceful and forced runtime restarts remain fail-closed;
- Talos nft/legacy and owner-match behavior matches the reviewed firewall assumptions.

## Rollback

Rollback is Git-only: revert this integration commit or pull request and let the normal reviewed GitOps process reconcile the prior deployment. Do not patch the workload, Secret, or firewall live. The old static session inputs must not be removed from the external credential store until the integration has completed live validation and a separate cleanup is reviewed.
