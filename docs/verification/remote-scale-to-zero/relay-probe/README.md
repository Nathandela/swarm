# Local Durable Object relay spike

This is an account-free compatibility spike, not production code. It uses the
pinned local Wrangler/Miniflare runtime to exercise a hibernatable, **E2EE-blind**
relay: two inbound WebSockets, pre-upgrade ticket verification, attachment API
use, durable opaque-envelope append before delivery, reconnect catch-up,
monotonic acknowledgement, an alarm that physically removes retained mail, and
revocation. Command execution, semantic receipts, raw-input safety, and end-user
plaintext are deliberately absent: those remain endpoint/daemon responsibilities.

## Reproduce

```sh
npm ci --ignore-scripts
npm run test:all
```

To rerun without copying dependencies into this evidence directory, set
`RELAY_PROBE_WRANGLER` to a compatible pinned Wrangler binary and run
`npm run test:all`.

`package-lock.json` pins Wrangler 4.129.0. No Cloudflare login, deployment, or
paid resource is used. The `RETENTION_MS=250` local value is only to make the
alarm test bounded; production retention must be policy-configured.

## What passed locally

The integration test passed against a real local DO SQLite runtime:

1. authenticated pre-upgrade routing (`route` is a sharding hint, signed ticket
   is authority);
2. two server-accepted inbound WebSockets and websocket attachment API use;
3. opaque ciphertext is stored before `DELIVER` is attempted;
4. unacknowledged mail is re-delivered after reconnect from the durable cursor;
5. acknowledgement suppresses replay; a due alarm physically removes abandoned
   mail; and revocation closes subscribers.

The negative control proves that a ticket bound to `route:a` cannot be used for
`route:b`. Wire cursors must be canonical decimal uint64 strings; JavaScript
validates them with `BigInt`, while fixed-width 20-character decimal values are
only sortable storage keys. They are never JavaScript `Number` or SQLite signed
`INTEGER` values.

The fixture intentionally does **not** implement production revoke authorization,
source quota, membership policy beyond its two test endpoints, bounded catch-up,
append deduplication, or real Swarm authentication. Its HMAC is a test fixture.

## Important non-results

This local runtime confirms API/runtime construction and the above contract only.
It cannot force or observe Cloudflare production eviction, hibernation duration
billing, edge latency, WS request billing, or production throughput. Those need a
separate hosted, capped trial. The test also does not establish a production
crypto scheme: the HMAC is a local routing-ticket test vector, not pairing auth.
Attachment deserialization occurs in the same local process and is therefore not
evidence that state survived an actual production eviction.

The actual service must avoid `setTimeout`, `setInterval`, awaited outgoing work,
or outgoing WebSockets in the hibernatable actor. Scheduled storage alarms are
compatible with hibernation, but each execution is billable. Auto WebSocket
response can avoid duration wakeups; it is not evidence of zero request billing.
