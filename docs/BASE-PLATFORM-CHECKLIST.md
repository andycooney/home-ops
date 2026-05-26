# Base platform checklist

Base platform components:

- Talos + Flux GitOps
- Cilium + BGP VIP advertising
- Envoy internal/external gateways
- Internal/external DNS split
- cert-manager wildcard TLS
- TLS certificate backup to 1Password
- External Secrets / 1Password integration
- Rook/Ceph + OpenEBS + VolSync/Kopia
- Observability stack
- Intel GPU DRA support
- Multus + IoT VLAN groundwork
- Tuppr installed with upgrades suspended
- Scoped GitHub Actions runner
- Flux Operator UI
- Cluster health script
- Repo validation workflow
- Pre-commit secret scanning
- App onboarding docs/templates

Optional later base-platform component:

- Descheduler

Descheduler is useful for rebalancing pods after node/resource changes, but it should be added as a separate runtime change and validated carefully.
