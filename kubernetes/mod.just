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
    @echo "=== Recent warning events ==="
    kubectl get events -A --field-selector type=Warning --sort-by=.lastTimestamp | tail -50

[doc('Run a read-only post-change sanity check')]
[group('kube')]
sanity-check:
    cd "{{ justfile_directory() }}" && ./scripts/sanity-check.sh
