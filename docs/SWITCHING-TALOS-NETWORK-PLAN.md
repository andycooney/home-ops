# Switch configuration and future Talos/Kubernetes network plan

_Last updated: 2026-06-17_

## Purpose

This document records the current UniFi switch, VLAN, and firewall posture after the recent UDM/Core/AP recovery work, plus the intended future Talos/Kubernetes network layout.

Home Assistant-specific firewall details are intentionally out of scope for this pass.

## Current routed networks

| Network | VLAN | Subnet | Notes |
| --- | ---: | --- | --- |
| Home | 1 | `172.16.1.0/24` | Wired/default Home LAN. Still native on UDM-to-switch uplinks for current stability. |
| Home Wireless | 103 | `172.16.3.0/24` | Trusted wireless client network. Production Home SSIDs moved here from `Native Network`. |
| Storage | 774 | `172.16.8.0/24` | Storage network. |
| Management | 777 | `172.16.32.0/24` | Infrastructure management for switches/APs. |
| IoT | 666 | `172.16.128.0/24` | Restricted. |
| Cameras | 667 | `172.16.132.0/24` | Restricted; internet blocked. |
| Guest | 668 | `172.16.136.0/24` | Restricted. |
| Pixel | 101 | `192.168.1.0/24` | Treated as restricted unless changed. |
| Kontainer | 200 | `192.168.42.0/24` | Current Talos/Kubernetes physical node network. |
| BlackHole | 999 | none | Native VLAN sink; no UDM-routed subnet. |

## Gateway IP list

UniFi network lists do not support shorthand like `172.16.*.1`, so gateway IPs are explicit.

```text
172.16.1.1      # Home
172.16.3.1      # Home Wireless
172.16.8.1      # Storage
172.16.32.1     # Management
172.16.128.1    # IoT
172.16.132.1    # Cameras
172.16.136.1    # Guest
192.168.1.1     # Pixel
192.168.42.1    # Kontainer
```

`BlackHole` has no gateway entry.

## Current switch and port design

### UDM to Core switch

This is the known-good recovered configuration.

```text
UDM Port 6 -> Core Switch
  Operation: Switching
  Native VLAN: Home (1)
  Tagged VLANs: Allow All
  Auto-negotiate: On
  Port isolation: Off
```

Matching Core side:

```text
Core Switch -> UDM Port 6
  Operation: Switching
  Native VLAN: Home (1)
  Tagged VLANs: Allow All
  Port mode: Uplink
  Auto-negotiate: On
  Port isolation: Off
```

Do not apply the BlackHole-native trunk profile to this link yet. A previous mismatch between Home-native and BlackHole-native broke wired DHCP.

### UDM to aggregation switch

If devices on the aggregation switch still expect untagged Home/VLAN 1, use the same pattern:

```text
UDM <-> Agg
  Operation: Switching
  Native VLAN: Home (1)
  Tagged VLANs: Allow All
```

Do not use LAG/port aggregation between UDM gateway LAN ports and switches.

### AP ports

APs use Management VLAN 777 via AP network override. Production trusted Wi-Fi now uses explicit VLAN 103.

```text
AP-facing switch port
  Port profile: AP Uplink Port
  Port mode: Uplink
  Native VLAN: BlackHole (999)
  Tagged VLANs:
    Home Wireless (103)
    Management (777)
    IoT (666)
    Cameras (667)
    Guest (668)
```

AP device setting:

```text
Network Override: Management
Virtual Network: Management / VLAN 777
IP Configuration: DHCP
```

This is safe because AP management and client SSIDs are explicitly tagged. The old `Native Network` SSID behavior depended on the AP port native VLAN.

### Normal wired client ports

```text
Normal wired client port
  Operation: Switching
  Native VLAN: Home (1)
  Tagged VLANs: Block All
  Port mode: Edge
  Port isolation: Off
```

Do not use AP uplink or trunk profiles for normal wired client ports.

### Mini switch uplinks

```text
Simple downstream clients only:
  Native VLAN: Home (1)
  Tagged VLANs: Block All

VLAN-aware downstream or infrastructure:
  Native VLAN: Home (1) for current recovery posture
  Tagged VLANs: Allow All or explicit required VLANs
```

### Current Talos/Kubernetes host ports

```text
Talos host port
  Native VLAN: Kontainer / VLAN 200 / 192.168.42.0/24
  Tagged VLANs: IoT, only if still required
```

Current Kubernetes node internal IPs observed:

```text
talos01  192.168.42.11
talos02  192.168.42.12
talos03  192.168.42.13
```

Current Kubernetes routed ranges:

```text
10.42.0.0/16    # pods
10.43.0.0/16    # services
```

These are routed prefixes, not switch-port VLANs.

## Firewall list intent

### PROTECTED_NETWORKS

`PROTECTED_NETWORKS` are destinations restricted networks should not initiate into.

Because UniFi lists cannot be nested, add the raw CIDRs directly:

```text
172.16.0.0/17       # Home, Home Wireless, Storage, Management, and protected 172.16.0-127 networks
192.168.42.0/24     # Current Talos/Kontainer node network
10.42.0.0/16        # Kubernetes pods
10.43.0.0/16        # Kubernetes services
```

### NON_MANAGEMENT_INTERNAL

Used to block non-management sources from Management.

```text
172.16.0.0/19       # 172.16.0.0 - 172.16.31.255; excludes Management at 172.16.32.0/24
172.16.128.0/17     # IoT, Cameras, Guest block
192.168.1.0/24      # Pixel
192.168.42.0/24     # Kontainer
```

### RESTRICTED_NETWORKS

```text
172.16.128.0/17     # IoT, Cameras, Guest
192.168.1.0/24      # Pixel, if still restricted
```

Do not add Home Wireless / `172.16.3.0/24` to `RESTRICTED_NETWORKS`.

### MANAGEMENT_NETWORKS

```text
172.16.32.0/24
```

## Firewall rule design

### LAN In

Routed traffic between networks/VLANs:

```text
1. Allow Established/Related LAN In
   Action: Accept
   Source: Any
   Destination: Any
   Protocol: All

2. TEMP - Allow Admin Workstation to All Networks
   Action: Accept
   Source: ADMIN_WORKSTATIONS
   Destination: Any
   Protocol: All

3. Block Non-Management to Management
   Action: Drop
   Source: NON_MANAGEMENT_INTERNAL
   Destination: MANAGEMENT_NETWORKS
   Protocol: All

4. Block Restricted to Protected
   Action: Drop
   Source: RESTRICTED_NETWORKS
   Destination: PROTECTED_NETWORKS
   Protocol: All

5. Block Restricted to Restricted
   Action: Drop
   Source: RESTRICTED_NETWORKS
   Destination: RESTRICTED_NETWORKS
   Protocol: All
```

UniFi built-in broad `Allow Network X Traffic` rules can remain below the custom rules.

### LAN Local

Traffic to the UDM/gateway itself:

```text
1. Allow Established/Related Traffic
   Action: Accept
   Protocol: All
   Source: Any
   Destination: Any

2. Allow Restricted to Gateway DNS
   Action: Accept
   Protocol: TCP/UDP
   Source: RESTRICTED_NETWORKS
   Destination: GATEWAY_IPS
   Destination Port: DNS_PORTS

3. Allow Restricted to Gateway DHCP
   Action: Accept
   Protocol: UDP
   Source: RESTRICTED_NETWORKS
   Destination: GATEWAY_IPS
   Destination Port: DHCP_PORTS

4. Block Restricted to Gateway Local
   Action: Drop
   Protocol: All
   Source: RESTRICTED_NETWORKS
   Destination: GATEWAY_IPS
```

This allows DHCP/DNS from restricted networks while blocking other UDM local services.

### Internet Out

Known intentional rules:

```text
Block Cameras to Internet
  Action: Drop
  Source: Cameras
  Destination: Any
  Protocol: All
```

Kubernetes/PIA WireGuard exception, if still required:

```text
Allow Kubernetes PIA WireGuard
  Action: Accept
  Protocol: UDP
  Source: KUBERNETES_WG_SOURCES or relevant Kubernetes/Talos source list
  Destination: WIREGUARD_ENDPOINTS
  Destination Port: WIREGUARD_PORTS
```

Known tested values from prior qBittorrent/Gluetun troubleshooting:

```text
PIA endpoint:     178.93.200.238 UDP/1337
qBittorrent pod:  10.42.2.60
talos02 node IP:  192.168.42.12
```

## Home Wireless migration

Production trusted Wi-Fi was moved from `Native Network` to `Home Wireless` / VLAN 103:

```text
Pretty Fly for a Wi-Fi       -> Home Wireless (103)
Pretty Fly for a Wi-Fi (2.4) -> Home Wireless (103)
```

Expected clients:

```text
IP:      172.16.3.x
Gateway: 172.16.3.1
```

This removes the ambiguity around UniFi native/default VLAN behavior for Home SSIDs.

## Future Talos/Kubernetes network design

Use BGP rather than static routes or a routing VIP for future routed Kubernetes ranges.

Reasons:

- Static routes to one node are fragile.
- A routing VIP would need to be a real L3 failover gateway, not merely a Kubernetes Service VIP.
- BGP lets multiple Talos nodes advertise the same cluster-owned routes and withdraw them when unavailable.

### Future allocation

```text
172.16.16.0/24  Talos physical node network / BGP peering
172.16.17.0/24  Kubernetes frontend / LB / ingress / VIPs
172.16.18.0/24  Reserved / future routed cluster network / spare
172.16.19.0/24  Reserved / future routed cluster network / spare
172.16.20.0/22  Kubernetes internal routed range A
172.16.24.0/22  Kubernetes internal routed range B
172.16.28.0/22  Reserved future cluster expansion
```

Summary block:

```text
172.16.16.0/20  # 172.16.16.0 - 172.16.31.255
```

This stays below Management at `172.16.32.0/24`.

Example future Talos node IPs:

```text
talos01  172.16.16.11
talos02  172.16.16.12
talos03  172.16.16.13
```

Expected BGP-learned routes:

```text
172.16.17.0/24 via 172.16.16.11/12/13
172.16.20.0/22 via 172.16.16.11/12/13
172.16.24.0/22 via 172.16.16.11/12/13
172.16.28.0/22 via 172.16.16.11/12/13, if used later
```

Future `PROTECTED_NETWORKS` can eventually include:

```text
172.16.16.0/20
```

### Ceph internal USB4 ring

Ceph internal storage traffic should remain non-routed and isolated:

```text
192.168.15.0/24  Ceph internal USB4 ring
```

Example:

```text
talos01 USB4/Ceph: 192.168.15.11
talos02 USB4/Ceph: 192.168.15.12
talos03 USB4/Ceph: 192.168.15.13
```

Rules:

```text
No default gateway
No UDM interface
No UniFi network
No static route
No firewall list entry
```

## Verification commands

Kubernetes node IPs:

```bash
kubectl get nodes -o wide
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .status.addresses[?(@.type=="InternalIP")]}{.address}{end}{"\n"}{end}'
```

Node pod CIDRs:

```bash
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.podCIDR}{"\n"}{end}'
```

UDM routing table:

```bash
ssh root@172.16.1.1
ip route
ip route get 10.42.2.60
ip route get 10.43.0.1
ip route get 192.168.42.12
ip rule
ip route show table all
```

Future route checks:

```bash
ip route get 172.16.17.10
ip route get 172.16.20.10
ip route get 172.16.24.10
```

## Lessons learned

1. UDM gateway LAN ports should not be used for normal switch-style LAG.
2. STP does not redistribute traffic; it only blocks/unblocks looped L2 paths.
3. UDM-to-Core and Core-to-UDM native VLANs must match.
4. UniFi `Native Network` SSIDs depend on AP port native VLAN behavior.
5. Moving trusted Wi-Fi to VLAN 103 removed ambiguity around VLAN 1/native behavior.
6. AP ports can use BlackHole native only when production SSIDs and AP management are explicitly tagged.
7. UniFi network lists cannot be nested; add raw CIDRs directly to lists used by rules.
8. Kubernetes pod/service ranges are routed prefixes, not VLANs on switch ports.
9. For future multi-node cluster routing, BGP is cleaner than a static route to one Talos node or a routing VIP.
