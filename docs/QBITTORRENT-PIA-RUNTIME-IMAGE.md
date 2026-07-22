# qBittorrent runtime PIA WireGuard image

## Decision and scope

qBittorrent needs a complete, fresh PIA WireGuard registration when an endpoint becomes unhealthy. A shell or ConfigMap wrapper cannot safely combine PID 1 duties, atomic firewall transitions, strict TLS-to-selected-IP registration, typed API validation, child reaping, deterministic tests, and generation publication without adding a large runtime toolchain. This PR therefore adds a static Go supervisor to a digest-pinned Gluetun image.

This is PR 1 only. Kubernetes manifests, ExternalSecrets, PF scripts/helpers, PVCs, storage, routes, VolSync, zeroscaler, probes, deployment configuration, and live Talos validation are explicitly deferred to PR 2.

## Process and ownership model

`/usr/local/bin/pia-runtime supervise` is PID 1. It owns signal handling, readiness, PIA discovery/registration, session generations, tunnel verification, health/failover decisions, and the firewall. `/gluetun-entrypoint` is a child whose only VPN role is to create and maintain the custom WireGuard tunnel. The supervisor forces Gluetun's userspace WireGuard implementation and configures its managed processes as dedicated UID `999`; the embedded tunnel engine remains in the root entrypoint. Live Talos testing found that kernel WireGuard kept authenticating handshakes while its dataplane stopped carrying usable traffic, whereas userspace WireGuard carried the same registered session.

The supervisor forwards SIGINT/SIGTERM as SIGTERM after first revoking readiness and UID 1000/PF-helper network permissions. It waits for the configured grace period, uses SIGKILL only after timeout and only while the child is still running, waits for the child, and leaves the fail-closed rules installed. Completion state is independent of the public `Done` result channel, so consuming `Done` cannot cause `Stop` to signal an already-reaped PID or process group. Transient signal-delivery failures are not cached: the supervisor retains the child and generation until a later cleanup pass confirms termination. Direct child processes are reaped. Gluetun's own five-second shutdown handling remains inside that outer bound.

Gluetun's firewall and restart ownership are disabled with the exact variables implemented by v3.41.1. In particular, reviewed upstream uses `HEALTH_RESTART_VPN=off`; `HEALTHCHECK_RESTART_VPN` is not recognized by this version. The resulting contract is:

> Supervisor-owned kill switch active while Gluetun supplies the tunnel.

PIA credentials and any environment names containing tokens, passwords, authorization data, or private keys are removed before Gluetun starts. The child receives only the path to the already-published `wg0.conf`; secrets are never command arguments.

## State machine

The explicit states are `BOOTSTRAP`, `SELECTING_SERVER`, `REGISTERING_WIREGUARD`, `STARTING_TUNNEL`, `VERIFYING_TUNNEL`, `HEALTHY`, `FAILING_OVER`, `AUTHENTICATION_FAILED`, `BACKOFF`, and `SHUTTING_DOWN`.

Startup installs/audits the firewall before external traffic, validates configuration, and obtains a pre-tunnel public IP without logging it. A transient public-IP failure remains live, fail-closed, and not ready while BOOTSTRAP retries with bounded jittered backoff; server discovery, token requests, generation creation, and Gluetun startup cannot begin until a valid address is available. Candidate selection accepts only distinct non-US, online, WireGuard-present, PF-capable endpoints, applies configured preference order, excludes cooldown endpoints, attempts at least three when available, and caps a batch at six by default. Each candidate is probed before token acquisition. A successful account token is reused for up to 23 hours across endpoint attempts and cycles, avoiding rate limits on PIA's token service; a registration authentication rejection invalidates it immediately. Candidate probe, registration, and initial tunnel-verification failures cool only the affected endpoint and continue the freshly fetched bounded batch. Once a session has reached HEALTHY, a Gluetun exit or consecutive-health-failure threshold cools the failed candidate and exits that snapshot immediately. After fail-closed cleanup, the supervisor obtains a fresh pre-tunnel public IP, fetches current PIA metadata, and selects/registers a new endpoint without outer backoff. Proactive session rotation follows the same immediate refresh path without cooling the healthy candidate. Authentication keeps its separate retry schedule; global token-service and local firewall, filesystem, key-generation, or process-start failures cool nothing and enter the outer bounded backoff after fail-closed cleanup. Each session attempt gets a new X25519 keypair, validates the endpoint's certificate hostname while dialing its IP, strictly validates the registration, and creates a new generation.

The application stays blocked while Gluetun starts. Verification binds root DNS and HTTPS sockets to `tun0`, requires the registered PIA DNS server to answer, obtains a tunneled public IP that differs from the pre-tunnel value, and requires RX and TX counters to increase. Only then are `ready` and UID 1000's `tun0` rule published.

Health runs every 15 seconds. On the first failure, status becomes not ready, a verified restricted firewall removes UID 1000, PF API, and forwarded-port access, and only then is `ready` invalidated. A restriction failure triggers best-effort LOCKED and immediate child shutdown. Recovery verifies HEALTHY firewall state before publishing `ready`; publication failure reverts to VERIFYING or LOCKED. Four consecutive failures identify the established endpoint as unhealthy. Failover locks first, invalidates metadata, stops and reaps Gluetun, removes the WireGuard policy rules and tunnel interface, restores the pod's pre-Gluetun resolver in place, deletes the failed generation, obtains a fresh pre-tunnel public IP, refetches the server list, and registers a current endpoint immediately. Resolver restoration prevents Gluetun's stopped localhost DNS service from blocking the next bootstrap; in-place writing is required for Kubernetes' file-mounted `/etc/resolv.conf`. Explicit network and resolver cleanup are required even after a child panic so stale state cannot block bootstrap or collide with the next WireGuard setup. It never resumes an hours-old candidate snapshot. The outer exponential backoff begins only when discovery or a fresh candidate batch cannot produce a session. A failed stop, network cleanup, or resolver restoration retains the generation and blocks all new session activity until cleanup succeeds. Sessions rotate proactively at 20 hours and likewise refresh discovery. Authentication failure stays live and retries no faster than 15 minutes. Other outages use jittered 30-second, one-minute, two-minute, four-minute, and capped five-minute backoff.

## Firewall model

`firewall-init` and every transition cover IPv4 and IPv6. They use dedicated `PIA_RUNTIME_INPUT`, `PIA_RUNTIME_OUTPUT`, and `PIA_RUNTIME_FORWARD` chains, `iptables-restore`/`ip6tables-restore` with `--noflush`, and never flush unrelated chains or temporarily set built-in policies to ACCEPT. INPUT, OUTPUT, and FORWARD policies are DROP. The manager reuses the chains idempotently, installs their built-in-chain hooks when absent, and audits policies, chains, and hooks after applying a transaction.

Loopback and established/related flows are allowed only after identity-specific restrictions. Family-matched rules first allow UID 1000 only ESTABLISHED TCP replies sourced from the configured WebUI service port to approved internal subnets. HEALTHY then permits UID 1000 through `tun0`; every state immediately follows with an unconditional UID-wide DROP, so pre-existing tunnel, internal, and WAN flows cannot reach generic conntrack acceptance outside HEALTHY. The PF-helper allowance and UID-wide drop follow. In VERIFYING and HEALTHY, dedicated managed-process UID `999` can send UDP only to the exact active WireGuard endpoint and can use `tun0`; every state then applies its unconditional UID-wide DROP. UID 0 receives state-scoped access: DNS and HTTPS in BOOTSTRAP, exact candidate TLS in SELECTED, and the exact active WireGuard UDP endpoint plus `tun0` only in VERIFYING and HEALTHY, followed in every state by an unconditional UID 0 DROP. Generic ESTABLISHED,RELATED acceptance is evaluated only after all identity blocks, so bootstrap and registration connections cannot survive a state transition. The exact unowned UDP WireGuard endpoint rule remains as backend defense in depth. HTTP connection reuse is disabled for bootstrap, metadata, token, and registration calls as defense in depth.

The configurable PF helper UID defaults to `65532` and is dropped before unrestricted established or subnet allowances in every state. In HEALTHY, its sole non-loopback exception is TCP through `tun0` to the active same-family PF gateway on port `19999`; it has no general tunnel, default-interface, other-destination, or other-port access. Once the active generation publishes a validated forwarded port, same-family new TCP and UDP input to that port is allowed only on `tun0`. VERIFYING and LOCKED transactions remove both the PF API and inbound-port rules before readiness invalidation or child shutdown. The helper has no firewall capability and the supervisor never executes helper-supplied data.

The userspace endpoint allowances are owner-matched to UID `999` and root and evaluated only before each identity's drop. The earlier unconditional UID `1000` DROP prevents the application from using either or the later unowned endpoint exception. Forced supervisor death leaves the terminal DROP rules and identity blocks in the shared network namespace; restart first reasserts and audits them.

## Runtime filesystem contract

PR 2 must mount `/run/pia` as tmpfs. The supervisor refuses a non-tmpfs filesystem on Linux. It creates:

```text
/run/pia/
├── current -> sessions/<generation>
├── ready -> sessions/<generation>
└── sessions/<generation>/
    ├── generation, region, endpoint, tls-hostname, wg-gateway, pf-gateway
    ├── pia.token
    ├── wg0.conf
    └── pf/{payload,signature,expires-at,port}
```

The root is `0750 root:65532`, `sessions` and generation directories are `0710 root:65532`, and `pf` is `0730 <PIA_PF_HELPER_UID>:65532`. Metadata and the token are `0640 root:65532`; `wg0.conf` is `0600 root:root`; PF files are `0600 <PIA_PF_HELPER_UID>:65532`. The PF identity can traverse only the needed directories and cannot read `wg0.conf`.

The helper publishes the forwarded port by atomically replacing `pf/port` with a `0600` JSON record containing only `generation` and `port`. The supervisor reads only the active generation, requires the embedded generation to match, limits the record size, rejects unknown fields and ports outside `1..65535`, and installs no rule for missing or invalid data. Stale generations therefore cannot update the active firewall.

Every file is created in its target directory, assigned mode and ownership, written, synced, closed, atomically renamed, and followed by a directory sync. `current` and `ready` use same-directory temporary symlinks and atomic rename. `current` appears only after the complete generation and empty permission-correct PF targets exist; a failed ownership, file, sync, or link step removes the new generation and temporary files while preserving an older active generation and unrelated directories. A post-rename link-sync failure rolls the link back. `ready` appears only after verification. Invalidation removes `ready` before child shutdown, and stale session material is removed after readers can observe invalidation.

This provides atomic visibility and tmpfs-only persistence. It does not claim secure RAM erasure: Go strings, kernel buffers, and tmpfs pages cannot be proven overwritten. No token, key, configuration, PF payload/signature, public IP, response body, authorization header, or credential is logged or passed as an argument.

## PIA and image supply chain

The PIA flow and CA come from `pia-foss/manual-connections` commit `a1412dbe2ca41edbb79c766bc475335cb6cb13ad`. Gluetun is v3.41.1 source commit `7f22fb32764d5d7548bc669cde88c57fc1a0de83`. Exact source URLs, reviewed behavior, certificate hash, image/platform metadata, and update instructions are in [`UPSTREAM.md`](../containers/qbittorrent-pia-runtime/UPSTREAM.md).

The runtime base is `ghcr.io/qdm12/gluetun:v3.41.1@sha256:1a5bf4b4820a879cdf8d93d7ef0d2d963af56670c9ebff8981860b6804ebc8ab`. The builder is `golang:1.26.5-alpine3.23@sha256:73f9732658b30852522ee5ebe698daa27e1829add9a70ff4f4a828409f8d0a99`. The static linux/amd64 binary has no module dependencies, and no runtime package is installed.

## Tests and publication

Offline tests cover strict schema rejection, distinct candidate filters/order/cooldown/minimum/maximum attempts, probe-before-token ordering, account-token reuse/expiry/invalidation, failure classification and endpoint-specific cooldown, token and registration parsing, authentication classification, HTTP connection closure, context cancellation, TLS hostname-to-IP binding and certificate rejection, fresh key generation, configuration validation, all firewall states and transactions, idempotent restoration/audit, established UID 0 and UID 1000 flow revocation, PF-helper destination/port/interface denial, dynamic TCP/UDP inbound rules, generation-bound PF publication, injected firewall/publication cleanup failures, fresh failover comparison IPs, no parallel child/generation, atomic files/symlinks/modes/ownership, PF isolation, stale removal, liveness/readiness exec checks, backoff bounds, proactive rotation, health thresholds, environment/log redaction, retryable SIGTERM/SIGKILL failure, post-reap signal prevention, repeated Stop, graceful SIGTERM, bounded SIGKILL, and fixture self-test. Ordinary tests never invoke iptables or live PIA services.

The pull-request workflow runs only on `ubuntu-latest`, uses full-SHA action pins, tests/vets/race-tests/staticchecks, builds linux/amd64, runs the image self-test with no network or credentials, inspects the image contract, generates/validates an SBOM, and scans vulnerabilities. The complete-image scan reports High/Critical findings inherited from the mandatory immutable Gluetun base, while a separate blocking scan rejects fixed High/Critical vulnerabilities in the PR-owned static supervisor binary. This preserves visibility into the pinned base without pretending PR 1 can upgrade or mutate it. It does not authenticate to GHCR or publish anything.

After a reviewed merge to `main`, the workflow repeats validation and publishes only `ghcr.io/andycooney/qbittorrent-pia-runtime:sha-<12-character-commit>` with OCI source, revision, version, creation, title, description, license, base-name, and base-digest labels. BuildKit SBOM/provenance and GitHub provenance attestation accompany the immutable digest. Trusted manual publication accepts only a selected repository ref already contained in `origin/main`. No mutable tag is created.

## Remaining PR 2 risks

PR 2 must validate the tmpfs mount, `/dev/net/tun`, capabilities, init ordering, shared network namespace, actual qBittorrent UID, service/CNI subnet exceptions, nft and legacy behavior on Talos, input policy effects on sidecars and probes, kernel WireGuard endpoint traffic, DNS routing, generation/PF handoff, graceful and forced restart behavior, and liveness-driven container restart. Until that integration is reviewed and tested, this image must not replace the live qBittorrent deployment.
