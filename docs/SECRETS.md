# Secrets and credentials

## Preferred model

The preferred model is:

```text
1Password -> External Secrets -> Kubernetes Secret
```

SOPS may still exist for bootstrap-level or legacy secrets. Do not delete SOPS files unless the bootstrap flow has been updated.

## Cluster variable item

Vault:

```text
Kubernetes
```

Item:

```text
home-ops-bootstrap
```

Required fields:

```text
INTERNAL_DOMAIN = cooney.site
EXTERNAL_DOMAIN = cooney.online
```

## Cloudflare item

Vault:

```text
Kubernetes
```

Item:

```text
cloudflare
```

Required fields:

```text
CLOUDFLARE_API_TOKEN
CLOUDFLARE_TUNNEL_ID
```

The Cloudflare API token must be able to manage DNS for certificate validation:

```text
Zone:Read
DNS:Edit
```

It also needs permissions required by the tunnel/external DNS setup.

## 1Password service account

Bootstrap/recovery requires access to the 1Password service account token used by External Secrets / OnePassword Connect.

Current local environment pattern:

```sh
export OP_SERVICE_ACCOUNT_TOKEN="$(op read 'op://kubernetes/onepass_principal/credential')"
```

If rebuilding from a new workstation, make sure the 1Password CLI is authenticated and this token can be read before attempting bootstrap.

## Flux GitHub deploy key

Flux pulls this repository over SSH using the Kubernetes Secret `flux-system/github-deploy-key`.

The private key bootstrap source is stored in 1Password as a Password item:

`op://kubernetes/flux-github-deploy-key/password`

During bootstrap, `bootstrap/mod.just` reads that value into a temporary file and creates the Kubernetes Secret with `identity` and `known_hosts` keys.

Do not store `github-deploy.key` or any plaintext private key in the repository.

## Flux webhook secret

Vault:

```text
Kubernetes
```

Item:

```text
flux
```

Field:

```text
GITHUB_WEBHOOK_TOKEN
```

The ExternalSecret creates:

```text
flux-system/github-webhook-token-secret
```

Validate:

```sh
kubectl -n flux-system get externalsecret github-webhook-token
kubectl -n flux-system get secret github-webhook-token-secret
kubectl -n flux-system get receiver github-webhook
```

## GitHub Actions runner app

Vault:

```text
Kubernetes
```

Item:

```text
actions-runner
```

Required fields:

```text
ACTIONS_RUNNER_APP_ID
ACTIONS_RUNNER_INSTALLATION_ID
ACTIONS_RUNNER_PRIVATE_KEY
```

These values back the GitHub App used by GitHub Actions Runner Controller for `andycooney/home-ops`.

## Cluster Secrets component

Shared cluster variables are generated through:

```text
kubernetes/components/externalsecret.yaml
```

This creates a namespace-local Secret:

```text
cluster-secrets
```

Expected keys:

```text
INTERNAL_DOMAIN
EXTERNAL_DOMAIN
CLOUDFLARE_TUNNEL_ID
```

Validate:

```sh
kubectl get externalsecret -A | grep cluster-secrets
kubectl get secret -A | grep cluster-secrets
```

Expected namespaces:

```text
cert-manager
default
flux-system
kube-system
network
```

## Remaining SOPS usage

Check remaining SOPS files:

```sh
find . -name "*.sops.yaml" -o -name "*.sops.yml"
```

Known historical files may include:

```text
talos/talsecret.sops.yaml
op://kubernetes/flux-github-deploy-key/password
bootstrap/sops-age.sops.yaml
kubernetes/apps/network/cloudflare-tunnel/app/secret.sops.yaml
```

The Flux GitHub webhook token was moved from SOPS to 1Password.

The old component path should not be used for domain substitution anymore:

```text
kubernetes/components/sops/cluster-secrets.sops.yaml
```

## Secret exposure response

If a secret is exposed:

1. Revoke/rotate it immediately.
2. Replace it in 1Password.
3. Reconcile External Secrets.
4. Remove it from the working tree.
5. If committed, consider history cleanup only after rotation.
