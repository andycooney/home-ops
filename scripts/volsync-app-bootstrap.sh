#!/usr/bin/env bash
set -euo pipefail

APP="${1:-}"

VAULT="${OP_VAULT:-kubernetes}"

if [[ -z "${APP}" ]]; then
  echo "Usage: $0 <app-name>"
  echo "Example: $0 sabnzbd"
  exit 1
fi

require_command() {
  local command_name="${1}"

  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "ERROR: Required command not found: ${command_name}" >&2
    exit 1
  fi
}

require_command op
require_command openssl
require_command kubectl

if ! op account get >/dev/null 2>&1; then
  echo "ERROR: 1Password CLI is not signed in or cannot access an account." >&2
  echo "Run: eval $(op signin)" >&2
  exit 1
fi

ITEM="${APP}"
REPOSITORY_VALUE="filesystem:///mnt/repository/${APP}"
PASSWORD_VALUE="$(openssl rand -base64 48)"

op_field_exists() {
  local field_name="${1}"

  op item get "${ITEM}" \
    --vault "${VAULT}" \
    --fields "${field_name}" \
    --format json \
    >/dev/null 2>&1
}

ensure_nfs_repository_directory() {
  local pod_name="volsync-bootstrap-${APP}"

  echo "Ensuring repository directory exists from inside Kubernetes: /home-ops-backups/${APP}"

  kubectl -n default delete pod "${pod_name}" --ignore-not-found --wait=true >/dev/null

  kubectl -n default run "${pod_name}" \
    --restart=Never \
    --image=busybox:1.37 \
    --overrides="
{
  \"spec\": {
    \"securityContext\": {
      \"runAsUser\": 1000,
      \"runAsGroup\": 1000,
      \"fsGroup\": 1000
    },
    \"containers\": [{
      \"name\": \"${pod_name}\",
      \"image\": \"busybox:1.37\",
      \"command\": [\"sh\", \"-c\", \"set -eu; umask 0002; mkdir -p /repository/${APP}; chmod 775 /repository/${APP} 2>/dev/null || true; rm -f /repository/._${APP} /repository/${APP}/._* 2>/dev/null || true; touch /repository/${APP}/.volsync-write-test; rm -f /repository/${APP}/.volsync-write-test; ls -ldn /repository/${APP}\"],
      \"volumeMounts\": [{
        \"name\": \"repository\",
        \"mountPath\": \"/repository\"
      }]
    }],
    \"volumes\": [{
      \"name\": \"repository\",
      \"nfs\": {
        \"server\": \"storage.cooney.site\",
        \"path\": \"/home-ops-backups\"
      }
    }]
  }
}
" >/dev/null

  if ! kubectl -n default wait --for=jsonpath='{.status.phase}'=Succeeded pod/"${pod_name}" --timeout=120s >/dev/null 2>&1; then
    local phase
    phase="$(kubectl -n default get pod "${pod_name}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"

    kubectl -n default logs "${pod_name}" 2>/dev/null || true
    kubectl -n default describe pod "${pod_name}" 2>/dev/null || true
    kubectl -n default delete pod "${pod_name}" --ignore-not-found --wait=false >/dev/null
    echo "ERROR: Failed to create or verify repository directory using kubectl. Bootstrap pod phase: ${phase:-unknown}" >&2
    echo "Check kubectl access, NFS mount access from the cluster, and QNAP export permissions." >&2
    exit 1
  fi

  local phase
  phase="$(kubectl -n default get pod "${pod_name}" -o jsonpath='{.status.phase}')"

  kubectl -n default logs "${pod_name}" || true

  if [[ "${phase}" != "Succeeded" ]]; then
    kubectl -n default describe pod "${pod_name}" 2>/dev/null || true
    kubectl -n default delete pod "${pod_name}" --ignore-not-found --wait=false >/dev/null
    echo "ERROR: Failed to create or verify repository directory using kubectl." >&2
    echo "Check kubectl access, NFS mount access from the cluster, and QNAP export permissions." >&2
    exit 1
  fi

  kubectl -n default delete pod "${pod_name}" --ignore-not-found --wait=true >/dev/null

  echo "Repository directory ready. If ownership is not 1000:1000, fix it on the QNAP or verify the export maps UID/GID 1000 correctly."
  echo
}

echo "Vault: ${VAULT}"
echo "Item:  ${ITEM}"
echo "Repo:  ${REPOSITORY_VALUE}"
echo

ensure_nfs_repository_directory

if op item get "${ITEM}" --vault "${VAULT}" >/dev/null 2>&1; then
  echo "Existing 1Password item found: ${VAULT}/${ITEM}"

  edit_args=()

  if op_field_exists "KOPIA_REPOSITORY"; then
    echo "KOPIA_REPOSITORY already exists; leaving it unchanged."
  else
    echo "KOPIA_REPOSITORY is missing; adding it."
    edit_args+=("KOPIA_REPOSITORY[text]=${REPOSITORY_VALUE}")
  fi

  if op_field_exists "KOPIA_PASSWORD"; then
    echo "KOPIA_PASSWORD already exists; leaving it unchanged."
  else
    echo "KOPIA_PASSWORD is missing; adding it."
    edit_args+=("KOPIA_PASSWORD[concealed]=${PASSWORD_VALUE}")
  fi

  if [[ "${#edit_args[@]}" -gt 0 ]]; then
    op item edit "${ITEM}" \
      --vault "${VAULT}" \
      "${edit_args[@]}" \
      >/dev/null
  else
    echo "No 1Password field updates needed."
  fi
else
  echo "Creating new 1Password item: ${VAULT}/${ITEM}"

  op item create \
    --vault "${VAULT}" \
    --category "Password" \
    --title "${ITEM}" \
    "KOPIA_REPOSITORY[text]=${REPOSITORY_VALUE}" \
    "KOPIA_PASSWORD[concealed]=${PASSWORD_VALUE}" \
    >/dev/null
fi

if ! op item get "${ITEM}" --vault "${VAULT}" --fields KOPIA_REPOSITORY,KOPIA_PASSWORD >/dev/null; then
  echo "ERROR: 1Password item was not created or updated successfully: ${VAULT}/${ITEM}" >&2
  exit 1
fi

echo
echo "Done."
echo
echo "External Secrets references should use:"
echo "  op://kubernetes/${APP}/KOPIA_REPOSITORY"
echo "  op://kubernetes/${APP}/KOPIA_PASSWORD"
echo
echo "NFS repository path created/verified:"
echo "  /home-ops-backups/${APP}"
echo "Repository directory was created/verified from inside Kubernetes as UID/GID 1000."
echo "If the QNAP shows different ownership, correct it on the QNAP or review NFS squash/mapping settings."
echo