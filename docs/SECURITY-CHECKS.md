# Security checks

## Secret scanning layers

Use multiple layers:

1. GitHub secret scanning / push protection.
2. Pre-commit hooks.
3. GitHub Actions validation workflow.
4. Manual review for secrets in terminal output and generated files.

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

## What the repo validation checks

`scripts/validate-repo.sh` checks for:

```text
turbo.ac
expanse.internal
/mnt/ceres/Kopia
onedr0p/home-ops
common private key/API token patterns
local kustomize render failures
```

## If a secret is exposed

1. Revoke/rotate the secret immediately.
2. Replace it in 1Password.
3. Reconcile External Secrets.
4. Remove the secret from the working tree.
5. If committed, consider history cleanup only after rotation.
