# TrueNAS Mini R

This document captures the current TrueNAS Mini R hardware state, disk inventory, burn-in results, and recommended pool layout for the home-ops storage migration.

## Summary

- Appliance: TrueNAS Mini R
- TrueNAS version: SCALE `25.04.2.6`
- System board observed: Supermicro `A2SDi-H-TF Rev 1.10`
- Boot device: `/dev/nvme0n1` (`iXTSUN3SCEQ120R1`, 111.8G usable)
- Data disks validated: six Toshiba `MG08ACA16TE` 16TB SATA enterprise HDDs
- Validation result: all six passed SMART short, SMART long, destructive write/read compare, and post-validation SMART checks
- Recommended pool: one 6-wide RAIDZ2 vdev
- Estimated usable pool capacity: about 58 TiB before snapshots, reservations, and dataset overhead

Do not use the boot NVMe device for pool creation or disk burn-in:

```text
/dev/nvme0n1
```

## Hardware notes

The Mini R should remain a storage appliance rather than becoming another compute node. Compute-heavy workloads should continue to run on the Talos/Kubernetes cluster, with the NAS focused on reliable shared storage.

Current memory observed during setup was 32GB total, likely 2x16GB DDR4 ECC Registered DIMM. The board uses DDR4 ECC RDIMM-class server memory. A clean upgrade path is either:

- 2x32GB DDR4 ECC RDIMM for 64GB total, replacing the existing 2x16GB modules
- 4x32GB DDR4 ECC RDIMM for 128GB total

Preferred memory search target:

```text
32GB DDR4 ECC Registered RDIMM 2Rx4 PC4-2400T or PC4-2666V
Samsung / SK hynix / Micron
```

## Disk inventory and validation table

Bay mapping assumes the physical drive labels `D01` through `D06` were inserted into bays 1 through 6. Verify against the TrueNAS enclosure view after pool creation if slot-level mapping is required.

| Label | Assumed bay | Linux device | Model | Serial | Capacity | Initial POH | Post-validation POH | SMART short | SMART long | Destructive write/read compare | Post SMART health | Reallocated | Pending | Offline uncorrectable | Notes |
| --- | --- | --- | --- | --- | --- | ---: | ---: | --- | --- | --- | --- | ---: | ---: | ---: | --- |
| D01 | 01 | `/dev/sda` | Toshiba MG08ACA16TE | `7170A0KUFVGG` | 16TB nominal / 14.6T reported | 38439 | 38509 | Completed without error | Completed without error | Passed, 0 bad blocks, `(0/0/0 errors)` | PASSED | 0 | 0 | 0 | Clean after burn-in |
| D02 | 02 | `/dev/sdb` | Toshiba MG08ACA16TE | `51S0A60JFVGG` | 16TB nominal / 14.6T reported | 38440 | 38509 | Completed without error | Completed without error | Passed, 0 bad blocks, `(0/0/0 errors)` | PASSED | 0 | 0 | 0 | Clean after burn-in |
| D03 | 03 | `/dev/sdc` | Toshiba MG08ACA16TE | `51S0A4P4FVGG` | 16TB nominal / 14.6T reported | 38440 | 38509 | Completed without error | Completed without error | Passed, 0 bad blocks, `(0/0/0 errors)` | PASSED | 0 | 0 | 0 | Clean after burn-in |
| D04 | 04 | `/dev/sdd` | Toshiba MG08ACA16TE | `51S0A3STFVGG` | 16TB nominal / 14.6T reported | 38440 | 38509 | Completed without error | Completed without error | Passed, 0 bad blocks, `(0/0/0 errors)` | PASSED | 0 | 0 | 0 | Clean after burn-in |
| D05 | 05 | `/dev/sde` | Toshiba MG08ACA16TE | `7170A0KWFVGG` | 16TB nominal / 14.6T reported | 38439 | 38509 | Completed without error | Completed without error | Passed, 0 bad blocks, `(0/0/0 errors)` | PASSED | 0 | 0 | 0 | Clean after burn-in |
| D06 | 06 | `/dev/sdf` | Toshiba MG08ACA16TE | `51S0A4JEFVGG` | 16TB nominal / 14.6T reported | 38440 | 38509 | Completed without error | Completed without error | Passed, 0 bad blocks, `(0/0/0 errors)` | PASSED | 0 | 0 | 0 | Clean after burn-in |

## Device list captured before burn-in

```text
NAME          SIZE MODEL               SERIAL             TYPE
sda          14.6T TOSHIBA MG08ACA16TE 7170A0KUFVGG       disk
sdb          14.6T TOSHIBA MG08ACA16TE 51S0A60JFVGG       disk
sdc          14.6T TOSHIBA MG08ACA16TE 51S0A4P4FVGG       disk
sdd          14.6T TOSHIBA MG08ACA16TE 51S0A3STFVGG       disk
sde          14.6T TOSHIBA MG08ACA16TE 7170A0KWFVGG       disk
sdf          14.6T TOSHIBA MG08ACA16TE 51S0A4JEFVGG       disk
nvme0n1     111.8G iXTSUN3SCEQ120R1    511260123110000053 disk
├─nvme0n1p1     1M                                        part
├─nvme0n1p2   512M                                        part
└─nvme0n1p3 111.3G                                        part
```

## Burn-in procedure used

The destructive validation was run before pool creation. This is important because `badblocks -w` overwrites the target disk.

One detached tmux session was created per disk:

```sh
cd /root

for d in sda sdb sdc sdd sde sdf; do
  tmux new-session -d -s "bb-$d" \
    "badblocks -wsv -b 4096 -t 0xAA /dev/$d 2>&1 | tee /root/badblocks-$d.log"
done
```

This performs one destructive pattern cycle per disk:

1. Write pattern `0xAA` across the disk.
2. Read the disk back and compare the pattern.
3. Report final error counters.

The final expected success line is:

```text
Pass completed, 0 bad blocks found. (0/0/0 errors)
```

All six disks reached that result.

## Monitoring commands used

List tmux sessions:

```sh
tmux ls
```

Check active `badblocks` processes:

```sh
pgrep -af badblocks
```

Capture live status from all tmux sessions without attaching:

```sh
for d in sda sdb sdc sdd sde sdf; do
  echo
  echo "===== $d ====="
  tmux capture-pane -p -S -100 -t "bb-$d" | sed '/^[[:space:]]*$/d' | tail -n 8
done
```

Show the last lines of all burn-in logs:

```sh
for f in /root/badblocks-sd{a..f}.log; do
  echo
  echo "===== $f ====="
  tail -n 8 "$f"
done
```

Confirm no burn-in processes remain:

```sh
pgrep -af badblocks || echo "No badblocks processes running"
```

## Final burn-in log result

Each disk log ended with the following pattern:

```text
Checking for bad blocks in read-write mode
From block 0 to 3906469887
Testing with pattern 0xaa: done
Reading and comparing: done
Pass completed, 0 bad blocks found. (0/0/0 errors)
```

## Post-validation SMART check

Command used:

```sh
for d in /dev/sd{a..f}; do
  echo
  echo "===== $d ====="
  smartctl -a "$d" | egrep -i \
    'Device Model|Serial Number|SMART overall-health|Reallocated_Sector_Ct|Current_Pending_Sector|Offline_Uncorrectable|Power_On_Hours'
done
```

Final counters were clean on all six disks:

```text
SMART overall-health self-assessment test result: PASSED
Reallocated_Sector_Ct: 0
Current_Pending_Sector: 0
Offline_Uncorrectable: 0
Power_On_Hours: 38509
```

## Pool recommendation

Recommended initial pool:

```text
Pool name: tank or storage
Layout: single 6-wide RAIDZ2 vdev
Data disks: /dev/sda /dev/sdb /dev/sdc /dev/sdd /dev/sde /dev/sdf
Do not select: /dev/nvme0n1
```

Expected usable capacity is roughly 58 TiB before snapshots, reservations, and dataset overhead.

After creating the pool, immediately run an initial scrub:

```sh
zpool status
zpool scrub <poolname>
zpool status
```

## Follow-up configuration checklist

After pool creation:

- Create datasets instead of storing data directly at the pool root.
- Configure SMART short and long test schedules.
- Configure a periodic ZFS scrub schedule.
- Configure alerts/email delivery.
- Configure shares and dataset permissions.
- Add UPS integration when the UPS is wired in.
- Preserve the `/root/badblocks-sd*.log` files until this inventory has been backed up elsewhere.
