#!/usr/bin/env sh
set -eu

RUNTIME_DIR="${RUNTIME_DIR:-/run/pia}"
READY_LINK="${RUNTIME_DIR}/ready"
QBIT_URL="${QBIT_URL:-http://127.0.0.1:80}"
POLL_SECONDS="${POLL_SECONDS:-5}"
RUN_ONCE="${PORT_SYNC_RUN_ONCE:-0}"
MAX_LOOPS="${PORT_SYNC_MAX_LOOPS:-0}"

SNAPSHOT_TARGET=""
GENERATION=""
GENERATION_DIR=""
PORT=""
LAST_PORT=""

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
  [ "$(cat "${generation_dir}/generation" 2>/dev/null)" = "${generation}" ] || return 1
  [ "$(readlink "${READY_LINK}" 2>/dev/null)" = "${target}" ] || return 1

  SNAPSHOT_TARGET="${target}"
  GENERATION="${generation}"
  GENERATION_DIR="${generation_dir}"
}

generation_is_still_ready() {
  [ -n "${GENERATION}" ] || return 1
  [ "$(readlink "${READY_LINK}" 2>/dev/null)" = "${SNAPSHOT_TARGET}" ] || return 1
  [ "$(cat "${GENERATION_DIR}/generation" 2>/dev/null)" = "${GENERATION}" ] || return 1
}

read_active_port() {
  snapshot_ready_generation || return 1
  port_file="${GENERATION_DIR}/pf/port"
  [ -s "${port_file}" ] || return 1

  jq -e '
    type == "object" and
    (keys | sort) == ["generation", "port"] and
    (.generation | type == "string") and
    (.port | type == "number" and floor == . and . >= 1 and . <= 65535)
  ' "${port_file}" >/dev/null || return 1

  record_generation="$(jq -er '.generation' "${port_file}")" || return 1
  port="$(jq -er '.port | tostring' "${port_file}")" || return 1
  [ "${record_generation}" = "${GENERATION}" ] || return 1
  case "${port}" in
    ""|*[!0-9]*) return 1 ;;
  esac
  [ "${port}" -ge 1 ] && [ "${port}" -le 65535 ] || return 1
  generation_is_still_ready || return 1
  PORT="${port}"
}

sync_port_once() {
  read_active_port || return 1

  if [ "${LAST_PORT}" = "${PORT}" ]; then
    return 0
  fi

  current="$(curl -fsS -m 5 "${QBIT_URL}/api/v2/app/preferences" | jq -er '.listen_port | select(type == "number" and floor == . and . >= 1 and . <= 65535)')" || return 1
  generation_is_still_ready || return 1

  if [ "${current}" != "${PORT}" ]; then
    curl -fsS -m 5 \
      -X POST \
      --data-urlencode "json={\"listen_port\":${PORT},\"random_port\":false}" \
      "${QBIT_URL}/api/v2/app/setPreferences" >/dev/null || return 1
    generation_is_still_ready || return 1
    log "updated qBittorrent for the active generation"
  fi

  LAST_PORT="${PORT}"
}

main() {
  loops=0
  while true; do
    if ! sync_port_once; then
      log "waiting for valid active-generation port metadata"
    fi
    loops=$((loops + 1))
    [ "${RUN_ONCE}" = "1" ] && return 0
    [ "${MAX_LOOPS}" -gt 0 ] && [ "${loops}" -ge "${MAX_LOOPS}" ] && return 0
    sleep "${POLL_SECONDS}"
  done
}

main "$@"
