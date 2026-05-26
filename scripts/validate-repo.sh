#!/usr/bin/env bash
set -euo pipefail

section() { printf '\n===== %s =====\n' "$*"; }

section "Forbidden upstream residue"
if grep -R -n -E 'turbo\.ac|expanse\.internal|/mnt/ceres/Kopia|onedr0p/home-ops' \
  kubernetes talos bootstrap .github \
  --exclude='README.md' \
  --exclude='*.md' \
  2>/dev/null; then
  echo "ERROR: forbidden upstream residue found"
  exit 1
fi

section "Dangerous plaintext secret patterns"
if grep -R -n -E '(BEGIN (RSA|OPENSSH|EC|DSA) PRIVATE KEY|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9_]{36,}|github_pat_[A-Za-z0-9_]+|xox[baprs]-[A-Za-z0-9-]+)' \
  --exclude-dir=.git \
  --exclude='*.sops.yaml' \
  --exclude='*.sops.yml' \
  .; then
  echo "ERROR: possible plaintext secret found"
  exit 1
fi

section "Kustomize renders"
paths=(
  "kubernetes/apps"
  "kubernetes/apps/flux-system"
  "kubernetes/apps/cert-manager"
  "kubernetes/apps/kube-system"
  "kubernetes/apps/network"
  "kubernetes/apps/default"
  "kubernetes/apps/o11y"
  "kubernetes/apps/openebs-system"
  "kubernetes/apps/rook-ceph"
  "kubernetes/apps/volsync-system"
  "kubernetes/apps/system-upgrade"
  "kubernetes/apps/actions-runner-system"
)

for path in "${paths[@]}"; do
  if [[ -f "${path}/kustomization.yaml" ]]; then
    echo "Rendering ${path}"
    kubectl kustomize "${path}" >/dev/null
  fi
done

section "Git status"
git status --short

echo
echo "Validation passed."
