# Metadata disclosure — what the relay operator and the push provider actually observe

**Scope: PB-OPS-3.** ADR-007 D11 states the rule this document obeys: *"the exposure is
documented, retention is bounded, logs carry no bodies, and the 'managed hosting leaks nothing'
claim is withdrawn."* D11 also forbids claiming **less** exposure than exists, which is why several
entries below (§1a, §2b, §2c, §3) are corrections to earlier, more flattering statements rather
than new findings. That they keep being needed is itself the point: each was found by someone
re-deriving a claim this document already made, and none was found by re-reading it.

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

It does **not** hold, and cannot read: session names, hostnames, agent names, Group labels,
terminal output, keystrokes, or any command body. Those are sealed under keys the relay never sees.
Logs carry no bodies.

### 1a. The push token is a NEW durable device identifier at rest in an untrusted store

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
| **The device's FCM registration token** | A durable, device-specific identifier. Also held by the relay (§1a). |
| **Timing** | Every wake, as delivered. Transitions are coalesced per session, so this is a lower bound on activity, not a transcript. |
| **Size** | Constant 78 bytes, for this producer. See §2c: it is not the only producer. |
| **A cleartext monotonic wake counter** | `seq`, 8 bytes at a fixed offset in the cleartext header. See §2b. |
| **A cleartext millisecond timestamp** | `issued_at`, the sending machine's clock. See §2b. |
| **That the message is data-only and high priority** | Delivery-class metadata, not content. |

It does **not** observe: session names, hostnames, agent names, Group labels, which machine, which
device, how many sessions exist, or what changed. The payload's plaintext is empty — there is
nothing inside it to name anything.

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

### 2c. There are TWO push producers, and they are separable by shape

The 78-byte constant belongs to **one** of them.

| Producer | Where | Payload |
|---|---|---|
| The gateway's wake | `handlePushTrigger`, carrying the sealed envelope | 78-byte ciphertext |
| The relay's presence sweep | `SweepPresence`, when a machine goes silent past `PresenceTimeout` | **no envelope at all** |

`SweepPresence` calls `deliverPush(..., PushPayload{Alert: GenericPushAlert})` with no `Ciphertext`
set. `FCM.marshalMessage` base64-encodes that field either way, so the provider receives:

    gateway wake  : 177 bytes   {"message":{...,"data":{"e":"<104 base64 chars>"},...}}
    presence sweep:  73 bytes   {"message":{...,"data":{"e":""},...}}

**A 104-byte difference, distinguishable with no key.** The provider can therefore tell *"a session
changed state"* from *"this machine went offline"* — a semantic distinction about the owner's
infrastructure, read off the payload shape. Constant size was the property that made size a benign
disclosure; across the two producers, size is not constant.

**The existing fence cannot see this, structurally.**
`TestPBPUSH3_WakeEnvelopeIsExactlyTheFixedContentFreeSize` lives in `internal/remotegw` and asserts
over the gateway's own `Pusher`; it does not import `internal/remote/relay` and never observes the
sweep. It is a correct assertion about the producer it watches, and it is the only producer it can
watch. A fence covering both has to sit where they converge — the `PushSink` boundary
(`internal/remote/push`), which is the first point that sees every message actually sent.

**This is recorded as an open weakness, not a fixed one.** The sweep's shape is unchanged at the
time of writing; closing it means giving the sweep the same constant-size envelope as the wake,
which is relay work and is not done here.

### 2d. Nothing here has ever run against Google

There is no Google account, no Firebase project and no `google-services.json` in this project. The
FCM sender is fully implemented and tested against a fake endpoint. **No claim in this section is
evidence that a wake was ever delivered**, and none is a measurement of what Google's
infrastructure retains — it is a statement of what this system *sends*. PB-E2E-5 (physical handset,
real provider) is deferred.

Running with push unconfigured is supported and removes the provider from the picture entirely
(PB-PUSH-5); the cost is that a backgrounded handset learns about a transition when it next
connects.

---

## 3. Anyone on the network path

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
