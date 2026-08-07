# ADR-011: Multi-device epochs — per-device sender ids, per-device inbound keys, per-device seq spaces

**Status**: Accepted (owner sign-off 2026-08-07)
**Date**: 2026-08-07
**Extends**: ADR-007 (`ADR-007-remote-access.md`) decisions D2, D6, D9, closure decisions C6 and sonnet#3, and residual B1.
**Amends**: the "cheap hardening" sentence at `ADR-007:460-462` — adopted here in both its halves (M1/M2 and M9), and found **insufficient as one line**. Two further amendments, stated rather than inherited: B133's trust boundary gains a stated delta for N devices (Context, below), and the 2026-07-24 revoke amendment's gateway-exit closure gains a second trigger (M5). Nothing else in ADR-007 is superseded; the frozen envelope format, the AAD, the nonce discipline and the seq/replay rules are untouched.
**Companions**: [ADR-009](ADR-009-structured-chat-interaction.md) (the structured-chat pivot), [ADR-010](ADR-010-adapter-structured-capture.md) (the adapter-contract extension), [docs/specifications/interaction-schema.md](../specifications/interaction-schema.md) (the normative item schema).

## Context

### What actually pins v1 to one device

Not a policy toggle. Five independent things assume it, and each is load-bearing:

1. **`SenderKeyID` is uniformly zero inbound.** Every phone seal leaves it at its zero value (`internal/phonecore/direction.go:57`, `internal/phonecore/input.go:44-47`, asserted by `internal/phonecore/direction_binding_test.go:140`). The gateway's inbound bucket is therefore always `{sender: 0, epoch}` (`internal/remotegw/mailbox_in.go:131`). Two devices would share one seq space and neither would be attributable past the shared high-water (`ADR-007:535-537`, restated B1 at `:636-638`).
2. **One epoch `ContentKey`, shared by every paired device (D2).** It is what makes the machine→phone fan-out work — one ciphertext, N mailboxes, `recipient_key_id` outside the AAD (D6, `ADR-007:60`). On the phone→machine leg the same sharing means a paired device could seal a frame that authenticates as any other paired device's (`ADR-007:456-461`).
3. **Enrollment is capped at one, transactionally.** `Registry.AddSole` (`internal/remote/device/registry.go:198`) refuses a second device id under the registry mutex; `BeginPairing` fast-rejects at `internal/skeleton/pairing.go:133`; `cmd/swarm-remote/config.go:125` refuses to start at `len(devices) != 1`.
4. **The gateway is single-target by construction.** One `PhoneTarget` (`internal/remotegw/service.go:22`), one `RelaySink.Target` (`relaysink.go:45`), one durable outbound seq, one durable reply counter, one scalar `InboundHighWater()` (`service.go:246`).
5. **The reconcile record's authorities are scalars on a fanned-out frame.** `schema.ReconcileRecord.InboundHighWater` and `.ReplyCeiling` (`internal/protocol/schema/reconcile.go:26-36`) are per-device quantities carried in a record every device would open identically, and the phone adopts `InboundHighWater` **monotonically** (`internal/phonecore/core.go:578`) — unrewindable.

What is **already** N-device: the relay (per-device ciphertext mailboxes, `authorize_device`, de-authorization + purge — D9), and `crypto.MailboxReceiver`, which already keys its high-water map by `{sender, epoch}` (`internal/remote/crypto/envelope.go:202-230`) and takes the key per `Accept` call. The crypto layer is multi-sender today; everything above it is not.

### The one boundary this moves

B133 (`ADR-007:7758-7763`) puts the relay, the network path and FCM in scope as the declared adversary, and puts "the phone, and whoever is holding it" out of scope as trusted. With one device that is one trusted holder. With N it is N, and M2 below is justified **entirely against a peer**: a paired device sealing a frame that authenticates as another paired device's, or the weakest device revoking the strongest (M7). So this ADR states the delta rather than inheriting the sentence: **with N devices, each paired device is in scope as an adversary of its peers** — for inbound attribution, for lease authorization, and for who-may-revoke-whom. The relay-facing half of B133 is untouched: the relay is still the declared adversary, still ciphertext-only, still no smarter. Nothing here makes a device less trusted with respect to its *own* sessions.

### Why record the decision now

The interaction program (`docs/adr/ADR-009-structured-chat-interaction.md`, `docs/adr/ADR-010-adapter-structured-capture.md`, `docs/specifications/interaction-schema.md`) makes the phone the primary surface for approvals, which is exactly the point at which "my tablet as well as my phone" becomes an obvious ask. Deciding the scheme while single-device is still the shipping shape costs nothing; discovering it after a second handset is in the field costs a re-pair of every device. ADR-007 named the primitive but not the design; this ADR is the design.

## Decision

**M1. A device's sender id is `crypto.KeyID(commandSigningPub)`, assigned at pairing, nonzero, and unique in the registry.** `AddSole` is replaced by an `Enroll` that (a) rejects a record whose sender id is all-zero — zero stays reserved for the legacy single-device inbound bucket and for the machine's own command-reply frames — and (b) rejects a collision with a sender id already registered to a *different* device id. Both are fail-closed pairing refusals, not runtime surprises. The name is not incidental: `Registry.Add` already exists as the general uncapped upsert (`registry.go:167`) and the epoch re-grant path re-persists an already-registered record through it (`internal/skeleton/api.go:375`), so `Enroll` is a third method rather than a redefinition of that one, and a same-device re-add stays an idempotent upsert exempt from (b) — exactly as `AddSole` treats it today. The device signs commands as it does today; the canonical tuple (`internal/remote/crypto/devicesig.go:31-38`) is **not** extended, because attribution for a signed op is the verifying key, which is why `device_id` is a lookup hint never trusted alone (A1).

**M2. A per-device inbound key, delivered in the grant, derived from a machine-only epoch root.** This is the part ADR-007's one-line hardening was missing: stamping `SenderKeyID` while every device holds the same `ContentKey` gives a *label*, not attribution — a peer device can seal a frame under any label it likes. So:

- `crypto.NewEpochKeys()` mints a third value, `RootKey`, which **never leaves the machine** and is not granted.
- `K_in,d = HKDF-SHA256(secret=RootKey, salt="swarm-remote/1 inbound", info=KeyID(device recipient pub))`, following the existing domain-separation convention (`epoch.go:45`, `devicesig.go:25`, `sas.go:62`).
- The recipient-sealed `EpochGrant` carries `K_in,d` alongside the wake and content keys: `grantVersion` becomes `0x02` and `grantInnerLen` becomes `1+8+4+8+32+32+32 = 117`. `0x01` is **not** accepted alongside it — the upgrade is a forced epoch rotation and re-grant, and an un-upgraded phone fails closed rather than silently falling back to the shared key. Downgrade resistance is the point.
- The phone seals **phone→machine** frames under `K_in,d`; the machine seals **machine→phone** frames under `ContentKey`, unchanged.
- The gateway selects the opening key by the frame's `SenderKeyID` **before** `Accept`, from the registry. The AEAD then authenticates the selection: a device that lies about its sender id gets its frame opened under a key it does not hold, and it fails the tag. That is what makes M1 load-bearing.

Two consequences worth stating rather than discovering. First, the machine→phone fan-out property is **untouched** — it is the leg with N recipients; the inbound leg has exactly one, so a per-device key costs nothing there. Second, key separation now kills the B81(1)/B102 reflection class cryptographically (a reflected frame is opened under the wrong key), and the AEAD-plaintext direction tag (`internal/remotegw/direction.go`, `internal/phonecore/direction.go`) **stays** as defense in depth and as the tested behaviour — it is not removed on the strength of this.

**M3. Seq spaces become per-device on both legs, and PB-SYNC-1's two-bucket model is preserved *per device*, unchanged.** Inbound: each device's frames land in `{sender: its id, epoch}`, so each device gets its own replay counter and no device's traffic can stale another's. Outbound: journal/terminal/reconcile keep the machine's routing key id and command replies keep sender-zero, so `streamsOf` (`internal/phonecore/snapshot.go:71-76`) — the single place the bucket→channel mapping lives — is **not edited**. The reply counter, however, must become **per target**: one shared counter across N mailboxes would deliver each device a sparse seq stream, i.e. a permanent gap on the one channel with no repair frame (PB-SYNC-2), which by PB-INPUT-2 costs typing for the life of the epoch — the B85 failure mode arriving through a different door. Per-device counters restore contiguity per device.

**M4. Grant fan-out to N devices — the mechanism does not change, only its cardinality.** Grants already carry their own per-device `(epoch_id, grant_seq)` anti-replay coordinate outside the journal seq stream (D6, `ADR-007:60`), are already mailboxed so an offline-at-rotation device receives its grant on reconnect (C5), and `GrantReceiver` already enforces strict `(epoch, seq)` monotonicity with a persisted seed (`epoch.go:165-200`). `GrantSeq` becomes per-device state in the machine identity instead of one counter, **and the reconcile record's grant watermark follows it into M6's keyed group** — a scalar watermark fanned out to N devices would seed one device's `GrantReceiver` from a peer's counter (`internal/phonecore/core.go:590`), and the receiver's strict monotonicity (`epoch.go:185-187`) would then refuse that device's own next grant as a replay: the terminal grant-lost state, unrecoverable without re-pairing. The grant is still not a seq bucket (PB-SYNC-1) and still rides the shared bucket when it is a rotation grant.

**M5. Revoke rotates to survivors — the existing transaction, plus a new gateway exit condition.** The 2026-07-24 amendment (`ADR-007:562-617`) already rotates the epoch on every successful removal, is already crash-atomic rotate-before-remove, and is already serialized with pairing under one `lifecycleMu`. That transaction is kept. What changes: rotation mints a new `RootKey` as well, so every survivor's `K_in,d` changes with the epoch; after removal the daemon re-grants **each surviving device** at `(epoch+1, grant_seq=1)`; and the last-device sever (`Count() == 0`) remains the trigger for disabling remote control, so revoking one of three devices severs only that device's leases.

**Epoch rotation becomes a gateway-exit condition in its own right, not only deregistration of the gateway's own device.** This is the clause a reader inheriting the amendment would get wrong. Today the exit fires on `!reg.Get(s.cfg.DeviceID)` alone (`internal/remotegw/service.go:424-435`) and the epoch key plus the sealed grant are resolved once at process start and never re-read (`service.go:73-80`, `cmd/swarm-remote/config.go:202-204`, `deliver.go:26`). With one device those two events coincide. With N they do not: revoking one device rotates the epoch, no *survivor's* gateway is deregistered, and each keeps sealing survivor traffic under the pre-rotation `ContentKey` the revoked device still holds — the codex#1 confidentiality gap that rotation alone was found not to close (`ADR-007:586-598`), reopened for the other N-1 devices. Liveness fails on the same edge: a survivor that receives the epoch+1 re-grant replaces its keys wholesale (`internal/phonecore/core.go:494`) and can no longer open the old-epoch frames its gateway is still producing. So each gateway re-reads the on-disk `EpochID` on every journal reconnect, alongside the registry check, and exits when it differs from the one it loaded. Exit-on-rotate is the same shape and the same v1-style closure as exit-on-revoke; the live in-place epoch reload stays Phase B (`ADR-007:597-598`) and, if it is taken first, discharges this clause instead of it.

The named **integrity** residual (a crash between the rotate persist and the remove persist) is inherited unchanged. Confidentiality is **not** inherited — multi-device widens it, which is why it is re-closed above rather than declared untouched.

**M6. The reconcile record's per-device authorities become per-device fields.** `ReconcileRecord` gains a keyed group so that `InboundHighWater`, `ReplyCeiling`, `GrantEpoch` and `GrantSeq` are read by sender id and a device adopts only its own; `JournalCeiling`, `Machine`, `EpochID` and `IssuedAt` stay scalar and shared. `GrantEpoch` travels with `GrantSeq` into the keyed group even though the epoch itself is machine-wide, because the phone merges the two as one coordinate and `GrantReceiver` compares them as one (M4). The `omitempty` prohibition (`reconcile.go:22-25`) extends to the new fields for the identical reason. This is a `wire`-visible change and therefore a GG-7 obligation: `docs/specifications/protocol.md`'s field table must move in the same change or CI fails the build.

**M7. The admin tier lands with this, not after it.** ADR-007's sonnet#3 deferral is explicit that any `CapFull` device may revoke any device and that a formal admin-vs-standard model is required once a peer exists to revoke. Multi-device without it means the weakest paired device can revoke the strongest. The capability model is a prerequisite of M1, not a follow-up to it — **and it is not specified here**: the same deferral requires it to get **its own ADR** (`ADR-007:541-545`), so this clause records it as an unmet dependency of the slice rather than discharging it. What that ADR owes: who may revoke whom, which tier may launch and take control, and how the tier is bound into the signed command tuple (or, if it is not bound there, what makes it unforgeable instead). Until it exists, M1-M9 cannot land, because M7 is inside them.

**M8. B29's rate-window residual is re-triggered, and its constraint binds.** `appendRate`/`pushRate` are keyed per target and shared across senders (`ADR-007:1665-1701`); the recorded trigger for revisiting is "multi-device support, or any change that widens who may append to a target". On the machine→phone leg the `<= 8 appends/s` budget is **per target**, so each device keeps its own 480/min against `MailboxAppendPerMin: 600` and the coalescing rule is unchanged — the gateway simply issues N times the appends and N times the push wakes.

**The phone→machine leg is where it breaks, and it breaks at N=2.** Every device appends to the *same* target — the machine's routing id (`mobile/app.go:243-246`, `mobile/commands.go:499`) — and the relay keys the window by that target (`internal/remote/relay/server.go:991-999`), while PB-INPUT-5 budgets 480/min **per device** (`remote-phaseB-requirements.md:355`). Two devices typing is 960/min against one 600/min window and the lease dies `codeQuotaExceeded`: B29's shared-budget failure mode arriving through the legitimate door, on the trigger B29 itself names. The drain is worse and no keying fixes it, because `mailbox_read`/`mailbox_ack` meter per **source** against `OpsPerMin: 600` (`server.go:1041-1044`, `:1073-1076`) and one device un-batched already computes to 960/min (`remote-phaseB-requirements.md:370`, resolved there by an adaptive drain that must now be sized for N). So the inbound budget is **renegotiated as part of this slice**, not inherited: a per-(sender, target) append window plus an inbound drain sized for N devices. Cutting the per-device input rate to 8/N frames/s is not the alternative — it breaks PB-INPUT-5's p50 typing budget at N=2. Whoever implements the window must not key a rate map by attacker-chosen keys; that constraint is B29's whole content and it binds here. Note that the inbound high-water map is safe by construction: `Accept` writes it only after a successful open (`envelope.go:259-267`), and under M2 only a registered device can produce an opening frame, so the map is bounded by the registry.

**M9. The lease is bound to the sender id that opened it.** C6 asks for two things and the cheap-hardening sentence names both — stamp the sender id on take_control + input envelopes **and bind the lease to that sender id** (`ADR-007:460-462`, `:535-536`). M1 and M2 deliver the first only. Without the second, key separation buys attribution and not authorization: `routeInput` routes a frame by the session id sealed inside it and nothing else (`internal/remotegw/command_loop.go:547-554`), so device B's keystrokes would still ride device A's lease — correctly attributed and wrongly honoured. So `LeaseRouter.Begin` records the `SenderKeyID` of the envelope that carried the take_control and `Input` takes it too (`command_loop.go:57-59`: both signatures gain it), and an input frame whose sender id is not the holder's is **dropped**, fail-closed, in the same shape as the existing gap-drop. Phone-vs-phone contention falls out of the same record: a second device does not steal an open lease, it is refused until the holder releases or the lease expires. That is P-5's exclusivity applied *between phones*; Decision G's relaxation (`ADR-007:474-484`) governs owner-tier-vs-phone for the single-owner model and is untouched — M9 does not re-exclude the owner tier, and it does not reopen the multi-user arbitration Decision G defers (see Sequencing).

## Blast radius

**Frozen surfaces that change** (each gated on this ADR and cross-model re-reviewed after GREEN, per the frozen-crypto rule):

| Surface | Change |
|---|---|
| `crypto.EpochKeys` | gains `RootKey`, machine-only, never granted |
| `crypto.SealEpochGrant` / `OpenEpochGrant` | `grantVersion 0x02`, inner length 85 → 117, third key |
| `crypto` (new) | `DeriveInboundKey(root, recipientPub)` — HKDF, existing dependency |
| `machineid` identity file | `RootKey` + per-device `GrantSeq`; a durable artifact, so D10's versioned migration + rollback tests apply |
| `schema.ReconcileRecord` | per-device `InboundHighWater` / `ReplyCeiling`; GG-7 field-table diff |
| `device.Registry` | `AddSole` → `Enroll` with zero-check, sender-id uniqueness, capability tier; the existing general `Add` is left alone |
| `remotegw` | gateway exits on epoch rotation as well as own-device deregistration (M5); `LeaseRouter.Begin`/`Input` carry the sender id (M9) |

**Invariants that hold, unchanged** — listed so no implementer "fixes" one of them on the way past:

- The envelope format, the AAD field set, the XChaCha20-Poly1305 nonce discipline, and `MailboxReceiver`'s seq/replay/age rules. No new header field; `RecipientKeyID` stays outside the AAD and stays binding nothing.
- PB-SYNC-1 in full: two seq buckets and four repair channels, `Bucket{SenderKeyID, EpochID}` as the discriminator, `streamsOf` as the only mapping site, the grant is not a seq bucket, and **no kind or direction tag in `SenderKeyID` or `EpochID`**. M1 puts a *sender identity* in `SenderKeyID` — which is what that field is for — not a tag; the per-device fork of the inbound bucket is the intent, not a collision.
- The AEAD-plaintext direction tag and its read-before-`Accept` ordering.
- The machine→phone one-ciphertext fan-out, and `recipient_key_id` as a routing hint only.
- The relay stays untrusted and ciphertext-only, and nothing here makes it smarter. B133's **relay-facing** boundary is unchanged. Its trusted-holder half is what this ADR moves, deliberately and stated in Context — do not restore "the trust boundary is unchanged" as a flat claim.
- Decision G (`ADR-007:474-486`): concurrent owner-tier and phone control stays permitted. M9 arbitrates phone-vs-phone only.
- Live-only input (D7 as amended by B43). Multi-device adds no queue.
- The interaction-item kinds of ADR-009/010 ride the same journal bucket under the same rules; this ADR changes who the frames are sealed *to*, never what an item is.

## Sequencing

**Recorded now, implemented post-v1, and before distribution scales beyond one handset.** v1 ships single-device by construction (C6) and this ADR does not relax that. The gate is distribution, not a date: the moment a second device of the same owner is to be paired — a tablet, or a replacement kept alongside the original — M1 through M9 land together. They are not separable: M1 without M2 asserts an attribution that does not exist; M2 without M9 attributes an input frame it then routes onto a peer's lease anyway; M3 without M1 has nothing to key on; M5 without its rotation-exit clause reopens codex#1 for every survivor; M7 without M1 has no peer to govern. Partial adoption is worse than none, and this paragraph exists so that a later reader does not take one of them in isolation.

**Shipping to a second *person's* hardware is a different gate, and this ADR is not it.** M9 arbitrates between one owner's own devices; multi-user exclusivity across tiers is deferred by Decision G (`ADR-007:474-484`) for the case where remote control is shared across distinct people, and nothing here decides it. That build needs that decision as well as these.

## Consequences

### Positive

- `SenderKeyID` becomes cryptographically load-bearing rather than advisory, which is what C6 and B1 both asked for and neither specified.
- Per-device inbound keys remove the shared-`ContentKey` forgery path on the inbound leg, and the M9 lease binding closes the other half the A7 residual names (`ADR-007:456-462`) — attribution *and* authorization — rather than declaring it moot.
- Per-device seq spaces mean one device's gap, restart or misbehaviour cannot stale another's channels.
- The reflection class B102 defended with a tag is additionally closed by key separation.
- The inbound high-water map becomes bounded by the registry, which is the shape B29 says any fix must have.

### Negative

- A frozen-crypto change with a grant format bump and a forced re-grant; every paired device must be re-granted at the upgrade, and an un-upgraded phone fails closed until it is.
- A durable-artifact migration (machine identity) with the full D10 migration + rollback test obligation.
- A GG-7 wire-schema change (`ReconcileRecord`), so `protocol.md` moves in the same commit or the build fails.
- N times the relay append and push operations outbound; and — the binding one — a **shared** inbound budget at the machine's single target, which must be renegotiated before the second device (M8).
- The admin tier is now a prerequisite and owes its own ADR, enlarging the slice beyond the crypto work and gating it on a document that does not exist yet (M7).
- A running gateway now exits on epoch rotation, not only on its own device's revocation, so revoking one device restarts every device's gateway (M5).

## Alternatives Considered

**Sender id alone, no inbound key** — ADR-007's literal cheap hardening. Rejected: with a shared `ContentKey` a paired device can stamp a peer's sender id, so the activity log would credit device A for keystrokes injected by device B into A's lease. That is a false claim, and this repo does not ship those.

**Per-device `ContentKey`, both legs.** Rejected: it destroys the D2/D6 one-ciphertext fan-out — N seals per journal event and N distinct ciphertexts — for a property only the inbound leg needs.

**A device tag in the AEAD plaintext instead of the header, by analogy with the direction tag.** Rejected: the direction tag is a *tag* and correctly rides the plaintext; a sender identity must be readable **before** `Accept` in order to select the opening key, and the plaintext is not available before `Accept`. `SenderKeyID` is the field that exists for this and is already in the AAD.

**Deriving `K_in,d` from the granted `ContentKey`.** Rejected outright: every device holds the `ContentKey`, so every device could derive every peer's inbound key. The root must be machine-only, which is the whole content of M2.

## Notes

This ADR exists because ADR-007 named the missing primitive in three places (C6, B1, the A7 residual) and specified it in none, leaving a one-sentence hint that turns out to be half a design. The half it was missing — that a shared content key makes a sender label unforgeable only against the relay, not against a peer — is the kind of gap that reads as complete until someone implements it. Written down before there is a second device, so that when there is one the decision is a reference rather than a rediscovery.
