# qBittorrent runtime PIA WireGuard integration

## Scope and immutable image

This integration consumes the PR 1 supervisor image without changing its runtime source. The qBittorrent pod uses the repository-plus-tag convention with this exact linux/amd64 OCI index reference:

```text
ghcr.io/andycooney/qbittorrent-pia-runtime:sha-b64d67430d9c@sha256:60ba85d3feb69ab795faca5b61af0f950e027f7618291ff98572559806800296
```

The digest is the OCI index digest published for merge commit `b64d67430d9ca9438eb4b2aa43f6439935ede527`. It is not the attestation-manifest digest. PR 1 defines the supervisor, firewall, state machine, and session contract in [`QBITTORRENT-PIA-RUNTIME-IMAGE.md`](QBITTORRENT-PIA-RUNTIME-IMAGE.md); this PR only integrates that contract with the existing application.

## Startup ordering and identities

The app-template controller runs `firewall-init` as a regular Kubernetes init container before any application or sidecar starts. It uses the same immutable runtime image and executes `/usr/local/bin/pia-runtime firewall-init` as UID/GID 0. It is non-privileged, disallows privilege escalation, drops every capability, and adds only `NET_ADMIN`. It receives only non-secret firewall configuration.

The existing `gluetun` container name is retained to keep the chart diff focused, but the container now starts the runtime image's default `/usr/local/bin/pia-runtime supervise` process. It also runs as UID/GID 0, is non-privileged, disallows privilege escalation, drops every capability, and adds only `NET_ADMIN`. It alone receives the PIA credential Secret and mounts `/dev/net/tun`.

qBittorrent remains UID/GID 1000, non-root, read-only-root, and capability-free. The PF helper and port-sync run explicitly as UID/GID 65532, are non-root and read-only-root, disallow privilege escalation, and have no capabilities. Neither helper receives credentials or `NET_ADMIN`.

## Runtime tmpfs and credentials

`/run/pia` is a 16 MiB `emptyDir` with `medium: Memory`. It is mounted only into the supervisor, PF helper, and port-sync. The init container does not require it, and qBittorrent cannot read session material. Although the pod retains `fsGroup: 1000`, the root supervisor creates and repairs `/run/pia` as `root:65532` using the modes defined by PR 1. The rendered pod specification is checked to confirm the expected mounts and identities.

The `qbittorrent-vpn-secret` ExternalSecret uses `mergePolicy: Replace` and emits only `PIA_USERNAME` and `PIA_PASSWORD` from the existing `pia` item's `username` and `password` fields. The old static WireGuard Secret, static PF hostname/gateway, Gluetun WireGuard mount, and `/tmp/gluetun/forwarded_port` handoff are removed. No 1Password values are changed.

## Generation-bound port forwarding

The unprivileged PF helper waits for `/run/pia/ready`, snapshots its `sessions/<generation>` target, and then reads `generation`, `pia.token`, `tls-hostname`, and `pf-gateway` from that immutable generation path. It never requests another token and never receives the username or password. Curl is bound to `tun0`, preserves TLS hostname verification with the reviewed PIA CA, and connects only to the generation's PF gateway on TCP 19999.

After validating the PIA response and decoded port range, the helper binds the port and rechecks that the same generation is still ready. It publishes `payload`, `signature`, `expires-at`, and finally `port` through same-directory mode-0600 temporary files and atomic renames. The port record is strict JSON containing the exact generation and a numeric port in `1..65535`. Renewal is 900 seconds, but readiness is polled every five seconds so invalidation or failover interrupts the wait promptly. Tokens, payloads, signatures, response bodies, and authorization material are never logged or placed in command arguments.

Port-sync reads `/run/pia/ready/pf/port`, requires exactly the `generation` and `port` JSON keys, verifies the embedded generation against the snapshotted ready generation, validates the port range, and rechecks readiness around qBittorrent API access. It updates `http://127.0.0.1:80` only when the active port differs and follows generation changes on the five-second poll. It uses the existing PF helper image, pinned as `sha-2ce6208d13b2@sha256:b0c572e124abbc1ba5bf061c9d6359febdb848b6cbe276d4e56524deee7c2937`, because that image already supplies the reviewed shell, curl, and jq toolchain.

## Probes and validation

The supervisor uses exec probes only:

- liveness: `/usr/local/bin/pia-runtime healthcheck` every 15 seconds, with four failures allowed;
- readiness: `/usr/local/bin/pia-runtime readycheck` every five seconds, with one failure removing readiness.

Recoverable PIA outages and failover therefore remain live while pod readiness follows the verified generation. qBittorrent's existing liveness, readiness, and startup probes are unchanged.

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
