# Security checks

## Secret scanning layers

Use multiple layers:

1. GitHub secret scanning / push protection.
2. Pre-commit hooks.
3. GitHub Actions validation workflow.
4. Manual review for secrets in terminal output and generated files.
5. Cloudflare Access protection for external application hostnames.
6. Renovate PR review for dependency and workflow updates.

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
just sanity-check
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

## GitHub Actions and Renovate

Renovate may update or pin GitHub Actions. Treat these PRs as CI/security-impacting changes and review the workflow diff before merging.

Notes:

```text
docs/RENOVATE.md
```

The validation workflow needs read-only repository and pull request permissions so secret scanning actions can inspect PR metadata:

```yaml
permissions:
  contents: read
  pull-requests: read
```

## Pager-safe output

Prefer stdout-only commands when sharing output for review:

```sh
export GIT_PAGER=cat
export GH_PAGER=cat

GIT_PAGER=cat git diff --stat
GIT_PAGER=cat git diff
GH_PAGER=cat gh pr diff <number>
```

Do not paste authenticated URLs, cookies, tokens, JWTs, private keys, kubeconfigs, Talos secrets, or 1Password values.

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
