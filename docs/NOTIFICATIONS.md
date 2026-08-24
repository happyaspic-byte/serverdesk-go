# Server-resident critical notifications

Serverdesk can deliver critical and recovery webhooks without an open browser.
The feature is disabled by default:

```json
{
  "notifications": {
    "enabled": false,
    "webhook_url": "",
    "escalation_hours": 0,
    "retry_max": 5,
    "retry_base_seconds": 5
  }
}
```

Use the authenticated management settings screen/API to save a destination.
Under `secret_policy=require-references`, the webhook is moved to the writable
managed credential store and the config retains only `secret://NAME`. GET and
health responses expose `configured`, never the URL.

## Destination security

Slack and Discord webhook hosts are allowed by default. Add an explicitly
approved gateway using a comma-separated, non-secret environment value:

```text
SERVERDESK_NOTIFY_HOSTS=alerts.example.com,backup-alerts.example.com
```

Set this on the service process, not only in an interactive shell. On Linux,
put the value in the deployment environment file (for example
`/etc/serverdesk/serverdesk.env` referenced by the systemd unit's
`EnvironmentFile=`) and restart `serverdesk`. On Windows, set a persistent
machine environment value from an elevated PowerShell session, then restart the
Serverdesk service or scheduled task:

```powershell
[Environment]::SetEnvironmentVariable(
  "SERVERDESK_NOTIFY_HOSTS",
  "alerts.example.com,backup-alerts.example.com",
  "Machine"
)
```

Exact hosts and their subdomains are accepted. Redirects and URL userinfo are
rejected. HTTPS is required except for literal loopback/`localhost` test
destinations. The sender ignores proxy environment variables, resolves the host
for every attempt, rejects a mixed or wholly private/link-local/loopback DNS
answer, and dials only an address from that validated answer while retaining the
original TLS hostname. An explicitly allowed `localhost` destination must resolve
only to loopback addresses. Validation and delivery errors never include the URL
because its path may be a bearer token.

## Delivery behavior

- The daemon reconciles critical snapshots every 10 seconds. After restart, each
  FT cluster and the Edge collector must independently become ready before that
  source can create an initial critical transition. A failed source does not
  suppress a critical signal from another ready source. Recovery reconciliation
  remains paused while any configured source is uncertain, so stale or failed
  input cannot create a false recovery.
- Initial critical delivery is suppressed during an active device maintenance
  window, but not by acknowledgement. Acknowledgement suppresses only the
  configured 4-hour or 24-hour unacknowledged escalation.
- A delivered critical gets one recovery notification when the condition clears.
- Queue and condition state live in
  `runtime_dir/notification-state.json` with atomic replacement. Delivery IDs
  remain stable across restart and are sent as `Idempotency-Key`.
- Unreadable or corrupt acknowledgement, maintenance, notes, or escalation state
  pauses notification reconciliation instead of treating silence controls as
  empty. The error is exposed in authenticated health and server logs.
- Each outbound attempt is durably claimed before the request. At most 32 due
  items are handled per scan and at most 8 requests run concurrently.
- Timeouts, network errors, HTTP 408/429, and 5xx responses use exponential
  backoff capped at one hour. Other 4xx responses are dead-lettered immediately.
  Attempts stop at `retry_max`.

Successful delivery, retry, dead-letter, settings changes, and persistence
failures are written through the server audit/log pipeline without target URLs.
Authenticated `/api/admin/health` includes source readiness, pending count,
retained delivered count, dead-letter count, and last success/error timestamps.

## Management API

- `GET /api/admin/notifications` returns non-secret settings and runtime status.
- `PUT /api/admin/notifications` changes settings. Omitting or sending an empty
  `webhook_url` preserves the stored destination.
- `POST /api/admin/notifications/test` with `{}` tests the stored destination.
  Supplying `{"webhook_url":"https://..."}` validates and tests that unsaved
  destination without persisting or returning it.

All writes use the normal authenticated same-origin management gate.

## Delivery guarantee and known boundary

The queue provides restart-safe at-least-once delivery. If the process crashes
after a remote endpoint accepts a request but before the local success state is
fsynced, the stable idempotency key is reused; endpoints that ignore that header
may show a duplicate. A critical condition that begins and fully clears between
two 10-second snapshots may not be observed. These boundaries require a durable
external event stream or destination-side idempotency for stronger guarantees.
