# Commercial release UAT

> **Current automatic NO-GO — Windows:** the packaged task still runs as `SYSTEM`. Windows commercial
> release remains blocked until executable and writable state are split between Program Files and
> ProgramData, the task uses LocalService or a dedicated least-privilege identity, migration is
> transactional, and every Windows item below passes on a real supported Windows Server host.

Use this checklist on the exact release candidate archive. Record date, operator, commit/tag, archive
SHA-256, Serverdesk version, OS build, Stratus product/release, device serial alias, network diagram,
and evidence links. Never put credentials, customer IPs, serial numbers, or unredacted screenshots in
the public repository.

## Gate A — clean installation and security

- [ ] Verify checksum and provenance before extracting the archive.
- [ ] Install on a clean supported Linux system and a clean supported Windows Server system.
- [ ] Confirm generated administrator credentials are unique, protected, rotated, and the bootstrap
      file is removed.
- [ ] Add every supported secret-bearing device through the UI; confirm the operation succeeds and
      the config/export/log/process list contains no plaintext secret.
- [ ] Restart the service and confirm every device reconnects. Rotate each device credential and repeat.
- [ ] Confirm remote access uses valid TLS, HSTS, secure cookies, same-origin writes, and no insecure
      listener or misleading firewall rule remains.
- [ ] Attempt wrong-origin login/logout/mutations, repeated bad logins, oversized bodies, malformed
      import files, symlinks, and untrusted certificates; each must fail closed and create a safe audit event.

## Gate B — real Stratus monitoring

Run separately for every everRun and ztC Edge release listed as supported.

- [ ] Cold start with both nodes healthy; inventory, nodes, VMs, networks, storage, license, alerts,
      availability, topology, and timestamps match the native Stratus console.
- [ ] Stop or isolate one node; status changes within the documented detection window and never remains
      falsely `LIVE`.
- [ ] Restore the node and allow resynchronization; state, alert lifecycle, and availability recover
      without duplicate or permanently stale incidents.
- [ ] Exercise planned maintenance and an HA/FT switchover appropriate to the lab. Serverdesk must remain
      usable and must not issue any control action to the Stratus system.
- [ ] Break AVCLI authentication, management routing, SSH, SNMP, and trap delivery one at a time; each
      source is identified independently and recovery is observable.
- [ ] Generate warning and critical alarms. With all browser windows closed, the server-resident notifier
      must deliver, retry transient failures, suppress duplicates, and expose its health/audit state.

ztC Endurance must not be selected or marketed in this gate until its collector implementation and a
separate Endurance certification plan are complete.

## Gate C — operator experience and accessibility

- [ ] Verify 0, 1, 25, 100, and the declared maximum device count.
- [ ] Verify 390, 768, 1025, 1100, 1279, 1280, 1440, and 1920 px widths; 200%/400% zoom; light/dark and
      forced-colors modes; Korean/English and long customer labels.
- [ ] Complete login, add/edit/delete device, acknowledge, maintenance, search, backup/restore, settings,
      logout, and error recovery using keyboard only.
- [ ] Test current NVDA + Chrome/Edge and VoiceOver + Safari. Confirm focus order, dialogs, tables,
      validation errors, status/severity announcements, and stale/offline transitions.
- [ ] Confirm every destructive or alert-suppressing operation shows target, impact, active-alert count,
      authenticated operator, mandatory reason, expiry where applicable, and a durable audit record.

## Gate D — lifecycle, resilience, and recovery

- [ ] Update from the oldest supported version while monitoring live devices; state and credentials remain.
- [ ] Inject invalid binary, failed health check, insufficient disk space, ACL denial, and interrupted update;
      rollback restores the exact prior binary/config/task/service/firewall/running state.
- [ ] Test normal and full uninstall; verify reported results match files, credentials, logs, services/tasks,
      firewall rules, and accounts actually remaining.
- [ ] Restore a documented full backup onto a clean host, including authentication, protected credentials,
      TLS material, configuration, known hosts, availability/events, and notification state.
- [ ] Run at least 72 hours with the declared maximum devices while injecting slow devices, network loss,
      collector hangs, alarm storms, disk pressure, clock changes, and process restarts.

## Release decision

Release is approved only when every applicable item is PASS with evidence and no open severity-1 or
severity-2 defect. `BLOCKED` and `NOT TESTED` are not PASS. Any credential exposure, false healthy/live
state, missed unattended critical alert, incorrect rollback, or unsupported control action is an
automatic NO-GO. A repository score or green synthetic CI run is not product certification and cannot
override the Windows privilege blocker or any missing hardware, browser/accessibility, or installation
evidence.
