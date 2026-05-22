#!/usr/bin/env bash
set -euo pipefail

APP="${1:-}"

VAULT="${OP_VAULT:-kubernetes}"

if [[ -z "${APP}" ]]; then
  echo "Usage: $0 <app-name>"
  echo "Example: $0 sabnzbd"
  exit 1
fi

ITEM="${APP}"
REPOSITORY_VALUE="filesystem:///mnt/repository/${APP}"
PASSWORD_VALUE="$(openssl rand -base64 48)"

echo "Vault: ${VAULT}"
echo "Item:  ${ITEM}"
echo "Repo:  ${REPOSITORY_VALUE}"
echo

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
echo "NFS repository path expected:"
echo "  /home-ops-backups/${APP}"