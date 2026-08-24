# Device and webhook credential storage

Production configurations use `"secret_policy": "require-references"`. Device passwords,
communities, and notification webhook URLs are persisted only as `secret://NAME` references and
resolved in process memory.

## Managed writable store

The packaged Linux unit sets:

```text
SERVERDESK_CREDENTIALS_STORE=/var/lib/serverdesk/credentials
```

The installer creates that directory as `serverdesk:serverdesk` mode `0700`; credential files are
mode `0600`. This lets the authenticated management UI enroll devices without persisting plaintext
secrets in JSON. Values are protected by OS permissions rather than application encryption, so use
full-disk encryption where that threat is in scope.

Windows uses the protected runtime directory's `credentials` child. Values are machine-scoped DPAPI
blobs and the installer ACL permits only SYSTEM and Administrators. Copying a `.dpapi` file to
another host does not make it decryptable there.

## Directory contract

Serverdesk separates the writable managed store from read-only credential providers:

| Variable | Purpose | Written by daemon | Resolution priority |
| --- | --- | --- | --- |
| `SERVERDESK_CREDENTIALS_STORE` | service-owned managed store for authenticated UI/API enrollment | yes | 1 |
| `SERVERDESK_CREDENTIALS_DIRECTORY` | legacy credential source and migration compatibility | no | 2 |
| `CREDENTIALS_DIRECTORY` | systemd `LoadCredential=` private runtime mount | never | 3 |

If `SERVERDESK_CREDENTIALS_STORE` is unset, the managed store is the `credentials` directory beside
`config.local.json`. The daemon never creates or modifies files in either read-only source. Do not
reuse a credential name across sources.

## Authenticated management enrollment

With `require-references`, an operator may submit a plaintext credential over the authenticated,
same-origin HTTPS management API. The configuration store:

1. creates a random, versioned `serverdesk.managed.*` name;
2. writes the value to the protected managed store;
3. fsyncs the credential and configuration temporary files;
4. atomically replaces the config with a `secret://...` reference; and
5. removes obsolete service-generated generations only after a durable config commit.

If provisioning, serialization, fsync, or replacement fails before commit, newly created unreferenced
generations are removed. If the visible rename succeeds but directory sync reports an error,
Serverdesk keeps both old and new credential generations and continues with the visible config so
runtime and disk do not diverge. Cleanup never deletes externally supplied names. Notification
webhook paths follow the same path and are never returned by the settings API.

## One-time plaintext migration

Stop Serverdesk, back up the state volume, and run migration as the service identity:

```bash
sudo systemctl stop serverdesk
sudo install -d -o serverdesk -g serverdesk -m 0700 /var/lib/serverdesk/credentials
sudo -u serverdesk /opt/serverdesk/serverdesk \
  -c /var/lib/serverdesk/config.local.json \
  -migrate-secrets /var/lib/serverdesk/credentials
```

The command converts live secret fields to references, enables `require-references`, and preserves
the original as `config.local.json.pre-secrets.bak`. That backup contains the old plaintext: move it
offline and remove it from the host after validation. Migration rejects symlink/non-regular,
oversized, malformed, or unsafe input and conflicting existing credentials.

## Optional systemd read-only credentials

Externally provisioned credentials may remain root-owned and be exposed read-only with a drop-in:

```ini
[Service]
LoadCredential=vendor.ft-a.admin_password:/etc/serverdesk/credentials/vendor.ft-a.admin_password
```

Use `LoadCredentialEncrypted=` and `systemd-creds encrypt` where supported, then run
`systemctl daemon-reload` and restart Serverdesk. UI-created or rotated credentials always go to
`SERVERDESK_CREDENTIALS_STORE`.

To create a managed value without placing it in argv or JSON, stop the service and send the value on
standard input with a trailing newline:

```bash
sudo -u serverdesk /opt/serverdesk/serverdesk \
  -set-device-secret serverdesk.pve-a.password-v2 \
  -credentials-dir /var/lib/serverdesk/credentials
```

The operation is create-only. Rotation uses a new versioned name; retain the prior generation until
the new configuration passes validation. Back up the config and managed credential directory
together—a `secret://` reference cannot recover its missing value.
