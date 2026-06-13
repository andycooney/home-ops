set quiet
set shell := ['bash', '-euo', 'pipefail', '-c']
set script-interpreter := ['bash', '-euo', 'pipefail']

[private]
default:
    just -l kube

[doc('Force Flux to pull in changes from your Git repository')]
[group('kube')]
reconcile:
    flux --namespace flux-system reconcile kustomization flux-system --with-source

[doc('Show a concise Flux/Kubernetes health overview')]
[group('kube')]
flux-overview:
    @echo
    @echo "=== Flux Git sources ==="
    flux get sources git -A
    @echo
    @echo "=== Flux Kustomizations ==="
    flux get ks -A
    @echo
    @echo "=== Flux HelmReleases ==="
    flux get hr -A
    @echo
    @echo "=== Not-ready pods ==="
    kubectl get pods -A --field-selector=status.phase!=Running,status.phase!=Succeeded
    @echo
    @echo "=== Ceph status ==="
    @if kubectl -n rook-ceph get deploy rook-ceph-tools >/dev/null 2>&1; then \
      kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status; \
    else \
      echo "rook-ceph-tools deployment not found"; \
    fi
    @echo
    @echo "=== Warning events from the last 15 minutes ==="
    @cutoff="$$(date -u -v-15M +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '15 minutes ago' +%Y-%m-%dT%H:%M:%SZ)"; kubectl get events -A --field-selector type=Warning --sort-by=.lastTimestamp -o json | jq -r --arg cutoff "$$cutoff" '.items[] | (.lastTimestamp // .eventTime // .metadata.creationTimestamp) as $ts | select($ts >= $cutoff) | [.metadata.namespace, $ts, .type, .reason, (.involvedObject.kind + "/" + .involvedObject.name), .message] | @tsv' | column -t -s "$$(printf '\t')" || true

[doc('Run a read-only post-change sanity check')]
[group('kube')]
sanity-check:
    cd "{{ justfile_directory() }}" && ./scripts/sanity-check.sh
