#!/usr/bin/env sh
set -eu

umask 077

RUNTIME_DIR="${RUNTIME_DIR:-/run/pia}"
READY_LINK="${RUNTIME_DIR}/ready"
RENEW_SECONDS="${RENEW_SECONDS:-900}"
POLL_SECONDS="${POLL_SECONDS:-5}"
PIA_CA_CERT="${PIA_CA_CERT:-/scripts/pia-ca.rsa.4096.crt}"
RUN_ONCE="${PIA_PF_RUN_ONCE:-0}"

SNAPSHOT_TARGET=""
GENERATION=""
GENERATION_DIR=""
PF_DIR=""
TLS_HOSTNAME=""
PF_GATEWAY=""
PAYLOAD=""
SIGNATURE=""
PORT=""
EXPIRES_AT=""
PAYLOAD_TMP=""
SIGNATURE_TMP=""
EXPIRES_TMP=""
PORT_TMP=""

log() {
  printf '%s %s\n' "$(date -Iseconds)" "$*" >&2
}

valid_generation() {
  case "$1" in
    ""|.|..|*/*|*\\*|*[!A-Za-z0-9._-]*) return 1 ;;
    *) return 0 ;;
  esac
}

snapshot_ready_generation() {
  target="$(readlink "${READY_LINK}" 2>/dev/null)" || return 1
  case "${target}" in
    sessions/*) generation="${target#sessions/}" ;;
    *) return 1 ;;
  esac
  valid_generation "${generation}" || return 1

  generation_dir="${RUNTIME_DIR}/sessions/${generation}"
  [ -d "${generation_dir}/pf" ] || return 1
  [ "$(cat "${generation_dir}/generation" 2>/dev/null)" = "${generation}" ] || return 1
  [ "$(readlink "${READY_LINK}" 2>/dev/null)" = "${target}" ] || return 1

  SNAPSHOT_TARGET="${target}"
  GENERATION="${generation}"
  GENERATION_DIR="${generation_dir}"
  PF_DIR="${generation_dir}/pf"
}

generation_is_still_ready() {
  [ -n "${GENERATION}" ] || return 1
  [ "$(readlink "${READY_LINK}" 2>/dev/null)" = "${SNAPSHOT_TARGET}" ] || return 1
  [ "$(cat "${GENERATION_DIR}/generation" 2>/dev/null)" = "${GENERATION}" ] || return 1
}

load_generation_metadata() {
  TLS_HOSTNAME="$(cat "${GENERATION_DIR}/tls-hostname" 2>/dev/null)" || return 1
  PF_GATEWAY="$(cat "${GENERATION_DIR}/pf-gateway" 2>/dev/null)" || return 1
  [ -s "${GENERATION_DIR}/pia.token" ] || return 1

  case "${TLS_HOSTNAME}" in
    ""|.*|*..*|*[!A-Za-z0-9.-]*) return 1 ;;
  esac
  case "${PF_GATEWAY}" in
    ""|*[!A-Fa-f0-9:.]*) return 1 ;;
  esac
  generation_is_still_ready
}

connect_gateway() {
  case "${PF_GATEWAY}" in
    *:*) printf '[%s]' "${PF_GATEWAY}" ;;
    *) printf '%s' "${PF_GATEWAY}" ;;
  esac
}

request_signature() {
  response="$(
    curl -fsS -m 10 \
      --interface tun0 \
      --connect-to "${TLS_HOSTNAME}:19999:$(connect_gateway):19999" \
      --cacert "${PIA_CA_CERT}" \
      -G \
      --data-urlencode "token@${GENERATION_DIR}/pia.token" \
      "https://${TLS_HOSTNAME}:19999/getSignature"
  )" || return 1

  PAYLOAD="$(printf '%s' "${response}" | jq -er 'select(type == "object" and .status == "OK") | .payload | select(type == "string" and length > 0)')" || return 1
  SIGNATURE="$(printf '%s' "${response}" | jq -er 'select(type == "object" and .status == "OK") | .signature | select(type == "string" and length > 0)')" || return 1

  decoded="$(printf '%s' "${PAYLOAD}" | base64 -d 2>/dev/null)" || return 1
  PORT="$(printf '%s' "${decoded}" | jq -er '.port | select(type == "number" and floor == . and . >= 1 and . <= 65535)')" || return 1
  EXPIRES_AT="$(printf '%s' "${decoded}" | jq -er '.expires_at | select(type == "string" and length > 0)')" || return 1
}

cleanup_temporary_files() {
  for path in "${PAYLOAD_TMP}" "${SIGNATURE_TMP}" "${EXPIRES_TMP}" "${PORT_TMP}"; do
    [ -z "${path}" ] || rm -f -- "${path}"
  done
}

write_temporary_file() {
  path="$1"
  value="$2"
  rm -f -- "${path}"
  printf '%s\n' "${value}" > "${path}"
  chmod 0600 "${path}"
  sync -d "${path}"
}

prepare_binding_files() {
  PAYLOAD_TMP="${PF_DIR}/.payload.tmp.$$"
  SIGNATURE_TMP="${PF_DIR}/.signature.tmp.$$"
  write_temporary_file "${PAYLOAD_TMP}" "${PAYLOAD}"
  write_temporary_file "${SIGNATURE_TMP}" "${SIGNATURE}"
}

bind_port() {
  response="$(
    curl -fsS -m 10 \
      --interface tun0 \
      --connect-to "${TLS_HOSTNAME}:19999:$(connect_gateway):19999" \
      --cacert "${PIA_CA_CERT}" \
      -G \
      --data-urlencode "payload@${PAYLOAD_TMP}" \
      --data-urlencode "signature@${SIGNATURE_TMP}" \
      "https://${TLS_HOSTNAME}:19999/bindPort"
  )" || return 1

  printf '%s' "${response}" | jq -e 'type == "object" and .status == "OK"' >/dev/null
}

publish_generation_files() {
  generation_is_still_ready || return 1

  EXPIRES_TMP="${PF_DIR}/.expires-at.tmp.$$"
  PORT_TMP="${PF_DIR}/.port.tmp.$$"
  write_temporary_file "${EXPIRES_TMP}" "${EXPIRES_AT}"
  write_temporary_file "${PORT_TMP}" "$(printf '{"generation":"%s","port":%s}' "${GENERATION}" "${PORT}")"

  generation_is_still_ready || return 1
  mv -f -- "${PAYLOAD_TMP}" "${PF_DIR}/payload"
  PAYLOAD_TMP=""
  mv -f -- "${SIGNATURE_TMP}" "${PF_DIR}/signature"
  SIGNATURE_TMP=""
  mv -f -- "${EXPIRES_TMP}" "${PF_DIR}/expires-at"
  EXPIRES_TMP=""
  mv -f -- "${PORT_TMP}" "${PF_DIR}/port"
  PORT_TMP=""
  sync -d "${PF_DIR}"
}

renew_generation() {
  load_generation_metadata || return 1
  request_signature || return 1
  generation_is_still_ready || return 1
  prepare_binding_files || return 1
  bind_port || return 1
  generation_is_still_ready || return 1
  publish_generation_files || return 1
  log "published active generation port metadata"
}

wait_while_generation_ready() {
  seconds="$1"
  deadline=$(( $(date +%s) + seconds ))
  while [ "$(date +%s)" -lt "${deadline}" ]; do
    generation_is_still_ready || return 1
    sleep "${POLL_SECONDS}"
  done
}

main() {
  trap cleanup_temporary_files EXIT
  trap 'exit 0' HUP INT TERM
  while true; do
    if ! snapshot_ready_generation; then
      log "waiting for an active ready generation"
      [ "${RUN_ONCE}" = "1" ] && return 0
      sleep "${POLL_SECONDS}"
      continue
    fi

    if renew_generation; then
      [ "${RUN_ONCE}" = "1" ] && return 0
      wait_while_generation_ready "${RENEW_SECONDS}" || true
      continue
    fi

    log "port-forward operation failed; retrying while generation remains ready"
    [ "${RUN_ONCE}" = "1" ] && return 1
    wait_while_generation_ready 30 || true
  done
}

main "$@"
