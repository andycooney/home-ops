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
CLOUDFLARE_TUNNEL_TOKEN
```

The Cloudflare API token must be able to manage DNS for certificate validation:

```text
Zone:Read
DNS:Edit
```

It also needs permissions required by the tunnel/external DNS setup.

The Cloudflare tunnel runtime token is also stored on this item:

```text
op://kubernetes/cloudflare/CLOUDFLARE_TUNNEL_TOKEN
```

The ExternalSecret at:

```text
kubernetes/apps/network/cloudflare-tunnel/app/externalsecret.yaml
```

creates:

```text
network/cloudflare-tunnel-secret
```

with key:

```text
TUNNEL_TOKEN
```

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

## Atuin

Vault:

```text
Kubernetes
```

Item:

```text
atuin
```

Required database fields for the shared CloudNativePG database:

```text
ATUIN_DB_NAME
ATUIN_DB_USER
ATUIN_DB_PASSWORD
ATUIN_DB_HOST
ATUIN_DB_PORT
ATUIN_DB_URI
```

The database fields create:

```text
database/atuin-db-secret
```

and Atuin runtime consumes:

```text
default/atuin-secret
```

Runtime/user fields currently stored on the same item:

```text
username / ATUIN_USERNAME
password / ATUIN_PASSWORD
ATUIN_EMAIL
ATUIN_KEY
```

`ATUIN_KEY` is the client sync encryption key. Keep it in 1Password because Atuin cannot recover it.

Validate non-secret fields:

```sh
op item get atuin --vault kubernetes \
  --fields label=ATUIN_DB_HOST,label=ATUIN_DB_NAME,label=ATUIN_DB_USER,label=ATUIN_DB_PORT
```

Validate Kubernetes secrets:

```sh
kubectl -n database get externalsecret atuin-db
kubectl -n database get secret atuin-db-secret
kubectl -n default get externalsecret atuin
kubectl -n default get secret atuin-secret
```

Detailed runbook:

```text
docs/ATUIN-CNPG.md
```

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

Current remaining SOPS files should be:

```text
talos/talsecret.sops.yaml
bootstrap/sops-age.sops.yaml
.sops.yaml
```

SOPS is now retained only for Talos/bootstrap-level material.

App/runtime secrets have been moved to 1Password and External Secrets:

```text
Flux GitHub deploy key -> op://kubernetes/flux-github-deploy-key/password
cluster-secrets bootstrap values -> op://kubernetes/home-ops-bootstrap and op://kubernetes/cloudflare
Cloudflare tunnel token -> op://kubernetes/cloudflare/CLOUDFLARE_TUNNEL_TOKEN
Atuin database and client key -> op://kubernetes/atuin
```

Do not delete the remaining Talos/bootstrap SOPS files unless the Talos recovery flow has been migrated and tested.

## Secret exposure response

If a secret is exposed:

1. Revoke/rotate it immediately.
2. Replace it in 1Password.
3. Reconcile External Secrets.
4. Remove it from the working tree.
5. If committed, consider history cleanup only after rotation.
