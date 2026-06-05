set -eu

PORT_FILE="/tmp/gluetun/forwarded_port"
QBIT_URL="http://127.0.0.1:80"
INTERVAL="60"

last=""

echo "Starting qBittorrent forwarded-port sync"
echo "Port file: ${PORT_FILE}"
echo "qBittorrent URL: ${QBIT_URL}"

while true; do
  if [ ! -s "${PORT_FILE}" ]; then
    echo "Waiting for forwarded port file: ${PORT_FILE}"
    sleep "${INTERVAL}"
    continue
  fi

  port="$(tr -dc '0-9' < "${PORT_FILE}")"

  if [ -z "${port}" ]; then
    echo "Forwarded port file is present but empty/unparseable"
    sleep "${INTERVAL}"
    continue
  fi

  current="$(curl -fsS "${QBIT_URL}/api/v2/app/preferences" \
    | sed -n 's/.*"listen_port":\([0-9][0-9]*\).*/\1/p' \
    | head -1 || true)"

  if [ "${current}" = "${port}" ]; then
    last="${port}"
    sleep "${INTERVAL}"
    continue
  fi

  if [ -z "${current}" ]; then
    current="unknown"
  fi

  if [ "${last}" != "${port}" ]; then
    echo "Updating qBittorrent listening port from ${current} to ${port}"
  fi

  curl -fsS \
    -X POST \
    --data-urlencode "json={\"listen_port\":${port},\"random_port\":false}" \
    "${QBIT_URL}/api/v2/app/setPreferences"

  last="${port}"
  sleep "${INTERVAL}"
done
