#!/usr/bin/env bash
set -euo pipefail

APP="${1:-}"
if [[ -z "${APP}" ]]; then
  echo "Usage: $0 <app>"
  exit 1
fi

yq -i 'del(.spec.suspend)' "kubernetes/apps/default/${APP}/app/helmrelease.yaml"

case "${APP}" in
  bazarr|qbittorrent|sabnzbd|radarr|sonarr|qui|stash|plex|whisparr)
    yq -i '
      .spec.components = ["../../../../components/zeroscaler"] + (.spec.components // []) |
      .spec.components |= unique
    ' "kubernetes/apps/default/${APP}/ks.yaml"
    ;;
esac

scripts/validate-repo.sh

git diff -- \
  "kubernetes/apps/default/${APP}/app/helmrelease.yaml" \
  "kubernetes/apps/default/${APP}/ks.yaml"
