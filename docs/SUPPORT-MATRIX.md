# Product support matrix

This matrix separates implemented code paths from combinations that Roobicom has certified on real
customer-equivalent equipment. A green automated build is not a hardware support claim.

## Certified combinations (paid support)

**None.** No `docs/COMMERCIAL-UAT.md` Gate A–B evidence sheet is attached to a release candidate
archive. Do not list an OS build or Stratus everRun/ztC Edge release here until that sheet records
PASS (not BLOCKED / NOT TESTED) for that exact archive SHA-256.

Windows packaged identity on current `main` still registers the Scheduled Task as `SYSTEM`. That is
an automatic commercial NO-GO (`docs/COMMERCIAL-UAT.md`, `docs/SECURITY.md`). LocalService work in
PR #7 is draft and is not a certified runtime.

ztC Endurance is **not supported** and must not be marketed or sold. The UI display model is a
preview only; there is no production collector.

## Platforms

| Platform | Current product status | Collection path | Required before paid support |
|---|---|---|---|
| Stratus everRun | Pilot monitoring — not certified | AVCLI, optional SSH/SNMP and traps | Gate A–B PASS on a named everRun release |
| Stratus ztC Edge | Pilot monitoring — not certified | AVCLI, optional SSH/SNMP and traps | Gate A–B PASS on a named ztC Edge release |
| Stratus ztC Endurance | Not supported; display-model preview only | No production collector | Implement SNMPv3/OPC UA/IPMI/Redfish and complete hardware UAT |
| Proxmox VE | Pilot monitoring — not certified | HTTPS API with SPKI pinning | Certify supported PVE versions and token/credential rotation |
| Redfish BMC | Pilot monitoring — not certified | HTTPS API with SPKI pinning | Certify each supported BMC vendor/firmware |
| NAS/printer/PLC/general server | Best-effort pilot monitoring — not certified | Device-specific read-only poller | Publish vendor/model/protocol-specific test results |

Serverdesk does not currently execute reboot, shutdown, failover, Smart Exchange, or other destructive
device-management actions. UI capability gates and the server API must continue to fail closed.

## Server operating systems

| Target | Automated evidence | Certified product versions | Release requirement |
|---|---|---|---|
| Linux amd64 | Build, Go tests, package static validation | **None listed** | Real systemd fresh install/update/rollback/uninstall on every listed distribution |
| Windows Server amd64 | Cross-build and test compilation | **None listed** (`SYSTEM` task = NO-GO) | Real Scheduled Task as LocalService, ACL, firewall, TLS, update/rollback/uninstall on every listed version |
| Windows 10/11 | None | **Not a supported product OS** | Do not certify client Windows as the commercial SKU host |

No operating-system version becomes supported until its completed evidence sheet is attached to the
release approval. Customer-specific AVCLI, MIB, and JRE artifacts are supplied only through authorized
channels and are outside the public GitHub release.
