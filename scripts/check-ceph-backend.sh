#!/usr/bin/env bash
set -euo pipefail

TALOSCONFIG="${TALOSCONFIG:-./talosconfig}"
TALOS_ENDPOINT="${TALOS_ENDPOINT:-172.16.16.10}"
NODES="${NODES:-172.16.16.11 172.16.16.12 172.16.16.13}"
ROOK_NS="${ROOK_NS:-rook-ceph}"

echo "== Kubernetes nodes =="
kubectl get nodes -o wide

echo
echo "== Ceph status =="
kubectl -n "${ROOK_NS}" exec deploy/rook-ceph-tools -- ceph status || true

echo
echo "== Ceph health detail =="
kubectl -n "${ROOK_NS}" exec deploy/rook-ceph-tools -- ceph health detail || true

echo
echo "== Ceph OSD tree =="
kubectl -n "${ROOK_NS}" exec deploy/rook-ceph-tools -- ceph osd tree || true

echo
echo "== Talos Ceph backend addresses/routes/links/schematic =="
for node in ${NODES}; do
  echo
  echo "### ${node} addresses"
  talosctl --talosconfig "${TALOSCONFIG}" \
    --endpoints "${TALOS_ENDPOINT}" \
    --nodes "${node}" \
    get addresses | grep -E '192\.168\.16' || true

  echo
  echo "### ${node} routes"
  talosctl --talosconfig "${TALOSCONFIG}" \
    --endpoints "${TALOS_ENDPOINT}" \
    --nodes "${node}" \
    get routes | grep -E '192\.168\.16' || true

  echo
  echo "### ${node} ceph backend links"
  talosctl --talosconfig "${TALOSCONFIG}" \
    --endpoints "${TALOS_ENDPOINT}" \
    --nodes "${node}" \
    get linkstatuses | awk 'NR == 1 || $0 ~ /ceph-tb/' || true

  echo
  echo "### ${node} schematic"
  talosctl --talosconfig "${TALOSCONFIG}" \
    --endpoints "${TALOS_ENDPOINT}" \
    --nodes "${node}" \
    get extensions | grep -E 'schematic|thunderbolt|NAME' || true
done
