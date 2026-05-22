#!/usr/bin/env bash
set -euo pipefail

APP="${1:-}"

VAULT="${OP_VAULT:-kubernetes}"
NFS_SERVER="${VOLSYNC_NFS_SERVER:-storage.cooney.site}"
NFS_EXPORT="${VOLSYNC_NFS_EXPORT:-/home-ops-backups}"
NFS_MOUNT_DIR=""

if [[ -z "${APP}" ]]; then
  echo "Usage: $0 <app-name>"
  echo "Example: $0 sabnzbd"
  exit 1
fi

ITEM="${APP}"
REPOSITORY_VALUE="filesystem:///mnt/repository/${APP}"
PASSWORD_VALUE="$(openssl rand -base64 48)"

cleanup() {
  if [[ -n "${NFS_MOUNT_DIR}" && -d "${NFS_MOUNT_DIR}" ]]; then
    if mount | grep -q " on ${NFS_MOUNT_DIR} "; then
      umount "${NFS_MOUNT_DIR}" >/dev/null 2>&1 || sudo umount "${NFS_MOUNT_DIR}" >/dev/null 2>&1 || true
    fi
    rmdir "${NFS_MOUNT_DIR}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

ensure_nfs_repository_directory() {
  NFS_MOUNT_DIR="$(mktemp -d -t volsync-nfs.XXXXXX)"

  echo "Mounting NFS export: ${NFS_SERVER}:${NFS_EXPORT}"
  echo "Temporary mount: ${NFS_MOUNT_DIR}"

  sudo mount_nfs "${NFS_SERVER}:${NFS_EXPORT}" "${NFS_MOUNT_DIR}"

  echo "Ensuring repository directory exists: ${NFS_EXPORT}/${APP}"
  mkdir -p "${NFS_MOUNT_DIR}/${APP}"
  chmod 0775 "${NFS_MOUNT_DIR}/${APP}" || true

  echo "Ensuring repository directory ownership is 1000:1000"
  sudo chown 1000:1000 "${NFS_MOUNT_DIR}/${APP}"

  echo "Repository directory ready."
  echo
}

echo "Vault: ${VAULT}"
echo "Item:  ${ITEM}"
echo "Repo:  ${REPOSITORY_VALUE}"
echo

ensure_nfs_repository_directory

if op item get "${ITEM}" --vault "${VAULT}" >/dev/null 2>&1; then
  echo "Updating existing 1Password item: ${VAULT}/${ITEM}"

  op item edit "${ITEM}" \
    --vault "${VAULT}" \
    "KOPIA_REPOSITORY[text]=${REPOSITORY_VALUE}" \
    "KOPIA_PASSWORD[concealed]=${PASSWORD_VALUE}" \
    >/dev/null
else
  echo "Creating new 1Password item: ${VAULT}/${ITEM}"

  op item create \
    --vault "${VAULT}" \
    --category "API Credential" \
    --title "${ITEM}" \
    "KOPIA_REPOSITORY[text]=${REPOSITORY_VALUE}" \
    "KOPIA_PASSWORD[concealed]=${PASSWORD_VALUE}" \
    >/dev/null
fi

echo
echo "Done."
echo
echo "External Secrets references should use:"
echo "  op://kubernetes/${APP}/KOPIA_REPOSITORY"
echo "  op://kubernetes/${APP}/KOPIA_PASSWORD"
echo
echo "NFS repository path created/verified:"
echo "  ${NFS_EXPORT}/${APP}"
echo "Recommended ownership applied: 1000:1000"
echo