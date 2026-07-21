#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
app_dir="${repo_root}/kubernetes/apps/default/qbittorrent/app"
expected_runtime_image='ghcr.io/andycooney/qbittorrent-pia-runtime:sha-b64d67430d9c@sha256:60ba85d3feb69ab795faca5b61af0f950e027f7618291ff98572559806800296'
expected_helper_image='ghcr.io/andycooney/qbittorrent-pia-port-forward:sha-2ce6208d13b2@sha256:b0c572e124abbc1ba5bf061c9d6359febdb848b6cbe276d4e56524deee7c2937'
test_root="$(mktemp -d)"
trap 'rm -r "${test_root}"' EXIT

helm template qbittorrent oci://ghcr.io/bjw-s-labs/helm/app-template \
  --version 5.0.1 \
  --namespace default \
  -f <(yq '.spec.values' "${app_dir}/helmrelease.yaml") \
  > "${test_root}/rendered.yaml"

yq -o=json -I=0 'select(.kind == "Deployment")' "${test_root}/rendered.yaml" > "${test_root}/deployment.json"
kubectl kustomize "${app_dir}" > "${test_root}/kustomized.yaml"
yq -o=json -I=0 'select(.kind == "ExternalSecret" and .metadata.name == "qbittorrent-vpn")' "${test_root}/kustomized.yaml" > "${test_root}/external-secret.json"

jq -e --arg image "${expected_runtime_image}" --arg helperImage "${expected_helper_image}" '
  .spec.template.spec as $pod |
  ($pod.hostNetwork == false) and
  ($pod.initContainers | length == 1) and
  ($pod.initContainers[0].name == "firewall-init") and
  ($pod.initContainers[0].image == $image) and
  ($pod.initContainers[0].command == ["/usr/local/bin/pia-runtime", "firewall-init"]) and
  (($pod.initContainers[0].envFrom // []) | length == 0) and
  ($pod.initContainers[0].securityContext.runAsUser == 0) and
  ($pod.initContainers[0].securityContext.runAsGroup == 0) and
  ($pod.initContainers[0].securityContext.privileged == false) and
  ($pod.initContainers[0].securityContext.allowPrivilegeEscalation == false) and
  (($pod.initContainers[0].securityContext.capabilities.add | sort) == ["NET_ADMIN"]) and
  ($pod.initContainers[0].securityContext.capabilities.drop == ["ALL"]) and
  (($pod.initContainers[0].volumeMounts // []) | length == 0) and

  ($pod.containers | map(select(.name == "gluetun")) | length == 1) and
  ($pod.containers[] | select(.name == "gluetun") | .image == $image) and
  ($pod.containers[] | select(.name == "gluetun") | (.command // null) == null) and
  ($pod.containers[] | select(.name == "gluetun") | (.args // null) == null) and
  ($pod.containers[] | select(.name == "gluetun") | .env == $pod.initContainers[0].env) and
  ($pod.containers[] | select(.name == "gluetun") | .env | map({key: .name, value: .value}) | from_entries |
    .PIA_ALLOWED_SUBNETS == "10.42.0.0/16,10.43.0.0/16,172.16.0.0/12,192.168.0.0/16" and
    .PIA_APPLICATION_UID == "1000" and
    .PIA_PF_HELPER_UID == "65532" and
    .PIA_READER_GID == "65532" and
    .PIA_RUNTIME_DIR == "/run/pia" and
    .PIA_RUNTIME_LISTEN == "127.0.0.1:8001" and
    .PIA_SERVICE_PORT == "80" and
    .PIA_TUNNEL_INTERFACE == "tun0" and
    (has("PIA_PREFERRED_REGIONS") | not)
  ) and
  ($pod.containers[] | select(.name == "gluetun") | .envFrom == [{"secretRef":{"name":"qbittorrent-vpn-secret"}}]) and
  ($pod.containers[] | select(.name == "gluetun") | .securityContext.runAsUser == 0) and
  ($pod.containers[] | select(.name == "gluetun") | .securityContext.runAsGroup == 0) and
  ($pod.containers[] | select(.name == "gluetun") | .securityContext.privileged == false) and
  ($pod.containers[] | select(.name == "gluetun") | .securityContext.allowPrivilegeEscalation == false) and
  ($pod.containers[] | select(.name == "gluetun") | (.securityContext.capabilities.add | sort) == ["CHOWN", "DAC_OVERRIDE", "NET_ADMIN"]) and
  ($pod.containers[] | select(.name == "gluetun") | .securityContext.capabilities.drop == ["ALL"]) and
  ($pod.containers[] | select(.name == "gluetun") | .livenessProbe.exec.command == ["/usr/local/bin/pia-runtime", "healthcheck"]) and
  ($pod.containers[] | select(.name == "gluetun") | .readinessProbe.exec.command == ["/usr/local/bin/pia-runtime", "readycheck"]) and

  ($pod.containers[] | select(.name == "app") | .securityContext.runAsUser == 1000) and
  ($pod.containers[] | select(.name == "app") | .securityContext.runAsGroup == 1000) and
  ($pod.containers[] | select(.name == "app") | .securityContext.runAsNonRoot == true) and
  ($pod.containers[] | select(.name == "app") | .securityContext.readOnlyRootFilesystem == true) and
  ($pod.containers[] | select(.name == "app") | .securityContext.capabilities.drop == ["ALL"]) and

  (["pia-port-forward", "port-sync"] | all(. as $name |
    ($pod.containers[] | select(.name == $name) |
      .image == $helperImage and
      .securityContext.runAsUser == 65532 and
      .securityContext.runAsGroup == 65532 and
      .securityContext.runAsNonRoot == true and
      .securityContext.readOnlyRootFilesystem == true and
      .securityContext.allowPrivilegeEscalation == false and
      .securityContext.capabilities.drop == ["ALL"] and
      ((.securityContext.capabilities.add // []) | length == 0) and
      ((.envFrom // []) | length == 0) and
      (all(.volumeMounts[]?; .name == "pia-runtime" or .name == "scripts"))
    )
  )) and

  ($pod.volumes[] | select(.name == "pia-runtime") | .emptyDir.medium == "Memory") and
  ($pod.volumes[] | select(.name == "pia-runtime") | .emptyDir.sizeLimit == "16Mi") and
  ([ $pod.containers[] | select(any(.volumeMounts[]?; .name == "pia-runtime")) | .name ] | sort == ["gluetun", "pia-port-forward", "port-sync"]) and
  ($pod.containers[] | select(.name == "gluetun") | .volumeMounts[] | select(.name == "pia-runtime") | (.readOnly // false) == false) and
  ($pod.containers[] | select(.name == "pia-port-forward") | .volumeMounts[] | select(.name == "pia-runtime") | (.readOnly // false) == false) and
  ($pod.containers[] | select(.name == "port-sync") | .volumeMounts[] | select(.name == "pia-runtime") | .readOnly == true) and
  ($pod.containers[] | select(.name == "gluetun") | [ .volumeMounts[].name ] | any(. == "config" or . == "media" or . == "unprocessed") | not) and
  ([ $pod.containers[] | select(any(.volumeMounts[]?; .name == "tun")) | .name ] == ["gluetun"]) and
  ([ $pod.containers[] | select((.envFrom // []) | any(.secretRef.name == "qbittorrent-vpn-secret")) | .name ] == ["gluetun"])
' "${test_root}/deployment.json" >/dev/null

jq -e '
  (.spec.target.template.mergePolicy == "Replace") and
  ((.spec.target.template.data | keys | sort) == ["PIA_PASSWORD", "PIA_USERNAME"])
' "${test_root}/external-secret.json" >/dev/null

for obsolete in \
  '/tmp/gluetun/forwarded_port' \
  'PIA_PF_GATEWAY' \
  'PIA_PF_HOSTNAME' \
  'VPN_PORT_FORWARDING_USERNAME' \
  'VPN_PORT_FORWARDING_PASSWORD' \
  'WIREGUARD_CONF_SECRET_FILE' \
  'qbittorrent-pia-wg-ca-ontario-secret'; do
  if grep -R -F \
    --exclude='test-rendered-manifest.sh' \
    --exclude='test-runtime-scripts.sh' \
    -- "${obsolete}" "${app_dir}" >/dev/null; then
    printf 'obsolete integration value remains: %s\n' "${obsolete}" >&2
    exit 1
  fi
done

yq 'select(.kind != null)' "${test_root}/rendered.yaml" > "${test_root}/rendered-resources.yaml"
kubeconform -strict -ignore-missing-schemas "${test_root}/rendered-resources.yaml"

printf 'qBittorrent rendered runtime manifest tests passed\n'
