# qBittorrent PIA runtime image

This directory builds a linux/amd64 Gluetun-derived image whose PID 1 is a purpose-built PIA WireGuard supervisor. It creates a fresh PIA token and WireGuard session for every generation, owns the namespace firewall, verifies tunneled traffic, and starts `/gluetun-entrypoint` only as its supervised child.

The image is intentionally not wired into Kubernetes in this PR. PR 2 must supply a tmpfs at `/run/pia`, credentials, capabilities, subnet policy, probes, and the PF helper integration.

## Commands

- `pia-runtime firewall-init` installs or audits the fail-closed IPv4/IPv6 baseline without needing PIA credentials.
- `pia-runtime supervise` runs the state machine, HTTP probes, firewall transitions, and Gluetun child.
- `pia-runtime self-test` performs offline binary and fixture checks without credentials or firewall changes.
- `pia-runtime healthcheck` checks supervisor liveness only. Recoverable PIA failures remain live but not ready.

HTTP probe paths are `/live` and `/ready` on `0.0.0.0:8001` by default.

## Required runtime contract

The supervisor runs as root with the network-administration capabilities needed for iptables and WireGuard. `/dev/net/tun` must be available. `/run/pia` must be a tmpfs; startup fails before contacting PIA when it is not. UID `1000` is the application identity and GID `65532` is the session/PF reader identity unless explicitly configured otherwise.

Required secret environment values are `PIA_USERNAME` and `PIA_PASSWORD`. The existing `VPN_PORT_FORWARDING_USERNAME` and `VPN_PORT_FORWARDING_PASSWORD` names are accepted as migration inputs. They are removed from the Gluetun child environment.

Useful non-secret settings:

| Variable | Default | Meaning |
|---|---:|---|
| `PIA_PREFERRED_REGIONS` | empty | Ordered comma-separated PIA region IDs |
| `PIA_ALLOWED_SUBNETS` | empty | Explicit non-WAN CIDRs required by the pod contract |
| `PIA_CANDIDATE_MIN` / `PIA_CANDIDATE_MAX` | `3` / `6` | Validated candidate-cycle bounds |
| `PIA_TUNNEL_TIMEOUT` | `120s` | Maximum startup verification window |
| `PIA_HEALTH_INTERVAL` | `15s` | Independent health interval |
| `PIA_HEALTH_FAILURES` | `4` | Consecutive failures before rotation |
| `PIA_AUTH_RETRY` | `15m` | Minimum authentication-failure retry |
| `PIA_SESSION_MAX_AGE` | `20h` | Proactive rotation age |
| `PIA_SHUTDOWN_GRACE` | `10s` | Child SIGTERM grace period |

All durations and bounds are validated. Public or default-route CIDRs such as `0.0.0.0/0` are rejected as allowed subnets.

## Development

From this directory:

```sh
gofmt -w cmd internal
go vet ./...
go test ./...
go test -race ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
go run ./cmd/pia-runtime self-test
```

Tests do not contact PIA or modify the host firewall. The Docker build and image self-test run in GitHub-hosted Actions when a local Docker daemon is unavailable.

See [UPSTREAM.md](UPSTREAM.md) for immutable inputs and [the architecture document](../../docs/QBITTORRENT-PIA-RUNTIME-IMAGE.md) for the security model and PR 2 limitations.
