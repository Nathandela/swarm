# Local relay-spike evidence

The normative proposed architecture and migration protocol are in
[`remote-scale-to-zero-plan.md`](../../../specifications/remote-scale-to-zero-plan.md).
This document records only local runtime evidence, source observations, and the
limits of this probe. It is not production protocol pseudocode.

## Reproduced local result

`npm run test:all` starts and stops local Wrangler itself. It passed on Node
22.16.0 with Wrangler 4.129.0 and local SQLite Durable Objects. The test uses
two inbound WebSockets and exercised:

* pre-upgrade rejection of a bad fixture ticket;
* `acceptWebSocket`, attachment API calls, `getWebSockets`, SQLite storage and
  `setAlarm`;
* opaque-envelope store before best-effort delivery, reconnect catch-up and
  monotonic ACK/high-water rejection;
* source/target fixture authorization, staggered physical retention expiry and
  revoke closing current subscribers; and
* negative controls for ticket identity binding and JavaScript `Number` loss of
  uint64 precision.

This uses a shared HMAC solely as a local routing/auth fixture. It is **not**
Swarm authentication, a v2 ticket format, a production authorization scheme,
or evidence that a WebSocket attachment survived a real eviction. The local
runtime cannot force/observe Cloudflare production eviction, hibernation billing,
edge latency, provider capacity, or production durability. Those require the
capped hosted gates in the main plan.

## Current-source findings relevant to partitioning

The latest source is multi-machine at rest, despite older comments describing
the legacy singleton path. `mobile/machines.go:1-23` and
`internal/phonecore/machineregistry.go:1-27,194-246` implement a registry with a
separate namespace per machine pairing. Each namespace has an independent
`device.key`: `internal/phonecore/keycustody.go:80-111` opens or generates it in
the namespace directory. Thus phone relay-auth keys/RIDs, sequence spaces and
cursors are per pairing once migrated; a machine-scoped home does not split a
current global phone mailbox. The present live-connection implementation is
still staged: `mobile/machines.go:20-23,230-248` says it does not yet run a
separate relay loop for every registry entry.

Machine relay identity is durable unless deliberately replaced. `swarm remote
init` loads an existing `remote/machine.key` and creates one only if absent
(`cmd/swarm/remote.go:129-145,205-228`), with byte-identical repeat-init coverage
in `cmd/swarm/remote_test.go:114-143`. `machineid.Generate` makes an entirely new
identity bundle, including relay-auth key (`internal/remote/machineid/machineid.go:56-84`);
ordinary revoke rotates epoch keys instead (`:145-164`). There is no current
transparent machine relay-auth key-rotation API.

Therefore the bounded initial partition can deterministically name a home from
the configured operator namespace and current machine relay RID. A normal host
migration retaining `machine.key` retains that home. Loss/replacement of the
machine key is a new identity/home and explicit re-pair/new-grant event, not
transparent continuity. Existing consent binds the grantee RID
(`internal/remote/pairing/pairing.go:170-185`), so a prior consent cannot
authorize a newly generated RID. Old homes retain their retired-consent and
revocation state; no random per-pair nonce may select a clean home for the same
RID.

## Cross-runtime numeric constraint

Current Go cursor fields are `uint64`. JavaScript `Number` loses precision above
2^53-1 and SQLite signed `INTEGER` cannot represent all uint64 values. V2 must
use canonical decimal strings on the wire, use `BigInt` for JavaScript arithmetic,
and use fixed-width 20-digit TEXT only for sortable storage keys. Reject numeric
JSON values, noncanonical syntax and overflow; never wrap at exhaustion. The
local negative control covers the Number-loss half, but the full codec and Go ↔
runtime vectors are separate evidence.

## Explicit omissions

The probe intentionally does not implement real challenge/auth verification,
membership/revocation authorization, source quotas, bounded indexed catch-up,
append deduplication, Cloudflare deployment, production crypto, semantic command
execution, operation receipts, or raw-input retry policy. The relay is E2EE-blind;
endpoint behavior remains the authority for those effects.

An alarm is compatible with hibernation but is not free. The spike corrected an
earliest-expiry scheduling bug and its staggered test proves only the local alarm
contract. Production must enforce logical expiry during reads, physically delete
in bounded batches, reschedule remaining work and measure its cost.

After any migration target mutation, do not automatically roll traffic back to
an old snapshot. Keep the current authority fenced and perform a forward,
fenced return migration if necessary.
