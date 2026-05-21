#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="external-secrets"
OP_CONNECT_FILE_REF="op://kubernetes/onepassword/OP_CONNECT_FILE"
OP_CONNECT_TOKEN_REF="op://kubernetes/onepassword/OP_CONNECT_TOKEN"

command -v op >/dev/null || { echo "Missing required command: op"; exit 1; }
command -v kubectl >/dev/null || { echo "Missing required command: kubectl"; exit 1; }

echo "Ensuring namespace exists: ${NAMESPACE}"
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

echo "Creating/updating onepassword-connect-credentials-secret from 1Password"
op read "${OP_CONNECT_FILE_REF}" | kubectl -n "${NAMESPACE}" create secret generic onepassword-connect-credentials-secret \
  --from-file=1password-credentials.json=/dev/stdin \
  --dry-run=client -o yaml | kubectl apply -f -

echo "Creating/updating onepassword-connect-vault-secret from 1Password"
kubectl -n "${NAMESPACE}" create secret generic onepassword-connect-vault-secret \
  --from-literal=OP_CONNECT_TOKEN="$(op read "${OP_CONNECT_TOKEN_REF}")" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "Done. Bootstrap secrets are present:"
kubectl -n "${NAMESPACE}" get secret \
  onepassword-connect-credentials-secret \
  onepassword-connect-vault-secret
