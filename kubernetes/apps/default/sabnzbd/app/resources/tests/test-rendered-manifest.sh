#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
app_dir="${repo_root}/kubernetes/apps/default/sabnzbd/app"
expected_runtime_image='ghcr.io/andycooney/qbittorrent-pia-runtime:sha-390dbe7e9ba1@sha256:f3b9d38012be7f670ca70e54883d836d6cf4d4bfff5439d25aefe3e670b13bb4'
test_root="$(mktemp -d)"
trap 'rm -r "${test_root}"' EXIT

helm template sabnzbd oci://ghcr.io/bjw-s-labs/helm/app-template \
  --version 5.0.1 \
  --namespace default \
  -f <(yq '.spec.values' "${app_dir}/helmrelease.yaml") \
  > "${test_root}/rendered.yaml"

yq -o=json -I=0 'select(.kind == "Deployment")' "${test_root}/rendered.yaml" > "${test_root}/deployment.json"
kubectl kustomize "${app_dir}" > "${test_root}/kustomized.yaml"
yq -o=json -I=0 'select(.kind == "ExternalSecret" and .metadata.name == "sabnzbd-vpn")' "${test_root}/kustomized.yaml" > "${test_root}/external-secret.json"

jq -e --arg image "${expected_runtime_image}" '
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
    .PIA_ALLOWED_COUNTRIES == "US" and
    .PIA_ALLOWED_SUBNETS == "10.42.0.0/16,10.43.0.0/16,172.16.0.0/12,192.168.0.0/16" and
    .PIA_APPLICATION_UID == "1000" and
    .PIA_PF_HELPER_UID == "65532" and
    .PIA_PORT_FORWARDING == "false" and
    .PIA_READER_GID == "65532" and
    .PIA_RUNTIME_DIR == "/run/pia" and
    .PIA_RUNTIME_LISTEN == "127.0.0.1:8001" and
    .PIA_SERVICE_PORT == "80" and
    .PIA_TUNNEL_UID == "999" and
    .PIA_TUNNEL_INTERFACE == "tun0" and
    .PIA_TUNNEL_TIMEOUT == "30s"
  ) and
  ($pod.containers[] | select(.name == "gluetun") | .envFrom == [{"secretRef":{"name":"sabnzbd-vpn-secret"}}]) and
  ($pod.containers[] | select(.name == "gluetun") | .securityContext.runAsUser == 0) and
  ($pod.containers[] | select(.name == "gluetun") | .securityContext.runAsGroup == 0) and
  ($pod.containers[] | select(.name == "gluetun") | .securityContext.privileged == false) and
  ($pod.containers[] | select(.name == "gluetun") | .securityContext.allowPrivilegeEscalation == false) and
  ($pod.containers[] | select(.name == "gluetun") | (.securityContext.capabilities.add | sort) == ["CHOWN", "DAC_OVERRIDE", "NET_ADMIN"]) and
  ($pod.containers[] | select(.name == "gluetun") | .securityContext.capabilities.drop == ["ALL"]) and
  ($pod.containers[] | select(.name == "gluetun") | .livenessProbe.exec.command == ["/usr/local/bin/pia-runtime", "healthcheck"]) and
  ($pod.containers[] | select(.name == "gluetun") | .readinessProbe.exec.command == ["/usr/local/bin/pia-runtime", "readycheck"]) and
  ($pod.containers[] | select(.name == "gluetun") | [ .volumeMounts[].name ] | any(. == "config" or . == "media" or . == "unprocessed") | not) and

  ($pod.containers | map(select(.name == "pia-port-forward" or .name == "port-sync")) | length == 0) and
  ($pod.volumes[] | select(.name == "pia-runtime") | .emptyDir.medium == "Memory") and
  ($pod.volumes[] | select(.name == "pia-runtime") | .emptyDir.sizeLimit == "16Mi") and
  ([ $pod.containers[] | select(any(.volumeMounts[]?; .name == "pia-runtime")) | .name ] == ["gluetun"]) and
  ([ $pod.containers[] | select(any(.volumeMounts[]?; .name == "tun")) | .name ] == ["gluetun"]) and
  ([ $pod.containers[] | select((.envFrom // []) | any(.secretRef.name == "sabnzbd-vpn-secret")) | .name ] == ["gluetun"]) and
  ($pod.containers[] | select(.name == "app") |
    .securityContext.runAsUser == 1000 and
    .securityContext.runAsGroup == 1000 and
    .securityContext.runAsNonRoot == true and
    .securityContext.readOnlyRootFilesystem == true and
    .securityContext.capabilities.drop == ["ALL"] and
    ([.volumeMounts[].name] | any(. == "tun" or . == "pia-runtime") | not)
  )
' "${test_root}/deployment.json" >/dev/null

jq -e '
  (.spec.target.template.engineVersion == "v2") and
  (.spec.target.template.mergePolicy == "Replace") and
  ((.spec.target.template.data | keys | sort) == ["PIA_PASSWORD", "PIA_USERNAME"])
' "${test_root}/external-secret.json" >/dev/null

for obsolete in \
  'ghcr.io/qdm12/gluetun' \
  'OPENVPN_USER' \
  'OPENVPN_PASSWORD' \
  'PIA_PREFERRED_REGIONS' \
  'WIREGUARD_CONF_SECRET_FILE' \
  'sabnzbd-pia-wg-' \
  'pia-port-forward' \
  'port-sync'; do
  if grep -R -F \
    --exclude='test-rendered-manifest.sh' \
    -- "${obsolete}" "${app_dir}" >/dev/null; then
    printf 'obsolete or forbidden SABnzbd integration value remains: %s\n' "${obsolete}" >&2
    exit 1
  fi
done

yq 'select(.kind != null)' "${test_root}/rendered.yaml" > "${test_root}/rendered-resources.yaml"
kubeconform -strict -ignore-missing-schemas "${test_root}/rendered-resources.yaml"

printf 'SABnzbd rendered reusable PIA runtime tests passed\n'
