# Upstream review record

This image is based on source and artifacts reviewed on 2026-07-18. Protocol behavior is implemented from authoritative upstream projects, not copied from third-party examples.

## Gluetun

- Image: `ghcr.io/qdm12/gluetun:v3.41.1@sha256:1a5bf4b4820a879cdf8d93d7ef0d2d963af56670c9ebff8981860b6804ebc8ab`
- Tag: [`v3.41.1`](https://github.com/qdm12/gluetun/releases/tag/v3.41.1)
- Source commit: [`7f22fb32764d5d7548bc669cde88c57fc1a0de83`](https://github.com/qdm12/gluetun/tree/7f22fb32764d5d7548bc669cde88c57fc1a0de83)
- linux/amd64 manifest: `sha256:2f33c71e5e164fcd51a962cb950134df25155593edf0c3e1201f888d027049b4`
- Reviewed files: [`Dockerfile`](https://github.com/qdm12/gluetun/blob/7f22fb32764d5d7548bc669cde88c57fc1a0de83/Dockerfile), [`cmd/gluetun/main.go`](https://github.com/qdm12/gluetun/blob/7f22fb32764d5d7548bc669cde88c57fc1a0de83/cmd/gluetun/main.go), [`internal/cli/healthcheck.go`](https://github.com/qdm12/gluetun/blob/7f22fb32764d5d7548bc669cde88c57fc1a0de83/internal/cli/healthcheck.go), [`internal/configuration/settings/firewall.go`](https://github.com/qdm12/gluetun/blob/7f22fb32764d5d7548bc669cde88c57fc1a0de83/internal/configuration/settings/firewall.go), [`internal/configuration/settings/health.go`](https://github.com/qdm12/gluetun/blob/7f22fb32764d5d7548bc669cde88c57fc1a0de83/internal/configuration/settings/health.go), and [`internal/configuration/sources/secrets/wireguard.go`](https://github.com/qdm12/gluetun/blob/7f22fb32764d5d7548bc669cde88c57fc1a0de83/internal/configuration/sources/secrets/wireguard.go).

The inspected image is linux/amd64 and has `/gluetun-entrypoint` as its entrypoint. Its inherited healthcheck is `/gluetun-entrypoint healthcheck`; this image replaces it with supervisor liveness. `/gluetun-entrypoint` handles SIGINT and SIGTERM and has a five-second internal shutdown sequence. A custom WireGuard configuration is read from `WIREGUARD_CONF_SECRETFILE` before environment values.

Live Talos validation showed the kernel implementation could authenticate and refresh WireGuard handshakes while its dataplane stopped carrying usable traffic. Gluetun's reviewed `WIREGUARD_IMPLEMENTATION=userspace` path carried the same dynamically registered PIA session successfully. The supervisor therefore forces userspace WireGuard and a dedicated numeric managed-process identity. Gluetun's embedded tunnel engine stays in the root entrypoint, so the firewall grants both identities only the active UDP endpoint and `tun0` while verifying or healthy, followed by their unconditional identity drops.

The base filesystem contains the system CA bundle at `/etc/ssl/certs/ca-certificates.crt`, nft frontends `iptables`, `ip6tables`, `iptables-restore`, and `ip6tables-restore`, plus the corresponding `*-legacy` and `*-legacy-restore` binaries. The supervisor uses the default frontends and `--noflush`; legacy availability is recorded for PR 2 runtime diagnosis, not selected automatically.

Gluetun v3.41.1 reads `HEALTH_RESTART_VPN`. It does not read `HEALTHCHECK_RESTART_VPN`. The supervisor therefore sets the upstream-recognized `HEALTH_RESTART_VPN=off`; using the latter spelling alone would leave Gluetun's default restart behavior enabled. It also sets `FIREWALL_ENABLED_DISABLING_IT_SHOOTS_YOU_IN_YOUR_FOOT=off`, `VPN_SERVICE_PROVIDER=custom`, `VPN_TYPE=wireguard`, `PUBLICIP_ENABLED=off`, `VERSION_INFORMATION=off`, and `VPN_PORT_FORWARDING=off`.

## PIA manual connections

- Repository: [`pia-foss/manual-connections`](https://github.com/pia-foss/manual-connections)
- Reviewed commit: [`a1412dbe2ca41edbb79c766bc475335cb6cb13ad`](https://github.com/pia-foss/manual-connections/tree/a1412dbe2ca41edbb79c766bc475335cb6cb13ad)
- Reviewed flow: [`get_region.sh`](https://github.com/pia-foss/manual-connections/blob/a1412dbe2ca41edbb79c766bc475335cb6cb13ad/get_region.sh), [`get_token.sh`](https://github.com/pia-foss/manual-connections/blob/a1412dbe2ca41edbb79c766bc475335cb6cb13ad/get_token.sh), [`connect_to_wireguard_with_token.sh`](https://github.com/pia-foss/manual-connections/blob/a1412dbe2ca41edbb79c766bc475335cb6cb13ad/connect_to_wireguard_with_token.sh), and [`port_forwarding.sh`](https://github.com/pia-foss/manual-connections/blob/a1412dbe2ca41edbb79c766bc475335cb6cb13ad/port_forwarding.sh).

The runtime fetches `https://serverlist.piaservers.net/vpninfo/servers/v6`, posts credentials as multipart fields to PIA's token endpoint, generates a new X25519 keypair, and calls `https://<validated-hostname>:1337/addKey` while dialing the selected IP directly. TLS SNI and hostname verification remain enabled against the vendored PIA CA.

The `/addKey` response is authoritative for the registered session. Its public `server_ip` and `server_port` form the WireGuard endpoint and its private `server_vip` is the virtual gateway used to test tunnel connectivity. The selected server-list IP is only the registration target and can differ from `server_ip`. The same public `server_ip` is used as the PIA port-forwarding API gateway on TCP port 19999 through the tunnel.

This response-field behavior was confirmed against the official PIA desktop 3.7.2 source at commit [`3d76d75c7e5693812aa1b72108cb44df7397dd32`](https://github.com/pia-foss/desktop/tree/3d76d75c7e5693812aa1b72108cb44df7397dd32), specifically [`wireguardmethod.cpp`](https://github.com/pia-foss/desktop/blob/3d76d75c7e5693812aa1b72108cb44df7397dd32/daemon/src/wireguardmethod.cpp). The older reviewed manual-connections script constructs the endpoint from the selected list IP and does not consume `server_vip`; the runtime follows the current desktop client when interpreting the registration response.

### Vendored PIA CA

- File: `third_party/pia/ca.rsa.4096.crt`
- Authoritative source: [`ca.rsa.4096.crt`](https://github.com/pia-foss/manual-connections/blob/a1412dbe2ca41edbb79c766bc475335cb6cb13ad/ca.rsa.4096.crt)
- Revision: `a1412dbe2ca41edbb79c766bc475335cb6cb13ad`
- SHA-256: `32e9b1d1433ea97614f2a14c6e358e3f57c0570cc9f6b2ee812699ba696c66ab`
- Purpose: authenticate the selected PIA WireGuard registration hostname while connecting to its selected IP.

To update it, review a new authoritative `pia-foss/manual-connections` commit, replace only the certificate, verify its certificate properties, record the new commit and SHA-256 here, and rerun the offline TLS tests. Never replace it from an arbitrary mirror or disable certificate validation.

## Go builder

- Image: `golang:1.26.5-alpine3.23@sha256:73f9732658b30852522ee5ebe698daa27e1829add9a70ff4f4a828409f8d0a99`
- Verified platform: linux/amd64
- Verified image configuration: Go `1.26.5`, Alpine `3.23`, `GOTOOLCHAIN=local`

The binary uses only the Go standard library, is built with `CGO_ENABLED=0`, and has no module dependencies. The final image installs no packages.

The Dockerfile frontend is also pinned: `docker/dockerfile:1.19@sha256:b6afd42430b15f2d2a4c5a02b919e98a525b785b1aaff16747d2f623364e39b6`.
