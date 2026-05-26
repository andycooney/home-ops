set no-exit-message
set quiet
set shell := ['bash', '-euo', 'pipefail', '-c']
set script-interpreter := ['bash', '-euo', 'pipefail']

kubernetes_dir := justfile_dir() + '/kubernetes'

[private]
default:
    just bootstrap -l

[doc('Bootstrap the Talos cluster')]
[group('bootstrap')]
talos: talos-secret talos-config talos-apply talos-bootstrap talos-kubeconfig

[doc('Bootstrap apps into the Talos cluster')]
[group('bootstrap')]
apps: apps-ready apps-namespaces apps-secrets apps-crds apps-helm
    just log info "Cluster is bootstrapped — Flux will start syncing the Git repository"

[private]
[script]
[working-directory('../talos')]
talos-secret:
    just log info "Generating talsecret"
    if [ ! -f talsecret.sops.yaml ]; then
        talhelper gensecret | sops --filename-override talsecret.sops.yaml --encrypt /dev/stdin > talsecret.sops.yaml
    fi

[private]
[working-directory('../talos')]
talos-config:
    just log info "Generating talos config"
    talhelper genconfig

[private]
[working-directory('../talos')]
talos-apply:
    just log info "Applying talos config"
    talhelper gencommand apply --extra-flags="--insecure" | bash

[private]
[working-directory('../talos')]
talos-bootstrap:
    just log info "Bootstrapping talos"
    until talhelper gencommand bootstrap | bash; do sleep 10; done

[private]
[working-directory('../talos')]
talos-kubeconfig:
    just log info "Fetching kubeconfig"
    until talhelper gencommand kubeconfig --extra-flags="{{ justfile_dir() }} --force" | bash; do sleep 10; done

[private]
[script]
apps-crds:
    just log info "Applying CRDs"
    if ! helmfile --file "{{ source_directory() }}/helmfile.d/00-crds.yaml" template --quiet | yq eval-all --exit-status 'select(.kind == "CustomResourceDefinition")' | kubectl apply --server-side --force-conflicts --filename -; then
        just log fatal "Failed to apply crds"
    fi

[private]
[script]
apps-helm:
    just log info "Syncing helmfile"
    if ! helmfile --file "{{ source_directory() }}/helmfile.d/01-apps.yaml" sync --hide-notes; then
        just log fatal "Failed to sync helmfile"
    fi

[private]
[script]
apps-namespaces:
    just log info "Applying namespaces for apps"
    for app in "{{ kubernetes_dir }}/apps"/*/; do
        ns="$(basename "$app")"
        if kubectl get namespace "$ns" &>/dev/null; then
            just log info "Namespace already exists" namespace "$ns"
        elif kubectl create namespace "$ns" --dry-run=client -o yaml \
                | kubectl apply --server-side --filename - &>/dev/null; then
            just log info "Namespace applied" namespace "$ns"
        else
            just log fatal "Failed to apply namespace" namespace "$ns"
        fi
    done

[private]
[script]
apps-secrets:
    just log info "Applying secrets for apps"

    tmpdir="$(mktemp -d)"
    trap 'rm -rf "$tmpdir"' EXIT

    op read "op://kubernetes/flux-github-deploy-key/password" > "$tmpdir/github-deploy-key"
    chmod 600 "$tmpdir/github-deploy-key"
    ssh-keyscan github.com > "$tmpdir/known_hosts"

    if kubectl --namespace flux-system create secret generic github-deploy-key \
            --from-file=identity="$tmpdir/github-deploy-key" \
            --from-file=known_hosts="$tmpdir/known_hosts" \
            --dry-run=client -o yaml \
            | kubectl apply --server-side --filename - &>/dev/null; then
        just log info "Secret applied" resource "github-deploy-key"
    else
        just log fatal "Failed to apply secret" resource "github-deploy-key"
    fi

    internal_domain="$(op read "op://kubernetes/home-ops-bootstrap/INTERNAL_DOMAIN")"
    external_domain="$(op read "op://kubernetes/home-ops-bootstrap/EXTERNAL_DOMAIN")"
    cloudflare_tunnel_id="$(op read "op://kubernetes/cloudflare/CLOUDFLARE_TUNNEL_ID")"

    for ns in cert-manager default flux-system kube-system network; do
        if kubectl --namespace "$ns" create secret generic cluster-secrets \
                --from-literal=INTERNAL_DOMAIN="$internal_domain" \
                --from-literal=EXTERNAL_DOMAIN="$external_domain" \
                --from-literal=CLOUDFLARE_TUNNEL_ID="$cloudflare_tunnel_id" \
                --dry-run=client -o yaml \
                | kubectl apply --server-side --filename - &>/dev/null; then
            just log info "Secret applied" resource "cluster-secrets" namespace "$ns"
        else
            just log fatal "Failed to apply secret" resource "cluster-secrets" namespace "$ns"
        fi
    done

    for secret in \
        "{{ source_directory() }}/sops-age.sops.yaml"
    do
        name="$(basename "$secret" .sops.yaml)"
        if sops exec-file "$secret" \
                "kubectl --namespace flux-system apply --server-side --filename {}" &>/dev/null; then
            just log info "Secret applied" resource "$name"
        else
            just log fatal "Failed to apply secret" resource "$name"
        fi
    done

# Wait until nodes register as Ready=False. They only become Ready=True once the CNI is healthy.
[private]
[script]
apps-ready:
    just log info "Waiting for nodes to register as Ready=False"
    if ! kubectl wait nodes --for=condition=Ready=True --all --timeout=10s &>/dev/null; then
        until kubectl wait nodes --for=condition=Ready=False --all --timeout=10s &>/dev/null; do
            just log info "Nodes not available, waiting for nodes to be available. Retrying in 5 seconds..."
            sleep 5
        done
    fi
