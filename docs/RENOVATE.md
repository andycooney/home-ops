# Renovate

Renovate is enabled for `home-ops` using the hosted Renovate GitHub App and the root `.renovaterc.json5` file.

## Operating model

- Renovate opens pull requests against `main`.
- Patch and digest updates may automerge after checks pass, except for sensitive platform packages.
- Minor, major, and cluster-impacting updates remain manual review by default.
- Use squash merge for manually reviewed Renovate PRs to keep `main` history compact.
- Rebase or retry Renovate PRs after significant platform changes before merging them.

## Normal review flow

For each Renovate PR:

```sh
gh pr view <number>
gh pr checks <number> --watch
GH_PAGER=cat gh pr diff <number>
```

If the checks are green and the diff is expected, squash merge the PR from the GitHub UI or with:

```sh
gh pr merge <number> --squash --delete-branch
```

Then update the local checkout:

```sh
git pull
```

## Rebase all open Renovate PRs

After broad repo changes, platform upgrades, or workflow changes, ask Renovate to rebase all open PRs from the GitHub UI or by commenting on each PR with the Renovate rebase command.

## GitHub Actions pinning

GitHub Actions should use immutable commit SHA pins with a release-version comment when available, for example:

```yaml
uses: trufflesecurity/trufflehog@37b77001d0174ebec2fcca2bd83ff83a6d45a3ab # v3.95.3
```

This keeps the workflow supply-chain safer while preserving a readable release version. Renovate detects these as `version@sha` and should update the version and SHA together.

## Suggested merge order

Prefer this order when Renovate opens a large initial backlog:

1. GitHub Actions pinning or low-risk CI patches.
2. Local tool updates from `.mise.toml` or aqua.
3. Patch-level application and container updates.
4. Cluster-impacting controllers one at a time.
5. Talos and Kubernetes upgrades only during an intentional maintenance window.
6. Major GitHub Actions or application updates after separate review.

## Sensitive platform packages

The Renovate config keeps these manual even when patch updates are generally allowed to automerge:

- Rook/Ceph packages
- kube-prometheus-stack
- Gluetun

Review these one at a time and run `just sanity-check` after merge.

## Talos-related Renovate PRs

Talos updates may appear in more than one place:

- `.mise.toml` for the local `talosctl`/aqua tool version.
- `talos/talenv.yaml` for generated Talos configuration input.
- `talos/clusterconfig/*` after config regeneration.
- `kubernetes/apps/system-upgrade/tuppr/upgrades/talosupgrade.yaml` for the Tuppr upgrade target.

Do not treat these as ordinary app updates. Merge them intentionally, regenerate Talos configs when needed, validate the repo, and only resume `tuppr-upgrades` when ready to roll nodes.

## Post-merge checks

After routine Renovate PRs:

```sh
scripts/validate-repo.sh
```

After cluster-impacting Renovate PRs:

```sh
just sanity-check
```

For GitOps-specific checks:

```sh
flux reconcile source git flux-system -n flux-system --timeout=2m
flux get ks -A
flux get hr -A
```
