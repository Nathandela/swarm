# Remote-control operator runbook

The sole relay is the Cloudflare relay-v2 Worker at
`wss://s.nathan-delacretaz.workers.dev`. It replaces the retired `swarm-relay`
binary, VPS, Caddy, Docker, bbolt and GCP relay deployment paths. There is no
staging relay and no self-hosted fallback.

The recorded owner-only relay pilot activated admission on 2026-09-06 for
namespace `owner-v2-20260906` and one machine. The [deployment evidence](../verification/remote-scale-to-zero/implementation.md#owner-only-foreground-relay-activation)
records the bounded hosted checks. This is a dated checkpoint, not a live-state
assertion: verify the deployed Worker configuration and current authorization
before operating it. Local workerd and emulator checks do not authorize public
Worker use. The pilot did not complete push enrollment or all phone acceptance
gates.

The source of truth for Worker configuration and the bounded post-upload smoke
check is [`services/relay/README.md`](../../services/relay/README.md). The
product constraints and remaining hosted gates are in the
[scale-to-zero plan](../specifications/remote-scale-to-zero-plan.md).

## Provision a machine

Install the two machine binaries from the same revision and put both on `PATH`:

```sh
BIN="$(mktemp -d)"
go build -o "$BIN/swarm" ./cmd/swarm
go build -o "$BIN/swarm-remote" ./cmd/swarm-remote
export PATH="$BIN:$PATH"
```

Once the owner has supplied an authorized namespace, provision the machine with
the sole relay URL. Web PKI is the normal policy for the Worker endpoint.

```sh
swarm remote init \
  --relay-url wss://s.nathan-delacretaz.workers.dev \
  --relay-namespace "$SWARM_RELAY_NAMESPACE" \
  --relay-tls-policy webpki
```

`remote init` creates or retains the local machine identity, writes the shared
relay-v2 profile with owner-only permissions, and installs the gateway
supervision unit. It is safe to repeat. With no paired phone the gateway stays
quiescent by design.

Do not hand-write `relay.json`, use `ws://` outside local tests, add a TLS pin
to the Web PKI Worker endpoint, or deploy a relay server beside the machine.

## Pair and operate

Run `swarm remote pair`, scan the displayed QR on the phone, and compare the
SAS. A successful pairing activates the local gateway. `swarm remote status`
and `swarm remote devices` show the resulting state.

If a device is lost or suspected compromised, act in this order:

```sh
swarm remote off
swarm remote revoke <device-id>
```

`off` is the immediate local fail-safe. Re-enable only after the device state is
understood:

```sh
swarm remote on
```

Use `swarm relay doctor` after provisioning or when a configured Worker cannot
be reached. It checks the saved relay-v2 profile and TLS path; it does not open
admission or substitute for a real paired-phone test.

## Limits of this runbook

Relay-v2 stores durable state in Cloudflare Durable Objects; removing the old
bbolt server does not remove backup/restore responsibilities. The v1 deployment
source is retired, but no live GCP resource was deleted by that source change;
see the [retained GCP inventory](gcp-production-iac.md). Cloudflare Worker
deployment, admission changes, production credentials and payment changes
remain owner-controlled gates, not routine operator steps. The 2026-09-06
evidence records a relay pilot and bounded phone foreground acceptance; it does
not establish current live state, push enrollment, background delivery or full
remote-control acceptance.
