# Restore drill

Use this for periodic VolSync/Kopia restore testing.

## Goal

Prove that a PVC can be restored without risking production app data.

## Recommended first drill

Use a small test app/PVC rather than a production app.

## Checks

```sh
kubectl get replicationsource,replicationdestination -A
kubectl -n volsync-system get pods
kubectl -n volsync-system get secret volsync-maintenance-secret -o json \
  | jq -r '.data.KOPIA_REPOSITORY | @base64d'
```

Expected:

```text
filesystem:///mnt/repository/volsync-maintenance
```

Kopia UI:

```text
https://kopia.cooney.site
```

Expected repository path in live ConfigMap:

```text
/repository/kopia
```
