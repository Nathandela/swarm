# Relay v2 Worker

This is the bounded Cloudflare Worker + SQLite Durable Object foundation specified in [`remote-relay-v2-protocol.md`](../../docs/specifications/remote-relay-v2-protocol.md). It has no v1 codec, wait/poll path, HMAC ticket or preconfigured machine admission.

Run the actual local-workerd suite with pinned Wrangler 4.129.0. The same command also runs the native Go Noise/SAS, encrypted mailbox, reconnect, replay-fence and revoke vertical slice against that Worker:

```sh
npm ci
npm test
```

Set `RELAY_TEST_PORT` when 8790 is unavailable. The runner refuses an occupied port, uses unique temporary config/state/log directories, has a 90-second outer deadline (including a cold Go build) and terminates the whole Wrangler/workerd process group on success, failure, timeout or interruption.

## One live Worker configuration

`wrangler.toml` defines exactly one public Worker: `s`, at
`wss://s.nathan-delacretaz.workers.dev`. That URL is 37 bytes, within the
39-byte QR relay field. `workers_dev = true` is explicit and
`preview_urls = false`; there is no Wrangler staging environment, custom route,
fixture variable, or admission variable in checked-in deployment config.

`RATE_LIMITER` is the native [Workers Rate Limiting binding](https://developers.cloudflare.com/workers/runtime-apis/bindings/rate-limit/).
Account namespace ID `1001` is reserved for this sole relay Worker; constant `ws` and `pair` keys
each allow an initial 60 pre-authentication dispatches per 60 seconds in one
Cloudflare location. Missing or failed binding access returns 503, and an
exhausted route returns 429 before a Durable Object is opened. This is a local,
permissive counter rather than a global or exact cost ceiling: an attacker can
exhaust a location's route bucket and temporarily deny a legitimate owner.

The local OAuth profile label `swarm-staging` is only a label in the local
encrypted credential file backed by the OS Keychain. It is not a Wrangler
`[env.staging]` environment and is not a deployment target. First validate the
checked-in deployment offline; this needs no credentials or network:

```sh
WRANGLER_SEND_METRICS=false ./node_modules/.bin/wrangler deploy --dry-run
```

For a separately authorized first upload, require the operator-held account ID
before invoking Wrangler. `CLOUDFLARE_AUTH_USE_KEYRING=true` makes Wrangler use
the OS keyring backend; `--profile swarm-staging` selects only that local
credential profile. The command creates the public Worker, its native
rate-limit binding and its two SQLite Durable Object namespaces. Current
Workers pricing lists no separate rate-limit operation charge, but the first
upload is also the account-level availability check: stop if Cloudflare
requests a plan upgrade, payment or new scope. Do not add `--keep-vars` or any
`--var` values.

```sh
: "${SWARM_RELAY_ACCOUNT_ID:?set intended account ID}"
CLOUDFLARE_AUTH_USE_KEYRING=true \
  CLOUDFLARE_ACCOUNT_ID="$SWARM_RELAY_ACCOUNT_ID" \
  ./node_modules/.bin/wrangler deploy --profile swarm-staging
```

After an authorized upload, the only bounded smoke checks are the public root
and deliberate empty-admission refusal:

```sh
test "$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' https://s.nathan-delacretaz.workers.dev/)" = 200
for path in '/v2/ws?machine_rid=00000000000000000000000000000000' \
            '/v2/pair?ceremony=00000000000000000000000000000000'; do
  test "$(curl --http1.1 -sS --max-time 10 -o /dev/null -w '%{http_code}' \
    -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
    -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==' \
    "https://s.nathan-delacretaz.workers.dev$path")" = 503 || exit 1
done
```

This repository does not deploy by itself, create paid upgrades, create
secrets, or activate admission. The initial Worker has no
`OPERATOR_NAMESPACE` or `ALLOWED_MACHINE_RIDS`: it rejects v2 traffic before
Durable Object state is opened. Passing these 200/503 checks is not a
production-ready claim; active-use security and client gates remain separate.
