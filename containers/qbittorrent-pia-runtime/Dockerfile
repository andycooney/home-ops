# syntax=docker/dockerfile:1.25@sha256:0adf442eae370b6087e08edc7c50b552d80ddf261576f4ebd6421006b2461f12

ARG GO_BUILDER=golang:1.26.5-alpine3.23@sha256:73f9732658b30852522ee5ebe698daa27e1829add9a70ff4f4a828409f8d0a99
ARG GLUETUN_BASE=ghcr.io/qdm12/gluetun:v3.41.1@sha256:1a5bf4b4820a879cdf8d93d7ef0d2d963af56670c9ebff8981860b6804ebc8ab

FROM --platform=$BUILDPLATFORM ${GO_BUILDER} AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN test "${TARGETOS}/${TARGETARCH}" = "linux/amd64" && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.version=${VERSION} -X main.revision=${REVISION} -X main.created=${CREATED}" \
      -o /out/pia-runtime ./cmd/pia-runtime

FROM ${GLUETUN_BASE}
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
LABEL org.opencontainers.image.source="https://github.com/andycooney/home-ops" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.title="qBittorrent PIA runtime" \
      org.opencontainers.image.description="Fail-closed runtime PIA WireGuard supervisor for qBittorrent" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.base.name="ghcr.io/qdm12/gluetun:v3.41.1" \
      org.opencontainers.image.base.digest="sha256:1a5bf4b4820a879cdf8d93d7ef0d2d963af56670c9ebff8981860b6804ebc8ab"
COPY --from=builder /out/pia-runtime /usr/local/bin/pia-runtime
COPY third_party/pia/ca.rsa.4096.crt /usr/local/share/pia/ca.rsa.4096.crt
ENV VPN_SERVICE_PROVIDER=custom \
    VPN_TYPE=wireguard \
    FIREWALL_ENABLED_DISABLING_IT_SHOOTS_YOU_IN_YOUR_FOOT=off \
    HEALTH_RESTART_VPN=off \
    PUBLICIP_ENABLED=off \
    VERSION_INFORMATION=off \
    VPN_PORT_FORWARDING=off
ENTRYPOINT ["/usr/local/bin/pia-runtime"]
CMD ["supervise"]
HEALTHCHECK --interval=15s --timeout=3s --start-period=15s --retries=3 \
  CMD ["/usr/local/bin/pia-runtime", "healthcheck"]
