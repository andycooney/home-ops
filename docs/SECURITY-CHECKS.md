# Security checks

## Secret scanning layers

Use multiple layers:

1. GitHub secret scanning / push protection.
2. Pre-commit hooks.
3. GitHub Actions validation workflow.
4. Manual review for secrets in terminal output and generated files.
5. Cloudflare Access protection for external application hostnames.

## Local setup

Install tools:

```sh
brew install pre-commit gitleaks trufflehog
pre-commit install
```

Run manually:

```sh
pre-commit run --all-files
scripts/validate-repo.sh
scripts/secret-scan.sh
```

## What repo validation checks

`scripts/validate-repo.sh` checks for:

```text
turbo.ac
expanse.internal
/mnt/ceres/Kopia
onedr0p/home-ops
common private key/API token patterns
local kustomize render failures
```

## External access checks

All normal external apps under:

```text
*.cooney.online
```

must require Cloudflare Access unless explicitly documented as public or bypassed.

Validate:

```sh
WEBHOOK_PATH="$(kubectl -n flux-system get receiver github-webhook -o jsonpath='{.status.webhookPath}')"

curl -I https://echo.cooney.online
curl -I "https://flux-webhook.cooney.online${WEBHOOK_PATH}"
curl -I https://flux-webhook.cooney.online
```

Expected:

```text
echo.cooney.online
  -> Cloudflare Access 302

flux-webhook.cooney.online/<exact hook path>
  -> no Access 302

flux-webhook.cooney.online/
  -> Cloudflare Access 302
```

Do not publicly paste Cloudflare Access redirect URLs, cookies, JWTs, `CF_Authorization` headers, or authenticated echo output.

## If a secret is exposed

1. Revoke/rotate the secret immediately.
2. Replace it in 1Password.
3. Reconcile External Secrets.
4. Remove the secret from the working tree.
5. If committed, consider history cleanup only after rotation.
