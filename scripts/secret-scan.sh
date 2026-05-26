#!/usr/bin/env bash
set -euo pipefail

echo "===== local secret scanning ====="

if command -v gitleaks >/dev/null 2>&1; then
  echo
  echo "===== gitleaks ====="
  gitleaks detect --source . --redact --verbose
else
  echo "gitleaks not installed; skipping. Install with: brew install gitleaks"
fi

if command -v trufflehog >/dev/null 2>&1; then
  echo
  echo "===== trufflehog filesystem ====="
  trufflehog filesystem . --results=verified,unknown --fail
else
  echo "trufflehog not installed; skipping. Install with: brew install trufflehog"
fi
