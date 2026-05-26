# README platform hardening addendum

Add these references to the main README after the base platform section.

## Platform hardening and onboarding docs

Additional operational docs:

```text
docs/BASE-PLATFORM-CHECKLIST.md
docs/APP-ONBOARDING.md
docs/RESOURCE-REQUESTS.md
docs/SECURITY-CHECKS.md
docs/RESTORE-DRILL.md
```

Reusable helpers:

```text
scripts/cluster-health.sh
scripts/validate-repo.sh
scripts/secret-scan.sh
```

Validation automation:

```text
.github/workflows/validate.yaml
.pre-commit-config.yaml
```

Template for new applications:

```text
templates/app/
```

## Local validation

```sh
brew install pre-commit gitleaks trufflehog
pre-commit install
pre-commit run --all-files
scripts/validate-repo.sh
scripts/secret-scan.sh
scripts/cluster-health.sh
```

## Base platform checkpoint

```sh
git tag -a base-platform-complete \
  -m "base platform complete before application onboarding"

git push origin base-platform-complete
```
