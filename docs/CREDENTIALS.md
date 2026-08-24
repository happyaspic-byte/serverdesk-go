# Device credential storage

Serverdesk never needs device passwords in the repository or in `config.local.json`.
Production configurations set `"secret_policy": "require-references"` and use
`secret://NAME` values. The process resolves those names only in memory.

## One-time migration

Stop Serverdesk, back up the state volume, and run:

```bash
sudo /opt/serverdesk/serverdesk \
  -c /var/lib/serverdesk/config.local.json \
  -migrate-secrets /etc/serverdesk/credentials
```

The command:

1. creates credential files without group/other permissions;
2. refuses to overwrite a credential with a different value;
3. replaces live password/community fields with `secret://NAME` references;
4. sets `secret_policy` to `require-references`; and
5. preserves the original configuration as `config.local.json.pre-secrets.bak`.

Migration rejects symlink/non-regular config and backup paths, oversized or malformed JSON,
unsafe credential values, and predictable temporary-file collisions. The atomic replacement
preserves the original owner and mode and fsyncs both file and parent directory.

It is idempotent. Store the backup offline and delete it from the server after verification,
because the backup intentionally contains the pre-migration plaintext.

## Linux and systemd credentials

The packaged unit can read the protected source directory through
`SERVERDESK_CREDENTIALS_DIRECTORY`. For stronger runtime isolation, map every name printed by
the migration command with a systemd drop-in:

```ini
[Service]
LoadCredential=serverdesk.clusters.ft-a.admin_password:/etc/serverdesk/credentials/serverdesk.clusters.ft-a.admin_password
```

Repeat `LoadCredential=` for each printed `CREDENTIAL=...` line, then run:

```bash
sudo systemctl daemon-reload
sudo systemctl restart serverdesk
```

To enroll or rotate one credential without ever placing its value in argv or JSON, stop the
service and send the value on standard input (a trailing newline is required):

```bash
sudo /opt/serverdesk/serverdesk \
  -set-device-secret serverdesk.pve-a.password-v2 \
  -credentials-dir /etc/serverdesk/credentials
```

Add the printed `secret://...` reference to the device form/config and add the corresponding
`LoadCredential=` line before restarting. The command is create-only and refuses to replace a
different existing value; rotation therefore always gets a new versioned name.

When systemd supplies `CREDENTIALS_DIRECTORY`, Serverdesk prefers its private read-only runtime
directory over the source directory. Use `LoadCredentialEncrypted=` and `systemd-creds encrypt`
where the host supports encrypted systemd credentials.

## Windows DPAPI

On Windows, the same migration command stores each credential as a machine-scoped DPAPI blob
(`NAME.dpapi`). Only processes running on that Windows installation with sufficient local access
can ask DPAPI to decrypt it. The installer ACL restricts the credential directory to SYSTEM and
Administrators. Copying a `.dpapi` file to another host does not make it decryptable there.

Set `SERVERDESK_CREDENTIALS_DIRECTORY` for the scheduled task/service to the directory passed to
`-migrate-secrets`. Do not convert the DPAPI files back into environment-variable passwords.

## Rotation and rollback

To rotate a device credential, stop the service, create a new credential name, update the
corresponding `secret://NAME` reference atomically, and restart. Keeping the old name until the
new configuration passes `-once` provides a quick rollback. Never edit the systemd runtime
`CREDENTIALS_DIRECTORY`; change its source and restart the unit instead.
