# Product support matrix

This matrix separates implemented code paths from combinations that Roobicom has certified on real
customer-equivalent equipment. A green automated build is not a hardware support claim.

## Platforms

| Platform | Current product status | Collection path | Required before paid support |
|---|---|---|---|
| Stratus everRun | Pilot monitoring | AVCLI, optional SSH/SNMP and traps | Certify each supported release and failure scenario |
| Stratus ztC Edge | Pilot monitoring | AVCLI, optional SSH/SNMP and traps | Certify each supported release and failure scenario |
| Stratus ztC Endurance | Not supported; display-model preview only | No production collector | Implement SNMPv3/OPC UA/IPMI/Redfish and complete hardware UAT |
| Proxmox VE | Pilot monitoring | HTTPS API with SPKI pinning | Certify supported PVE versions and token/credential rotation |
| Redfish BMC | Pilot monitoring | HTTPS API with SPKI pinning | Certify each supported BMC vendor/firmware |
| NAS/printer/PLC/general server | Best-effort pilot monitoring | Device-specific read-only poller | Publish vendor/model/protocol-specific test results |

Serverdesk does not currently execute reboot, shutdown, failover, Smart Exchange, or other destructive
device-management actions. UI capability gates and the server API must continue to fail closed.

## Server operating systems

| Target | Automated evidence | Release requirement |
|---|---|---|
| Linux amd64 | Build, Go tests, package static validation | Real systemd fresh install/update/rollback/uninstall on every listed distribution |
| Windows Server amd64 | Cross-build and test compilation | Real Scheduled Task, ACL, firewall, TLS, update/rollback/uninstall on every listed version |

No operating-system version becomes supported until its completed evidence sheet is attached to the
release approval. Customer-specific AVCLI, MIB, and JRE artifacts are supplied only through authorized
channels and are outside the public GitHub release.
