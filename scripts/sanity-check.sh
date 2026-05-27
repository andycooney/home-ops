#!/usr/bin/env bash
set -euo pipefail

section() {
  echo
  echo "===== $* ====="
}

section "Git status"
GIT_PAGER=cat git status --short

section "Recent commits"
GIT_PAGER=cat git log --oneline -10

section "Open PRs"
if command -v gh >/dev/null 2>&1; then
  gh pr list --state open || true
else
  echo "gh not installed; skipping PR check"
fi

section "Open Issues"
if command -v gh >/dev/null 2>&1; then
  gh issue list --repo andycooney/home-ops --state open --limit 20 || true
else
  echo "gh CLI not found; skipping open issues list"
fi

section "Validate repo"
scripts/validate-repo.sh

section "Flux not-ready Kustomizations"
flux get ks -A | grep -v True || true

section "Flux not-ready HelmReleases"
flux get hr -A | grep -v True || true

section "Nodes"
kubectl get nodes -o wide

section "Non-running pods"
kubectl get pods -A | grep -Ev 'Running|Completed' || true

section "Ceph status"
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status 2>/dev/null || \
  echo "rook-ceph-tools unavailable; skipping Ceph status"

section "Sanity check complete"
