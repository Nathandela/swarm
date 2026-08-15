# Metadata disclosure — what the relay operator and the push provider actually observe

**Scope: PB-OPS-3.** ADR-007 D11 states the rule this document obeys: *"the exposure is
documented, retention is bounded, logs carry no bodies, and the 'managed hosting leaks nothing'
claim is withdrawn."* D11 also forbids claiming **less** exposure than exists, which is why several
entries below (§1a, §2b, §2c, §3, §4) are corrections to earlier, more flattering statements rather
than new findings — §3 (2026-08-15, ADR-015) adds the gateway as a party this document previously
had no occasion to name at all. That they keep being needed is itself the point: each was found by
someone re-deriving a claim this document already made, and none was found by re-reading it.

E2EE hides payloads. It hides none of the following.

> **This document's ADR status.** PB-OPS-3's acceptance criterion is "ADR section consistent with
> PB-PUSH-3 and ADR-007 D11". The section text is drafted in
> `docs/verification/remote-phaseB-s20-evidence.md` for merging into ADR-007 by the owner of that
> file; this document is the operator-facing form and the two must not diverge.

---

## 1. The relay operator

The relay is **untrusted by design** and it is assumed to be honest-but-curious at best. It holds,
and can read:

| What | Where | Retention |
|---|---|---|
| **Which machines and devices exist**, as routing ids | `bucketTokens`, mailbox keys, presence, authorization records | For the life of the pairing |
| **Which device is authorized to append to which target** | authorization records | For the life of the pairing |
| **Connection and presence timing** — when a machine's gateway is connected, when it went silent | presence entries | Not persisted |
| **Message sizes and cadence** — every mailbox append, its size and its arrival time | mailbox items | Purged after ack, and hard-capped by `retention_cap` (default 7 days) |
| **Per-item sender routing id**, since ADR-007 B27/B28 | mailbox item record | With the item |
| **Push trigger timing** | push rate window | Not persisted |
| **The device's push token** — see §2 | `bucketTokens` | Until deleted or the device is revoked |
| **The real client's external IP**, when the relay sits behind a configured trusted reverse proxy | in-memory quota maps (`authRate`, `opsRate`), keyed by `serverConn.sourceKey` | Not persisted, not logged; reaped when the last connection sharing that key disconnects |

It does **not** hold, and cannot read: session names, hostnames, agent names, Group labels,
terminal output, keystrokes, or any command body. Those are sealed under keys the relay never sees.
Logs carry no bodies.

> **AMENDED BY ADR-018 (2026-08-15):** Multi-machine pairing (wave R4) widens what the first row
> above discloses. Two machine pairings that share one relay put two routing ids behind the same
> handset's connection, presence and timing pattern, so that relay can infer the two machines are
> co-owned — a designed disclosure, not an incident (`ADR-018:123`). It stays inside the scope
> above: still routing ids, timing and presence, never content, and each mailbox keeps its own
> content key. A self-hosted relay operator is already trusted with one machine's metadata; pairing
> multiple machines to the same relay extends that trust to the fact that they are the same
> owner's. A user who does not want the inference splits the pairings across relays.

> **AMENDED (2026-08-15, R2 "proxy-quota", playbook 6.5):** the client-IP row above is new. Before
> this work package, a Caddy-fronted relay saw every client's transport peer as `127.0.0.1` — no
> client IP was ever derivable, by design. `trusted_proxies` (config, default empty) changes that:
> when the TCP peer that reached the relay is in that list, the relay recovers the real client's
> address from the rightmost `X-Forwarded-For` hop and uses it in place of the peer address to key
> `max_concurrent_connections_per_source` / `conn_per_min` — otherwise every real client behind one
> proxy would collapse into one shared quota bucket. The recovered address lives only in the
> in-memory `authRate`/`opsRate` maps for as long as a connection from it is open; it is never
> written to `relay.db`, never logged, and `removeConn`'s existing per-source-key reaping deletes
> the map entry the moment the last connection sharing it disconnects — the same reaping every
> other `sourceKey` already gets, not a new retention path.

### 1a. The push token is a NEW durable device identifier at rest in an untrusted store — LEGACY

> **AMENDED BY ADR-015 (2026-08-15):** Push moves to the Swarm-operated gateway (§3). Once a pairing
> migrates its `push_transport` from `legacy_relay` to `gateway` (playbook §12), the relay holds no
> provider-issued device identifier at all — the three properties below become properties of the
> gateway's installation record instead, keyed by the opaque push address rather than the routing
> id, and are restated at §3. What follows describes the legacy relay-hosted transport, which a
> pairing keeps only for the length of the compatibility window.

This is the correction. The relay's store was documented as holding "only ciphertext + routing
metadata". Making push tokens survive a relay restart (PB-PUSH-6) added something else: a
**provider-issued, long-lived, device-specific identifier**, in the clear, keyed by routing id.

- It is **not encrypted**, and cannot be: the relay must hand the token to the push provider, so it
  cannot be opaque to itself.
- It is **correlatable with the mailbox by routing id** — the same key indexes both — so an
  operator (or anyone who obtains the store) can tie a specific handset's push identity to that
  handset's message cadence and presence history.
- It is **the same identifier the push provider holds**, which is what makes it a *join key*
  between two parties who otherwise see disjoint views. Neither party alone learns much; the token
  is what would let them collude usefully.

It lives in its own named bucket (`tokens`) rather than being smuggled into the mailbox item log,
so an operator auditing the store can find every device identifier in one place. That is an
auditability property, not a confidentiality one, and it is fenced by
`TestPBPUSH6_TokenIsNotStoredInTheClearAlongsideTheCiphertext`.

**Deletion is as durable as registration.** A token the device deleted or the owner revoked is
removed in the same transaction as the revocation, so a restart cannot resurrect it and resume
waking a handset that was deliberately silenced.

### 1b. Retention is bounded, and the bound is operator-tunable

`retention_cap` purges mailbox items that old even if never acked; the default is 7 days. Presence
is not persisted. An operator who raises `retention_cap` extends exactly the window in the first
table above, and nothing in the system tells the user they did.

---

## 2. The push provider (Google, via FCM v1)

> AMENDED BY ADR-015 (2026-08-15): this section, §2b and §2c describe the **legacy relay-hosted
> transport**'s fixed 78-byte header — the one the relay still produces during a pairing's
> `push_transport` compatibility window (§1a). The target gateway design (§3) replaces it with a
> 74-byte `WakeV1` object (ADR-015 P8) and removes the relay's second producer (P10), leaving one
> producer where §2c below counts three. R3 builds that transport; until it ships, what follows is
> what Google observes today, against the legacy transport only.

The provider observes **the token, the timing, the size, and two cleartext header fields** — and
PB-PUSH-3 records in as many words that the shorter "token, timing, size" claim was **false for the
obvious implementation**, which is why it is enforced rather than asserted. It was still
incomplete after that enforcement; §2b is the correction.

`crypto.Envelope.Marshal` emits a **62-byte cleartext header** carrying version, type, epoch id,
seq, `RecipientKeyID` (8 bytes), `SenderKeyID` (8 bytes) and `IssuedAt`. Put into a push payload
verbatim, that hands the provider **two stable endpoint identifiers** linking every wake to one
machine/device pair for the life of the epoch, plus a monotonic wake counter. So, per ADR-007 B20:

- **The wake envelope's key ids are ZERO on the push path.** Both `RecipientKeyID` and
  `SenderKeyID` are zeroed; the provider gets no stable endpoint identifier out of the payload.
- **The payload is a constant 78 bytes** — the 62-byte header plus a 16-byte AEAD tag over an
  **empty** plaintext. Size is a benign disclosure only while it is *constant*. A size that varied
  with the session name, or with how many transitions were coalesced, would be a covert channel,
  and this document's honesty claim would quietly become untrue with nothing failing.
- **`seq` and `IssuedAt` are NOT zeroed and are NOT encrypted.** This is the bullet the list
  stopped one short of. The paragraph above names the counter and the previous version of this
  section then addressed only the two fields B20 changed, so a reader reasonably concluded the
  header had been closed. §2b states what those two fields disclose and why they are still there.

The first two are pinned by test (`TestPBPUSH3_WakeEnvelopeIsExactlyTheFixedContentFreeSize`,
`_WakeHeaderCarriesNoStableEndpointIdentifiers`, `_WakePlaintextIsEmptyAndNamesNothing`). **The
third is pinned the other way up** — `_WakeCarriesAMonotonicReplayCoordinate` and
`_WakeSeqDoesNotRestartAfterAGatewayRestart` require the counter to be present, increasing and
durable, because replay resistance depends on it (§2b).

So the provider observes, per wake:

| What | Detail |
|---|---|
| **The device's FCM registration token** | A durable, device-specific identifier. Also held by the relay during the legacy transport (§1a) — the target design moves this to the gateway (§3, ADR-015). |
| **Timing** | Every wake, as delivered. Transitions are coalesced per session, so this is a lower bound on activity, not a transcript. |
| **Size** | Constant 78 bytes on the **legacy transport** this section describes, and constant across every producer into that channel — see §2c. The target design moves the constant to 74 bytes with one producer (ADR-015 P8/P10). |
| **A cleartext monotonic wake counter** | `seq`, 8 bytes at a fixed offset in the cleartext header. See §2b. |
| **A cleartext millisecond timestamp** | `issued_at`, the sending machine's clock. See §2b. |
| **The opaque push address** — **target design only**, not on the legacy transport this section otherwise describes | `WakeV1`'s routing coordinate, cleartext at a fixed header offset (ADR-015 P8 delta 1). The legacy header carries none — two zeroed key-id fields instead. §3 records what this field, stable per machine pairing, lets the gateway and FCM infer together. |
| **That the message is data-only and high priority** | Delivery-class metadata, not content. |

It does **not** observe, on the legacy transport this section describes: session names, hostnames,
agent names, Group labels, which device, how many sessions exist, or what changed. The payload's
plaintext is empty — there is nothing inside it to name anything. **"Which machine" is struck from
this list deliberately**: it held only while the header carried no address to observe. The target
`WakeV1` design's opaque push address (row above, §3) is a stable per-pairing identifier, so under
that design the provider *can* tell which machine, not merely which device.

The list above is what the payload does not name. It is **not** a claim that the payload is
information-free; §2b and §2c are the two ways it is not.

### 2a. What the provider infers anyway, stated because D11 forbids understating it

A token plus wake timing is an **activity trace**. Someone who knows which human holds that token
learns when their agents change state, at whatever granularity the coalescing window allows. That
is unavoidable for any push-woken design and is not fixed by an empty payload. The honest statement
is that the *content* is hidden and the *rhythm* is not.

### 2b. The wake carries a cleartext counter, and this section used to imply it did not

This is a correction, in D11's sense: the exposure was understated. The **Size** row above read
"Constant 78 bytes. Carries no information." That sentence is defensible about *size* — a constant
is information-free — but it sat at the end of a table that purported to enumerate what the
provider observes per wake, and the enumeration was incomplete. **Zeroing the key ids was treated
as having closed the header.** It closed two fields of it.

Measured with a **keyless reader** — raw byte offsets, no key, no parser, the provider's own
position:

    wake 0: size=78 seq=1 issued_at=1700000000000 recipient_key_id=00.. sender_key_id=00..
    wake 1: size=78 seq=2 issued_at=1700000120000 recipient_key_id=00.. sender_key_id=00..

`crypto.Envelope.Marshal` writes `seq` as a big-endian `uint64` at **bytes 6:14**, ahead of the key
ids, and `issued_at` at **bytes 30:38**. Both are outside the AEAD's confidentiality (they are
authenticated, not encrypted). So:

- **The counter is a wake odometer.** Two wakes an observer actually receives tell them exactly how
  many were sent in between, including any they never saw — a relay that dropped them, a phone that
  was off, a window they were not watching. It converts a sampled view of activity into an exact
  count.
- **`issued_at` is the sending machine's clock, to the millisecond.** Finer than the delivery timing
  the provider already has, and a clock reading is a weak fingerprint of the machine that made it.

**The counter is load-bearing and cannot simply be removed.** PB-PUSH-3 gives the wake a 10-minute
TTL "with the replay coordinate persisted per PB-STATE-1"; `TestPBPUSH3_WakeCarriesAMonotonicReplayCoordinate`
requires it to be strictly increasing, and `_WakeSeqDoesNotRestartAfterAGatewayRestart` requires it
to survive a restart. A wake with no replay coordinate is a wake the relay can replay. **This is a
real trade, not an oversight to fix**: the design buys replay resistance with a cleartext counter,
and the honest statement is that the provider learns the count. Removing the disclosure means
finding a replay coordinate the provider cannot read, which nothing here has designed.

The requirements table already named "plus a monotonic seq" as part of the cleartext header
(`remote-phaseB-requirements.md`, PB-PUSH-3). **The requirement never claimed the counter was
removed; this document is where it went unmentioned.**

### 2c. There are THREE push producers; size is now constant across them, shape is not

Size used to belong to **one** of them. This section records what that was, what closed it, and the
part of it that is still open — the last being the point, because closing a disclosure on one
channel while it survives on the next one over is how a green test comes to mean less than it reads.

**What it was.** Measured through the real marshaller:

    gateway wake  : 177 bytes   {"message":{...,"data":{"e":"<104 base64 chars>"},...}}
    presence sweep:  73 bytes   {"message":{...,"data":{"e":""},...}}

`SweepPresence` called `deliverPush(..., PushPayload{Alert: GenericPushAlert})` with no `Ciphertext`
set, and `push_trigger` applied no schema to the caller's `envelope`, so a third size was whatever
that caller chose. **A 104-byte difference, distinguishable with no key**, shipping in normal
operation: the provider could tell *"a session changed state"* from *"this machine went offline"* —
a semantic fact about the owner's infrastructure, read off payload shape without touching crypto.

**What closed it.** Every producer now puts `relay.PushEnvelopeSize` (78) bytes of ciphertext on the
channel:

| Producer | Where | Payload |
|---|---|---|
| The gateway's wake | `handlePushTrigger`, carrying the sealed envelope | 78-byte sealed ciphertext |
| The relay's presence sweep | `SweepPresence`, when a machine goes silent past `PresenceTimeout` | 78 random bytes it cannot seal |
| Any other caller of `push_trigger` | `handlePushTrigger` | **refused** unless it is exactly 78 bytes |

The relay **refuses** rather than pads, because it forwards the opaque envelope byte for byte and
must keep doing so; a refused push puts nothing on the channel and so discloses nothing. The sweep
is the one producer that cannot be given a real envelope: it is a liveness signal, the relay holds
**no** key to seal one with (which is what the two-tier key split is for), and it carries no epoch
coordinate that would go inside one.

**The fence moved to the channel.** `TestPBPUSH3_WakeEnvelopeIsExactlyTheFixedContentFreeSize` lives
in `internal/remotegw` and asserts over the gateway's own `Pusher` — a correct assertion about the
only producer it can construct. The quantifier is now enforced where the property is stated:
`internal/remote/relay/pbpush3_channel_test.go` drives all three producers through the real sender
into a recording endpoint and compares **raw request bytes**, and
`pbpush3_producers_test.go` pins that those three ARE the set, so a fourth producer fails by name.

**Still open: the sweep is separable by SHAPE, just not by SIZE.** A genuine wake's envelope header
is cleartext (§2b — version, type, epoch id, `seq`, `issued_at`, a trade the replay window rests
on), so a provider that **parses** rather than measures still sees that a sweep's 78 bytes are not a
well-formed wake. The random filler is chosen over a fixed one so that the separation is not also
handed over as a constant byte pattern, but it does not make the two indistinguishable. Closing that
needs either a key the relay must not hold, or dropping the sweep's push entirely — and the sweep's
push is already refused at the receiver (`phonecore.AcceptWake` cannot authenticate it, so
`SwarmMessagingService` renders nothing), so what it costs today is a wakeup and a provider-visible
event with no user-visible result. **Both remedies are decisions above the relay seam.**

> AMENDED BY ADR-015 (2026-08-15): "Both remedies are decisions above the relay seam" — one now
> is taken: ADR-015 P10 takes the second branch, the relay loses its push transport, so it has no
> producer left to separate. The residual closes by removal of its subject, not by a new fence
> (§3 describes the target design). **Also note the term collision this table introduces**: "The
> gateway's wake" row above names `internal/remotegw`, the machine-side push-trigger caller — not
> the Swarm push gateway of §3, an external HTTPS service ADR-015 adds. The two share a word and
> nothing else.

### 2d. Nothing has been delivered against Google yet, but the Firebase project now exists

> **AMENDED BY ADR-015 (2026-08-15):** "There is no Google account, no Firebase project" is corrected
> rather than left to rot: Firebase project `swarm-8404f` exists, the Android app `dev.swarm.phone`
> was registered on 2026-08-14, the FCM v1 API is enabled, and the sender/project number is
> `733314021126`. What has **not** changed: no production token has been collected, the Google
> Services plugin is not applied to a shipping build, and `google-services.json` is present locally
> only and deliberately untracked.

The FCM sender is fully implemented and tested against a fake endpoint. **No claim in this section
is evidence that a wake was ever delivered**, and none is a measurement of what Google's
infrastructure retains — every delivery claim here is a design commitment, not evidence. PB-E2E-5
(physical handset, real provider) is deferred.

Running with push unconfigured is supported and removes the provider from the picture entirely
(PB-PUSH-5); the cost is that a backgrounded handset learns about a transition when it next
connects.

---

## 3. The Swarm push gateway

> **AMENDED BY ADR-015 (2026-08-15):** New party, new section. ADR-015 splits the FCM sender off the
> relay entirely: Swarm operates a small versioned HTTPS gateway that Android registers with and
> that `swarm-remote` submits wakes to. It sits beside §1 (the relay, which after migration holds
> no push credential of its own) and §2 (the provider), and it is a **named party in the adversary
> set** — the fourth, after the relay operator, Google, and whoever runs the network path (§4). A
> colluding relay-and-gateway pair still cannot decrypt content or forge a command (playbook
> §11.3); what the gateway alone can observe is fixed and disclosed below.

| What | Where | Retention |
|---|---|---|
| **The opaque, gateway-minted push address** — a stable per-pairing routing coordinate, carried in the wake's cleartext header | Registration/allocation records | Until the address is revoked |
| **The submit-source IP** — the machine running `swarm-remote`, not the handset | Delivery diagnostics | 7 days |
| **Wake timing** | Delivery diagnostics | 7 days |
| **A fixed or padded wake size** | Not persisted beyond the request | Not persisted |
| **The FCM delivery outcome** (accepted / refused / `UNREGISTERED`) | Delivery diagnostics | 7 days |
| **The FCM token, the installation public key and hashed capability verifiers** — the join key that used to live at the relay (§1a); the token is encrypted at rest and excluded from logs and traces | Installation records | 180-day inactivity expiry |
| **An unbound allocation** — a pending pairing that has not yet completed | Allocation records | Ten minutes, or immediately on a failed pairing |

**The join key travels with the same three properties §1a named, against the opaque push address
rather than the routing id — a rewrite of subject, not a new finding, except where the rewritten
subject's own answer changes.** It is **readable by the gateway itself, and cannot be otherwise**:
the gateway must hand the token to FCM, so it cannot be opaque to itself. **Unlike the legacy
relay-hosted store §1a describes, this is where the subject's answer changes**: the gateway's copy
**is encrypted at rest and excluded from logs and traces** (ADR-015 P11, playbook §6.6:531). It is
**correlatable with delivery diagnostics by push address** — the same address indexes both — so
an operator of the gateway (Swarm, not a self-hoster) can tie one installation's push identity to
its wake cadence. And it is **the same identifier the push provider holds**, the join key between
two parties whose views are otherwise disjoint. That is an auditability property, not a
confidentiality one.

It does **not** hold, and cannot receive: a relay URL, relay-auth key, pairing secret, content key,
device command key, daemon credential, or the wake key itself. A compromised gateway can spam,
suppress or correlate wakes; it cannot mint a wake authenticator, submit a command, or read a
conversation.

**One handset, several machines, one correlation the token alone did not give.** Under multi-machine
pairing, one phone presents one FCM token but a distinct opaque push address per machine. The
gateway and FCM can therefore count and separate a handset's machines from each other — an
inference the token alone did not support. The address stays opaque and gateway-minted, names no
machine the provider can otherwise resolve, the plaintext stays empty, and the size stays one
pinned constant.

---

## 4. Anyone on the network path

> AMENDED BY ADR-015 (2026-08-15): this section was **§3** before the gateway section above was
> inserted. `docs/verification/remote-phaseB-s20-evidence.md` cites it by the old number as dated
> evidence and is correctly left unedited by convention — read its "§3" as this section.

TLS between the phone and the relay hides the routing metadata of §1 from a passive observer, which
is the whole reason PB-NET-2 refuses cleartext outside the loopback carve-out. Two limits:

> **CORRECTED 2026-07-30.** This section previously stated that the gateway dialled with
> `relay.Dial` rather than `relay.DialSecure`, and that the mobile façade and pairing did the same
> — "recorded as an open finding, not a property". **Both statements were false at the time this
> correction was made**, and had been since ADR-007 B34/B37 applied the policy. The stale text is
> replaced rather than annotated in place because it described a missing safeguard that exists:
> a reader acting on it would have gone looking for a hole that was already closed, and
> `relay-runbook.md` §0 already carried the correct account — two operator documents in the same
> directory disagreeing about a security property.

Every dial path now carries a `relay.Security`, verified at HEAD:

| Path | Call | Policy |
|---|---|---|
| Gateway sidecar | `cmd/swarm-remote/main.go` → `relay.DialSecure` | `relay.MachineSecurity()` |
| Handset | `mobile/relay.go` `App.dial` → `relay.DialSecure` | `App.handsetSecurity()` |
| Handset pairing | `mobile/pairing.go` → `relay.DialRawSecure` | `relay.PairingSecurity()` |

No production call site uses the insecure `relay.Dial` or `relay.DialRaw` entry points. So the
cleartext refusal, the redirect re-check and the pin apply on every hop, and a `ws://` URL to
anything but a loopback literal is refused outright rather than run unverified.

Three limits that remain, stated because D11 forbids reading the table above as more than it is:

- **The handset's pin is conditional on provisioning.** `handsetSecurity` sets
  `PinnedSPKISHA256` only `if pin := a.core.State().RelaySPKIPin; len(pin) > 0`. A handset with no
  pin provisioned still gets TLS and the cleartext refusal, but validates against platform trust
  roots alone — so it is protected from a passive observer and from cleartext downgrade, not from a
  CA that mis-issues for the relay's name.
- **Loopback cleartext is admitted on every path**, deliberately: a `ws://127.0.0.1:PORT`
  connection has no on-path position for an observer to occupy. It is the gateway's normal
  configuration (`relay-runbook.md` §0).
- **Traffic volume and timing are visible to a network observer regardless of TLS, at both hops.**
