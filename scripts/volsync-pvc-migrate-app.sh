#!/usr/bin/env bash
set -euo pipefail

APP="${1:-}"
NAMESPACE="${2:-default}"

if [[ -z "${APP}" ]]; then
  echo "Usage: $0 <app> [namespace]"
  exit 1
fi

KOPIA_POD="${APP}-kopia-debug"
RESTORE_POD="${APP}-restore-manual"

confirm() {
  local prompt="$1"
  read -r -p "${prompt} [y/N] " ans
  [[ "${ans}" == "y" || "${ans}" == "Y" ]]
}

delete_pod() {
  kubectl -n "${NAMESPACE}" delete pod "$1" --ignore-not-found >/dev/null 2>&1 || true
}

echo "==> App: ${APP}"
echo "==> Namespace: ${NAMESPACE}"

echo
echo "==> Current status"
kubectl -n "${NAMESPACE}" get deploy "${APP}" || true
kubectl -n "${NAMESPACE}" get hr "${APP}" || true
kubectl -n "${NAMESPACE}" get pvc "${APP}" || true
kubectl -n "${NAMESPACE}" get replicationsource "${APP}" || true
kubectl -n "${NAMESPACE}" get replicationdestination "${APP}-dst" || true

echo
echo "==> Scaling ${APP} to 0 and suspending HelmRelease"
kubectl -n "${NAMESPACE}" scale deploy "${APP}" --replicas=0 2>/dev/null || true
kubectl -n "${NAMESPACE}" patch helmrelease "${APP}" --type merge -p '{"spec":{"suspend":true}}' 2>/dev/null || true
kubectl -n "${NAMESPACE}" rollout status deploy/"${APP}" --timeout=5m || true

echo
echo "==> PVC usage"
kubectl -n "${NAMESPACE}" describe pvc "${APP}" | grep -A5 "Used By" || true

if ! confirm "Continue only if pvc/${APP} is not in use. Continue?"; then
  echo "Aborted."
  exit 1
fi

echo
echo "==> Listing Kopia snapshots for ${APP}"
delete_pod "${KOPIA_POD}"

cat <<YAML | kubectl apply -f -
---
apiVersion: v1
kind: Pod
metadata:
  name: ${KOPIA_POD}
  namespace: ${NAMESPACE}
spec:
  restartPolicy: Never
  securityContext:
    runAsUser: 1000
    runAsGroup: 1000
    fsGroup: 1000
    fsGroupChangePolicy: OnRootMismatch
  containers:
    - name: kopia
      image: kopia/kopia:latest
      env:
        - name: USER
          value: kopia
        - name: HOME
          value: /tmp
        - name: KOPIA_CONFIG_PATH
          value: /tmp/kopia.config
        - name: KOPIA_CACHE_DIRECTORY
          value: /tmp/kopia-cache
        - name: KOPIA_LOG_DIR
          value: /tmp/kopia-logs
        - name: TZ
          value: America/New_York
        - name: TZ
          value: America/New_York
        - name: KOPIA_PASSWORD
          valueFrom:
            secretKeyRef:
              name: ${APP}-volsync-secret
              key: KOPIA_PASSWORD
      command:
        - sh
        - -c
        - |
          set -eu
          mkdir -p /tmp/kopia-cache /tmp/kopia-logs
          kopia repository connect filesystem --path=/mnt/repository/${APP}
          kopia snapshot list --all --json
          sleep 3600
      volumeMounts:
        - name: repo
          mountPath: /mnt/repository
  volumes:
    - name: repo
      nfs:
        server: storage.cooney.site
        path: /home-ops-backups
YAML

kubectl -n "${NAMESPACE}" wait --for=condition=Ready "pod/${KOPIA_POD}" --timeout=120s || true

for i in {1..30}; do
  if kubectl -n "${NAMESPACE}" logs "${KOPIA_POD}" 2>/dev/null | grep -q '\['; then
    break
  fi
  sleep 2
done

SNAPSHOT_LOG="$(mktemp)"
kubectl -n "${NAMESPACE}" logs "${KOPIA_POD}" > "${SNAPSHOT_LOG}" || true

python3 - "${SNAPSHOT_LOG}" <<'PYFMT'
import json
import sys
from datetime import datetime

path = sys.argv[1]
raw = open(path).read()

start = raw.find("[")
end = raw.rfind("]")

if start == -1 or end == -1 or end < start:
    print(raw)
    raise SystemExit(0)

data = json.loads(raw[start:end + 1])

print()
print(f"{'LOCAL_TIME':<24} {'SNAPSHOT_ID':<40} {'SIZE':<12} {'RETENTION'}")
print("-" * 105)

for item in data:
    utc = item.get("startTime", "")
    snap = ((item.get("rootEntry") or {}).get("obj")) or item.get("id") or ""
    size = ((item.get("stats") or {}).get("totalSize")) or ""
    retention = ",".join(item.get("retentionReason") or [])

    local = utc
    try:
        if "." in utc:
            base, _ = utc.split(".", 1)
            utc_parse = base + "Z"
        else:
            utc_parse = utc
        dt = datetime.fromisoformat(utc_parse.replace("Z", "+00:00"))
        local = dt.astimezone().strftime("%Y-%m-%d %H:%M:%S %Z")
    except Exception:
        pass

    print(f"{local:<24} {snap:<40} {str(size):<12} {retention}")
PYFMT

rm -f "${SNAPSHOT_LOG}"

echo
read -r -p "Paste snapshot root/ID to restore for ${APP}: " SNAPSHOT_ID

if [[ -z "${SNAPSHOT_ID}" ]]; then
  echo "No snapshot ID provided. Aborted."
  delete_pod "${KOPIA_POD}"
  exit 1
fi

delete_pod "${KOPIA_POD}"

echo
echo "==> Deleting old PVC ${APP}"
if ! confirm "This deletes pvc/${APP}. Continue?"; then
  echo "Aborted."
  exit 1
fi

kubectl -n "${NAMESPACE}" delete pvc "${APP}"

echo
echo "==> Reconciling Kustomization to recreate PVC"
flux reconcile ks "${APP}" -n "${NAMESPACE}" --with-source || true

echo
echo "==> Waiting for new PVC to become Bound"
for i in {1..60}; do
  phase="$(kubectl -n "${NAMESPACE}" get pvc "${APP}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  echo "PVC phase: ${phase:-missing}"
  [[ "${phase}" == "Bound" ]] && break
  sleep 5
done

kubectl -n "${NAMESPACE}" get pvc "${APP}"

if ! confirm "PVC is Bound. Restore snapshot ${SNAPSHOT_ID} into /config?"; then
  echo "Aborted."
  exit 1
fi

echo
echo "==> Restoring snapshot into ${APP} PVC"
delete_pod "${RESTORE_POD}"

cat <<YAML | kubectl apply -f -
---
apiVersion: v1
kind: Pod
metadata:
  name: ${RESTORE_POD}
  namespace: ${NAMESPACE}
spec:
  restartPolicy: Never
  securityContext:
    runAsUser: 1000
    runAsGroup: 1000
    fsGroup: 1000
    fsGroupChangePolicy: OnRootMismatch
  containers:
    - name: kopia
      image: kopia/kopia:latest
      env:
        - name: USER
          value: kopia
        - name: HOME
          value: /tmp
        - name: KOPIA_CONFIG_PATH
          value: /tmp/kopia.config
        - name: KOPIA_CACHE_DIRECTORY
          value: /tmp/kopia-cache
        - name: KOPIA_LOG_DIR
          value: /tmp/kopia-logs
        - name: SNAPSHOT_ID
          value: "${SNAPSHOT_ID}"
        - name: KOPIA_PASSWORD
          valueFrom:
            secretKeyRef:
              name: ${APP}-volsync-secret
              key: KOPIA_PASSWORD
      command:
        - sh
        - -c
        - |
          set -eux
          mkdir -p /tmp/kopia-cache /tmp/kopia-logs
          rm -rf /config/* /config/.[!.]* /config/..?* 2>/dev/null || true
          kopia repository connect filesystem --path=/mnt/repository/${APP}
          kopia snapshot restore "\${SNAPSHOT_ID}" /config
          echo RESTORE_DONE
          echo
          echo "Restored size:"
          du -sh /config
          echo
          echo "Restored file sample:"
          find /config -maxdepth 3 -type f | sort | head -150
      volumeMounts:
        - name: config
          mountPath: /config
        - name: repo
          mountPath: /mnt/repository
  volumes:
    - name: config
      persistentVolumeClaim:
        claimName: ${APP}
    - name: repo
      nfs:
        server: storage.cooney.site
        path: /home-ops-backups
YAML

kubectl -n "${NAMESPACE}" logs -f "${RESTORE_POD}"

echo
echo "==> Releasing restore pod"
delete_pod "${RESTORE_POD}"

echo
if ! confirm "Restored file listing above looks correct. Start ${APP}?"; then
  echo "Leaving ${APP} stopped."
  echo "Restore pod has completed and been deleted, so the PVC is released."
  exit 0
fi

echo
echo "==> Starting ${APP}"
kubectl -n "${NAMESPACE}" patch helmrelease "${APP}" --type merge -p '{"spec":{"suspend":false}}'
flux reconcile hr "${APP}" -n "${NAMESPACE}" --force --timeout=10m
kubectl -n "${NAMESPACE}" rollout status deploy/"${APP}" --timeout=10m || true

echo
echo "==> Final status"
kubectl -n "${NAMESPACE}" get deploy "${APP}" || true
kubectl -n "${NAMESPACE}" get pvc "${APP}" || true
kubectl -n "${NAMESPACE}" get replicationsource "${APP}" || true
kubectl -n "${NAMESPACE}" get replicationdestination "${APP}-dst" || true

echo
echo "Done. If ${APP} validates in the UI, run the Git resume helper and commit."
