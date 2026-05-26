#!/usr/bin/env bash
set -euo pipefail

section() { printf '\n===== %s =====\n' "$*"; }

section "Flux sources"
flux get sources git -A

section "Flux Kustomizations not Ready"
flux get ks -A | awk 'NR==1 || $5!="True"'

section "HelmReleases not Ready"
flux get hr -A | awk 'NR==1 || $5!="True"'

section "Pods not Running/Completed"
kubectl get pods -A | awk 'NR==1 || ($4!="Running" && $4!="Completed")'

section "Recent warning events"
kubectl get events -A --sort-by=.lastTimestamp \
  | grep -E "Warning|Failed|BackOff|Unhealthy|FailedMount|FailedScheduling|Error" \
  | tail -80 || true

section "Kopia / VolSync"
flux get ks kopia volsync volsync-maintenance -n volsync-system
flux get hr kopia -n volsync-system
kubectl -n volsync-system get pods
kubectl -n volsync-system get externalsecret
kubectl -n volsync-system get replicationsource,replicationdestination
kubectl -n volsync-system get cm kopia -o yaml | grep -n -A8 -B4 'repository.config\|"path"' || true
curl -Ik https://kopia.cooney.site || true

section "Network / cert backup"
flux get ks -n network
flux get hr -n network
kubectl -n network get pods
kubectl -n network get gateway,httproute
kubectl -n network get certificate
kubectl -n network get pushsecret

section "kube-system platform"
flux get ks -n kube-system
flux get hr -n kube-system
kubectl -n kube-system get pods
kubectl get resourceslices
kubectl get network-attachment-definitions -A

section "System upgrade / Actions runner"
flux get ks -A | grep -E "system-upgrade|tuppr|actions-runner|runner" || true
flux get hr -n system-upgrade || true
kubectl -n system-upgrade get pods || true
kubectl -n system-upgrade get kubernetesupgrades,talosupgrades || true
flux get hr -n actions-runner-system || true
kubectl -n actions-runner-system get pods || true
kubectl -n actions-runner-system get externalsecret || true
kubectl -n actions-runner-system get autoscalingrunnersets,autoscalinglisteners,ephemeralrunnersets || true

section "Rook/Ceph"
flux get ks -n rook-ceph
flux get hr -n rook-ceph
kubectl -n rook-ceph get cephcluster
kubectl -n rook-ceph get pods | awk 'NR==1 || ($3!="Running" && $3!="Completed")'
if kubectl -n rook-ceph get deploy rook-ceph-tools >/dev/null 2>&1; then
  kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status || true
  kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph health detail || true
fi

section "PVCs not Bound"
kubectl get pvc -A | awk 'NR==1 || $3!="Bound"'

section "Nodes"
kubectl get nodes -o wide

section "Node resource allocation"
kubectl describe nodes | grep -E "Name:|Allocated resources|Requests|Limits|cpu |memory " -A6

section "Top memory requests"
kubectl get pods -A -o json | jq -r '
  def mem_to_mi($m):
    if $m | test("Ki$") then ($m | sub("Ki$";"") | tonumber / 1024)
    elif $m | test("Mi$") then ($m | sub("Mi$";"") | tonumber)
    elif $m | test("Gi$") then ($m | sub("Gi$";"") | tonumber * 1024)
    else 0 end;

  .items[]
  | .metadata.namespace as $ns
  | .metadata.name as $pod
  | (.spec.nodeName // "Pending") as $node
  | .spec.containers[]
  | select(.resources.requests.memory != null)
  | .resources.requests.memory as $mem
  | [mem_to_mi($mem), $ns, $pod, $node, .name, $mem]
  | @tsv
' | sort -nr | head -40

section "Internal endpoints"
for url in \
  https://kopia.cooney.site \
  https://rook.cooney.site \
  https://sabnzbd.cooney.site \
  https://grafana.cooney.site \
  https://prometheus.cooney.site/-/ready \
  https://alertmanager.cooney.site/-/ready
do
  echo
  echo "===== $url ====="
  curl -kI "$url" | head || true
done

section "External endpoints"
for url in \
  https://echo.cooney.online \
  https://flux-webhook.cooney.online
do
  echo
  echo "===== $url ====="
  curl -kI "$url" | head || true
done
