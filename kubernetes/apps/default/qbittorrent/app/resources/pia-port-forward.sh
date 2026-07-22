#!/usr/bin/env sh
set -eu

PORT_FILE="${PORT_FILE:-/tmp/gluetun/forwarded_port}"
RENEW_SECONDS="${RENEW_SECONDS:-43200}"
STARTUP_TIMEOUT_SECONDS="${STARTUP_TIMEOUT_SECONDS:-180}"
PIA_CA_CERT="${PIA_CA_CERT:-/scripts/pia-ca.rsa.4096.crt}"

PIA_USER="${VPN_PORT_FORWARDING_USERNAME:-}"
PIA_PASS="${VPN_PORT_FORWARDING_PASSWORD:-}"
PF_GATEWAY="${PIA_PF_GATEWAY:-}"
PF_HOSTNAME="${PIA_PF_HOSTNAME:-}"

log() {
  printf '%s %s\n' "$(date -Iseconds)" "$*" >&2
}

need_env() {
  var="$1"
  eval val="\${$var:-}"
  if [ -z "$val" ]; then
    log "ERROR missing required env: $var"
    exit 1
  fi
}

need_env PIA_USER
need_env PIA_PASS
need_env PF_GATEWAY
need_env PF_HOSTNAME

wait_for_vpn() {
  deadline=$(( $(date +%s) + STARTUP_TIMEOUT_SECONDS ))

  while [ "$(date +%s)" -lt "$deadline" ]; do
    if ip link show tun0 >/dev/null 2>&1; then
      log "VPN interface tun0 exists"
      return 0
    fi
    log "waiting for VPN/tun0..."
    sleep 5
  done

  log "ERROR VPN did not become ready before timeout"
  return 1
}

get_token() {
  response="$(
    curl -fsS --retry 3 --retry-delay 5 \
      -u "${PIA_USER}:${PIA_PASS}" \
      "https://privateinternetaccess.com/gtoken/generateToken"
  )"

  token="$(printf '%s' "$response" | jq -r '.token // empty')"

  if [ -z "$token" ] || [ "$token" = "null" ]; then
    log "ERROR token response did not contain token: $response"
    return 1
  fi

  log "PIA token acquired, length=$(printf '%s' "$token" | wc -c | tr -d ' ')"
  printf '%s\n' "$token"
}

get_signature() {
  token="$1"

  # This intentionally mirrors PIA manual-connections port_forwarding.sh:
  # --connect-to maps PF_HOSTNAME to PF_GATEWAY while preserving TLS SNI/cert validation.
  curl -sS -m 10 \
    --connect-to "${PF_HOSTNAME}::${PF_GATEWAY}:" \
    --cacert "${PIA_CA_CERT}" \
    -G \
    --data-urlencode "token=${token}" \
    "https://${PF_HOSTNAME}:19999/getSignature"
}

bind_port() {
  payload="$1"
  signature="$2"

  curl -sS -m 10 \
    --connect-to "${PF_HOSTNAME}::${PF_GATEWAY}:" \
    --cacert "${PIA_CA_CERT}" \
    -G \
    --data-urlencode "payload=${payload}" \
    --data-urlencode "signature=${signature}" \
    "https://${PF_HOSTNAME}:19999/bindPort"
}

write_port_from_payload() {
  payload="$1"

  port="$(
    printf '%s' "$payload" \
      | base64 -d 2>/dev/null \
      | jq -r '.port // empty'
  )"

  expires_at="$(
    printf '%s' "$payload" \
      | base64 -d 2>/dev/null \
      | jq -r '.expires_at // empty'
  )"

  if [ -z "$port" ]; then
    log "ERROR could not parse forwarded port from payload"
    return 1
  fi

  tmp="${PORT_FILE}.tmp"
  printf '%s\n' "$port" > "$tmp"
  chmod 0644 "$tmp"
  mv "$tmp" "$PORT_FILE"

  log "forwarded port written: ${port}"
  [ -n "$expires_at" ] && log "forwarded port expires_at: ${expires_at}"
}

while true; do
  wait_for_vpn || {
    sleep 30
    continue
  }

  log "requesting PIA token"
  token="$(get_token || true)"

  if [ -z "$token" ]; then
    log "ERROR failed to obtain PIA token"
    sleep 60
    continue
  fi

  log "requesting PIA port-forward signature via ${PF_HOSTNAME}/${PF_GATEWAY}"
  payload_and_signature="$(get_signature "$token" 2>/tmp/pia-signature-error.log || true)"

  if [ -z "$payload_and_signature" ]; then
    log "ERROR failed to obtain PIA port-forward signature: $(cat /tmp/pia-signature-error.log)"
    sleep 60
    continue
  fi

  status="$(printf '%s' "$payload_and_signature" | jq -r '.status // empty' 2>/dev/null || true)"
  if [ "$status" != "OK" ]; then
    log "ERROR payload_and_signature status was not OK: $payload_and_signature"
    sleep 60
    continue
  fi

  payload="$(printf '%s' "$payload_and_signature" | jq -r '.payload')"
  signature="$(printf '%s' "$payload_and_signature" | jq -r '.signature')"

  log "binding PIA forwarded port"
  bind_response="$(bind_port "$payload" "$signature" 2>/tmp/pia-bind-error.log || true)"

  if [ -z "$bind_response" ]; then
    log "ERROR bindPort failed: $(cat /tmp/pia-bind-error.log)"
    sleep 60
    continue
  fi

  bind_status="$(printf '%s' "$bind_response" | jq -r '.status // empty' 2>/dev/null || true)"
  if [ "$bind_status" != "OK" ]; then
    log "ERROR bindPort response was not OK: $bind_response"
    sleep 60
    continue
  fi

  write_port_from_payload "$payload" || true
  log "bindPort status: ${bind_status}"
  log "sleeping ${RENEW_SECONDS}s before renew"
  sleep "$RENEW_SECONDS"
done
