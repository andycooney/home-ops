#!/usr/bin/env sh
set -eu

umask 077

RUNTIME_DIR="${RUNTIME_DIR:-/run/pia}"
READY_LINK="${RUNTIME_DIR}/ready"
RENEW_SECONDS="${RENEW_SECONDS:-900}"
POLL_SECONDS="${POLL_SECONDS:-5}"
PIA_CA_CERT="${PIA_CA_CERT:-/scripts/pia-ca.rsa.4096.crt}"
RUN_ONCE="${PIA_PF_RUN_ONCE:-0}"
MAX_CYCLES="${PIA_PF_MAX_CYCLES:-0}"

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
TOKEN_REQUEST_TMP=""
PAYLOAD_REQUEST_TMP=""
SIGNATURE_REQUEST_TMP=""
PAYLOAD_PUBLISH_TMP=""
SIGNATURE_PUBLISH_TMP=""
EXPIRES_PUBLISH_TMP=""
PORT_PUBLISH_TMP=""

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

cleanup_request_files() {
  for request_path in "${TOKEN_REQUEST_TMP}" "${PAYLOAD_REQUEST_TMP}" "${SIGNATURE_REQUEST_TMP}"; do
    [ -z "${request_path}" ] || rm -f -- "${request_path}"
  done
  TOKEN_REQUEST_TMP=""
  PAYLOAD_REQUEST_TMP=""
  SIGNATURE_REQUEST_TMP=""
}

cleanup_publication_files() {
  for publish_path in "${PAYLOAD_PUBLISH_TMP}" "${SIGNATURE_PUBLISH_TMP}" "${EXPIRES_PUBLISH_TMP}" "${PORT_PUBLISH_TMP}"; do
    [ -z "${publish_path}" ] || rm -f -- "${publish_path}"
  done
  PAYLOAD_PUBLISH_TMP=""
  SIGNATURE_PUBLISH_TMP=""
  EXPIRES_PUBLISH_TMP=""
  PORT_PUBLISH_TMP=""
}

cleanup_temporary_files() {
  cleanup_request_files
  cleanup_publication_files
}

write_exact_file() {
  output_path="$1"
  output_value="$2"
  rm -f -- "${output_path}"
  printf '%s' "${output_value}" > "${output_path}"
  chmod 0600 "${output_path}"
  sync -d "${output_path}"
}

prepare_token_request_file() {
  TOKEN_REQUEST_TMP="${PF_DIR}/.token.request.$$"
  rm -f -- "${TOKEN_REQUEST_TMP}"
  if ! jq -Rjse '
    sub("[\\r\\n]+$"; "") |
    select(length > 0) |
    select((contains("\\r") or contains("\\n")) | not)
  ' "${GENERATION_DIR}/pia.token" > "${TOKEN_REQUEST_TMP}"; then
    cleanup_request_files
    return 1
  fi
  [ -s "${TOKEN_REQUEST_TMP}" ] || {
    cleanup_request_files
    return 1
  }
  chmod 0600 "${TOKEN_REQUEST_TMP}"
  sync -d "${TOKEN_REQUEST_TMP}"
}

validate_allocation_values() {
  case "${PAYLOAD}" in
    ""|*[!A-Za-z0-9+/=]*) return 1 ;;
  esac
  case "${SIGNATURE}" in
    ""|*[!A-Za-z0-9+/_=-]*) return 1 ;;
  esac

  decoded="$(printf '%s' "${PAYLOAD}" | base64 -d 2>/dev/null)" || return 1
  decoded_port="$(printf '%s' "${decoded}" | jq -er '.port | select(type == "number" and floor == . and . >= 1 and . <= 65535)')" || return 1
  decoded_expiry="$(printf '%s' "${decoded}" | jq -er '.expires_at | select(type == "string" and length > 0)')" || return 1
  expiry_epoch="$(jq -nr --arg expires "${decoded_expiry}" '$expires | fromdateiso8601')" || return 1
  now_epoch="$(date +%s)"
  [ "${expiry_epoch}" -gt "${now_epoch}" ] || return 1

  PORT="${decoded_port}"
  EXPIRES_AT="${decoded_expiry}"
}

request_signature() {
  prepare_token_request_file || return 1
  if ! response="$(
    curl -fsS -m 10 \
      --interface tun0 \
      --connect-to "${TLS_HOSTNAME}:19999:$(connect_gateway):19999" \
      --cacert "${PIA_CA_CERT}" \
      -G \
      --data-urlencode "token@${TOKEN_REQUEST_TMP}" \
      "https://${TLS_HOSTNAME}:19999/getSignature"
  )"; then
    cleanup_request_files
    return 1
  fi
  cleanup_request_files

  PAYLOAD="$(printf '%s' "${response}" | jq -er 'select(type == "object" and .status == "OK") | .payload | select(type == "string" and length > 0)')" || return 1
  SIGNATURE="$(printf '%s' "${response}" | jq -er 'select(type == "object" and .status == "OK") | .signature | select(type == "string" and length > 0)')" || return 1
  response=""
  PORT=""
  EXPIRES_AT=""
  validate_allocation_values
}

load_existing_allocation() {
  [ -s "${PF_DIR}/payload" ] || return 1
  [ -s "${PF_DIR}/signature" ] || return 1
  [ -s "${PF_DIR}/expires-at" ] || return 1
  [ -s "${PF_DIR}/port" ] || return 1

  PAYLOAD="$(cat "${PF_DIR}/payload" 2>/dev/null)" || return 1
  SIGNATURE="$(cat "${PF_DIR}/signature" 2>/dev/null)" || return 1
  stored_expiry="$(cat "${PF_DIR}/expires-at" 2>/dev/null)" || return 1
  record_generation="$(jq -er '
    select(type == "object" and (keys | sort) == ["generation", "port"]) |
    .generation | select(type == "string")
  ' "${PF_DIR}/port")" || return 1
  record_port="$(jq -er '
    select(type == "object" and (keys | sort) == ["generation", "port"]) |
    .port | select(type == "number" and floor == . and . >= 1 and . <= 65535)
  ' "${PF_DIR}/port")" || return 1
  [ "${record_generation}" = "${GENERATION}" ] || return 1

  PORT=""
  EXPIRES_AT=""
  validate_allocation_values || return 1
  [ "${stored_expiry}" = "${EXPIRES_AT}" ] || return 1
  [ "${record_port}" = "${PORT}" ] || return 1
  generation_is_still_ready
}

publish_allocation() {
  PAYLOAD_PUBLISH_TMP="${PF_DIR}/.payload.publish.$$"
  SIGNATURE_PUBLISH_TMP="${PF_DIR}/.signature.publish.$$"
  EXPIRES_PUBLISH_TMP="${PF_DIR}/.expires-at.publish.$$"
  PORT_PUBLISH_TMP="${PF_DIR}/.port.publish.$$"

  write_exact_file "${PAYLOAD_PUBLISH_TMP}" "${PAYLOAD}" || {
    cleanup_publication_files
    return 1
  }
  write_exact_file "${SIGNATURE_PUBLISH_TMP}" "${SIGNATURE}" || {
    cleanup_publication_files
    return 1
  }
  write_exact_file "${EXPIRES_PUBLISH_TMP}" "${EXPIRES_AT}" || {
    cleanup_publication_files
    return 1
  }
  write_exact_file "${PORT_PUBLISH_TMP}" "$(printf '{\"generation\":\"%s\",\"port\":%s}' "${GENERATION}" "${PORT}")" || {
    cleanup_publication_files
    return 1
  }

  generation_is_still_ready || {
    cleanup_publication_files
    return 1
  }
  mv -f -- "${PAYLOAD_PUBLISH_TMP}" "${PF_DIR}/payload" || {
    cleanup_publication_files
    return 1
  }
  PAYLOAD_PUBLISH_TMP=""
  mv -f -- "${SIGNATURE_PUBLISH_TMP}" "${PF_DIR}/signature" || {
    cleanup_publication_files
    return 1
  }
  SIGNATURE_PUBLISH_TMP=""
  mv -f -- "${EXPIRES_PUBLISH_TMP}" "${PF_DIR}/expires-at" || {
    cleanup_publication_files
    return 1
  }
  EXPIRES_PUBLISH_TMP=""
  mv -f -- "${PORT_PUBLISH_TMP}" "${PF_DIR}/port" || {
    cleanup_publication_files
    return 1
  }
  PORT_PUBLISH_TMP=""
  sync -d "${PF_DIR}"
}

prepare_binding_request_files() {
  PAYLOAD_REQUEST_TMP="${PF_DIR}/.payload.request.$$"
  SIGNATURE_REQUEST_TMP="${PF_DIR}/.signature.request.$$"
  write_exact_file "${PAYLOAD_REQUEST_TMP}" "${PAYLOAD}" || {
    cleanup_request_files
    return 1
  }
  write_exact_file "${SIGNATURE_REQUEST_TMP}" "${SIGNATURE}" || {
    cleanup_request_files
    return 1
  }
}

bind_port() {
  prepare_binding_request_files || return 1
  if ! response="$(
    curl -fsS -m 10 \
      --interface tun0 \
      --connect-to "${TLS_HOSTNAME}:19999:$(connect_gateway):19999" \
      --cacert "${PIA_CA_CERT}" \
      -G \
      --data-urlencode "payload@${PAYLOAD_REQUEST_TMP}" \
      --data-urlencode "signature@${SIGNATURE_REQUEST_TMP}" \
      "https://${TLS_HOSTNAME}:19999/bindPort"
  )"; then
    cleanup_request_files
    return 1
  fi
  cleanup_request_files

  printf '%s' "${response}" | jq -e 'type == "object" and .status == "OK"' >/dev/null
}

obtain_or_reuse_allocation() {
  if load_existing_allocation; then
    return 0
  fi

  PAYLOAD=""
  SIGNATURE=""
  PORT=""
  EXPIRES_AT=""
  request_signature || return 1
  generation_is_still_ready || return 1
  publish_allocation || return 1
  log "published active generation port metadata"
}

renew_generation() {
  load_generation_metadata || return 1
  obtain_or_reuse_allocation || return 1
  generation_is_still_ready || return 1
  bind_port || return 1
  generation_is_still_ready || return 1
  log "bound active generation port"
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
  case "${MAX_CYCLES}" in
    ""|*[!0-9]*) log "invalid helper cycle limit"; return 1 ;;
  esac

  trap cleanup_temporary_files EXIT
  trap 'exit 0' HUP INT TERM
  completed_cycles=0
  while true; do
    if ! snapshot_ready_generation; then
      log "waiting for an active ready generation"
      [ "${RUN_ONCE}" = "1" ] && return 0
      sleep "${POLL_SECONDS}"
      continue
    fi

    if renew_generation; then
      completed_cycles=$((completed_cycles + 1))
      [ "${RUN_ONCE}" = "1" ] && return 0
      [ "${MAX_CYCLES}" -gt 0 ] && [ "${completed_cycles}" -ge "${MAX_CYCLES}" ] && return 0
      wait_while_generation_ready "${RENEW_SECONDS}" || true
      continue
    fi

    log "port-forward operation failed; retrying while generation remains ready"
    [ "${RUN_ONCE}" = "1" ] && return 1
    wait_while_generation_ready 30 || true
  done
}

main "$@"
