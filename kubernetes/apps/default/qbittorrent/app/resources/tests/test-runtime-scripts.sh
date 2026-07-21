#!/usr/bin/env bash
set -euo pipefail

resources_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pf_script="${resources_dir}/pia-port-forward.sh"
sync_script="${resources_dir}/port-sync.sh"
stub_dir="${resources_dir}/tests/stubs"
test_root="$(mktemp -d)"
trap 'rm -r "${test_root}"' EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  grep -F -- "$2" "$1" >/dev/null || fail "$1 does not contain $2"
}

assert_not_contains() {
  if grep -F -- "$2" "$1" >/dev/null; then
    fail "$1 contains forbidden value $2"
  fi
}

assert_count() {
  count="$(grep -F -c -- "$2" "$1" || true)"
  [ "${count}" = "$3" ] || fail "$1 contains $2 ${count} times, want $3"
}

file_mode() {
  if stat -c '%a' "$1" >/dev/null 2>&1; then
    stat -c '%a' "$1"
  else
    stat -f '%Lp' "$1"
  fi
}

create_generation() {
  runtime="$1"
  generation="$2"
  port="$3"
  mkdir -p "${runtime}/sessions/${generation}/pf"
  printf '%s\n' "${generation}" > "${runtime}/sessions/${generation}/generation"
  printf '%s\n' 'ca.example.invalid' > "${runtime}/sessions/${generation}/tls-hostname"
  printf '%s\n' '10.0.0.1' > "${runtime}/sessions/${generation}/pf-gateway"
  printf '%s\n' 'fixture-token-never-log' > "${runtime}/sessions/${generation}/pia.token"
  if [ -n "${port}" ]; then
    printf '{"generation":"%s","port":%s}\n' "${generation}" "${port}" > "${runtime}/sessions/${generation}/pf/port"
    chmod 0600 "${runtime}/sessions/${generation}/pf/port"
  fi
}

run_pf() {
  runtime="$1"
  curl_log="$2"
  stdout="$3"
  stderr="$4"
  shift 4
  env \
    PATH="${stub_dir}:${PATH}" \
    CURL_LOG="${curl_log}" \
    RUNTIME_DIR="${runtime}" \
    READY_LINK_FOR_TEST="${runtime}/ready" \
    PIA_PF_RUN_ONCE=1 \
    TEST_SIGNATURE_RESPONSE="${signature_response}" \
    TEST_BIND_RESPONSE='{"status":"OK"}' \
    "$@" \
    sh "${pf_script}" >"${stdout}" 2>"${stderr}"
}

run_sync() {
  runtime="$1"
  curl_log="$2"
  stdout="$3"
  stderr="$4"
  shift 4
  env \
    PATH="${stub_dir}:${PATH}" \
    CURL_LOG="${curl_log}" \
    RUNTIME_DIR="${runtime}" \
    READY_LINK_FOR_TEST="${runtime}/ready" \
    SLEEP_SWITCH_MARKER="${runtime}/.sleep-switched" \
    TEST_QBIT_CURRENT=40000 \
    "$@" \
    sh "${sync_script}" >"${stdout}" 2>"${stderr}"
}

payload_json='{"port":49152,"expires_at":"2030-01-01T00:00:00Z"}'
payload="$(printf '%s' "${payload_json}" | base64 | tr -d '\n')"
signature='fixture-signature-never-log'
signature_response="$(jq -cn --arg payload "${payload}" --arg signature "${signature}" '{status:"OK",payload:$payload,signature:$signature}')"

runtime="${test_root}/pf-success"
mkdir -p "${runtime}/sessions"
create_generation "${runtime}" gen-one ""
ln -s sessions/gen-one "${runtime}/ready"
curl_log="${test_root}/pf-success.curl"
: > "${curl_log}"
run_pf "${runtime}" "${curl_log}" "${test_root}/pf-success.out" "${test_root}/pf-success.err"

pf_dir="${runtime}/sessions/gen-one/pf"
[ "$(cat "${pf_dir}/port")" = '{"generation":"gen-one","port":49152}' ] || fail 'PF port record is not strict generation JSON'
[ "$(cat "${pf_dir}/payload")" = "${payload}" ] || fail 'payload publication failed'
[ "$(cat "${pf_dir}/signature")" = "${signature}" ] || fail 'signature publication failed'
[ "$(cat "${pf_dir}/expires-at")" = '2030-01-01T00:00:00Z' ] || fail 'expiry publication failed'
for file in payload signature expires-at port; do
  [ "$(file_mode "${pf_dir}/${file}")" = 600 ] || fail "${file} mode is not 0600"
done
assert_contains "${curl_log}" '--interface tun0'
assert_contains "${curl_log}" '--connect-to ca.example.invalid:19999:10.0.0.1:19999'
assert_contains "${curl_log}" "token@${runtime}/sessions/gen-one/pia.token"
assert_contains "${curl_log}" '/getSignature'
assert_contains "${curl_log}" '/bindPort'
for output in "${curl_log}" "${test_root}/pf-success.out" "${test_root}/pf-success.err"; do
  assert_not_contains "${output}" 'fixture-token-never-log'
  assert_not_contains "${output}" "${payload}"
  assert_not_contains "${output}" "${signature}"
  assert_not_contains "${output}" "${signature_response}"
done

runtime="${test_root}/pf-wait"
mkdir -p "${runtime}/sessions"
curl_log="${test_root}/pf-wait.curl"
: > "${curl_log}"
run_pf "${runtime}" "${curl_log}" "${test_root}/pf-wait.out" "${test_root}/pf-wait.err"
[ ! -s "${curl_log}" ] || fail 'PF helper contacted an API without ready metadata'
assert_contains "${test_root}/pf-wait.err" 'waiting for an active ready generation'

runtime="${test_root}/pf-race"
mkdir -p "${runtime}/sessions"
create_generation "${runtime}" gen-old ""
create_generation "${runtime}" gen-new ""
ln -s sessions/gen-old "${runtime}/ready"
curl_log="${test_root}/pf-race.curl"
: > "${curl_log}"
if run_pf "${runtime}" "${curl_log}" "${test_root}/pf-race.out" "${test_root}/pf-race.err" SWITCH_READY_ON_SIGNATURE=gen-new; then
  fail 'PF helper accepted a generation change during signature acquisition'
fi
[ ! -s "${runtime}/sessions/gen-old/pf/port" ] || fail 'stale generation port was published'
[ ! -s "${runtime}/sessions/gen-new/pf/port" ] || fail 'old data was written into the new generation'
assert_count "${curl_log}" '/bindPort' 0

runtime="${test_root}/pf-error-redaction"
mkdir -p "${runtime}/sessions"
create_generation "${runtime}" gen-one ""
ln -s sessions/gen-one "${runtime}/ready"
curl_log="${test_root}/pf-error-redaction.curl"
: > "${curl_log}"
error_response='{"status":"ERROR","payload":"response-body-never-log","signature":"signature-never-log"}'
if run_pf "${runtime}" "${curl_log}" "${test_root}/pf-error-redaction.out" "${test_root}/pf-error-redaction.err" TEST_SIGNATURE_RESPONSE="${error_response}"; then
  fail 'PF helper accepted an unsuccessful signature response'
fi
assert_not_contains "${test_root}/pf-error-redaction.err" 'response-body-never-log'
assert_not_contains "${test_root}/pf-error-redaction.err" 'signature-never-log'
assert_not_contains "${test_root}/pf-error-redaction.err" "${error_response}"

assert_contains "${pf_script}" 'RENEW_SECONDS:-900'
assert_not_contains "${pf_script}" 'PIA_USERNAME'
assert_not_contains "${pf_script}" 'PIA_PASSWORD'
assert_not_contains "${pf_script}" 'VPN_PORT_FORWARDING_USERNAME'
assert_not_contains "${pf_script}" 'VPN_PORT_FORWARDING_PASSWORD'

run_sync_case() {
  name="$1"
  generation="$2"
  json="$3"
  expected_posts="$4"
  runtime="${test_root}/${name}"
  mkdir -p "${runtime}/sessions"
  create_generation "${runtime}" "${generation}" ""
  ln -s "sessions/${generation}" "${runtime}/ready"
  if [ -n "${json}" ]; then
    printf '%s\n' "${json}" > "${runtime}/sessions/${generation}/pf/port"
    chmod 0600 "${runtime}/sessions/${generation}/pf/port"
  fi
  curl_log="${test_root}/${name}.curl"
  : > "${curl_log}"
  run_sync "${runtime}" "${curl_log}" "${test_root}/${name}.out" "${test_root}/${name}.err" PORT_SYNC_RUN_ONCE=1
  assert_count "${curl_log}" '/setPreferences' "${expected_posts}"
}

run_sync_case sync-valid gen-one '{"generation":"gen-one","port":49152}' 1
assert_contains "${test_root}/sync-valid.curl" 'json={"listen_port":49152,"random_port":false}'
run_sync_case sync-malformed gen-one '{not-json' 0
run_sync_case sync-mismatch gen-one '{"generation":"gen-old","port":49152}' 0
run_sync_case sync-zero gen-one '{"generation":"gen-one","port":0}' 0
run_sync_case sync-high gen-one '{"generation":"gen-one","port":65536}' 0
run_sync_case sync-unknown gen-one '{"generation":"gen-one","port":49152,"extra":true}' 0

runtime="${test_root}/sync-repeat"
mkdir -p "${runtime}/sessions"
create_generation "${runtime}" gen-one 49152
ln -s sessions/gen-one "${runtime}/ready"
curl_log="${test_root}/sync-repeat.curl"
: > "${curl_log}"
run_sync "${runtime}" "${curl_log}" "${test_root}/sync-repeat.out" "${test_root}/sync-repeat.err" PORT_SYNC_MAX_LOOPS=2
assert_count "${curl_log}" '/setPreferences' 1

runtime="${test_root}/sync-generation-change"
mkdir -p "${runtime}/sessions"
create_generation "${runtime}" gen-one 49152
create_generation "${runtime}" gen-two 49153
ln -s sessions/gen-one "${runtime}/ready"
curl_log="${test_root}/sync-generation-change.curl"
: > "${curl_log}"
run_sync "${runtime}" "${curl_log}" "${test_root}/sync-generation-change.out" "${test_root}/sync-generation-change.err" PORT_SYNC_MAX_LOOPS=2 SWITCH_READY_ON_SLEEP=gen-two
assert_count "${curl_log}" '/setPreferences' 2
assert_contains "${curl_log}" 'json={"listen_port":49152,"random_port":false}'
assert_contains "${curl_log}" 'json={"listen_port":49153,"random_port":false}'

printf 'qBittorrent runtime integration script tests passed\n'
