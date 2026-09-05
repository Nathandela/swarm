# Relay v2 Worker

This is the bounded Cloudflare Worker + SQLite Durable Object foundation specified in [`remote-relay-v2-protocol.md`](../../docs/specifications/remote-relay-v2-protocol.md). It has no v1 codec, wait/poll path, HMAC ticket or deployment defaults.

Run the actual local-workerd suite with pinned Wrangler 4.129.0. The same command also runs the native Go Noise/SAS, encrypted mailbox, reconnect, replay-fence and revoke vertical slice against that Worker:

```sh
npm ci
npm test
```

Set `RELAY_TEST_PORT` when 8790 is unavailable. The runner refuses an occupied port, uses unique temporary config/state/log directories, has a 90-second outer deadline (including a cold Go build) and terminates the whole Wrangler/workerd process group on success, failure, timeout or interruption.

Deployment requires non-empty `OPERATOR_NAMESPACE` and `ALLOWED_MACHINE_RIDS`; absent configuration fails closed. This repository does not create or deploy paid resources.
