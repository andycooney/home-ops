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
    kubectl get gitrepositories.source.toolkit.fluxcd.io -A -o json \
      | jq -r '.items[] | [.metadata.namespace, .metadata.name, (.status.artifact.revision // "-"), (.spec.suspend // false), ((.status.conditions[]? | select(.type=="Ready") | .status) // "-"), ((.status.conditions[]? | select(.type=="Ready") | .message) // "-")] | @tsv' \
      | column -t

    @echo
    @echo "=== Flux Kustomizations ==="
    kubectl get kustomizations.kustomize.toolkit.fluxcd.io -A -o json \
      | jq -r '.items[] | [.metadata.namespace, .metadata.name, (.status.lastAppliedRevision // .status.lastAttemptedRevision // "-"), (.spec.suspend // false), ((.status.conditions[]? | select(.type=="Ready") | .status) // "-"), ((.status.conditions[]? | select(.type=="Ready") | .message) // "-")] | @tsv' \
      | column -t

    @echo
    @echo "=== Flux HelmReleases ==="
    kubectl get helmreleases.helm.toolkit.fluxcd.io -A -o json \
      | jq -r '.items[] | [.metadata.namespace, .metadata.name, (.status.lastAttemptedRevision // .status.lastAppliedRevision // "-"), (.spec.suspend // false), ((.status.conditions[]? | select(.type=="Ready") | .status) // "-"), ((.status.conditions[]? | select(.type=="Ready") | .message) // "-")] | @tsv' \
      | column -t

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

[doc('Run a read-only post-change sanity check')]
[group('kube')]
sanity-check:
    cd "{{ justfile_directory() }}" && ./scripts/sanity-check.sh
