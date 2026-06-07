# Tuppr upgrades

Tuppr is installed for controlled Talos/Kubernetes upgrades, but the actual upgrade definitions are intentionally held behind a suspended Flux Kustomization.

Expected steady state:

```text
system-upgrade/tuppr            SUSPENDED=False  READY=True
system-upgrade/tuppr-upgrades   SUSPENDED=True   READY=False
```

`system-upgrade/tuppr` is the controller and should be healthy.

`system-upgrade/tuppr-upgrades` is the manual upgrade gate and is expected to be suspended. Because it is suspended, Flux does not reconcile it, so its Ready message can be stale. For example, it may still mention an old dependency condition even after the dependency has recovered. That is acceptable as long as:

```text
tuppr is Ready=True
tuppr-upgrades is SUSPENDED=True
there are no active upgrade Jobs or unexpected upgrade Pods
```

Validate:

```sh
flux get ks -A | grep -i tuppr
flux get hr -n system-upgrade
kubectl -n system-upgrade get all
kubectl -n system-upgrade get kubernetesupgrades,talosupgrades
```

To list active non-ready Flux resources while ignoring intentionally suspended gates:

```sh
flux get ks -A | awk 'NR==1 || ($4!="True" && $3!="True")'
flux get hr -A | awk 'NR==1 || ($4!="True" && $3!="True")'
```

Only resume the upgrade gate after reviewing the target Talos/Kubernetes versions and confirming the cluster is ready for node upgrades:

```sh
flux resume ks tuppr-upgrades -n system-upgrade
```

Watch upgrade progress:

```sh
kubectl get talosupgrades -A -o wide
kubectl get nodes -o wide
kubectl -n system-upgrade get jobs,pods -o wide
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status
```

After upgrades complete and all nodes are healthy, suspend the gate again:

```sh
flux suspend ks tuppr-upgrades -n system-upgrade
flux get ks tuppr-upgrades -n system-upgrade
```
