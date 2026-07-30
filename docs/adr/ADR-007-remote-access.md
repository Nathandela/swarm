# ADR-007: Remote access — identity, pairing, two-scheme crypto, relay trust, journal, launch authority

**Status**: Proposed (design lock for the remote-control epic `agents-tracker-5h5`; ratifies to Accepted at Phase-1 close. Feeds the implementation plan of record `.claude/tmp/remote-control-implementation-plan.md`, coverage-audited in `docs/verification/audit-003-remote-control-plan.md`.)
**Date**: 2026-07-18

## Context

ADR-004 deferred remote access to "its own ADR: identity, pairing, E2EE/relay trust, idempotency, audit logging," and left "SO_PEERCRED checks per request ... reconsidered in the V2 remote ADR." This is that ADR.

The product: a phone app that interacts with swarm sessions from anywhere — read output, send input, answer permission prompts, interrupt, and (later) spawn — across multiple computers, with push notifications, at the "Claude-app seamless" bar, and "extremely safe" as a hard requirement. The threat model is unforgiving: the phone commands processes that edit code and run tools on personal machines, over the public internet, and a stolen phone or a compromised relay must not become code execution or data exfiltration.

Two prior committee reviews shaped this: audit-002 on the design (E2EE-relay direction accepted; every write feature re-priced; crypto split into two schemes; launch scoped as RCE) and audit-003 on the implementation plan (verdict REVISE; the remote-origin trust tier had no unforgeable basis; several Phase-1 contracts contradicted the source; async-delivery/crash-recovery/connection-lifecycle contracts were unspecified). A delta re-audit (003b) confirmed the plan's D.0 amendments close every finding and green-lit Phase 0, routing four residuals to this ADR. Those four are decided below.

swarm's read path is already remote-ready (transport-neutral schemas F-2, versioned capability-negotiated handshake F-1, namespaced session ids, server-derived status Groups, escape-filtered VT snapshots N-6). Every write feature is new work, and the daemon has no concept of a "remote" client today: the TUI, the hook, and a future gateway all dial the same UDS and reach the identical DaemonAPI.

## Decision

### D1. Transport: self-hosted E2EE relay (Option A), behind an interface

A small stateful relay on a VPS; the daemon-side gateway and the phone both dial outbound TLS/443 (WebSocket); the relay stores and forwards ciphertext only. Chosen over Option D (Tailscale + blind push broker) because it is one coherent product surface (presence, mailbox, push, pairing) and the only shape that matches the cold-start-from-cellular bar. The transport module stays behind an interface so Option D can be adopted as a direct path later without rework. This A-vs-D decision is made here (Option A) and gates the Phase-1 crypto/pairing/relay work; if a future revision picks D, those areas are revisited before implementation, not mid-stream.

### D2. Two crypto protocols

- **Live transport**: Noise XX, suite `Noise_XX_25519_ChaChaPoly_SHA256` (SHA-256 for Swift/CryptoKit interop), via `github.com/flynn/noise`. Static keys are the pinned per-party X25519 identity keys; a fresh handshake per connection (no 0-RTT) gives forward secrecy at each boundary; the prologue binds protocol/role/route; peer static keys are compared to the pinned value and the handshake aborts before any transport byte on mismatch (authenticated, not TOFU).
- **Async envelopes**: a machine **epoch content key** model. The machine holds `(epoch_id uint32, K_epoch)`; mailbox events and push wakes are AEAD-encrypted (XChaCha20-Poly1305 — 24-byte nonce, mandatory because K_epoch is reused across events) once under the epoch key and fanned out; the epoch key reaches each active device sealed (nacl `box.SealAnonymous`, crypto_box_seal-compatible — verified present in `golang.org/x/crypto/nacl/box`) to that device's long-term key. Revocation rotates to `(epoch_id+1, K_epoch')` and re-grants only to survivors.

No hand-rolled primitives: `flynn/noise`, `golang.org/x/crypto` (nacl/box, curve25519, chacha20poly1305, hkdf), `crypto/ed25519` (stdlib), only.

**Two X25519 keys per phone device, not one** (audit-003 M1 / A14): a Noise-static key and a sealed-box-recipient key, both pinned/stored at pairing (R-PAIR.3/.7, R-DEV.1); the EpochGrant seals to the recipient key, not the Noise static. Reusing one key across both protocols has no demonstrated oracle (neither protocol exposes the raw shared secret) but voids the clean composable argument; a second key is nearly free and buys it back.

**Two epoch keys per epoch — wake vs content** (audit-003 M2 / A15): each EpochGrant delivers a **wake key** (after-first-unlock, app-group-readable by the Notification Service Extension, decrypts only content-free "activity on machine X" push payloads) and a **content key** (biometric-gated, not NSE-readable and not derivable from the wake key, decrypts mailbox session content). A once-unlocked stolen phone yields only the wake key — no session history — closing the content-at-rest exposure. The device long-term and command-signing keys stay biometric-gated.

### D3. Identity and pairing

At `swarm remote init` the machine generates its X25519 identity keypair (Keychain or 0600 file) and an Ed25519 activity-log signing key. The phone generates, in the Secure Enclave / biometric-gated Keychain, its X25519 keys (kept X25519 on the wire — the Enclave cannot do X25519 natively, so a biometric-gated Keychain item or an SE-P-256 key wrapping a stored X25519 key backs it) plus an **Ed25519 device command-signing key** and an Ed25519 relay-auth key.

Pairing (`swarm remote pair`): a single-use 32-byte QR secret (60 s TTL) that never touches the relay — the camera is the out-of-band physical-presence channel; phone and machine meet through an opaque relay rendezvous mailbox and run Noise XXpsk0 with the secret as PSK; a 6-emoji SAS is derived from the handshake channel binding (fixed 64-entry table, identical in Go and Swift; widened from 4 to 6 emoji per the 2026-07-23 amendment, see below); a **mandatory local desktop confirm** (`Allow "<device>"? [y/N]`) is the independent second gate that defeats a photographed/leaked QR, failing closed on no/timeout. Outcome: mutual static-key pinning of both device X25519 keys + registration of the device command-signing and relay-auth public keys. Pairing requires a local console (Phase 1); headless/SSH-only pairing is refused (it collapses the OOB and the confirm into one in-band channel — RCE-via-shell); a headless OOB-code flow is a Phase-3 follow-up.

### D4. Remote-origin authority — the unforgeable basis (residual R1)

The daemon establishes remote origin **by construction, not by a self-declared capability**:

- A **dedicated remote-tier UDS** (`<stateDir>/remote.sock`, 0600), distinct from the owner-trusted main socket. Connections on it are unconditionally remote-tier; the gateway dials only it; a `remote-gateway` capability, if kept, is a non-trust feature flag, never the trust basis.
- **Per-command device signatures**: every remote mutating op carries a detached Ed25519 signature (device command-signing key) over the canonical tuple `(action, machine=endpoint id, session, operation_id, expires_at, content_hash?)`; the daemon verifies it against the pinned device key and the device's capability grant **before** executing. A compromised relay cannot forge commands; only paired, unrevoked devices can issue them; no remote-class mutating op executes on any listener without a valid signature.

**Threat-model scope (residual R1, the honest boundary).** A `0600` socket does not isolate two processes running as the same owner uid, and the gateway must run as the owner (it holds the machine identity key and reads the 0700 state dir), so SO_PEERCRED cannot distinguish a compromised gateway from the local TUI. Therefore the cryptographic containment boundary is the **untrusted relay** and the **semi-trusted phone**: a process compromised while running as the owner (the gateway included) already holds the machine identity key and can act as the owner directly, without the daemon — the same status as a compromised shell on a single-owner machine, and outside the cryptographic boundary by construction. Sidecar isolation (below) is a process boundary — the gateway is the only component parsing attacker-influenced relay bytes and does not share an address space with the PTY-owning daemon — and it is not a cryptographic barrier. **AMENDED 2026-07-30 (ADR-007 B41, restated B62(3), closed B68).** This sentence previously claimed "defense-in-depth" on daemon/PTY state. **B41 ruled that claim false and demanded it be made true or withdrawn**, because the generated systemd unit carried no `NoNewPrivileges`, `ProtectSystem`, `ProtectHome`, `PrivateTmp`, `RestrictAddressFamilies` or `SystemCallFilter` — so there was no OS-level confinement to be in depth about, and the process boundary alone was doing all the work the sentence credited to isolation. B62(3) found the remediation still not carried out nineteen entries later, and a round-4 committee reviewer confirmed it again at HEAD. The claim is now scoped to what the process boundary actually buys; whatever OS-level confinement the unit carries is stated in B68 and in the unit template itself, so the two cannot drift apart again. This ADR adopts the scoped threat model for the personal-deployment default and records the stronger option (a dedicated non-owner service uid with its own key custody, or an OS sandbox/MAC profile denying the gateway the main-socket path) as an available hardening if multi-user isolation is later required. Revisiting ADR-004's deferred SO_PEERCRED question: it does not help here because both trusted and untrusted processes share the owner uid.

### D5. Gateway: supervised sidecar

`cmd/swarm-remote` runs as its own process under an external supervisor (macOS launchd LaunchAgent, Linux systemd user unit), never spawned by the daemon; it dials the daemon's remote socket in and the relay out; it holds no state a restart loses except live connections and the persisted `(K_epoch, epoch_id, relay-acked journal cursor)`. It is the only component parsing attacker-influenced relay bytes and must not share an address space with the PTY-owning, agent-spawning daemon.

### D6. Durable journal + two-phase idempotency (in the daemon)

A single daemon-wide append-only journal under `<stateDir>/journal/`, versioned records `(schema_version, cursor uint64, ts, session_id, type, payload)`, written at the `saveMetaLocked` **choke point** (covering `SetStatus`, `finalizeTerminal`, `Launch`, and the two `reconcile.go` startup transitions) plus a separate `Delete` tombstone hook — enumerate the choke point, never the callers. The journal append is a WAL-style step in the same recoverable commit as the meta write (no meta-without-journal or vice versa across a crash), fsync'd before its cursor is acked (D-5). Resume contract: "snapshot as of cursor N, then events after N," atomic. Flap debounce lives at the delivery layer (push-wakes + coalesced snapshots), never in the durable journal.

Idempotency is **two-phase** (residual R2 / audit-003 CRITICAL): a durable `prepared -> executing -> completed/failed` record keyed by `operation_id`, fsync'd **before** the side effect; for launch the `operation_id` is persisted as part of the existing two-phase session reservation (same fsync), so a crash between spawn and commit is resolved by reconcile against the reserved id, not by re-spawning. Replay returns the cached outcome and executes nothing. `interrupt` is **at-most-once** (SIGINT delivery is not verifiable from terminal state): its record resolves to `completed` or, after a mid-interrupt crash, to a terminal `outcome-unknown` state the phone surfaces — never a claimed exactly-once; `kill` (SIGKILL + terminal-state-verifiable) stays exactly-once-verifiable.

The async mailbox `seq` is the durable journal cursor (one coordinate; a gateway restart holds no independent counter). `recipient_key_id` is a routing hint **outside** the AEAD AAD, so the ciphertext under the shared K_epoch is identical for every recipient and the relay's per-device mailbox does the routing. EpochGrants are not in the journal-seq stream; they carry their own `(epoch_id, grant_seq)` per-device anti-replay coordinate and are mailboxed (so an offline-at-rotation device receives its grant on reconnect).

### D7. Input and approval semantics (residuals R3, R4 folded into D6)

Raw `input`/`resize` are **live-only** — they require a live connection holding the current lease generation and are never durably queued or replayed; on disconnect a queued keystroke resolves to an explicit "delivery unknown / not sent." Take-control opens a **signed one-shot `take_control` op** (device signature + a single biometric gate token) establishing a bounded authenticated control session (TTL + explicit end); keystrokes ride that session, not per-keystroke signatures. Discrete ops (interrupt/kill/approve/launch) each carry their own signature + gate token. ~~Only high-level idempotent ops enter the offline queue.~~ **WITHDRAWN 2026-07-26 (ADR-007 B43): there is no offline queue and there cannot be one built from these commands.** A queued op carries a signature valid for one minute and is never re-signed on replay, and re-signing at drain would need PB-SEC-2's per-use biometric for exactly the op list this sentence names — a prompt, not a queue. The phone instead refuses while offline with `ErrClassOffline`, tells the user, and surfaces the state, which is what PB-NET-4's other clauses require.

Approval binds an immutable `(machine, session, agent-instance{shim_pid, shim_start_time}, interaction_id, content_hash, expires_at)` tuple, with `operation_id` separated from `interaction_id`, daemon-authoritative expiry (phone countdowns are display-only), a byte-exact content canonicalization + SHA-256 hash, and interaction consumption/supersession state; a stale or mismatched approve is rejected daemon-side and never translated into a blind keystroke. The delivery mechanism (whether a minutes-later decision can be applied to the synchronous in-PTY prompt) is resolved by spike S-C; the binding/validation layer above is Phase 1 regardless.

### D8. Launch authority

Remote launch is the highest-privilege verb (RCE). Authorization is evaluated **before** any argv composition or cwd stat: kill switch on? cwd within a machine-configured allowed root (checked and handed to the shim as the same fully-resolved real path — no check-on-resolved/use-on-original gap)? device capability permits launch? `dangerously-skip-permissions` and full-access options are refused from remote, hard-coded; `Options` are allowlisted (not just `Env` dropped — audit-003 m2); no phone-supplied env (env comes from daemon policy — also the correct fix for the ADR-006 billing-env class); worktree isolation by default; per-device capability policy (read-only / read+approve / full); an explicit phone confirm. Live launch execution is Phase 2; the builder + policy enforcement + crash-recovery are Phase 1.

### D9. Relay: untrusted, with a full account/routing lifecycle

The relay authenticates connections by an Ed25519 relay-auth signed challenge (it never learns the X25519 identity keys), stores per-device ciphertext mailboxes with its own untrusted storage cursor (distinct from the authenticated seq the device trusts), forwards push wakes to APNs with a generic outer payload and ciphertext only, tracks presence and emits a "machine went silent" wake on gateway drop (laptop sleep is a first-class phone state, N-7), and persists to an embedded transactional store (bbolt) holding only ciphertext + routing metadata. It defines: machine registration + routing-id derivation/proof, device authorization scoped to paired routes, an APNs push-token registration/refresh/deletion op, device de-authorization + mailbox purge on revocation (a revoked device keeps neither connectivity nor a drainable pre-rotation mailbox; an offline-at-revoke machine defers the purge to reconnect), duplicate-connection resolution, and day-one rate limits/quotas on every endpoint. TLS is metadata defense only — E2EE confidentiality does not depend on it.

### D10. Kill switch, activity log, connection lifecycle, migrations

A durable kill-switch flag: when off, the daemon refuses every remote-origin op at the boundary (needing neither phone nor relay); `swarm remote off` also severs the gateway; auto-off at zero paired devices. A plain append-only signed activity log for every remote-originated mutation — the signature detects out-of-band edits only (the key is co-located under the same uid; on-machine tamper-proofing would need off-machine anchoring, deferred). A stable machine-readable error-code taxonomy (policy / kill-switch / rate-limit / stale-approval / not-authorized / invalid-field / transient-vs-permanent) that every refusal uses and the phone renders. Client reconnect backoff + jitter on both hops. Versioned migration + rollback tests for every durable artifact (identity, device registry, policy, journal, idempotency, relay DB, activity log). Every TTL is pinned to a single authoritative clock (rendezvous relay-side; idempotency + approval expiry daemon-side).

### D11. Metadata exposure (honesty)

E2EE hides payloads, not metadata. The relay sees which machines and devices exist, connection/presence timing, message sizes and cadence, and push timing; Apple sees push routing and timing. This exposure is documented, retention is bounded (mailbox purge after ack + a cap; presence not persisted), logs carry no bodies, and the "managed hosting leaks nothing" claim is withdrawn.

### D12. Platform and distribution

Native SwiftUI, iOS-first; an Apple developer account ($99/yr) for APNs + NSE is a hard Phase-0 dependency. All protocol/crypto/state logic lives in a gomobile-ready Go phone-core (tested against itself on the build machine); the SwiftUI layer is a thin shell compiled on-device later. A mandatory pre-production Xcode/device gate (archive, gomobile bind, entitlements, killed-app push, NSE timeout, biometric cancel, Keychain-after-reboot) precedes any real-world use — "Go core + uncompiled iOS source complete" is not "shipped," and the on-device key-custody + biometric surface is an aggregated deferred residual risk retired only at that gate. Live Activities and an Android thin client are later. The blind-push-gateway seam (relay cannot hold the APNs signing key if the app is ever distributed) is a conscious deferral, moot for the personal-only build (open question 6).

## Consequences

### Positive
- Compromise containment against the *actual* untrusted components: a hostile relay cannot read, forge, or undetectably reorder anything; a stolen once-unlocked phone yields no session content; a lost phone is revoked with immediate epoch rotation + lease release + relay de-authorization; the kill switch severs everything from the machine alone.
- The read path reuses existing, tested seams (namespaced ids, Groups, N-6 snapshots, lease supersede) unchanged.
- Exactly-once mutating semantics survive daemon crash/upgrade (D-5) via two-phase records; input is honestly live-only rather than pretending to be replayable.
- One coherent product surface; the transport interface keeps Option D open.

### Negative
- You operate an internet-facing stateful relay (mailboxes, cursors, device registry, APNs custody, abuse controls) — not "stateless."
- Two crypto protocols and a per-command signature layer are real implementation and review cost.
- A process compromised as the owner uid (gateway included) is outside the cryptographic boundary on a single-owner machine — documented, with a dedicated-uid/sandbox hardening available but not the default.
- Metadata (presence, timing, sizes) is exposed to the relay and Apple; mitigations (padding/batching) are optional.

## Alternatives Considered

- **Option D (Tailscale + blind push broker)**: dramatically less custom security code (WireGuard is the transport security; identity/revocation are Tailscale's problem), but requires the Tailscale app/account, has iOS VPN reconnect lag on cold open, still needs the daemon journal + a sync protocol, and infers presence rather than brokering it. Kept as the fallback with the better security-effort ratio and reachable behind the transport interface. Options B (pure Tailscale, no push) and C (WebRTC, dies in iOS background) rejected.
- **One X25519 key for Noise-static and sealed-box-recipient**: no demonstrated oracle, but a second key is nearly free and preserves clean composable proofs. Rejected the reuse.
- **In-process gateway**: rejected — the one remotely-reachable parser of attacker bytes must not share an address space with the PTY-owning daemon (audit-002 item 14).
- **Self-declared remote-origin capability**: rejected — a capability offer is negotiation, not authentication; origin is by socket + device signature (D4).
- **Cached-outcome-only idempotency**: rejected — leaves the execute-then-crash re-execution window; two-phase records close it.
- **NSE decrypts session content with the epoch/device key**: rejected — it would expose all current-epoch content to an after-first-unlock key; a content-free wake key separates it.
- **transcript_delta / one-tap structured approval as assumed Phase-1 features**: rejected as assumptions — gated behind spikes S-A/S-B/S-C, which return PASS/PARTIAL/FAIL verdicts that scope Phase 2.

## Spec amendments this ADR governs

`docs/specifications/protocol.md` gains the remote protocol extensions (new negotiated capabilities; additive omitempty `Control` fields incl. `operation_id`/`interaction_id`/`device_sig`/`cursor`/`expires_at` and the `approve` sub-struct; journal/activity/policy/pairing ops) drift-checked field-by-field (GG-7). `docs/specifications/system-spec.md` gains the remote-origin trust tier, the journal/idempotency/kill-switch/activity artifacts, and new invariants ("no remote op executes when the kill switch is off"; "remote mutating ops are idempotent"; "no remote-class mutating op executes without a valid device signature"). Both are amended in the Phase-1 epics that implement each piece, never silently.

## Amendment 2026-07-20 — Relay pre-authentication rate-limiting model (refines D9)

**Status**: Accepted. **Context**: the relay-hardening R1 review
(`docs/verification/remote-phase1-relay-review.md`, findings R1-H1/H2/H3) showed that
keying pre-authentication rate limits by the *presented* relay-auth pubkey is unsafe.
`auth_init` carries an UNPROVEN pubkey (no signature yet), so (a) an attacker floods
`auth_init` presenting a victim's pubkey to exhaust that victim's window — a targeted
lockout; (b) attacker-chosen keys create unbounded per-key rate-limit state — a memory
DoS; and (c) a single global counter charged on each successful auth is monopolizable by
one valid key. All three defeat the "day-one rate limits on every endpoint" intent of D9
for the auth path, on a component whose whole job is to be safe while untrusted.

**Decision** (refines D9's rate-limit obligation; the untrusted-relay threat model is
unchanged):

1. **Connection admission is source-agnostic and first.** A global concurrent-connection
   cap and an idle/handshake read deadline bound fds/goroutines/memory before any auth
   work, independent of any claimed identity.
2. **Pre-signature rate limiting is keyed by TRANSPORT SOURCE** (client IP; per-connection
   only as a fallback when no IP is available), NEVER by the unproven presented pubkey.
   This covers `auth_init` and the unauthenticated rendezvous ops. A per-source window
   bounds one network source; no single source can exhaust another source's or a victim
   identity's budget.
3. **No global auth counter that a single source can monopolize.** If a coarse global
   safety valve is kept at all, its budget is strictly larger than any single source's
   per-window budget; the primary control is the per-source window.
4. **Per-key (per-routing-id) rate limits apply only AFTER signature verification**, where
   the identity is proven (post-auth ops). Those maps are reaped on disconnect and bounded
   by a TTL sweep; the relay MUST NOT retain any per-presented-key state before a signature
   verifies.

**Consequence**: the pre-auth DoS surface is bounded per network source, not per claimed
identity, and no unproven-key state is retained — closing R1-H1/H2/H3. **Test impact**
(tracked, and reframed with review, never silently): `TestRelay_AuthRatePerSource`
asserted per-unproven-key independence (the unsafe premise) and is replaced by a
poison-resistance test (an attacker flooding `auth_init` with a victim's pubkey does not
consume the victim's budget) plus a post-auth per-key fairness test;
`TestRelay_ConnRateLimited` is reframed as the per-source pre-auth limit / coarse global
valve. Findings: `agents-tracker-40o` (H1), `agents-tracker-45s` (H2), `agents-tracker-a0u`
(H3).

## Amendment 2026-07-20 — Pairing conveys the device command-signing public key

**Context**: D4/A1 mandate a device Ed25519 COMMAND-SIGNING keypair (R-CRY.16),
"minted at pairing, its public key pinned in the daemon device registry (R-DEV.1)",
so the daemon can verify each remote mutating op's detached signature (R-POL.9)
independently of the untrusted gateway. The crypto layer already provides this key
(`crypto.KeyStore.CommandSigningPublic()`/`SignCommand`, `crypto.VerifyCommandSig`),
domain-separated from the relay-auth key. But the shipped pairing handshake's
`DevicePayload` (msg3) carries only {name, routing id, relay-auth pub, recipient pub}
— it never transmits the command-signing public key, so the machine cannot pin it and
R-POL.9 is unimplementable. R-DEV.1's field list likewise omitted it.

**Decision** (completes A1/R-CRY.16; the trust model is unchanged): the device's
authenticated pairing `DevicePayload` gains a fifth field, `DeviceCommandSignPub` (the
device's Ed25519 command-signing public key), sent inside the encrypted, mutually
authenticated Noise XXpsk0 msg3. On affirmative confirm the machine pins it alongside
the Noise-static and recipient keys; the device registry (R-DEV.1) stores it as the
key R-POL.9 verifies against. Rejected alternative: a separate post-pairing
key-registration op — it contradicts A1's "at pairing", opens a window where a device
is paired but not yet command-capable, and adds an unauthenticated-until-bound surface.

**Consequence**: R-POL.9 becomes implementable; the command-signing key is pinned in
the same atomic, SAS-confirmed step as every other device key (no separate trust
window). **Test impact** (tracked, reframed with review, never silently): the pairing
payload round-trip and outcome tests gain assertions that `DeviceCommandSignPub` is
conveyed and surfaced for pinning; this is additive coverage for a new field, not a
modification to force an existing assertion to pass. The pairing handshake change is
re-reviewed (the slice was previously security-reviewed). Finding tracked under the
R-DEV epic.
## Amendment 2026-07-20 — Pairing conveys the machine grant-signing public key (enrollment keystone)

**Context**: D2/D3 and F3/A15 deliver each epoch's wake/content keys to a paired
device as a sealed, machine-signed `crypto.EpochGrant`: sealed to the device's
recipient X25519 key and signed with the machine's Ed25519 grant-signing key so an
untrusted relay can neither read nor forge it (`crypto.SealEpochGrant` /
`OpenEpochGrant`). The device verifies the grant against the machine's Ed25519 pub.
But the shipped pairing handshake's `MachinePayload` (msg2) carried only {hostname,
routing id, relay-auth pub, recipient pub, epoch id} — it never transmitted the
machine's grant-signing public key, so a just-paired phone had no key to verify epoch
grants against and the async content-key delivery path could not be bootstrapped from
a pairing. This is the exact mirror of the device-command-signing-key gap above, on
the machine->device direction.

**Decision** (symmetric with the DeviceCommandSignPub amendment; the trust model is
unchanged): the machine's authenticated `MachinePayload` gains a `MachineSignPub`
field (the machine's Ed25519 grant-signing public key), sent inside the encrypted,
mutually authenticated Noise XXpsk0 msg2, carried as a length-prefixed field before
the trailing epoch id so the epoch-trailer wire contract is undisturbed. On a
completed pair the device pins it in its `DeviceOutcome`, and later verifies every
`EpochGrant` against it. A new machine-side `internal/remote/enroll` package composes
the pairing outcome into (a) a `device.Registry` record the daemon authorizes commands
against (R-POL.9) and (b) the initial sealed `EpochGrant`; the phone accepts the grant
via `phonecore.AcceptGrant`. Rejected alternative: reuse the machine's Noise-static
(X25519) key — it cannot produce Ed25519 signatures, and grant authenticity must not
depend on the confidential DH key.

**Consequence**: a single SAS-confirmed pairing now bootstraps BOTH halves of the
remote trust — the pinned device command key (verify inbound commands) and the pinned
machine grant key (verify inbound epoch keys) — with no out-of-band provisioning. The
end-to-end remote flow runs without any hand-built registry record or manually shared
content key (proved by `TestEnrollmentE2E_PairThenCommandNoManualSetup`). **Test
impact** (tracked, additive): the pairing payload round-trip/outcome tests gain
`MachineSignPub` assertions; new `enroll` and `phonecore.AcceptGrant` tests are
failing-first. Tracked under agents-tracker-qo4.
## Amendment 2026-07-23 — Widen the SAS from 24 to 36 bits (close the grind attack)

**Context**: D3 / R-PAIR.4 has the two operators compare a Short Authentication String
(SAS) out-of-band during pairing to detect a man-in-the-middle on the Noise handshake.
The shipped SAS (`crypto.SAS`, `internal/remote/crypto/sas.go`) is
`HKDF-SHA256(channelBinding)` truncated to 3 bytes = **24 bits**, rendered as four 6-bit
indices into the 64-emoji table. An independent adversarial review of the pairing chain
(2026-07-22, `docs/verification/remote-phase1-review-consolidated.md` finding MED-1)
showed 24 bits is grindable: an attacker who has BOTH (a) obtained the 32-byte pairing
secret (a photographed/leaked QR) AND (b) a live network man-in-the-middle position can,
at msg3, grind ~2^24 candidate keypairs (seconds on commodity hardware) to force its own
leg's channel binding to a SAS equal to the honest leg's — the operator then sees a
matching SAS and a plausible device name and confirms the impostor. The SAS is precisely
the designed defense for the leaked-QR case, so 24 bits does not meet the "extremely safe"
bar for that case. Noise XX exposes ephemerals in the clear with no pre-commitment, so
there is nothing today that stops the grind other than the SAS width.

**Decision**: widen the SAS to **36 bits** — read 5 bytes (40 bits) of HKDF output and
render **six** 6-bit indices into the SAME unchanged 64-emoji table, so
`crypto.SAS` returns `[6]string` instead of `[4]string`. 36 bits raises the grind cost to
~2^36 keypair-generations-plus-hash (tens of billions), which at microseconds each is
weeks of compute — infeasible inside a pairing window (the rendezvous/pairing session is
seconds-to-minutes and rate-limited). The 64-emoji wordlist, the HKDF salt
(`swarm-remote/1 sas`), and the derivation construction are otherwise UNCHANGED, so this
is a length extension, not a redesign. Six emoji to compare remains a comfortable
out-of-band check (comparable to consumer pairing UIs) and is the minimal robust fix.

**Rejected alternative — ephemeral commitment.** Adding an initiator ephemeral
pre-commitment (`H(e)` sent before the responder reveals) would make grinding impossible
at ANY SAS width, but it changes the Noise XX message flow (an extra commitment field /
round) and its wire contract, a larger and more error-prone change to a frozen,
independently-reviewed handshake. Widening the SAS is a smaller, self-contained change
that reduces the attack to computational infeasibility. The commitment remains available
as a future hardening if a wider threat model demands grind-immunity rather than
grind-infeasibility.

**Consequence**: `crypto.SAS` signature changes `[4]string -> [6]string`; every caller
that displays or compares the SAS (the pairing SAS callbacks in
`internal/remote/pairing`, `phonecore` SAS surfacing, and their tests) updates to six
elements. This edits the FROZEN crypto layer, so per project rule it is gated on this
ADR and re-reviewed cross-model after GREEN (the SAS change is security-critical). **KAT /
on-device impact (tracked)**: the byte-identical SAS table is mirrored in the Swift/Android
clients; the on-device cross-language KAT vector (an explicit release gate, not verifiable
in this repo) MUST be regenerated for the new 6-emoji output and both clients must produce
the identical six emoji from the same channel binding, or pairing SAS comparison breaks
across platforms. Existing SAS tests that assert a 4-emoji result are updated to six
(additive-length harness correction, assertions preserved: determinism, divergent-binding
divergence, empty-binding error). Tracked under the remote-control epic.

## Amendment 2026-07-23 — Client strategy (iOS + Android both first-class), full remote input in v1, hardening into Phase A, existing design adopted as binding spec

**Status**: accepted. Supersedes the D12 iOS-first stance; confirms and scopes D7/D8
for v1; refines the D9/D10 hardening ordering. No crypto-layer change (the frozen layer
is untouched by this amendment).

**1. Client: iOS AND Android, both first-class (amends D12).** D12's "Native SwiftUI,
iOS-first ... an Android thin client is later" is replaced. The gomobile-ready Go
phone-core (unchanged in role) is the single shared core: it binds to an iOS xcframework
AND an Android AAR, with two thin native UIs over one protocol/crypto/state core.
Rationale: Android is buildable and testable on THIS machine with no Apple developer
account, so the first real on-phone artifact is an Android build; iOS follows unchanged
when an Xcode + Apple-account environment is available (D12's on-device release gate still
governs iOS). Consequence: the phone-core exported surface is designed gomobile-bind-safe
from the first line (no generics or unsupported types on the boundary) — a retrofit after
the core is written is the expensive failure mode. The Apple-account Phase-0 dependency is
no longer a blocker for first on-phone testing; it becomes an iOS-release dependency only.

**2. Full remote input is in v1 (confirms D7, lifts the interim fail-close).** v1 includes
remote keystroke input into a live session via D7's signed one-shot `take_control` op +
lease-bound control session — not per-keystroke signatures, not an offline queue (input
stays live-only per D7). The Phase-1 safety fix-pack fail-closed remote `OpDataIn` /
`OpAttach` / `OpResize` on the remote tier as an interim measure; Phase A REOPENS them, but
only behind a valid `take_control` session (device signature + biometric gate token +
current lease generation + the `requireRemoteAuthz` choke point). Until `take_control`
lands, remote input stays fail-closed. The v1 input UX is the already-designed **terminal
peek + take-control** screen (design §8), NOT the chat/voice composer, which remains Phase
2 (gated on spike S-A). D8 live launch execution likewise stays Phase 2; v1 launch is the
builder + policy + crash-recovery path already scoped to Phase 1.

**3. Safety hardening moves into Phase A, alongside the input backend (refines D9/D10
ordering).** Because remote input is the highest-blast-radius capability — keystrokes into
a code-editing agent through an untrusted relay — the two remaining hardening items are NOT
deferred: relay round 3 (per-source concurrent-connection cap + cumulative handshake
deadline, mailbox depth cap on by default, atomic revoke that closes the live socket,
device-consent pairing proof + machine allowlist) and kill/delete routed through the
two-phase idempotency store land in the same phase that reopens remote input. Enabling the
capability and shipping the controls that bound it are one unit of work, not two.

**4. The existing UI/UX design is adopted as the binding client spec.** The client UI is
not designed from scratch. `docs/research/remote-control-design.md` §8 (the eight screens:
pairing/onboarding, triage inbox, session detail, terminal peek, machines, approval sheet,
activity feed, settings) is the phone-core output contract — the exported surface must feed
exactly these screens. `docs/research/remote-control-design-directions.html` fixes the
visual identity (skins 01 Substrate + 02 Void, purple retired, phosphor-green terminal
hero, light+dark token sets); the native UIs implement it and the tokens are lifted into
one shared source both clients consume. `docs/research/remote-control-mock.html` fixes the
pairing flow (QR -> SAS "check both screens" compare -> paired); the Phase-A machine-side
TUI/CLI confirm shows the SAME SAS the phone shows. Phase-2+ screens per the design's own
§9 phasing (chat transcript S-A, approval sheets S-B/S-C, voice, quiet hours,
activity-feed depth, Live Activities) are explicitly out of v1.

**Consequence / tracking**: D12 is updated as above. The client + backend work breakdown
lives in `docs/research/remote-v1-roadmap.md` (Phases A/B/C, dependency-ordered). The
on-device cross-language SAS KAT gate (prior amendment) now applies to BOTH clients, not
iOS alone.

## Amendment 2026-07-23 — Pairing host: the daemon runs Machine.Pair (Option A), owner-tier pair_* ops

**Status**: accepted. Refines D3 (identity/pairing) and D5 (gateway) with the concrete
Phase-A pairing wire flow. Reuses the frozen pairing/enroll/crypto layer unchanged.

**Decision.** The **daemon is the pairing host**. An owner-tier `pair_start` triggers the
daemon to run `pairing.Machine.Pair` in a background goroutine whose `ConfirmFunc` bridges
to the `pair_pending`/`pair_confirm` wire events; on accept it runs `enroll.Enroll` +
`device.Registry.Add` in-process. The `RendezvousTransport` is injected (a `memRendezvous`
in tests; a `relay.DialRaw` adapter in production), so the daemon holds no *standing* relay
coupling — it dials per-pairing.

**Why (Options B and C rejected).** (1) **Key custody** — `enroll.Enroll` needs the machine
grant-signing key + epoch keys, which have no production home yet and must live in a
long-lived trusted process; the daemon already owns the device registry (the enroll target
and the R-POL.9 authorization authority). A short-lived `swarm remote pair` CLI must not
hold grant-minting authority. (2) **R-PAIR.5** — a long-lived Bubble Tea TUI cannot cleanly
fork a CLI and screen-scrape its confirm prompt, so the SAS must arrive as a wire event
(`pair_pending`) and the decision return as one (`pair_confirm`); the wire events are the
only clean mechanism for the TUI path and serve the CLI path identically. Option B
(CLI-hosted pairing) splits the security-critical path across two processes and adds a
"trust the CLI's word that the SAS matched" step without removing any daemon work; Option C
(gateway-hosted) is most exposed to the untrusted relay and holds neither the registry nor
the enroll keys. Both rejected.

**Op set** (all **owner-tier only** — the remote tier refuses them; `CapPairing`-gated;
bound to the triggering connection for correlation, one pairing in flight per connection):
- `pair_start` (client->daemon; reply carries the QR + rendezvous id) — the trigger; the
  daemon generates the rendezvous id + single-use secret (kept in the trusted process),
  builds the QR, creates the rendezvous, spawns the handshake goroutine.
- `pair_pending` (daemon->client push) — SAS (six emoji) + device name at the confirm gate.
- `pair_confirm` (client->daemon) — the human's allow/deny, routed to the waiting `ConfirmFunc`.
- `pair_result` (daemon->client) — terminal `paired: <device>` / `failed: <reason>` (since
  `Machine.Pair` returns asynchronously relative to `pair_confirm`).
Seam: an additive optional interface `PairingHost{ BeginPairing(ctx, req, confirm, result)
(PairView, error) }` on the daemon API keeps pairing/enroll/crypto types out of the
`protocol` package (mirrors `DeviceLister`/`DeviceRevoker`/`PolicyDescriber`). Every new
direct `Control` field gets a `protocol.md` field-table row (GG-7).

**New daemon attack surface (recorded for the audit committee).** The daemon makes an
**ephemeral, human-triggered, outbound-only** `DialRaw` to the configured relay during the
pairing window: opaque Noise bytes only, no relay-auth key disclosed, no standing listener,
torn down on complete/decline/TTL/disconnect. This **relocates** rather than adds surface
(B/C would open the same dial in another process) and is the smaller blast radius given the
daemon must hold the enroll keys regardless. Abstracted behind `RendezvousTransport`.

**Security invariants preserved.** SAS compared out-of-band by a human at the local console
(never auto/timeout-confirmed); confirm never inferred or remembered (fresh SAS + single-use
secret per handshake); disconnect-before-confirm **fails closed** (goroutine ctx derived from
the connection; `ConfirmFunc` errors -> decline frame -> rendezvous burned -> nothing
enrolled); headless refused (`LocalConsole=true` only while a live owner client is present —
no standing auto-pair listener, R-PAIR.8); **owner-tier only** because the pairing device has
no pinned `CommandSignPub` yet, so `requireRemoteAuthz` cannot and must not gate pairing;
`enroll`/`Add` fail-closed. **Documented edge (tracked, not blocking):** `enroll`+`Add` run
after the frozen single-byte accept frame is sent, so a `Registry.Add` I/O failure yields a
confirmed-but-unenrolled device — fail-closed on the daemon (its future commands won't
authorize), tiny window (enroll is pure; only `Add` does I/O).

**Deferred (NOT A3.3).** Production machine-key provisioning (grant-signing + epoch keys)
shares A2's `swarm remote init`. Sealed `EpochGrant` delivery to the phone goes **out-of-band
via the relay mailbox** (gateway `MailboxAppend`), because in-band delivery would require
editing the frozen single-byte decision frame — belongs to A2/A7. The live-relay
`RendezvousTransport` adapter (sub-slice e) is blocked on A2; sub-slices a-d land now against
`memRendezvous`. Frozen layer untouched (pairing/enroll/crypto/registry/qr reused; the relay
`DialRaw`/`Rendezvous*` calls are wrapped by a new additive adapter, not a relay change).

## Amendment 2026-07-24 — Keystroke transport (R1) + terminal renderer (A7): the two A7 decisions

Resolves the two A7 blockers the A5 cross-model review flagged (docs/verification/
remote-phaseA-a5-review.md R1) and that Phase A left open. Both are the operator's decision,
recorded here before A7 implementation. Neither touches the frozen crypto layer.

**Decision 1 — keystroke transport: sealed + sequence-gated, riding the control lease
(option (a) of R1; per-keystroke MAC rejected).** Live input frames (`OpDataIn`/`OpResize`)
travel as **sealed mailbox envelopes** (the relay is mailbox-only) on the SAME machine
mailbox as commands: each frame is E2E-sealed under the epoch `ContentKey` (the relay sees
opaque bytes — it cannot read or forge keystrokes, and lacks the key regardless) and carries
a **monotonic mailbox sequence number**. The **gateway** (machine side) seq-gates the stream
with the same `crypto.MailboxReceiver` discipline already used for command-IN — reusing the
SAME single `(sender, epoch)` seq space, since the phone stamps commands AND input from one
monotonic allocator — rejecting replay/reorder/dup, then forwards the ordered, deduped raw
input as `TDataIn`/`resize` over ONE persistent lease-holding UDS connection it dials to the
daemon (take_control is forwarded on that same connection to establish the lease first).
Keystrokes are NOT individually signed — they ride the lease per D7. The daemon's existing
four-clause input gate (`controlGateOpen`: kill-switch -> `cc.control != nil` -> now < expiry
-> target/leaseGen match) authorizes every frame on the ordered lease connection.
Per-session anti-replay is structural: input flows only while the lease is live, the
session+generation match is the per-session authorization, and the mailbox seq is the
anti-replay coordinate. **Seam note (grounded 2026-07-24):** the seq-gate lives on the
**machine side at the gateway**, NOT in the daemon — the daemon holds no `ContentKey` and
receives an already-ordered UDS stream, so a daemon-side seq field would add GG-7 surface for
zero gain under this trust model (relay = adversary, defended at the gateway; gateway =
owner-uid residual). The daemon change is R7 only; the rest is phone-core encoders + gateway
lease plumbing.

**Trust boundary (the R1 question, answered).** The **relay is the adversary** and is fully
defended: it cannot forge sealed frames (no `ContentKey`) and cannot replay/reorder them past
the daemon's seq gate. A **compromised owner-uid gateway** could inject keystrokes into a live
lease — this is the **documented D4/D5 owner-uid residual** (a compromised owner-uid process
is already owner compromise; the gateway holds no more authority than the owner it runs as).
Per-keystroke MAC (option (b)) would additionally defend gateway *integrity*, but is rejected
for v1: it adds per-keystroke crypto + a key-custody question for marginal gain, since a
gateway compromise is game-over regardless of keystroke authentication. Dedicated-uid /
sandbox gateway hardening remains the deferred path if gateway integrity is later required.

**R7 folded in.** The control-session lifetime binds to `min(signed command ExpiresAt,
now + server-max)` where the phone signs `take_control` with `ExpiresAt = desired session
end` — the lifetime is what the device signed, not an unsigned `TTLSeconds` hint (which
remains an accepted upper-bound cap). A7's phone-core signs accordingly.

**Decision 2 — terminal renderer: server-side VT render (option (a)).** The **daemon/gateway
renders the live VT grid to a sanitized text snapshot + live tail** on the trusted machine
side; the phone displays ready-made safe text. Rationale: (1) hostile-PTY sanitization stays
on the **trusted** side — no VT-emulator / control-sequence-injection attack surface on the
phone (the A7 "no control-sequence injection at the phone" criterion becomes structural, not
a phone-core burden); (2) the phone-core stays thin (no terminal emulator over the gomobile
boundary); (3) it matches the binding design doc's "terminal peek = snapshot + live tail".
The renderer reuses the existing in-tree VT emulator (ADR-005) on the machine side; the phone
receives sanitized snapshot frames, not raw PTY bytes.

**Consequences.** A7 gains a machine-side snapshot renderer/sanitizer and a sealed+seq'd input
data-plane; the phone-core gains an input encoder (seal + seq) and a snapshot decoder, but no
VT emulator and no keystroke signing. A7 remains a **security-critical slice** (cross-model
review required, DoD §0). GG-7: the input channel adds NO new GG-7-covered `Control` fields —
keystrokes are the already-documented raw `TDataIn`, resize reuses `cols`/`rows`, take_control
reuses `ttl_seconds`/`gate_token`/`expires_at`; only prose updates to `protocol.md` (no field
table rows). Frozen crypto reused unchanged (`SealMailbox`/`ContentKey` for sealing;
`MailboxReceiver` seq discipline for the gate) — no ADR needed for the crypto layer.

**Grounded residuals (2026-07-24).** (1) **Cross-device input (single-device v1: moot).** The
epoch `ContentKey` is shared across paired devices (D2) and `SenderKeyID` is zero on the wire,
so the gateway cannot cryptographically attribute an unsigned input frame to a specific paired
device; a second paired device could seal frames routed to the lease holder. For the D12
personal single-device v1 this cannot occur. Cheap hardening if multi-device is added: stamp
`SenderKeyID = KeyID(commandSigningPub)` on take_control + input envelopes and bind the lease
to that sender id. (2) **Keystroke latency is a Phase B UX concern, not Phase A correctness.**
The relay is mailbox-only; input rides the command-IN poll (default 500ms — unusable for live
typing). Phase A proves the backend correct at any poll interval (phonesim uses a tight one);
a live/long-poll transport (or a hard poll-interval drop) is a Phase B decision, not a blocker
for the input backend.

## Amendment 2026-07-24 — A7 review: concurrent multi-tier control + supersede seed fidelity

The A7 cross-model review (docs/verification/remote-phaseA-a7-review.md, findings F and G)
surfaced two design questions the shared per-session tap (A7 F1) raises. Both are resolved here
for the PERSONAL single-owner v1; neither blocks Phase A.

**Decision G — concurrent owner + phone control is ALLOWED in v1 (drifts from P-5, scoped).**
The owner-tier controller (`d.srv`) and a remote take_control lease (`d.remoteSrv`) hold
independent read-write leases on the SAME PTY via the shared tap; input interleaves and neither
supersedes the other. system-spec P-5 ("exclusive controller lease; one controller per session in
v1") and ADR-002 were written for a MULTI-USER contention model. For the personal v1 the owner and
the phone user are the SAME person driving their own machine — concurrent control is not a
contention hazard, and both controllers are authenticated (the phone via a signed take_control
through requireRemoteAuthz). So P-5's exclusivity is **relaxed for the personal single-owner
model**: concurrent owner + phone control is permitted. Multi-user exclusivity (locking or
arbitration across tiers) is a later concern if remote control is ever shared across distinct
people. **Recommended hardening (A4/TUI):** the TUI SHOULD show an indicator when a remote lease is
active on a session, so the owner is aware a phone is driving (safety/awareness, not
authorization). Recorded as an A4/TUI follow-up, not a Phase A blocker.

**Decision F — an owner supersede concurrent with an active peek is mirror-seeded (accepted
fidelity residual).** When a peek (or a remote lease) keeps the tap alive, an owner supersede
becomes a LATE tap subscriber seeded from `mirror.Snapshot()` rather than a fresh shim re-dial
(the shim is single-consumer — a fresh dial is impossible while the tap holds the upstream; that
is precisely why the tap exists). The mirror's initial seed (`seedMirror` via `vt.RenderSnapshot`)
drops pre-tap SGR pen + title + scrollback, so an owner repainted this way can miss pre-tap
styling that the ADR-002 fresh-dial path preserves. This is **accepted for v1**: it occurs ONLY on
the narrow "owner supersede WHILE a phone peek/lease is concurrently active" path, the loss is
cosmetic (colors/title of the pre-tap screen; live-frame styling is tracked correctly by the
emulator from that point on), and the alternative — forbidding concurrent peek so the owner can
fresh-dial — is strictly worse UX. Full-fidelity concurrent supersede would require a lossless
mirror (preserve SGR/title/scrollback in the mirror seed) — recorded as a future enhancement, not
a Phase A blocker. The sole-subscriber (no concurrent peek) supersede stays byte-identical to
today (tested).

## Amendment 2026-07-24 — Phase-A audit-committee closure: grant delivery, single-device v1, admin tier

Resolves three committee findings (docs/verification/remote-phaseA-committee.md C5, C6, sonnet#3).
None changes the frozen crypto layer. Refines D3 (pairing), D5 (gateway), D8/D9 (launch/relay).

**Decision C5 — sealed EpochGrant delivery is WIRED via the gateway mailbox (implements the
2026-07-23 deferral).** The pairing host mints `res.Grant` (a `crypto.EpochGrant` sealed to the
device RECIPIENT key and signed by the machine grant key) in `enroll.Enroll`, but `BeginPairing`
discarded it, so a real (non-in-process) phone could never recover the epoch ContentKey. Delivery
now follows the topology already chosen 2026-07-23 (out-of-band over the relay mailbox, NOT in-band
in the frozen decision frame): (1) the daemon PERSISTS the sealed grant addressable by device id at
enroll time (opaque at rest — recipient-sealed, only the phone's recipient private key opens it, so
storing it owner-uid and forwarding it through the untrusted relay leaks nothing); (2) the GATEWAY —
the process that already holds an authenticated relay `Client` with the device `RoutingID` — on
connect calls relay `authorize_device` for the paired device (closing HI-3's unused-authorize gap)
and `MailboxAppend`s the sealed grant to the device mailbox; (3) the phone BOOTSTRAPS by reading the
grant from its mailbox and `AcceptGrant`-ing it BEFORE it can build the ContentKey-keyed
`MailboxRouter` — the grant is NOT a router frame (it is recipient-sealed, not ContentKey-sealed:
it is what DELIVERS the ContentKey, a chicken-and-egg the router cannot resolve). Delivery is
idempotent (the phone dedups by grant seq), so at-least-once mailbox semantics are fine and no
synchronous ack couples the `swarm remote pair` CLI to the relay round-trip; the SAS confirm remains
the security gate and the registry commit remains the pairing completion. **Why gateway not daemon:**
the pairing daemon holds only a raw per-pairing `DialRaw` rendezvous `Conn` (burned before the grant
exists) with no `MailboxAppend`; giving it a standing authenticated relay client would relocate more
surface into the trusted process than the gateway (which must hold that client regardless) already
carries.

**Decision C6 — single device enforced at the daemon for v1 (the gateway already assumes it).**
`Registry.Add` had no count cap, but `cmd/swarm-remote` refuses `len(devices) != 1` at startup, so a
2nd pairing bricked the gateway on the next restart. v1 is single-device by construction: pairing
REJECTS enrollment when a device is already registered (fail-closed, transactional — the 2nd
handshake declines rather than adding an unusable record). Multi-device is DEFERRED to a later phase
and requires (a) binding a nonzero per-device `SenderKeyID` into every inbound envelope + lease so a
device is cryptographically attributable past the shared seq high-water (a FROZEN-CRYPTO change
needing its own ADR — today `SenderKeyID` is uniformly zero inbound, the accepted A7 residual), and
(b) an admin capability tier (below). Until both land, more than one device is neither attributable
nor serviceable, so admitting a 2nd is strictly a footgun.

**Decision sonnet#3 — no admin tier in v1 (formal deferral).** Any `CapFull` device can revoke any
device; there is no admin/owner distinction among paired devices. For single-device v1 this is moot
(one device cannot revoke a peer that does not exist). When multi-device lands, a formal capability
model (admin vs standard, who-may-revoke-whom) is required and gets its own ADR. Recorded here as a
deliberate v1 scope decision, not an oversight.

**Decision ME-1 — relay-socket close on revoke is DEFERRED to a later phase (formal hardening
ruling).** C1 + C2a already sever a revoked (or kill-switched) device's lease + peek + journal at the
DAEMON choke point immediately, and the daemon fail-closes every subsequent op from an unregistered
device, so the injection/read hole -- the unanimous C1 blocker -- is CLOSED and tested. The relay-side
live-socket close (ME-1, fully implemented at the relay, `server.go` handleDeviceRevoke, but unreached
from the daemon path) is defense-in-depth TRANSPORT hygiene: it would free the revoked device's relay
socket and stop it holding a connection, but the daemon already rejects its every op and the gateway
stops sealing new frames to its mailbox (C2a severs the journal/peek source), so its marginal security
over the daemon severance is near-zero. Wiring it needs a cross-process revoke signal (daemon ->
gateway) plus a gateway registry-watch loop -- disproportionate infrastructure for v1. The mechanism
is now cheap to add when justified: the gateway holds an authenticated relay client (C5's
`deliverEpochGrant`), so on observing its paired device removed it would call relay `DeviceRevoke(
RoutingID(rec.RelayAuthPub))` and shut down. Recorded as a Phase-B hardening item, not a v1 blocker,
because the required C1 deliverable (daemon-side severance) is complete.

## Amendment 2026-07-24 — Revoke ROTATES the machine epoch key (closes the re-audit codex#1 confidentiality gap)

**Status**: accepted (operator-directed). The re-audit found that revoke removed the device record but
did NOT rotate the shared machine epoch key: a revoked phone retains `K_content`, and because every
device shares the one fixed epoch key (`machineid.Generate` set EpochID=1 once, no rotation), pairing a
REPLACEMENT phone reuses the same key -- so the untrusted relay could copy the replacement phone's
ciphertext to the revoked one, which still decrypts it (`recipient_key_id` is outside the AEAD). That
breaks revocation confidentiality in the revoke-because-compromised case. Daemon-side severance (C1/C2a)
stops the revoked device's LIVE ops but does nothing about the retained KEY being reused for a FUTURE
device. sonnet+opus accepted this as a v1 residual; codex rated it a blocker; the operator directed
that it be closed in v1.

**Decision.** `RevokeDevice` ROTATES the epoch on every successful removal: `machineid.RotateEpoch()`
mints fresh `crypto.NewEpochKeys()`, increments EpochID, resets GrantSeq=1, and re-persists the machine
identity atomically (the existing `Identity.Save`: temp+fsync+rename, 0600). The daemon then reloads its
in-memory pairing snapshot (`a.pairing`) so the NEXT pairing seals the new device's grant under the NEW
epoch. The revoked device's retained old-epoch `K_content` is now dead for all future traffic.

**Why this is sufficient + safe in single-device v1.** The crypto layer is rotation-ready: a higher
EpochID is always accepted by `GrantReceiver` (a rotated grant opens cleanly on a fresh phone), and a new
EpochID is a new `(sender, epoch)` mailbox bucket, so the durable outbound seq files need no reset.
`a.pairing` gains a mutex (it was read lock-free by BeginPairing; revoke now mutates it), and the
rotation is coherent + crash-atomic with pairing (see the round-3 corrections below).

**Round-3 corrections (the rotation must compose with the running gateway + concurrent pairing).** A
re-audit found the first cut of this decision was INCOMPLETE, because the gateway reads the epoch
ContentKey once at startup and reconnects forever with no reload path: after revoke -> re-pair the still
-running gateway resumed sealing the NEW session to the REVOKED device's mailbox under the OLD key (which
the revoked device holds), so rotation alone did not close the confidentiality gap. Also, a concurrent
`RevokeDevice` could rotate the epoch DURING an in-flight `BeginPairing`, enrolling the replacement under
the stale (about-to-be-revoked) epoch. Corrected:
- The GATEWAY now EXITS when its paired device is no longer registered: on each journal reconnect (the
  sever->reconnect cycle a revoke triggers) it re-reads the device registry, and if its device is gone it
  returns `ErrDeviceRevoked` and shuts down, tearing down every peek + lease. This fires during the
  Count==0 deviceless window, BEFORE any re-pair, so the stale-key gateway is gone before a replacement is
  served. "Restart after pairing" is no longer an unenforced assumption. (A live in-place epoch-reload
  stays Phase B; exit-on-revoke is the v1 closure.)
- Rotation is CRASH-ATOMIC with removal (confidentiality direction): `RevokeDevice` rotates the epoch
  BEFORE removing the device (so "device removed => epoch rotated" holds across a crash), and a rotation
  fault aborts the revoke. **Recorded residual (round 4, opus#3, integrity-only):** the CONVERSE is not
  atomic across the two files -- a crash after the rotate persists but before the Remove persists leaves
  the epoch rotated AND the device still registered, so on restart it retains signed command authority
  (kill/launch/take_control) until the operator re-runs revoke. Confidentiality is preserved (the rotated
  epoch means the device cannot read re-sealed journal/peeks); the integrity residual in this narrow
  operator-directed crash window is deferred (a two-file atomic commit is out of scope for v1).
- Revoke and pairing are SERIALIZED by one outermost `lifecycleMu` (round 4): RevokeDevice holds it across
  its whole transaction and BeginPairing across its commit section only (never the handshake), so a
  replacement can never be enrolled under a stale epoch mid-rotation and two revokes cannot both rotate.
- Pairing re-validates the epoch at the COMMIT point: `BeginPairing` aborts fail-closed if `a.pairing`'s
  EpochID changed since the handshake's entry snapshot, so a replacement is never enrolled under a stale
  epoch. This composes with rotate-before-remove: when the re-check does not fire, the to-be-revoked
  device is still present, so `AddSole`'s single-device guard fails the enrollment closed anyway.
Together these make revoke -> (rotate + gateway-exit + sever) -> re-pair (fresh epoch) -> restart gateway
(new epoch) leak nothing to the revoked device. ME-1's relay-socket close remains a Phase-B defense-in
-depth item; it is no longer load-bearing for this property now that the gateway exits on revoke.

## Amendment 2026-07-25 — Phase B (Android handset v1): the decisions the phase rests on

**Status**: accepted. Discharges PB-DOC-1 of
`docs/specifications/remote-phaseB-requirements.md` (v3.5, requirements-complete after five
committee rounds), which binds the Phase B implementation. Refines D2 (key tiers), D5 (gateway
lifecycle), D6/D9 (durable state, transport, resync, push) and D12; supersedes the 2026-07-23
client-strategy amendment on skin (it retained a *pair*, not a choice) and narrows its
"light+dark token sets" wording for the client. One frozen-crypto **signature** change is
authorised (B14); no crypto semantics change. Decisions are recorded here with the code that
forced them; the requirements document holds the detail and the acceptance criteria, and each
slice named below owns the implementation.

**B1. v1 is SINGLE-MACHINE, and stays single-device. The machine switcher is cut.** The phone
core is structurally single-machine — one `ContentKey` per `MailboxRouter`
(`internal/phonecore/snapshot.go:137-157`), one machine/target/grant/epoch/sequencer per phone
(`internal/phonesim/phonesim.go:52-59`) — so frames from two machines are sealed under different
epoch keys and one router cannot open both. The roadmap's machine switcher assumed a capability
nothing supported, and the binding exit criterion says "a real session", singular. Multi-machine
therefore joins multi-device (deferred by the 2026-07-24 closure amendment, decision C6) in
Phase C, where both need the same missing primitive: a nonzero per-device `SenderKeyID` bound
into every inbound envelope. The design's machines screen ships as a single-machine pane
(presence, the one paired device, revoke, kill switch, activity log), not a switcher.

**B2. Light mode is DEFERRED to Phase C — and this is not a correction to the 2026-07-23
amendment's claim.** The four `--p-*` product skins in
`docs/research/remote-control-design-directions.html` are dark-only; the artifact itself does
ship a light set (`@media (prefers-color-scheme: light)` at `:8-10`, `:root[data-theme="light"]`
at `:12`), so the earlier "light+dark token sets" sentence was true of what it described. What is
deferred is *authoring a light product theme for the Android client*, the single largest
non-load-bearing item in v1, against an exit criterion that is a dark phosphor terminal. Recorded
explicitly because a round-3 requirement (PB-DOC-6) proposed amending this ADR to say the light
set never existed; it was **withdrawn** on verification. A future reader must not resurrect it:
the deferral stands on its own merits and needs no such justification. Consequence: since the app
ships one mode, its theme must not inherit a `DayNight`/system-mode parent, or a system-light
handset renders it unstyled.

**B3. One skin: Substrate (d1).** Supersedes the 2026-07-23 amendment, which retained the pair
`01 Substrate + 02 Void` and never chose. Substrate is the artifact's default direction and its
restrained near-black surface ladder suits an information-dense monitoring list better than
Void's true-black treatment, which flattens the Group sections. The machine-readable token source
(JSON, one origin for the Android theme) pins the single skin; the phosphor-green monospace
terminal treatment and the retirement of purple are unchanged.

**B4. The bound surface is one non-internal façade over a closure constrained by an executable
allowlist.** `internal/phonecore` reached `internal/protocol` -> `internal/daemon`, dragging the
shim, engine, VT emulator, transcript, persistence and PTY — 52 non-stdlib packages — into a
package destined for a handset an adversary may hold, directly against the 2026-07-24 amendment's
Decision 2, which keeps the VT emulator and raw PTY bytes off the network-facing edge. Two
decisions: (a) the daemon-free wire types move to a leaf package `internal/protocol/schema` with
`internal/protocol` **aliasing** every moved name, so type identity, wire encoding and every
consumer are untouched (shipped in S1: closure 52 -> 18, zero forbidden packages, guard covering
host + android/arm64 + ios/arm64 because those closures already differ); (b) only the **bound**
package must live outside `internal/` — verified empirically with a probe AAR — so the façade is a
single non-internal package that consumes the internal tree freely, and no relocation of
`phonecore` is needed. The constraint is an **allowlist** of exact import paths, not a denylist
(the prior denylist already omitted `internal/shimwire`) and not categories (unmachine-checkable,
and they omitted required transitive deps). Recorded trap: `LaunchContentHash` deliberately stayed
in `internal/protocol`; the façade may move it or re-export it, but **reimplementing its canonical
length-prefixed encoding is forbidden** — a one-byte divergence yields silent signature
verification failures with no compile error and no test linking the two implementations.

**B5. The phone gets durable state; the send-seq strategy is reserve-a-ceiling-and-burn-the-gap.**
`internal/phonecore` performs no persistence at all and its outbound sequencer is a bare
`atomic.Uint64` returning 1 on first call (`input.go:33-36`), while the gateway rejects
`seq <= highest` as stale (`internal/remote/crypto/envelope.go:33-34,240-243`). Android kills
backgrounded processes as routine behaviour, so **one** process death permanently bricks typing,
launch and kill under the same epoch — the exit criterion fails on the second app launch — and the
mirror direction is worse: an in-memory `MailboxReceiver.highest` (`envelope.go:211-216`) resets
the phone's replay high-water to zero, handing a retaining relay a redelivery window. Decisions:

- **One persisted schema** enumerating everything resume-critical: device keys, pinned machine
  static + sign pub + routing id, epoch id and keys, outbound send-seq, per-`(sender, epoch)`
  receive high-waters, the grant receiver's `(epoch, grant_seq)` watermark (which
  `internal/remote/crypto/epoch.go:155,167` already demands be persisted or "a relay could replay
  an old correctly-signed grant after a phone/app restart"), the push replay coordinate, the relay
  mailbox cursor, caches, pending idempotent ops and per-bucket stale flags. Versioned, with an
  unknown future version failing closed.
- **Send-seq: reserve a ceiling, burn the gap** (block 256), mirroring the gateway's own
  `internal/remotegw/seqstore.go` (block 64) rather than an fsync per keystroke. Rejected:
  per-frame durability (unusable on the input hot path) and a non-durable counter (the brick
  above).
- **The burned gap must be absorbed by the re-lease command frame, never by an input frame.** The
  gateway silently drops input/resize whose `Gap` bit is set while ignoring `Gap` on commands
  (`internal/remotegw/command_loop.go:208-216`), so without this rule the first post-restart
  keystroke vanishes with no signal.
- **The receive path commits as one transaction before the ack**: `{high-water, relay cursor,
  decoded cache mutation, stale flags}`. Today the high-water advances inside `Accept`
  (`envelope.go:254`), caches mutate afterwards (`phonecore/snapshot.go:201`) and the ack comes
  later still, so a crash between them either loses a frame forever or permits a replay.
- **Rollback has a named trust anchor: authenticated remote reconciliation, with a distinct
  authority per coordinate.** AEAD and atomic writes detect corruption, not rollback — a valid
  older blob sealed by the same Keystore key stays valid — and KeyMint rollback-resistance
  protects key blobs, not app state. One authority is not enough either: the gateway's inbound
  high-water describes only phone->machine sequences. So (a) phone send-seq answers to the
  gateway's durable inbound accepted high-water, with reserved-but-unused blocks accounted for;
  (b) each receive bucket answers to the gateway's durable outbound ceiling *for that bucket* —
  journal/terminal from the outbound outbox, command-reply from the already-durable
  `outbound-reply.seq` (`cmd/swarm-remote/config.go:95`); (c) the grant watermark answers to the
  daemon's epoch/grant issuance coordinate. An unreachable authority fails closed for mutating
  ops, marks the affected channels stale, and reseeds. The carrier is a machine->phone reconcile
  record sealed onto the **existing** outbound stream (see B6 for why not a phone-initiated signed
  reconcile).
- **Fail-closed must not mean bricked.** `BeginPairing` refuses while a device is registered
  (`AddSole`, the 2026-07-24 single-device decision), so a phone that fails closed on corrupt
  state would have no exit but physical access to the machine. An unconditional owner-side
  recovery flow — identify the stranded device, revoke/unregister, purge machine and relay state,
  re-pair — is required; a re-grant path does not substitute, because it cannot recover a phone
  whose local state is already fail-closed.

**B6. Resync: staleness per SEQ BUCKET, repair per CHANNEL, and the repair is UNSIGNED.** The
phone receives multiple independent sealed streams, and `MailboxResult` carries only
`{Plaintext, Gap bool}` (`envelope.go:195-200`) with no frame kind, so a gap in the shared
journal+terminal sequence space **cannot** be attributed to one of them. There are three buckets
(shared journal+terminal; command-reply, kept separate by the deliberate `SenderKeyID` split at
`internal/remotegw/command_in.go:104-109`; grant) and four repair channels: journal via an atomic
roster+events snapshot, terminal via a fresh full snapshot (a journal reseed cannot repair a
missed grid), command replies via the durable operation outcome, grant via the re-grant/terminal
state. A shared-bucket gap conservatively stales **both** journal and terminal; attributing it to
one is a failing implementation. `Stale()` clears only after that channel's successful reseed,
committed with its transport watermark; a failed resync stays stale.

**Authorization (the PB-SYNC-4/-5 decision): resync is NOT device-signed.** Journal repair rides
`handleJournalRead`'s existing gate — the negotiated `journal` capability plus the kill switch
(`internal/protocol/server.go:1657-1683`) — not `requireRemoteAuthz`, which guards the mutating
ops. Terminal repair rides the already-unsigned `terminal_watch` read, which the gateway routes to
its `TerminalWatcher` without consulting the device authenticator while the daemon gates the peek
itself per snapshot (`internal/protocol/schema/remote.go:51-59`,
`internal/remotegw/command_loop.go:238-256`). Consequence: **no new `Action*` constant**, so the
closed, fail-closed `actionClass` switch (`internal/skeleton/deviceauth.go:17-26`) is untouched.
Rejected — a signed resync: the only fitting existing class is `ActionControl`, and `rec.Capability`
is pinned at enrollment and never read from the wire, so an observe-tier device could never repair
a read it is entitled to. A read-repair must not require the control tier. The machine->phone
reconcile frame of B5 follows the same rule and is carried on the existing outbound stream for the
same reason.

**Journal reseed REPLACES the phone's cursor.** The daemon emits roster records with `Cursor`
deliberately unset — "a roster record is a set member keyed by SessionID, NOT a point in the
cursor-ordered event stream" (`internal/daemon/journal.go:60-73`) — while `SessionCache.Apply`
drops any record with `rec.Cursor < c.cursor` (`internal/phonecore/journal.go:110-115`). Once the
first event advances the cursor, every subsequent roster snapshot is silently discarded, which
makes the designated journal repair channel a no-op and hides reconcile-adopted Running sessions
permanently. Either the reseed replaces the cursor wholesale or roster records carry the boundary
cursor; no test may use a nonzero roster cursor, since production never emits one.

**B7. Transport: request-id correlation with concurrent dispatch on BOTH hops, plus a bounded
server-side wait. Server-push frames rejected.** §6.0 of the requirements assigns this choice to
this document. Today `Conn.roundtrip` holds `c.mu` across write-then-blocking-read with no request
ids (`internal/remote/relay/client.go:108-126`) and the relay's `serveConn` is strictly
`readFrame -> dispatch -> readFrame` (`internal/remote/relay/server.go:382-390`), so a naive
long-poll head-of-line-blocks the very keystrokes it exists to accelerate — and a second
connection is not available, because one conn per routing id with newest-wins takeover
(`server.go:675-691`) is what revoke and presence severance depend on. Both candidate mechanisms
therefore need demux, which is why the earlier "needs no client demux change" tiebreaker
evaporated. Correlation wins on three grounds: the numeric budget is already written against the
wait form (25 s wait ceiling, one pending wait per client, <=50 ms for an append issued while a
wait is outstanding, 10 s non-wait timeout); metering stays legible, since `mailbox_read`/
`mailbox_ack` meter against `OpsPerMin` (`server.go:766,798`) while `mailbox_append` does not and
is capped by `MailboxAppendPerMin` alone; and an unsolicited server-push frame would add a second
inbound path with its own flow control and make the budget's criterion unmeasurable as written.
**The change covers both hops**: the gateway's fixed 500 ms command-IN poll
(`internal/remotegw/service.go:27`) goes too — a phone-side-only fix passes the letter while
typing stays 500 ms-gated. Unweakened by this change: the per-source pre-auth limits and
cumulative handshake deadline of the 2026-07-20 amendment, and the newest-wins single-connection
property.

Two rate consequences are part of the decision. Drain is budgeted, not just append: <=3 reads/s
and batched acks <=1/s per routing id on each hop, because at 8 appends/s a wait returning on the
first item would put 960 metered ops into a 600/window and kill the live tail mid-demonstration.
And the machine->phone direction is **coalesced at the gateway** to <=8 appends/s across journal
and terminal combined — they share one `RelaySink` and one target, against a render loop that can
emit ~62 snapshots/s (`internal/daemon/terminalrender.go:33`) — with the seq allocated only after
local admission. The obvious remedy "a failed append never consumes a seq" is **forbidden as
unsafe**: the relay commits the item before replying (`server.go:758-762`) and `MailboxAppend`
errors when the *response* read fails (`client.go:268`), so reusing the seq after a
delivery-unknown failure lets two different plaintexts claim it and the phone stale-drops
whichever loses. A definitive pre-commit refusal may release the seq; a delivery-unknown failure
must either burn it or retry the byte-identical sealed envelope, whose duplicate the receiver
drops for free (`envelope.go:255-257`) — so no relay protocol change is needed for it.

**B7 AS BUILT (S6b, 2026-07-25).** The decision above is unchanged; this records the mechanism
that implements it, plus two facts the original entry did not carry.

*Correlation is scoped to the wait, and the wait alone.* §6.0 caps pending waits per client at
one, so the correlation state is a single slot, not a pending map: the request carries a
monotonic `wait_id` and the reply comes back on its own frame tag, `MsgWaitReply` (0x04), which
the client's existing read pump routes straight to the parked waiter instead of onto the
serialised reply queue (`relay/client.go` `pump`/`deliverWait`, `relay/wait.go`). Every other
exchange keeps today's strict write-then-read under `c.mu` untouched. The tag carries the
"is this the out-of-order reply?" question and the id carries "which wait?" — the id is what
discards the reply to a wait the client already withdrew, which is the only way a cancelled wait
and its replacement can be told apart. Two consequences worth stating: the ordinary reply path is
byte-for-byte unchanged, so no Phase A framing test moves; and writes now need their own mutex
(`Conn.wmu`) because a parked wait writes outside `c.mu`.

*Concurrent dispatch is one goroutine, not a worker pool.* Only the wait handler leaves the
connection's request loop (`serveConn` still reads and dispatches everything else in order), which
is the whole of the concurrency the decision needs, because at most one wait is outstanding per
client. It is also what keeps the clause (d) fences intact for free: `sc.authed` stays a
single-writer field read by `readFrame`'s cumulative handshake deadline, per-source admission
control stays under `s.mu` in `serveConn` where CR-1 put it, and no ordinary op is reordered.
A wait is refused **inline** — pre-auth (on the ordinary error frame, since no wait was created),
superseded, quota-exceeded, or already-in-progress — so nothing a refusal touches ever parks.
A takeover severs the superseded connection's wait under `s.mu` in `registerSession`; without
that the superseded connection, which issues no further requests, would hold the single wait slot
for the remaining ceiling and live typing would be dead for up to 25 s after every reconnect.

*Cancellation crosses the wire, and is deliberately unmetered.* A client-side context
cancellation sends `mailbox_wait_cancel{wait_id}` — fire-and-forget, no reply of its own, the
wait's own reply being the answer. It has to cross: releasing only the client's slot would leave
the server's held until the ceiling, so the next wait would be refused and the cancellation would
be a lie. It is not metered because a cancel strictly *releases* server state; refusing one on
quota would strand the slot, and it is already bounded by the one-wait-per-client cap.

**Acks must not ride inline on the delivery path — a LATENCY requirement, not only a quota one.**
The original entry justified batched acks on quota grounds alone. `MailboxAck` measures p50
30.8 ms / **max 129.2 ms** on the reference host (one synchronous bolt fsync each), so a single
ack taken between an item's arrival and the next wait can consume **86% of the entire 150 ms p50
input budget**. Both hops therefore ack from a separate goroutine at <= 1/s
(`transport.AckBatcher`), never between `fn(item)` and the next wait. Dropping an ack is safe:
both hops advance a durable cursor before recording one, so an un-acked item is never
re-delivered whatever the relay does with its copy.

**The drain is adaptive, and its two regimes are decided by evidence.** §6.0's <= 3 reads/s is a
sustained-regime average (2026-07-25 amendment); read flat it contradicts p50 <= 150 ms, because
an un-batched wait returns one read per item. `transport.DrainPacer` therefore spaces reads at
1/3 s by default and drops the spacing after **two consecutive** spaced reads come back without a
batch — the spacing produced nothing but latency, so the producer, not the drain, is the limit.
It re-enters spacing the moment any read returns more than one item. *(Literally true, but the S6b
reviewer showed it is not what actually restores batching under §6.0's own 8 frames/s workload: an
un-spaced read returns on the FIRST arrival, so it returns exactly one item every time, and
`!p.spaced` short-circuits the idle counter so nothing accumulates. What restores batching there is
the token bucket's forced delay ~36 s in, during which items gather and the next read returns >1.
The rule is right and no workload latches the harmful regime -- a burst that alternates re-enters
batching immediately on any multi-item page -- but the trigger is the bucket, not evidence of
backlog.)* Two consecutive rather than
one, because a single slow append widens one gather window and one hiccup must not strand the
drain in the interactive regime. A token bucket sized to the relay's own one-minute window backs
both regimes, so the sustained average is capped however the regime flaps. *(Asymptotically. The
S6b reviewer quantified the transient: capacity is `3*60 = 180` and the bucket starts FULL, so one
relay window can carry 180 burst + 180 refill = **360 reads/min (6/s)** plus 60 acks = 420 metered
ops/min. That is 1.75x §6.0's stated 240/min and 2x its "<=3 reads/s sustained average over a
1-minute window" -- but it is inside `OpsPerMin: 600`, so the failure the budget exists to prevent,
the tail dying `codeQuotaExceeded` mid-session, genuinely cannot occur. Recorded rather than
re-tuned: shrinking the bucket to make the stated number literal would cost latency at the start of
every burst to buy nothing.)*

**Recorded limit of the request/response model.** Because the ADR rejects server-push frames,
one reply per request is a hard floor: delivering N items with a per-item latency bound of L
costs at least one metered request per item whenever items arrive more than L apart. At §6.0's
8 frames/s that floor is **6.67 requests/s** -- NOT 8 -- and a 3 reads/s ceiling forces ~167 ms of
mean queueing. *(Corrected by the S6b reviewer before it could harden into a constraint on future
requirements. The general rule holds -- one reply per request forces a reply before item k+1 exists
WHENEVER arrivals are more than L apart -- but the instantiation dropped its own precondition: at 8
frames/s the gap is 125 ms and L is 150 ms, so 125 < 150 and the precondition is FALSE there. One
request returning at t=125 ms delivers both items. The correct floor is `min(R, 1/L)` = 6.67 req/s;
the queueing figure is 333/2 = 167 ms, which §6.0 line 336 already states. The qualitative
conclusion is untouched -- 6.67 > 3, so a hard <=3 reads/s at 8 items/s with sub-150 ms per-item
latency still requires the streaming subscribe this ADR rejected -- but the gap is 2.2x, not 2.7x.)*
The regimes above are how the two coexist; they do not make the floor go away, and a future
requirement that needs both a hard <= 3 reads/s at 8 items/s *and* sub-150 ms per-item latency
needs a streaming subscribe, which is the option this ADR rejected and would have to re-open.

**B8. JNI key custody: exactly one secret crosses, inbound only.** The Go core holds its keys in
native heap; the Android Keystore API is Java-only and never exports private keys. The single
deliberate crossing is therefore a **transient per-tier data key, unwrapped by an
authenticated-Keystore AES KEK on the Java side and passed Java -> Go**, one per tier (wake,
content), zeroized after use. No long-term private key crosses in either direction: Go returns
only sealed blobs, public keys and signatures, and no exported façade method returns raw private
material. This is the one documented exception to the "no secret crosses the boundary" rule, and
it is directional and named so the guard test can pin it. The per-role platform matrix — whether
`{NoiseStatic, Recipient, CommandSign, RelayAuth}` is generated and used natively in Keystore,
held as an app-format key wrapped by this KEK, or software-only with a recorded residual — is
decided in the Android slice against real `KeyInfo` attestation, and Curve25519 entering KeyMint
only in Android 13 with device-dependent hardware backing means it cannot be settled from here.
That matrix may only **narrow** this crossing (a role moving natively into Keystore removes its
material from Go entirely), never widen it.

**B9. Wake vs content tiers on Android: enforced by Keystore auth-gating, not by process
isolation.** D2/A15's two-key split is honored, but its iOS argument does not transfer: A15 leans
on the Notification Service Extension being a separate process, whereas `FirebaseMessagingService`
runs **in the app process**. The enforcement mechanism on Android is therefore Keystore
authentication-gating (the unwrap fails while locked) plus code discipline — and the emulator's
software Keystore proves the code path, not the hardware guarantee. Tier assignment per role:
**RelayAuth is wake-tier** (after-first-unlock; background reconnect must work on a locked
handset); **Recipient, NoiseStatic and CommandSign are content-tier** (user-authentication-gated),
with per-use authorization for revoke, kill switch, launch and kill. Recipient is the load-bearing
one: `OpenSealedBox` recovers **both** the wake and the content key from a grant
(`internal/remote/crypto/keystore.go:163`), so an after-first-unlock recipient key plus the
persisted sealed grant would hand a stolen once-unlocked handset the content key and falsify this
ADR's own claim at `:89` in the very phase meant to implement it. For the same reason the sealed
grant blob is retained only under the content tier, or discarded once opened. Persisted state
follows the same split: only the push token and the push dedup coordinate are wake-tier; send-seq,
receive high-waters and decrypted caches are content-tier. And **lock purges live memory** —
invalidating the gate is not enough while `MailboxRouter` holds `ContentKey` by value and caches
decrypted sessions and snapshots (`internal/phonecore/snapshot.go:88,132`); on lock, background or
auth expiry the core stops content operations, zeroizes key custody, purges decrypted caches, and
requires a fresh unwrap.

**B10. Push: a gateway-side trigger on Group transitions, sealed under the wake key, with a
content-free pinned payload.** Nothing machine-side calls `PushTrigger`/`TokenRegister` today, so
v1 would have shipped a push transport with no producer; the trigger is in scope. It fires on
journal **Group transitions** (the roadmap's "wake on Group transitions"), coalesced 30 s per
session, sealed under the **wake key** — the content key is never used on the push path. This
introduces a new key crossing into the sidecar: `gatewayParams` carries only `ContentKey` and
`WakeKey` appears nowhere in the gateway or relay outside tests. It is accepted because the
gateway already holds the strictly more powerful content key, so the blast radius does not widen;
the requirement is that it holds the wake key **only** for the push path. The payload is a
data-only FCM message (no `notification` block, so a locked handset renders a generic alert and
the app decides everything else) carrying one opaque wake envelope; the sealed plaintext is
content-free per D2 ("activity on machine X"), carries a persisted replay coordinate and a
10-minute expiry, and must not carry session names, hostnames, agent names or Group labels. The
provider observes token, timing and size — D11 is unchanged and this is stated to it, not around
it. The exact field list is pinned by a schema test in the push slice. Two consequences recorded:
the seam is renamed transport-neutral (`PushSink`/`PushPayload`) rather than keeping the APNs name
for an FCM backend, and the user-facing push toggles need a real **device->machine preference
verb** whose suppression is durable and machine-authoritative where delivery is decided — local
filtering is not sufficient, since the push would still have been sent and the provider would
still see token, timing and size. That verb's action class and capability tier are a genuine open
sub-decision, deliberately left to the push slice rather than pre-empted here; it is the one place
in Phase B where a new `Action*` constant is expected, and it must be mapped in `actionClass`
(`internal/skeleton/deviceauth.go:17-26`) or it fails closed.

**B11. Gateway supervision has THREE states, and "no paired device" is not a failure.** D5's
external supervisor (launchd LaunchAgent / systemd user unit, generated from one source, running
as the owner, restart-on-exit with backoff, no embedded credentials) is finally shipped, and the
daemon still never spawns it. But naive restart-on-exit is a permanent crash loop after every
revoke, because `resolveGatewayParams` fails unless exactly one device exists
(`cmd/swarm-remote/config.go:77-78`) and the 2026-07-24 amendment made the gateway **exit** when
its device is no longer registered. The three states: (a) **no paired device** -> unit quiescent,
and this is a normal state, not a fault; (b) **paired** -> gateway active and grant delivery
completes; (c) **revoked** -> the process exits, the unit returns to quiescent, and only a later
successful re-pair activates a gateway under the new epoch. A successful `swarm remote pair`
ensures the gateway is running, so no manual restart is part of the flow. `swarm-remote` and
`swarm-relay` become released artifacts; today `.goreleaser.yaml` builds `./cmd/swarm` only.

**B12. §4.6 — the gateway's non-durable inbound replay guard: scoped, and the exploit claim
RETRACTED. The Phase A closure must NOT be amended to assert an exploit.** An earlier revision of
the Phase B requirements claimed that a relay retaining phone->machine frames could, after a
gateway restart, re-inject observed keystrokes into a live lease on the **shipped** tree. That
claim was **investigated and disproved**, and the disproof independently re-verified: a restart
builds a fresh, empty `LeaseManager`; `LeaseManager.Input` drops input for a session with no lease
conn (`internal/remotegw/leasemanager.go:67-72`); and a retained `take_control` cannot recreate the
lease, because its `operation_id` is claimed through the **durable** two-phase idempotency store
and a consumed one stays consumed (`internal/protocol/server.go:1452-1462`) — the daemon does not
restart when the gateway does. The blunter reason it is unreachable today is that no production
binary imports `internal/phonecore`, so a retaining relay has nothing to replay against.

What remains true is narrower and is what Phase B fixes: the guard is **not durable** —
`NewCommandBridge` builds a fresh `crypto.NewMailboxReceiver()` on every start
(`internal/remotegw/command_loop.go:106`), `SetCursor` is never called from production startup,
and the gateway persists **no** inbound state (`cmd/swarm-remote/config.go:91,95` open outbound
files only) — and its bounded-age backstop is disabled, since `NewMailboxReceiver` leaves
`maxAge == 0` (`internal/remote/crypto/envelope.go:219-221`). On a fresh receiver the staleness
test is skipped entirely (`seen == false`), so the first replayed frame at any seq is accepted and
so is every contiguous frame after it. The property therefore rests on *incidental* mechanisms — an
empty lease map, a shared monotonic sequencer, single-use operation ids — rather than on the guard
meant to provide it, which is exactly how a future routing or sequencing change turns a latent
defect into a live one.

It **is** reachable inside Phase B's own implementation window, against a phone holding durable
keys but a regressed send-seq — the precise intermediate state this phase creates: a legitimate
`take_control` at seq 1 is accepted and opens a lease (new operation_id, unexpired), retained
input at seq 60 sets the gap bit and is dropped, and seqs 61..100 are contiguous, carry no gap, no
signature and no expiry, and route to the live lease. Hence the sequencing rule: the gateway's
durable inbound state and the phone's durable send-seq/rollback anchor **land together**, or Phase
B briefly builds the hole it is closing.

**Two documentation rulings follow, and they are the reason this section is long.** (1) The Phase
A committee closure gains a **scoped note, not a retraction**: its "no relay-adversary-reachable
confidentiality/integrity hole" statement stands; what it records is the reproduced finding (no
durable inbound high-water, disabled age check, the original claim verified within a single
gateway run) plus the distinction between the disproved shipped-Phase-A exploit and the valid
conditional Phase-B trace. Writing a false correction into a committee-signed document is its own
harm, and no future reader should resurrect the exploit claim from this amendment. (2) Enabling
the age check requires the phone to stamp `IssuedAt` **first**: every phone->machine seal sets only
`{Version, EpochID, Seq}` (`internal/phonecore/input.go:59`, `command.go:100,121,143`), so an age
check turned on today computes an age of ~56 years and rejects every legitimate command and
keystroke. The bound is 10 minutes — well above the 60 s command TTL, well below the 7 d retention
cap.

**B13. The QR carries the relay URL; `MachineStaticPub` is NOT pinned in v1; the relay URL ceiling
is 39 characters.** The minted payload never set `RelayURL` although the codec reserved it and the
URL was available two frames up, so a scanning phone got a rendezvous id and a secret and **no
endpoint to dial** — and the "a malicious QR cannot silently point the phone at an attacker-chosen
relay" threat model presupposed a destination the QR did not carry. `BeginPairing` now populates it
verbatim (the machine's own dial target is the one endpoint known reachable, and the one displayed
before joining). The ceiling is forced arithmetic, not preference: the payload is
`13 + base64url(3 + L + 16 + 32)`, so L=39 gives 133 characters -> byte-mode ECC-L version 6 = 41
modules, which a standard 80x24 terminal can draw at half-block density; **L=40 jumps to version 7
(45 modules, 49x25) and no 24-row terminal can show it**. Adding `MachineStaticPub` pushes the
payload to ~162 characters -> version 8 = 49 modules, which fits under no 80x24 budget at all —
hence **not pinned in v1**. That is also defensible on its merits: the machine static is already
pinned from Noise msg2 and the six-emoji SAS of the 2026-07-23 amendment is the designed human
anti-MITM check, so a QR pin would be belt-and-braces rather than the primary defense. Revisit only
with a denser glyph family (sextants, U+1FB00) or a shorter payload encoding. Operational
consequence, recorded because it constrains deployment: `wss://swarm-relay.us-east-1.example.com:8443`
is 44 characters and does not fit, so `swarm remote init --relay-url` refuses blank,
whitespace-only, unparseable, non-`ws`/`wss`, host-less and over-length URLs **before any
filesystem write**, and the fallback names the real cause instead of blaming the terminal. Achieved
symbol budget (S3): 45x23 at `LINES=23` (quiet zone 2), 47x24 at 24 (quiet zone 3), 49x25 at 25+
(quiet zone 4, the QR standard's full zone) — better than the requirements' earlier quiet-zone-2
estimate, which assumed the renderer could not use the full box.

**B14. `crypto.KeyStore` becomes failable — and the signature change lands in the Go core, not the
Android slice.** `SignCommand(msg []byte) []byte` and `SignRelayAuth(challenge []byte) []byte` are
errorless and `NoiseStatic() *NoiseStatic` materialises raw private material
(`internal/remote/crypto/keystore.go:47-56`). Neither is implementable against Android Keystore,
which never exports private keys and whose every operation can fail — user-authentication-required,
and **permanent invalidation on biometric-enrollment change**, which the handset security
requirements explicitly demand be handled. Until this changes, "Keystore-enforced sign
authorization" is unimplementable and the biometric gate can only be a UI boolean, which is
precisely the shortcut the criteria are written to fail. Decision: every operation returns an
error, and the `NoiseStatic` accessor stays an **opaque handle** (never raw scalar export), backed
on Android by an app-format key unwrapped under B8's content-tier data key where the platform
cannot perform the DH natively. **Sequencing is part of the decision**: the interface change is
hoisted into the Go durable-state slice (S7), because the transport, state and façade slices all
consume this interface and leaving it in the Android slice would guarantee rework across every
Go-side slice; only the Android *implementation* stays there. `crypto` is inside the bound-closure
allowlist and is the FROZEN layer, so per project rule this edit is gated on this ADR and
re-reviewed cross-model after GREEN, exactly as the 2026-07-23 SAS widening was.

**Consequence / tracking.** The requirements document is the binding detail; slice ownership is
machine-checked in `docs/specifications/remote-phaseB-manifest.tsv` +
`remote-phaseB-slices.tsv`, not asserted in prose. Deferred to Phase C with reasons already
stated: iOS (a rebind of the same façade), multi-machine and multi-device with the `SenderKeyID`
binding and the admin tier, light mode, and production relay ops. Deferred within Phase B and
named rather than hidden: the push-preference verb's action class (B10), the per-role Keystore
matrix (B8), and the physical-handset gate — no handset exists on this machine, so Phase B's
ceiling here is "provisionally implemented", and reclassifying that gate as an accepted limit is
not permitted.

**B15. The daemon opens the remote socket when the machine is PROVISIONED for remote, not when
an environment variable happens to be set.** D4 names `<stateDir>/remote.sock` as *the*
dedicated remote-tier UDS, with no opt-in qualifier. The implementation disagreed with that:
the daemon set `RemoteSocketPath` from `os.Getenv(daemon.EnvRemoteSocket)` with no default
(`cmd/swarm/main.go:309`, "empty => remote control off"), while the supervision unit pointed the
gateway at D4's canonical path by default (`cmd/swarm/remote.go:318-321`). So on a stock install
the daemon served nothing there, `swarm remote init` installed a unit aimed at nothing, the
gateway exited failure, and the supervisor respawned it every `ThrottleInterval` forever. The
user-visible symptom was exactly **"the phone pairs, then silence"** — the exit criterion
delivering its first step and nothing after it. Recorded as PB-LIFE-7, slice S4b.

**Decision: option (a), conditioned on provisioning.** `skeletonConfigFromEnv` defaults
`RemoteSocketPath` to the *same* `gatewaySocket()` the unit Spec uses, gated on the machine being
provisioned (the identity artifact `swarm remote init` creates). Both sides then derive the path
from **one function**, so "two independent defaults that must agree" stops being a bug class
rather than being tested for.

**Why not (b) — refuse loudly at `swarm remote init`.** Measured, not assumed: (b) breaks four
existing PB-LIFE-2 tests, and more seriously **its detector is unsound in scope**. `swarm remote
init` cannot read the daemon's environment; dialing is the only probe available, and a dial
failure cannot distinguish "remote is off" from "the daemon isn't running" — a state `init` must
tolerate by its own design. Making it sound needs a second probe of the main socket and remains a
TOCTOU check on another process's environment. (b) keeps two definitions and adds a racy referee
between them. It is also the option that departs from D4's stated mechanism.

**The security narrative survives, carried by something stronger.** "Remote off unless asked"
previously meant "no listener, decided once at daemon start". It now means `RemoteControlEnabled()`
— derived from device presence, recomputed at read time, and already gating every remote op
(`internal/protocol/server.go:1359,1580,1774,2393`). That is strictly more reliable than a
start-time listener decision, and conditional (a) still leaves the socket **absent entirely** on
any machine that never ran `swarm remote init`. The existing guard
`TestSkeletonConfigFromEnv_RemoteSocketEmptyByDefault` keeps passing unchanged, because its state
dir is unprovisioned.

**Two consequences this decision creates, which the implementation must handle:**
- `skeleton.Serve` **aborts assembly if the remote listener fails to bind** (`serve.go:223-226`,
  `return nil, rerr`). Under (a) a stale `remote.sock` left by a crash would take down the whole
  daemon — a worse failure than the bug being fixed. The socket must be unlinked-if-stale before
  bind, or the remote bind must be non-fatal. Silently killing the daemon is not acceptable.
- `ServeRemoteWithID` **never chmods the socket** (`internal/protocol/remote.go:148`): it
  `net.Listen`s and inherits umask, relying on the 0700 state dir. D4 specifies **0600**.
  Pre-existing and previously reachable only for opted-in operators; under (a) it is on every
  provisioned machine, so it is fixed here rather than left as a residual.

**Residual, filed not fixed.** The CLI still reads `SWARM_DAEMON_REMOTE_SOCK` from *its own*
environment while the daemon reads its own, so an operator who exports it in one shell and starts
the daemon from another still gets a mismatch. Closing it requires the daemon to be the authority
(a durable record or a protocol query). Both routes land in packages frozen for other in-flight
slices, so it is recorded rather than rushed.

**B16. Backgrounding DISCONNECTS. No foreground service in v1, and high-priority FCM is the sole
background wake path. `minSdk` is 33.** PB-RUN-3 required "a policy compatible with PB-NET-5's
waiting mechanism" without saying whether the app holds a connection while backgrounded. The S13
RED author showed the two answers have very different costs that neither §6.0 nor B7 priced, and
correctly declined to pick between them. Deciding here.

**The problem.** B7's mechanism parks a socket in a bounded server-side wait for up to 25 s, which
is exactly what Doze, App Standby and battery saver exist to kill. So:
- **Hold the connection** -> a foreground service is mandatory, and its `foregroundServiceType` is
  forced to one of two bad options. `dataSync` is capped from API 34 at roughly 6 h/day per app,
  after which the system **force-stops the service** — so "observe all day" fails by design, and
  fails silently late in the day. `specialUse` requires a `PROPERTY_SPECIAL_USE_FGS_SUBTYPE`
  declaration and an explicit Play-review justification, which is a review dependency on a
  personal tool.
- **Drop the connection** -> PB-RUN-4's high-priority FCM becomes the *only* wake path, and its
  quota is per-app-per-device with no published number, so §6.0 — which budgets everything else to
  three significant figures — has no budget for the thing the design then depends on.

**Decision: drop the connection.** Foreground holds a connected socket and issues waits; every
background state (backgrounded, Doze, App Standby, battery saver) closes it and relies on a
high-priority FCM push to wake the app, after which the user foregrounds and the socket
re-establishes. No foreground service ships in v1.

**Why.** This is what the architecture already assumed — D6 is "push-wakes + coalesced snapshots",
and push exists *because* the socket is not held. The alternative buys a persistent notification, a
measurable battery cost, a Play-review dependency, and a 6 h/day cliff that force-stops the service
mid-afternoon, in exchange for saving a reconnect the user does not perceive: they are looking at
their phone in order to act, and the reconnect happens while they read the notification. The
unpublished FCM quota is a real unknown, but the volume here is small and already coalesced —
an agent needs its owner a handful of times an hour, debounced by PB-PUSH-0 — which is far below
the rate any messaging app sustains on the same mechanism.

**Consequences, stated so they are not discovered later:**
- Push is now **load-bearing, not a convenience**. A dropped push is a missed hand-off with no
  fallback, which raises the bar on PB-PUSH-6 (relay-restart loss) and PB-PUSH-9 (token lifecycle),
  and makes PB-E2E-5's real-FCM leg non-optional rather than nice-to-have.
- Nothing observes while the phone is in a pocket. That is the correct behaviour for this product
  and should be stated in the docs rather than discovered: the phone is a remote control, not a
  monitor.
- **A revoke cannot reach a backgrounded phone until the next push.** The gateway severs
  regardless, so the machine side is unaffected and the window is one of stale local display, not
  of retained access.

**`minSdk` = 33, decided here rather than left to S13.** PB-KEY-8 binds custody to PB-RUN-1's
`minSdk`, and Curve25519 entered KeyMint only in Android 13 (API 33) — below that the
`NoiseStatic`/`Recipient` roles cannot be Keystore-native at all, which would silently degrade the
central security property on old devices. Choosing 33 makes PB-KEY-8 clean by construction instead
of forcing a per-device fallback path nobody would exercise. The cost is excluding pre-2022
handsets, which for a single-maintainer personal tool is not a cost worth a fallback matrix.

**B17. Amends B16's `minSdk` rationale, and records two consequences of B16 that nothing else
catches.** The S14 RED author falsified the reason B16 gave, and the correction matters more than
the number.

**(a) B16's stated ground for `minSdk` 33 does not hold.** B16 pinned 33 because "Curve25519
entered KeyMint only in API 33, so below it the `NoiseStatic`/`Recipient` roles cannot be
Keystore-native". But KEYSTORE_NATIVE means the private key never leaves Keystore, which means the
*operation* must RUN inside Keystore — and that needs a Java -> Go reverse seam for `NoiseStatic()`'s
DH and for `OpenSealedBox`. **B8 pins the crossing to ONE INBOUND artifact and permits the matrix
only to NARROW it**, and the gomobile facade has been golden-pinned since S8 with no such seam. So
no role can be Keystore-native regardless of API level: every role is KEYSTORE_WRAPPED or
SOFTWARE_ONLY, and PB-KEY-8's matrix is satisfiable at API 23.

**The floor stays at 33, as a product choice rather than a cryptographic one**, and this ADR now
says so instead of implying a technical necessity that does not exist. Android 13 is a reasonable
2026 floor for a single-maintainer personal tool and avoids compatibility paths nobody would
exercise. Widening B8 to admit a reverse operation seam was considered and **rejected**: the single
inbound crossing is a strong, cheaply-verified property (the S8 reviewer confirmed in the shipped
binary that no bound method returns `[]byte`), and trading it for hardware-native Curve25519 on a
personal tool is the wrong side of that bargain. If a later phase wants native backing, widening B8
is the decision to revisit — not the API floor.

The consequence is fenced rather than left implicit: `KeyCustodyMatrixTest.native_rows_perform_
their_operation_in_keystore` forces any row claiming KEYSTORE_NATIVE to declare an
`ANDROID_KEYSTORE` boundary, so a false native claim surfaces at test time rather than at
integration.

**(b) `App.PurgeKeys` clears BOTH tiers, so the push path must re-arm after every lock.**
`mobile/app.go:317` does `st.Keys = crypto.EpochKeys{}`, zeroing the **wake** key along with the
content key. B16 makes high-priority FCM the sole background wake path, and B9 notes
`FirebaseMessagingService` runs in the app process — so a push arriving after a lock purge finds no
wake key in the core, and the Android side must re-install it, **without any authentication**,
before it can open the envelope. An implementation that assumes the wake tier survived the purge
produces a wake path that works until the first screen lock and then goes quiet — silently, and
only on a real device. No requirement stated this and nothing else would have caught it. Pinned by
`LockPurgeTest.the_wake_tier_is_reinstallable_after_a_purge_without_authentication`.

**B18. Three authorisations for S14a's custody seam, decided on the S14a RED author's evidence.**
B14 authorised making `crypto.KeyStore` failable, "signatures only". Implementing it turned up three
things that decision did not anticipate. All three are granted; the reasoning is recorded because
each one widens a frozen package.

**(a) The fence extends to `relay.ClientAuth.Sign`.** `internal/remote/relay/client.go:47` declares
`Sign func(challenge []byte) []byte`, and the ONLY production `SignRelayAuth` call site lives
inside that closure. Inside S14a's original fence the choices were to swallow the error with `_` —
**the exact defect B14 exists to remove** — or return nil and let the relay reject opaquely. Either
way PB-KEY-6's "every signing path" is unmet. `Sign` becomes `func([]byte) ([]byte, error)`: one
declaration, one use, three test fixtures. Granting the smaller widening beats recording a residual
that guts the requirement.

**(b) `crypto.NewKeyStoreFromMaterial(KeyMaterial) KeyStore` is authorised.** `NewFileKeyStore` /
`OpenFileKeyStore` own `device.key` I/O *inside* the frozen package and there is no in-memory
constructor (`internal/phonecore/core.go:104-105` says so). Sealing that file requires phonecore to
own the I/O, which requires exactly this one function. **No semantics change, no new I/O, no new
interface** — it is the existing construction path with the file read lifted out. Strictly beyond
"signatures only", hence recorded here rather than assumed.

**(c) With no sealer, `Resume` FAILS CLOSED.** The alternative — seal anyway with a KEK derived from
something on the same disk — would satisfy the letter of PB-SEC-1's assertion while the property its
own comment states ("worth nothing against ADB backup, a restored image") stays unmet. That is
precisely this project's standing *"requirement satisfiable while the defect ships"* class, which
has now been caught five times, and choosing it here would be choosing it knowingly. Fail-closed
costs a mechanical migration of existing `Resume` callers; any test that genuinely wants unsealed
state must say so **loudly at the call site**, with a sealer whose name contains its own warning —
never by omission. Production must not be able to reach cleartext by forgetting a field.
`android/gate/keycustody_test.go` may be edited to inject sealers: it is the acceptance gate for
half of PB-KEY-9 and its assertions do not move, only its setup.

**The consequence S14 must carry, stated now rather than discovered later.** The Android side
**cannot reach `phonecore.Config`** — gomobile cannot set a Go struct field, and the facade is
golden-pinned (`mobile/testdata/exported_surface.golden`, enforced by `mobile/contract_test.go`)
with no verb to supply a sealer. So the seam is reachable from Go tests and **not from Kotlin**:
the gate tests could go green while the shipped app still writes cleartext. That is the fifth
standing defect class — the fence guarding a path production does not take — one layer up. **S14
must add a facade verb and change the golden**, and PB-KEY-9 is not delivered until it does.

**B19. The wake key MAY reach `internal/remotegw`. It widens the INVENTORY of that package, not
the exposure of the process.** PB-PUSH-0 requires the push trigger's payload to be sealed under
the content-free wake key (PB-KEY-2, A15), and `gatewayParams` carries only `ContentKey` today —
`WakeKey` appears nowhere in `internal/remotegw/`, `cmd/swarm-remote/` or `internal/remote/relay/`
outside tests. I initially framed that as a **new key crossing into the network-facing sidecar**
and asked for a blast-radius analysis before granting it. The analysis falsified the framing, and
the correction is the decision.

The premise is true at the **package** boundary and false at the **process** boundary.
`cmd/swarm-remote/config.go:78` loads the machine identity via `machineid.Load`, and
`machineid.marshal`/`unmarshal` (`internal/remote/machineid/machineid.go:225-283`) write and read
ONE buffer containing the Noise static private, the recipient private, the grant-signing private,
the relay-auth private, **the wake key** and the content key. So the sidecar process already
materialises the wake key at startup, in the same address space; `resolveGatewayParams` simply
drops it on the floor (`config.go:152` takes `id.EpochKeys().ContentKey` alone).

An attacker who compromises that process therefore already holds the **content key** (decrypts all
session content — strictly more sensitive), the **relay-auth private key** (impersonates the
machine), the **grant-signing private key** (mints epoch grants), the recipient key and the Noise
static. What the wake key adds is the ability to forge a content-free wake and to read one — and
they already observe every wake, because the trigger passes through them. It yields nothing about
session content: `crypto.OpenWake` refuses type 0x01 and `MailboxReceiver.Accept` refuses type
0x02, both structurally (F10/A15).

**Granted on that basis.** The honest cost is legibility and refactor discipline — one more secret
named in one more struct — not a new attack surface. Recorded explicitly so a later reader does not
re-open it as though a new key were reaching a new process.

**Two consequences.** First, PB-PUSH-0's test criterion "the gateway holds the wake key only" is
**unimplementable as written**: the gateway MUST hold the content key, because `RelaySink` seals
every journal, terminal and reconcile frame with it and `CommandBridge` opens every phone command
with it. The enforceable form is "**the push path** holds the wake key only", which Go's typed keys
make nearly free — a reflective fence over `PushConfig` rejecting any `crypto.ContentKey` or
`crypto.EpochKeys` field, with a positive control requiring a `crypto.WakeKey` field so an empty
struct cannot pass. §6.0 and PB-PUSH-0 are amended to that wording. Second, ruling the other way
was measured and rejected: it would make the gateway-side trigger unimplementable and push PB-PUSH-0
into the daemon, which would mean the daemon calling the relay — a boundary D5 does not have.

**B20. A push payload MUST NOT carry the mailbox envelope header verbatim. Key ids zeroed, size
constant.** PB-PUSH-3 states the provider observes only **token, timing and size**. That statement
is **false** for the obvious implementation, and the requirement is amended to make the honest
version enforceable rather than aspirational.

`crypto.Envelope.Marshal` emits a **62-byte cleartext header** (`envelope.go:24,153-158`) carrying
version, type, epoch id, seq, **`RecipientKeyID` (8 bytes)**, **`SenderKeyID` (8 bytes)** and
`IssuedAt`. `handlePushTrigger` (`relay/server.go:930`) puts the whole marshalled envelope into the
push payload. Reused verbatim, the provider additionally observes two **stable** endpoint
identifiers — linking every wake to one machine/device pair for the life of the epoch — plus a
monotonic wake counter. That is strictly more than the ADR claims, and D11 (metadata honesty)
forbids claiming less exposure than exists.

**Decision**: the wake envelope's key ids are **zero** on the push path, and the payload is a fixed
**78 bytes** (`headerLen` 62 + a 16-byte AEAD tag over an EMPTY plaintext). Both are pinned by test,
the second because "size" is a benign disclosure only while it is **constant** — a size that varied
with the session name, or with how many transitions were coalesced, would be a covert channel and
the ADR's own honesty claim would quietly become untrue with nothing failing.

**Recorded, not fixed**: PB-PUSH-8's capability gate cannot refuse. `Capability.Allows` admits
`ActionRead` at every tier, so mapping `push_prefs` to `ActionRead` — which is the right tier, since
the verb cannot start, stop or type into anything, and a control-class mapping would leave a
read-only paired phone unable to silence notifications it has no way to stop — yields a check that
can never fail. The **signature** is the load-bearing gate for this verb, and the tests exercise it
failing (forged key, expired command) with a positive control so the refusals are attributable.
Naming this rather than dressing the tier check up as a guard: a guard that cannot fail is this
project's most-repeated defect, and one that is known and documented is not the same object as one
that is believed to work.

**B21. The QR scanner is `com.google.zxing:core` for decoding plus `androidx.camera` (CameraX) for
frame capture. ML Kit is named and rejected.** PB-PAIR-3 requires the scanner choice to be recorded
here rather than discovered in a build file, and PB-SEC-14 is the cost it trades against.

**The property that decides it**: everything shipped is inside the APK the release key signs. No
Play Services, no downloaded model, no dynamic code loading. That is the supply-chain property that
actually matters for a device the threat model assumes an adversary may hold — a signed artifact
whose contents are fully enumerable is auditable in a way that "and then it fetches a model" is not.

- **`com.google.zxing:core`** is pure Java, Apache-2.0, with **zero transitive dependencies** and an
  auditable decoder.
- **`androidx.camera`** is a first-party AndroidX artifact from the same repository this module
  already trusts for `appcompat`, so it widens the trust set by one auditable library rather than by
  a vendor.
- It also works on **de-Googled and AOSP handsets**, which is not incidental here: PB-OPS-1's LAN
  demonstration describes exactly the self-hosted deployment those users choose. A scanner that
  requires Play Services would make the flagship demonstration impossible on the devices most likely
  to want it.

**Rejected, and why, so this is not re-opened as an oversight:**

- **`com.google.mlkit:barcode-scanning` (bundled)** decodes measurably better on damaged, small or
  badly-lit codes. It costs a **closed native blob** on the security-sensitive surface — reported as
  roughly 2.4 MB by the implementer, **a figure neither of us measured**, so treat the magnitude as
  indicative and the kind as the decision: closed, vendor-supplied, on the surface this project is
  most careful about. Traded off, not dismissed: the decode advantage is real.

  **One open question deliberately NOT asserted here.** The bundled variant is usually presented as
  the Play-Services-free option, and the implementer's understanding is that it still resolves
  `play-services-basement` transitively through `com.google.mlkit:common` for the Tasks API — which,
  if true, means the Play Services surface arrives by the very route chosen to avoid it and would
  make this rejection unambiguous rather than a trade. **It could not be resolved offline and is
  recorded as unverified rather than cited as fact.** Anyone revisiting this decision should check
  the POM first; it would strengthen the conclusion, and a conclusion resting on an unchecked
  transitive closure is exactly what this ADR should not contain.
- **`com.google.android.gms:play-services-mlkit-barcode-scanning`** is rejected **outright** rather
  than traded off: it executes code the APK does not contain and the release signature does not
  cover. That is disqualifying independent of its merits.
- **`com.journeyapps:zxing-android-embedded`** is a third-party wrapper carrying its own Activity and
  camera stack — more surface than the decoder we need, from a smaller maintainer, to save wiring we
  are writing anyway.

**The accepted cost, stated rather than waved through**: ZXing has a weaker decoder and no auto-zoom.
Two things make that acceptable in this product specifically. The QR is rendered **on a screen about
a metre away**, not printed on a crumpled surface, which is the case ML Kit's advantage is largest
for. And PB-PAIR-2 already requires a **manual-entry fallback** — the same QR payload, typed — for
the denied-camera path, which doubles as the fallback for a code that will not decode. The failure
mode is therefore a slower pairing, not a blocked one.

**Ordering note, because the fence is bidirectional**: `TestPBPAIR3_TheScannerChoiceIsRecordedInTheADR`
fails both when the ADR names no library and when a scanner dependency appears in
`android/app/build.gradle.kts` that the ADR does not name. So this entry lands **before** the
dependency, not alongside it.

**B22. `authorize_device` CLEARS the relay's revoked bit, and the CLI issues the relay purge itself.
Without this, "fail closed" and "re-pair" are mutually exclusive and PB-STATE-10 is unsatisfiable.**

Found by the S18b RED author while testing the recovery flow PB-STATE-10 requires, and verified: the
only relay operation that purges a mailbox is `device_revoke`, and `store.revokeAndPurge` writes the
routing id into a `revoked` bucket **in the same transaction**. Nothing anywhere clears that bucket —
grep confirms two writers, one reader, **no delete** — and the relay's auth path refuses a revoked
routing id outright. The phone's relay-auth key lives in `device.key` and is minted **once per
install**, so a recovered handset returns on the same routing id and is **permanently locked out of
the relay**.

That makes the error taxonomy's own shipped `REVOKED -> re_pair` row false today, and it means the
owner-side recovery PB-STATE-10 mandates — revoke, purge, re-pair — cannot complete. Revoking to
unstick a phone bricks it harder, which is precisely the failure this requirement exists to forbid.

**Decision: `handleAuthorizeDevice` clears the revoked bit for the routing id it authorizes.**

The safety argument, which is why this is not a weakening: `authorize_device` is served only on an
**authenticated** connection, and a revoked device cannot authenticate — the auth path refuses it
before any op is dispatched. So the only party who can issue it is the machine, over its own
authenticated connection. **The machine choosing to authorize a routing id IS the owner's decision to
un-ban it**; there is no path by which a revoked device un-bans itself, and the relay gains no new
authority it did not already have. A ban that only the owner can lift, through the same verb that
grants access in the first place, is the correct shape.

**Rejected alternative**: rotating the phone's relay-auth key on every pairing. It would also work,
but it changes PB-KEY and PB-PAIR assumptions about a device identity that is deliberately stable
across an install, and it **orphans the old routing id's relay state anyway** — so the purge is still
owed and the cost is paid twice.

**Consequence for the CLI, and it is a new responsibility rather than a wiring change.** Neither the
CLI nor the daemon holds a relay connection — only the gateway sidecar does, and `swarm remote
revoke` **stops the gateway** as part of its own flow. So the relay purge must be issued by the CLI
dialling the relay directly with the machine identity (`machineid.RelayAuthPublic` /
`RelayAuthSign` plus `relay.json`). The RED author demonstrated this composes.

**Also required by the same finding, and separately unowned**: `relay.Client.DeviceRevoke` has **no
production caller at all** — the capability ships as a function nobody invokes, which is this
project's standing "requirement satisfiable while the defect ships" class. And the machine's own
outbox (`<stateDir>/remote/outbound-journal.outbox`) holds reserved-but-uncommitted entries as
**exact sealed bytes, replayed verbatim by contract**; the revoke rotates the epoch, so those
envelopes are sealed under a key no future device holds and the next gateway replays them into the
re-paired phone's mailbox where nothing can open them. Revoke must purge both.

**B23. Two consequences of B22 that B22 did not name, both found by the fence rather than by
reasoning, and both real production bricks.**

B22 decided that authorizing a device clears the relay's ban. Getting the recovery to actually
complete needed two more changes, and neither is a test artifact — each is a way the recovered
handset stayed dead.

**(a) `swarm remote pair` authorizes the new device at the relay itself.** B22 assigned only the
*purge* to the CLI, on the reasoning that `authorize_device` already has a production caller. It
does — **the gateway** — and `swarm remote revoke` **stops the gateway** as part of its own flow. So
after a revoke nothing clears the ban until the supervisor gets round to restarting the sidecar.
Meanwhile `relay.ErrRevoked` is **terminal** on the phone: the dial loop returns rather than
retrying. The recovered handset therefore latches its revoked state *before* the gateway boots, and
stays there. Pairing is the moment the owner grants access, so it is where the grant is now made;
the gateway's own call remains and is idempotent.

**(b) The phone re-arms a transport that a revocation killed, when a pairing pins a destination.**
The terminal revoked state is latched at `Start()` — *before* the pairing that recovers it — so
without this the handset shows revoked until the Android process is rebuilt, which on a phone can be
hours. A bounded 30-second grace covers the genuinely unorderable race between the phone's first
post-pairing dial and the machine's authorize.

**The property that keeps (b) honest**, CORRECTED against measurement — the original wording said
the state "stays revoked throughout the grace window", and that is not what the code does. The
re-armed generation opens with `first=true`, so `connecting` is set before the first dial resolves.
The measured sequence on the event plane is:

- **generation 1 (stranded)**: `connecting` -> `revoked` (run returns; `setConn(offline)` is never reached)
- **generation 2 (after re-arm)**: `connecting` -> `revoked` -> `online`

After that first refusal the state **is** `revoked` and is held across every retry — `run`'s guard
skips the `reconnecting` overwrite — so the *held* state is genuinely revoked, just not from the
first instant. **The property that actually matters is that `reconnecting` never appears**, because
that, not `connecting`, is the failure loop PB-APP-10 forbids. Nothing hides behind a spinner. A
generation still *running* is left alone, so the ordinary case of an owner revoking an online phone
stays terminal, as its test still asserts.

The correction is recorded rather than quietly reworded: the original claim was stronger than the
code, it was never false in a way a user would see, and it would have read as verified in the audit.
A test now pins the exact post-pairing triple, so the wording is checked rather than asserted.

**Also accepted here: a second error class, `swarm/device-unsupported`.** Giving the
key-reprovisioning recovery its own route needs a destination, and every existing no-user-action row
maps to INTERNAL — which would tell a user "the app hit an internal fault" about a handset whose
platform silently downgraded a Keystore key. The app is healthy; one thing outside it is not. This
follows the existing precedent of a row sharing INTERNAL's remedy while carrying a different state
for a different cause.

**Its wart, recorded rather than smoothed over**: the Go facade exports a class it never stamps —
the condition is produced only on the Android side. That is structural rather than accidental, since
the taxonomy is a closed set checked for equality across the golden, the table and the Kotlin enum,
so a class that exists in one must exist in all three. Noted at the constant and in the table.

**B24. CORRECTION to B22: its safety argument was FALSE as written, and the fix is a policy the
code did not have.** An independent reviewer falsified it and I verified the falsification.

B22 asserted: *"there is no path by which a revoked device un-bans itself"*, resting on the claim
that `authorize_device` is served only on an authenticated connection and a revoked device cannot
authenticate. **The second half is true and the conclusion does not follow.** Relay auth is
**open registration** — `handleAuthInit` accepts any self-minted keypair — and `handleAuthorizeDevice`
checks only `requireAuth()`, with **no ownership or role check at all**. So "authenticated" does not
mean "the owner's machine".

The concrete path: a revoked device mints a throwaway identity, authenticates as it, and calls
`authorize_device` naming its **own** revoked key. `authorizePair` deletes the ban in the same
transaction. The device then re-authenticates as itself and can pair and append again.

**Bounds, stated honestly rather than as mitigation**: end-to-end crypto is intact — the revoked
device cannot open new-epoch frames, and its commands still fail the registry signature check. The
reachable harm is relay-plane: appends against the machine's mailbox up to the per-target depth cap,
i.e. a denial of service against the legitimate phone's appends. The **openness itself pre-dates**
this work; any identity could always self-pair with any target. What B22 added was making that same
open verb clear bans, and then asserting a property the code does not have.

**The fix**: `bucketRevoked` stores the **banning** routing id as its value, and `authorizePair`
clears a ban only when the pairer matches the banner. That is a one-line policy which keeps B22's
semantics — the owner's machine lifts the ban it placed — and makes its argument true rather than
aspirational.

**A mirror hole, pre-existing and now recorded**: `handleDeviceRevoke` likewise lets any
authenticated routing id ban **any** routing id, including the machine's. It is out of this slice's
scope, but it is the same missing check and it should not be discovered a third time.

**The lesson worth keeping**, because I made this error: a security argument that names a mechanism
("only an authenticated connection can issue it") must be checked against **what that mechanism
actually gates**. Authentication proves identity, not authority. I wrote the argument, it read as
sound, and it was wrong at the join between two true statements.

### B25 — B24 escalated the mirror hole from transient to permanent, and that is on this ADR

B24's fix is correct and it made a **second, recorded hole strictly worse**. The remediation agent
found this in its own change and reported it rather than shipping quietly. It is the more serious
finding of the two, and it exists because of a decision made here.

**The attack, verified in the tree.** `handleAuthorizeDevice` (`internal/remote/relay/server.go`)
calls `authorizePair(sc.rid, deviceRID)`: it creates a pairing between the **caller's** routing id
and **any** routing id the caller names. `requireAuth` gates it, and relay registration is open, so
that gate proves identity and **nothing about authority**. `handleDeviceRevoke` then gates on
exactly that pairing. So any party mints a throwaway keypair, self-pairs with the machine's routing
id, and revokes the machine — `revokeAndPurge` bans it and destroys its relay state.

**What B24 changed.** Before it, every `authorize_device` cleared every ban, and the phone's
`onConnected` authorizes the machine on each reconnect (`mobile/relay.go`) — so an attacker-placed
ban on the machine **self-healed at the next reconnect**. After B24 only the banner may clear, and
the banner is the attacker. The ban is now **permanent**: the machine's routing id cannot be
un-banned by any party the owner controls.

**Why this is not covered by "the relay is untrusted".** The threat model concedes availability *to
the relay operator* — a hostile relay can always refuse to route. It does not concede that **any
anonymous internet party** can permanently destroy a machine's identity at an honest relay. Those
are different adversaries with different costs, and collapsing them would excuse the bug rather than
answer it. Confidentiality and integrity are untouched: no content is readable and no command is
forgeable. This is availability, permanent, and remotely reachable by anyone.

**The fix direction: pairing must be mutual.** The root cause is that a pairing is created
**unilaterally** by one side naming the other. Making `isPaired` require an authorization in **both
directions** removes the whole class: an attacker's self-pair is a one-sided intent, and the machine
never authorizes a throwaway rid, so the pair never forms and neither revoke nor append is reachable.
This must be checked against the real flow before it is implemented — both legs plausibly already
occur (the machine authorizes the device when pairing; the phone authorizes the machine on
reconnect), but "plausibly" is exactly the word that produced B22. **It is not to be implemented off
the back of this entry**; it needs its own RED proving the attack, then the change.

**The generalisation, which is the reason this entry is long.** B24 was a *narrowing* — it made a
permissive rule stricter, which is the safe direction by every instinct. It still caused a
regression, because a second defect was **relying on the permissiveness to heal itself**. Tightening
one rule can convert another bug from self-correcting to permanent, and neither rule looks wrong
alone. When a fix narrows a policy, the question is not only "what does this now forbid" but "what
was silently depending on what it used to allow".

### B26 — B25's fix direction was falsified by measurement; the ban is scoped to the pair instead

The B25 RED author was asked to check the recorded fix direction against the real flow before
anyone implemented it, and it does not survive. **The direction was mine and it was wrong in its
justification**, which is the second time on this defect that a plausible argument was accepted
without being run.

**What was falsified.** B25 said mutual pairing was safe because "both legs plausibly already
occur". Both legs do exist — but **not in the required order**. `deliverEpochGrant`
(`cmd/swarm-remote/deliver.go:29`) authorizes the device and **immediately appends the sealed epoch
grant**, and that append is what delivers the ContentKey, i.e. what makes a pairing usable at all.
Its failure is **fatal** (`cmd/swarm-remote/main.go:64`). On a first pairing, `swarm remote pair`
boots the gateway before the phone has necessarily ever connected, so under a drop-in mutual
`isPaired` the grant delivery is refused and pairing cannot complete. Verified empirically with a
reverted experiment: `TestDeliverEpochGrant_AuthorizesAndAppendsBootstrap` fails at that line with
`not authorized for route` — **production code, not a fixture**. The one device->machine authorize
site is `mobile/relay.go:283` inside `onConnected`; `internal/phonesim` never issues the leg at all.

**Re-examining what the global ban actually buys.** Forced back to the question, the answer is
uncomfortable: **very little.** Relay registration is open, so a revoked handset can mint a fresh
keypair and register again — the ban stops the *same identity*, which is the one thing an attacker
trivially sidesteps and an honest owner never needs. What it does buy is real but narrow: it is how
a revoked phone **learns** it was revoked (`ErrRevoked` at registration), which PB-APP-10 requires
as an explicit re-pair prompt rather than a failure loop. So the ban is carrying a **signalling**
job on a **global-authority** mechanism, and the mismatch is the vulnerability.

**The direction, revised: scope the ban to the (banner, banned) PAIR rather than to the banned
party's registration.** A revoke severs that relationship and purges its mailbox; it does not touch
the target's ability to register or to talk to anyone else.

- The stranger's attack becomes a **no-op**: it can only sever a relationship it invented, and the
  machine's registration is untouched. The permanent DoS disappears without needing mutuality.
- **Bootstrap is unaffected**, because grant delivery is an append and one-sided authorization still
  permits it. This is the constraint that killed the previous direction.
- **The revoked signal survives**, and improves: the phone learns it was revoked when it tries to
  reach *its pinned machine*, which is both the moment it matters and strictly more informative than
  a bare registration refusal.
- **B22 and B24 dissolve into it.** A ban keyed by its banner *is* the ownership check B24 added by
  hand, so the special case stops being a special case.

**Separately, and not optional: the mailbox depth cap must be accounted per (source, target).** It
is enforced per *target* today (`internal/remote/relay/server.go:808`,
`mailboxDepth(req.Target) >= capN`), shared across every sender, so any authorized sender can starve
the legitimate phone. The RED measured this: after a drain and ack the refusal lifted, proving it is
the depth cap and not a rate window, so the flood is **sustainable** rather than one-shot. Scoping
the ban does not fix this; it is an independent defect behind the same gate.

**This entry is a direction, not a decree.** It has now been wrong once about this exact code. The
implementer is required to test it against the bootstrap path and the revoked-signal path **before**
building on it, and to report a contradiction rather than route around it — which is precisely how
this entry came to exist.

### B27 — the authority rule is about the TARGET, not the caller (B26 falsified too)

B26 was prototyped and **does not make the RED tests pass**. Two directions have now been recorded
here and both were wrong; this entry records the third, which is **measured rather than argued** —
the property the first two lacked.

**Where B26 was wrong, on its own terms.** It claimed the stranger's attack "becomes a no-op: it can
only sever a relationship it invented". False. `revokeAndPurge` also **deletes the target's entire
mailbox and its push token**, and both are keyed **per target**, not per pair. Scoping the ban does
not scope the purge. Under B26 an anonymous party could still destroy every undelivered frame in the
machine's mailbox and silence its push, repeatedly, on demand. Only the *permanence* was removed.
The entry reasoned about the ban and never checked what else the same function does.

**Why both directions failed — the structural fact neither entry saw.** At the relay, bootstrap and
the attack are **the same shape**: *C authorizes D, then C appends to D*. Machine->phone and
stranger->machine are **indistinguishable by any predicate over the pairs bucket**, because the thing
that distinguishes them — the QR/SAS ceremony — **never reaches the relay**. Any fix must therefore
either carry the target's consent to the relay out of band, or find an asymmetry that is not the
caller's own say-so. Both B25 and B26 looked for the asymmetry in the caller, where it does not exist.

**The asymmetry is in the target.** The stranger's target is an **established** identity that has
already authorized somebody. Bootstrap's target has authorized **nobody**. One rule follows, and it
replaces `isPaired(caller, target)` at every gate:

> **You may act on a target's route if the target has authorized you — or if the target has
> authorized nobody at all.**

- `authorize_device` records a **directed intent** (`pairer -> device`) rather than an undirected
  edge written both ways. A one-sided authorize forms no pairing.
- `isPaired(a, b)` becomes mutual — true only with both intents — so a stranger's say-so no longer
  makes it true in either direction.
- `mayActOn(source, target)` = `granted(target, source) || (granted(source, target) && target has
  granted nobody)`, gating `mailbox_append`, `push_trigger` and `device_revoke` uniformly.

**Measured**: all three RED tests pass and the rest of the suite passes with **zero fixture edits** —
a strong signal the rule matches the system's real semantics rather than being imposed on it. The
bootstrap path, the revoked signal (`ErrRevoked` at registration, kept), PB-STATE-10's grace window
and B22/B23/B24 are all untouched, and B24's ownership check remains correct as written.

**The residual, accepted here with its reason.** The first-use clause is trust-on-first-use: a party
that knows a **never-yet-paired** identity's relay-auth pubkey can act on it before it authorizes
anyone. Two bounds make this acceptable. First, that pubkey is disclosed only at the relay handshake
and over the SAS-authenticated pairing channel, so in practice the window is reachable by **the relay
operator** — and **ADR-007 already concedes availability to the relay operator**, who can deny service
to an unpaired machine simply by not routing. **The residual therefore grants no capability to a party
that does not already have a strictly greater one**, which is the precise line B25 drew: not *any
anonymous party*, which was the real defect. Second, the window closes **permanently** once an
identity has either granted or banned anyone, so a revoke never re-opens a machine's window, and a
previously-paired (or revoked) device is outside it by construction.

**The stronger fix, recorded and NOT taken.** Carrying the phone's **consent signature** — its
relay-auth key over the machine's routing id, obtained during the SAS ceremony — into
`authorize_device` for the relay to verify would close the window completely and needs no crypto
change. It touches the pairing message format (spec), `mobile`, `phonesim` and the device record: a
substantially larger slice, and at this stage a larger risk than the residual it removes. **If the
threat model ever stops conceding availability to the relay operator, this becomes required rather
than optional.**

**The lesson, and it is the same one twice.** B25 asserted a security property from a mechanism's
name; B26 reasoned about one effect of a function and missed another in the same body. Both read as
sound and both were checkable in minutes. **An architectural direction recorded without being run is
a hypothesis wearing a decision's clothes** — and this one cost two rounds because I wrote it that
way twice.

### B28 — the relay item record gains a version byte; in-place upgrades fail closed

B27's per-sender depth accounting needs the sender's identity **in the stored record**, so the
mailbox item layout changes from `[8 time][envelope]` to `[1 version][8 time][32 sender rid][envelope]`.
This is a **persisted-format change on a deployed component**, which is a decision rather than an
implementation detail, and it is recorded because the failure mode is silent and operator-facing.

**What a naive upgrade would do.** A relay upgraded in place over an existing store holds
pre-version records. Read as the new layout, one would yield **32 bytes of ciphertext served as a
routing id** and a truncated envelope served as the frame. The frame is undecodable, and **the
phone's drain never advances past a frame it cannot open** — so a single legacy record stalls that
mailbox for its entire retention window. The phone would show no new sessions and no error worth
acting on, and the relay would report itself healthy.

**The decision.** The version byte makes the two layouts distinguishable — an old record begins with
a millisecond timestamp's top byte, which is `0x00` for the rest of this millennium — and the store
**fails closed on any record it did not itself write**: skipped, never served. A skipped record is a
lost frame, which the receive path already tolerates (the phone reseeds), whereas a misread record is
a wedged mailbox. Fenced by a test that serves a legacy record and fails if it is misread rather than
skipped.

**Recorded because the implementer flagged it rather than leaving it in a commit message.** A
storage-format change whose failure mode is "the product silently stops working for one user until a
retention window expires" is exactly the class of decision that must be findable by whoever
eventually runs the upgrade.

### B29 — the append/push RATE windows share the same defect; accepted for v1, with the reason

`appendRate[req.Target]` and `pushRate[req.Target]` (`internal/remote/relay/server.go`) are keyed
**per target and shared across senders** — the identical "shared resource with no owner" shape as
the depth cap B27 fixed, five lines from it. An authorized sender can burn a target's whole
`MailboxAppendPerMin` and the legitimate phone gets `ErrQuotaExceeded` until the window rolls.

**Reach after B27, which is why this is acceptable rather than urgent.** You must be authorized by
the target to append at all, so in single-device v1 the only party who can do this is the owner's own
phone. The exception is a fresh target's **first-use window**: a stranger holding a new phone's
relay-auth pubkey could burn its append budget and delay the epoch grant by up to a minute. That is
**transient and self-healing** — the window rolls, the gateway retries — where the B25 defect it
shares a shape with was permanent.

**Why it is NOT fixed here, which is the part worth recording.** The obvious fix — key the rate
window per `(source, target)` — **mints map entries under attacker-chosen keys**, which is precisely
the unbounded per-key state the auth path already refuses by design: `handleAuthInit`'s R1-H1/H2
remediation meters by **transport source** specifically so that a victim identity's budget cannot be
exhausted and no per-key state is minted. Per-sender *depth* is free of that hazard because it is
**derived from stored items** rather than kept in a map; per-sender *rate* has no such backing store.
So the fix needs a bounded or swept map, and that is a design decision rather than a line change —
one that would trade a fairness bug for a memory-exhaustion bug if taken carelessly.

**Decision: accepted as a residual for single-device v1.** The trigger for revisiting is explicit:
**multi-device support, or any change that widens who may append to a target** — the relay code
already supports more than one device, so this is a "when", not an "if". Whoever takes it must not
key an unbounded map by attacker-chosen keys; that constraint is the whole content of this entry.

**The general result behind B27 and this entry**, argued rather than asserted, and recorded at the cap
site in code: no quota policy can simultaneously bound a target's storage and guarantee fairness
between senders while the sender set is unbounded. With a finite total ceiling, `k` minted identities
divide it and starve the legitimate sender — eviction is not available, because the read/ack cursor
protocol requires a stored item eventually reach its reader. With no total ceiling, storage grows
per identity. The only remaining lever is not a quota at all: **bound the sender set**, which is what
the authority rule does. The two defects were filed as independent and **one of them was only ever
fixable second** — the depth fix alone would have *measured* as a fix, because the flood test would
have stopped showing the phone refused while the mailbox grew per identity with no test watching.

### B30 — `Core.Save` is a deliberate whole-blob adopt; `Core.Mutate` is the field-update verb

A lost-update defect in `phonecore.Core.Save`, found by an agent investigating a flake and fixed by
another. **Both the reported mechanism and my brief restating it were wrong**, and the correction
matters more than the fix, because the wrong mechanism was plausible enough to survive two retellings.

**What was claimed**: seven facade sites read the state, work with the core lock released, then
`Save` it back; a rolled-back `SendSeq` re-issues a consumed seq and every later frame is rejected
with `ErrStaleSeq` forever.

**Why that is false.** `fileStore.Save` already calls `mergeGuards` (`internal/phonecore/state.go`),
which raises **every replay-guard coordinate monotonically** — `SendSeq`, `Receive`,
`GrantEpoch`/`GrantSeq`, `WakeReplay`, `RelayCursor`. Durable custody refuses to rewind them. Five of
the seven fields named were already protected, and the `ErrStaleSeq` brick is **unreachable**. (They
are unprotected only behind an injected in-memory store, which is test wiring; production always goes
through `OpenStore` — so the hazard was demonstrable only in a configuration production never runs.)

**The real mechanism, which is worse.** What *is* adopted blindly is **`EpochID` and `Keys`**.
`App.pin` zeroes `State.Keys` when a pairing lands in a new epoch — correct against its own snapshot
— and destroys the content key the concurrent drain just installed; `resealTier` then writes no
content-key field at all, so the destruction reaches disk. Meanwhile the grant watermark **was**
merged monotonically, so it stands at the coordinates of the grant whose key was just destroyed, and
`crypto.GrantReceiver` enforces strict `(epoch, seq)` monotonicity. **The gateway re-appending the
same bootstrap frame is therefore refused as a replay forever.** The handset holds no content key,
cannot obtain one, and the only exit is a machine-side re-grant at a higher seq. Silent, and
unrecoverable from the phone — the original verdict, reached by a different route.

**The decision.** `Core.Mutate(fn func(*State))` — clone under `c.mu`, apply, persist, unlock,
rebind — is the verb for changing a field; all seven sites are converted. **`Save` remains a
whole-blob adopt on purpose**, for reseed, restore and fixture, and that split is now load-bearing
and enforced by an AST fence over every non-test importer. A `PinMachine`-style alternative was
evaluated and rejected: it needs a bespoke core verb per hazard and another the next time the facade
gains a field, where `Mutate` is one seam.

**A second defect fell out of the same change**: `SetPushPreference` drew its PB-PUSH-10 version
**outside** the lock, so two toggles both read N and both claimed N+1 — and the machine refuses
anything not strictly exceeding what it holds, so the second was silently dropped while the settings
screen displayed its value. Now atomic.

**Not to be confused with B29.** That entry concerns the **relay's** append/push rate windows keyed
per target, a shared-resource-with-no-owner defect on a different component with a different fix and
a different trigger. This is a **phone-side** lost update. They are not the same class and closing
one says nothing about the other; recorded because the implementer reasonably asked whether they were.

**The lesson.** The brief I wrote asserted a failure chain I had not run, taken from a report that
had not run it either, and it survived because it was *coherent* — a rolled-back counter causing
stale-seq rejection is exactly what one would expect. It was falsified in minutes by someone who
printed the values. **A mechanism nobody has executed is a story, however many people have repeated
it**, and this is the fourth time in this ADR that a plausible chain proved wrong under measurement.

### B31 — PB-E2E-5 (physical-handset gate): the exclusion, approved here and scoped by enumeration

GG-3 permits a requirement to go untested only via **an exclusion approved by ADR or audit waiver,
never a bare "documented justification"**, and E15.5 restates it. PB-E2E-5's deferral has until now
lived only in its own requirement row, which is the bare justification GG-3 rejects. This entry is
the approval, and it is deliberately written as an **enumeration of what is unverified** rather than
a statement that the deferral is acceptable — a reader must be able to see the size of the hole
without reconstructing it.

**Why it cannot run.** There is no physical handset attached to this machine. Everything Phase B
demonstrates on an emulator or in Go is, for the properties below, evidence about a model.

**Unverified, in full. None of the following has been observed on real hardware:**

- **Hardware-backed Keystore.** Every custody claim is asserted against a software or emulator
  provider. `KeyInfo`/attestation has never confirmed hardware backing for any of
  `{NoiseStatic, Recipient, CommandSign, RelayAuth}`, and PB-KEY-8's per-role backing matrix is
  therefore a design, not a measurement. **Curve25519 entered KeyMint only in Android 13 and hardware
  backing is device-dependent**, so the fallback paths are the ones most likely to be taken and least
  likely to have been exercised.
- **Real biometrics.** No BiometricPrompt has ever run: not the success path, not user cancel, not a
  re-enrolled fingerprint invalidating a key. The two-tier key design (ADR-007 D2/A15) rests on the
  content tier being biometric-gated, and that gating is modelled everywhere it is tested.
- **Real camera pairing.** QR capture is exercised against synthetic input. ZXing + CameraX (B21) has
  never decoded a physical screen under real optics or lighting.
- **Real FCM.** No registration, no delivery, no token rotation observed against Google's service.
  This build **configures no Firebase project by design**, so the push transport has never moved a
  byte end to end. High-priority wake from **Doze**, notification behaviour after reboot, and
  locked-device push handling are all unobserved.
- **Reboot, lock/unlock, and radio handoff** on real hardware, including Wi-Fi <-> cellular.

**What is NOT excluded, so the hole is not read as larger than it is.** `am force-stop` mid-session
was deliberately moved *out* of this gate into PB-E2E-2 because an emulator can issue it, and it is
strictly stronger than a plain process kill (it also puts the package in the STOPPED state, so no
implicit broadcast reaches the app until a manual launch). The protocol, crypto, durable-state,
relay, gateway and daemon paths are demonstrated end to end by PB-E2E-1 with no simulator seam.

**The standing prohibition this entry enforces.** Nothing in this repository may claim or imply that
any of the enumerated items is covered. **An emulator is not a handset**, and an artifact produced in
one may not be cited as evidence about one. This has been a live instruction to every agent this
phase, and no substitute has been accepted — the S19 RED author declined to write an instrumented
test that would have *read* as PB-E2E-2 coverage while the product remained unpairable from a phone,
which is the behaviour this entry ratifies.

**Consequence, stated as the requirement states it: until this gate runs, Phase B is "provisionally
implemented", not done.** The audit may find every other requirement met and must still record Phase
B as provisional. An exclusion that let the phase be called complete would be a waiver of the
conclusion rather than of the test.

**Owner and runbook.** The gate needs a named human owner with a device — it is not assignable to an
agent — and a runbook written in advance, with **every step marked unrun**. The runbook is owed by
S20; writing it now is what makes the deferral checkable later rather than a promise.

### B32 — Metadata disclosure, restated because push persistence added to it

D11 documents what the relay and the push provider observe, and forbids claiming less exposure than
exists. Two things changed in Phase B and the disclosure moves with them.

**The relay's at-rest footprint gained a durable device identifier.** PB-PUSH-6 made push tokens
survive a relay restart, so `bucketTokens` holds a provider-issued, long-lived, device-specific
identifier **in the clear** — it cannot be encrypted, because the relay must hand it to the provider.
It is keyed by routing id, the same key that indexes the mailbox, so it is directly correlatable with
that handset's message cadence and presence history. It is also **the same identifier the push
provider holds**, which makes it a **join key between two parties whose views are otherwise
disjoint** — the sharpest way to put it, and the reason this entry exists rather than a line in a
runbook. Deletion is as durable as registration (same transaction as the revocation), and it lives in
its own named bucket rather than in the item log so an operator can audit every device identifier in
one place. That is an **auditability** property, not a confidentiality one, and must not be cited as
the latter.

**The provider's view is now what PB-PUSH-3 claims, because B20 made it so**: key ids zeroed, a
constant 78-byte payload, an empty plaintext. What an empty payload does **not** fix: a token plus
wake timing is an **activity trace**. The content is hidden; the rhythm is not. D11's honesty rule
requires saying so rather than resting on the payload being empty.

The operator-facing form is `docs/operations/metadata-disclosure.md`; the two must not diverge.

### B33 — The relay pin is over the SPKI, and key reuse is part of the pin

`Security.PinnedCert` compares the full leaf DER. Android's trust-root source is `TrustRootsPinned`,
so on that platform the pin is the whole of relay TLS verification, and a reissue — which changes the
serial and validity window even when nothing else does — takes every paired handset offline. On the
Let's Encrypt cadence that is every 60-90 days.

**Decision**: `Security` gains `PinnedSPKISHA256`, SHA-256 over the presented certificate's
`RawSubjectPublicKeyInfo`. Either pin alone admits the peer; both may be set. The security level is
unchanged — the digest admits exactly one public key — and a malformed pin is refused **before the
dial** with `ErrPinMalformed` rather than inside `VerifyPeerCertificate`.

**The requirement's own claim is wrong as written and is amended rather than repeated.** PB-OPS-5
said pinning the SPKI "survives renewal at the same security level". That holds **only if the renewal
reuses the key**, and certbot generates a fresh keypair per renewal by default — a fresh key is a
fresh SPKI, which breaks an SPKI pin on exactly the cadence it was adopted to survive. An SPKI pin is
a **necessary half, not the fix**; the operator must also configure key reuse. The S20 implementer
refused to restate the claim unqualified and pinned it in the opposite direction:
`TestPBOPS5_SPKIPinIsBrokenByARenewalThatROTATESTheKey` **asserts the pin failing** on a rekeyed
renewal, so the key-reuse step cannot be silently dropped later.

**Not closed by this entry** — see B34.

### B34 — The transport-security policy has no production caller (PB-NET-2)

The most serious finding of the closure slice, and it belongs to no requirement that slice owned.

`relay.Security` — the certificate pin, the cleartext refusal, the redirect re-check — is applied
**only** by `relay.DialSecure`, and **no non-test file in the repository constructs a `relay.Security`
at all**. Production dials go through `relay.Dial` (`mobile/relay.go`, `cmd/swarm-remote/main.go`) or
`relay.DialRaw` (`mobile/pairing.go`).

**Consequence**: the handset applies **no transport policy**. A `ws://` URL arriving in a pairing QR
would run the session in cleartext with nothing refusing it, and `ErrPinRequired` — the entire point
of `TrustRootsPinned` on Android — is raised inside a function the app never reaches.
`internal/remote/transport/tls_test.go` is green and guards the unreached path. This is the standing
defect class of this phase — *a fence guarding a path production does not take* — applied to the
phase's transport-security requirement.

**It also relocates B33's premise.** The brief for that work said the leaf-DER pin breaks handsets on
every renewal. It would, if it were reached; the live defect is not renewal fragility but the absence
of any policy. The SPKI pin is right, cheap and prerequisite, and it is **not yet on the path a phone
takes**.

**Not a one-line fix, which is why it is recorded rather than patched at closure.** Carrying a pin to
the handset has no channel: the pairing QR has no pin field, and `MaxRelayURLLen = 39` exists because
the symbol must remain scannable at 80x24 in QR version 6 — a 32-byte pin is ~43 base64 characters
and pushes the symbol to version 7. So this needs a decision about the pairing payload's size budget,
not merely a call-site change.

**Owner required before any deployment where the relay is not the operator's own trusted host.**
Recorded here, in `docs/verification/remote-phaseB-residuals.md`, and in both operator runbooks,
because a security control that exists, is tested, and is never invoked is indistinguishable from an
absent one at runtime, and distinguishable from it only by a reader who checks call sites.

### B35 — B17(b) is FALSE; PB-KEY-7 is unwired AND unimplementable as specified

The investigation into the seventh uncalled-symbol instance returned **both** answers, and the split
is the finding. `KeyCustodySession` was written as one class holding two halves with opposite fates.

**The INSTALL half is SUPERSEDED, correctly.** Epoch keys reach `State.Keys` entirely inside Go —
`App.Start` -> drain -> `AcceptCommit` -> `acceptBootstrap` -> `Core.installGrant` — opened under the
recipient key in `device.key`. That is **PB-KEY-10's fix (S10)**, and its own headline test is named
`TestPBKEY10_AFreshPairingObtainsTheEpochKeyWithoutAnyInstallCall`. The Kotlin install path is dead at
**both** ends: no production `CoreKeyCustody` implementation, and **all 22 `CustodyBlobs.tierKey`
references are under `src/test/`** — so even a wired adapter would have thrown on first call. A
one-line wire-up would have produced a crash, not a feature.

**B8 still HOLDS on the live path.** One artifact crosses — `KeyCustody.WakeKEK()/ContentKEK()`,
reverse-bound so the result is inbound — and the epoch keys crossing *less* is a **narrowing**, which
B8 explicitly permits. No bound `App` method returns `[]byte`; both directions remain fenced.

**B17(b) is now FALSE and is hereby STRUCK — see B44, which supersedes it entirely.** It states that after a purge "the Android side must re-install it, without any
authentication, before it can open the envelope". **The Android side no longer has any source for
those bytes.** The same false claim is repeated in code at `mobile/app.go` on `InstallContentKey`
("PB-KEY-7's recovery path... so the first screen lock does not brick the app").

**PB-KEY-7 is not achieved, and wiring it as specified would make the product worse.** There is no
trigger anywhere — no `ProcessLifecycleOwner`, no `ACTION_SCREEN_OFF`, no `onStop`/`onTrimMemory`;
`PhoneActivity.onPause` reaches no custody verb. And `dropKeyMaterial` (`internal/phonecore/state.go`)
clears `Keys` while leaving `GrantEpoch`/`GrantSeq` standing, so re-delivery of the same grant is
refused as a replay — **only a seq-advancing re-grant recovers**, as S10's own evidence states.
**Wired today, the first screen lock would destroy both epoch keys with no on-device recovery and
land the phone in PB-KEY-3's terminal state** — the exact outcome the code comment claims it prevents.

**So this is the one instance of the class where wiring the symbol would have been the wrong fix**,
and it is the reason the investigation was worth running instead of patching.

**What went wrong process-wise, and it is not S10's fault.** PB-KEY-10's fix was correct. It removed
the Kotlin-side epoch-key copy that PB-KEY-7's purge/recovery cycle had been architected on, and
**nothing re-derived PB-KEY-7 against the new arrangement**. A requirement can be invalidated by a
fix to a different requirement, and nothing in this phase's apparatus looks for that.

**`LockPurgeTest` pins a property the product cannot have**:
`the_wake_tier_is_reinstallable_after_a_purge_without_authentication` is B17(b)'s named fence, over a
hand-built session and a test-only core. It reads as coverage for something impossible on the live
path.

**The decision is NOT taken here** — three options, each with a real cost, and it needs its own round:
(1) purge memory only and leave the sealed tiers at rest, which contradicts S14a's fixes and
PB-KEY-7's own at-rest clause; (2) keep at-rest destruction and accept a machine re-grant per lock,
which is unusable; (3) have the phone signal grant-loss and the machine auto-re-grant on the next
gateway session, which is new machine-side behaviour and PB-KEY-3 currently says only the owner may
re-grant.

**What IS lost today, stated exactly.** The at-rest gate still holds across a process restart. The
live-process exposure does not: after one unlock, the Go core keeps `State.Keys.ContentKey` and the
decrypted session, snapshot and reply caches, with `MailboxRouter` still bound to the content key —
precisely the scenario PB-KEY-7 was written to close. The wake tier is unaffected and correct.

### B36 — PB-SEC-2's freshness gate does nothing on the send path

Found by the audit committee, tracing further than B35's investigation. The content key is unwrapped
**once**, at `Resume`, through the tier sealer (which does reach Keystore). Thereafter
`mobile/commands.go` `resolveSend` reads `core.State().Keys.ContentKey` **straight from Go memory**,
with no Keystore round-trip, for every outbound send.

So after a single resume, neither a screen lock nor PB-SEC-2's stated 60-second freshness window
stops any content operation. `AuthorizationLedger.invalidate()` — the layer that decides *when* to
re-prompt — has **zero production callers**. This is exactly the failure `mobile/keycustody.go`'s own
comment warns against: "a Go core that had cached the answer would keep decrypting content after the
screen locked... while every restart-based test still passed." That is the current state, described
in advance by the code that was meant to prevent it.

**PB-SEC-2 is NOT MET**, alongside PB-KEY-7.

**And the two requirements are in genuine tension with a third, which nobody recorded.**
`PhoneActivity.onPause` deliberately reaches no facade verb — because the Activity is
launcher-exported and **PB-SEC-11 forbids an exported component acting on the session**. So the fix
is not "call PurgeKeys from onPause": that reopens PB-SEC-11. It needs a component the exported
Activity cannot reach. **PB-SEC-11 and PB-KEY-7/PB-SEC-2 pull in opposite directions and only the
first was ever resolved** — the requirement set never noticed it had two answers to one question.

### B37 — B27 and B34 COMPOSE into a critical on-path DoS; both residuals are withdrawn

**This is the most serious finding of the audit, and it is mine twice over: I accepted both residuals
separately and never asked what they did together.**

Each was defensible alone. B27's trust-on-first-use window was accepted because it is "reachable in
practice only by the relay operator, to whom availability is already conceded". B34 recorded that no
production caller applies a transport policy, and deferred it as needing a pairing-payload size
decision.

**Together they are an unauthenticated, remotely-reachable, permanent denial of service against any
not-yet-paired identity, by an adversary who is NOT the relay operator.** The chain, all cited in the
tree:

1. Production dials with `relay.Dial` — no pin, no cleartext refusal (`mobile/relay.go`, B34).
2. `auth_init` sends **the full relay-auth public key** over that connection
   (`internal/remote/relay/client.go`).
3. A **passive on-path observer** of a `ws://` connection therefore learns the victim's public key.
4. The observer registers a throwaway identity and calls `authorize_device` naming the victim.
5. `mayActOn` permits it: the never-paired victim has authorized nobody (`store.go`, B27's first-use
   clause).
6. `device_revoke` is gated by the **same** permissive rule (`server.go`).
7. `revokeAndPurge` bans the victim and destroys its mailbox and token.

**B27's residual argument is unsound in the shipping composition.** Its premise — that the public key
is disclosed only at the relay handshake and over the SAS channel — is true of the *protocol* and
false of the *deployment*, because B34 leaves the handshake itself observable.

**Both residuals are withdrawn. Neither may be re-accepted alone.**

- **B34 is no longer deferred**: transport policy must be applied on the production dial path. Its
  cost — the pairing QR has no pin field and `MaxRelayURLLen = 39` — is now a cost that must be paid,
  not a reason to defer. Refusing `ws://` outright does not need a pin channel and closes step 1.
- **B27's consent-signature design, recorded there and declined as "a substantially larger slice", is
  now REQUIRED** unless transport security is made mandatory first and proven on the dial path.

**The generalisable failure, which is the reason this entry is long.** Every residual in this ADR was
judged **in isolation**, against the threat model, and each judgement was defensible. Nothing in this
phase's apparatus ever asked *what two accepted residuals do in combination* — and the composition
was strictly worse than either part, converting "conceded to the relay operator" into "available to
any passive observer". **A list of individually-acceptable residuals is not an acceptable residual
set.** Every future acceptance must state which other open residuals it was checked against.

### B38 — B37's remedy is INSUFFICIENT; the consent signature is required unconditionally

**B37 was written two hours before this entry and its escape hatch is false.** It closed with:
"B27's consent-signature design... is now REQUIRED **unless transport security is made mandatory
first and proven on the dial path**." An audit reviewer measured a **second, transport-independent
path** to the same capability, with a passing negative control.

`Machine.Pair` sends msg2 — carrying `MachinePayload.MachineRelayAuthPub` and `MachineRoutingID`
(`internal/remote/pairing/pairing.go`) — **one full round-trip before the mandatory desktop confirm,
and before the SAS is derived at all**. At that moment the channel is **PSK-authenticated, not
SAS-authenticated**: anyone holding the 32-byte QR secret opens it. B27's premise says the pubkey is
disclosed "over the SAS-authenticated pairing channel"; the code discloses it **before the SAS
exists**.

**Measured**: an impostor that photographed the QR, sent msg1 and never sent msg3, read
`MachineRelayAuthPub` with **zero desktop-confirm invocations**. D3 names the photographed QR as
precisely the threat the confirm defeats — it defeats **pinning**, not **disclosure**.

**And the window stays open**: `authorizeAtRelay` runs only on a *successful* pair
(`cmd/swarm/remote.go`), so a declined or abandoned pairing leaves the machine having authorized
nobody. **Measured**: a machine that has **never dialled the relay** is permanently locked out by the
QR photographer; its first dial returns `ErrRevoked`, and the owner's phone cannot lift a ban it did
not place. The negative control — the same body against a machine that has authorized one device —
is refused, so the fence can fail.

**This path needs no on-path position, no `ws://`, and survives `wss://` with a valid public-CA
certificate. B27's consent signature is therefore REQUIRED UNCONDITIONALLY.** Transport hardening
remains necessary and is no longer sufficient. Nobody may schedule work off B37's `unless`.

**PB-E2E-2 is also marked NOT MET here**: its own evidence file disclaims it, no log or screenshot
exists, and it was counted `shipped` **only because `scripts/phaseb-traceability.py` measures
evidence per SLICE, not per requirement**. That is the E15.1 defect I strengthened — still alive in
the artifact the audit reads.

### B39 — the 2026-07-20 pre-auth amendment states two facts the code does not have, and B29 rests on them

The fourth "names a mechanism without checking what it gates" entry, and it makes **both** errors.

The amendment claims per-routing-id rate maps are "**reaped on disconnect and bounded by a TTL
sweep**". Neither is true. `removeConn` reaps `authRate` and `opsRate` only; **`appendRate` and
`pushRate` are reaped nowhere** — verified: created and written, never deleted. There is **no TTL
sweep**; `runSweeps` runs presence and retention and nothing else.

It also claims per-key limits are safe because they apply "where the identity is **proven**". Proven
is not scarce: registration is open and re-authenticating on a live socket under a fresh key is
legal — recorded as residual 1.4 and filed as "the hazard is the *next* feature keyed on `sc.rid`".
**That feature already shipped, in this same amendment**: `opSource()` returns `"rid:"+sc.rid`, so
`OpsPerMin` — the only meter on `authorize_device`, `device_revoke` and `mailbox_read` — **resets on
demand**. Measured: with `OpsPerMin=4`, **18 `authorize_device` calls landed on one socket in one
window** by re-authenticating between rounds.

**B29 rests its acceptance on that false premise** — it declined the per-`(source,target)` fix
because it "mints map entries under attacker-chosen keys, which is precisely the unbounded per-key
state the auth path already refuses by design". The auth path refuses no such thing, and the
per-target keying B29 kept is **already attacker-chosen**, because `authorize_device` accepts any 32
bytes and B27's first-use clause authorizes append and push to the invented identity. **B29 traded
nothing.**

**A fourth badly-composing pair: B29 + B27's first-use clause.** Measured, after the attacker's
connection **closed**: 200 `appendRate` + 200 `pushRate` entries, 200 durable pairs rows and 200
durable mailbox sub-buckets from self-minted victims. A 30-day retention sweep deleted the items and
**left every sub-bucket and pairs row standing**. No `Quotas` field caps total storage.

### B40 — B27's rule is SYMMETRIC: a stolen once-unlocked handset permanently bans the owner's machine

A third badly-composing pair, unrecorded until now. `mayActOn` grants authority to whoever the target
authorized **in either direction**, so `granted(machine, phone)` authorizes the **phone** to
`device_revoke` the **machine**. B27 analysed the machine-as-caller direction only.

It composes with a custody decision made correctly elsewhere: RELAY_AUTH lives in the **wake tier**,
whose KEK is "deliberately NOT user-authentication-gated", because background reconnect must work on
a locked handset (B9/B16).

**Measured**: a legitimately paired phone banned its own machine; the machine's re-dial returns
`ErrRevoked` and **only the phone can lift it**.

**This falsifies half of the stolen-phone claim.** ADR-007 states "a once-unlocked stolen phone
yields only the wake key — no session history". Confidentiality survives via epoch rotation. The wake
key **also** yields permanent destruction of the machine's relay identity, and it **pre-empts the
designed remedy**: `swarm remote revoke` needs the machine to reach the relay, and it no longer can.

### B41 — "the gateway is owner-uid trusted" is circular with respect to the declared adversary

D5 names the gateway "the only component parsing attacker-influenced relay bytes". R1 then places
that same process **outside the cryptographic boundary**. So the one component the declared adversary
can reach is the one declared trusted, and R1's argument — a compromised owner-uid process "can act
as the owner directly" — is a claim about **authority** applied silently to **confidentiality and
identity**.

Concretely: `cmd/swarm-remote` loads `machine.key` and is handed `EpochKeys().ContentKey` and
`WakeKey`. A relay achieving code execution in the sidecar obtains the **machine identity key**
(impersonate the machine to the phone indefinitely) and the **epoch content key** (read all session
content) — the two things E2EE exists to deny the relay. The "compromised shell" analogy assumes the
attacker already has the machine, which is the whole question. **R1 argues the endpoint, not the
path.**

Worse to describe accurately: D4's per-command signatures **do** hold against the gateway, and the
compromised gateway does not need to forge one — it dials the owner-tier main socket, where no
signature, kill switch, allowed-root or `Options` allowlist applies. Every D8 launch restriction is
bypassed through the front door.

**The recorded hardening is entirely unimplemented**: `internal/remote/supervise` writes units with
no `User=`, `NoNewPrivileges`, `ProtectSystem` or sandbox profile, so "sidecar isolation limits blast
radius (defense-in-depth)" buys address-space separation and nothing else.

**The model is defensible in practice only because Go removes the memory-safety class from a
relay-bytes parser, leaving logic bugs** — and that qualification, which is the actual load-bearing
argument, appears nowhere in R1. R1 must state it, and must stop claiming defence-in-depth it did not
ship.

### B42 — two more requirements shipped-but-unmet, and an amendment resting on a dead mechanism

**PB-PAIR-5 — the clean instance of the failure mode I said I could not see.** I amended it to retire
`already-paired` and substitute `different_machine`, with the criterion "each is user-legible, not an
opaque error". Verified in the app: `PairingStep` **still declares `ALREADY_PAIRED`** — the state the
amendment declared unreachable — and declares **no `DIFFERENT_MACHINE`**. `stepOf` returns null for
it and the user gets the **generic** pairing-failed message. The Go guard is correct and does not
break same-machine re-pair; the *criterion* is unmet, in the app, for the state my own amendment
created. It read `shipped`. **An amendment is a change to what must be true, and nothing re-checked
the surface it moved the requirement onto.**

**PB-NET-4 — a requirement invalidated by a fix to a different one, the second confirmed instance.**
Its clause "only high-level idempotent ops may queue, with a stated bound" is counted met over
`OpQueue`, whose evidence cites a boundedness test. `QueuedOp{}` is constructed **nowhere outside
tests**, and `resolveSend` requires a live connection before authoring any mutating op — so the queue
is not merely unreached, it is **unreachable by design**. The connection model and ADR-007 D7's
live-only input rule killed it; nothing re-derived PB-NET-4. Same shape as PB-KEY-7 dying to
PB-KEY-10's fix.

**And PB-STATE-9's amendment — also mine — reasons at length about `PendingOps` carrying "session ids
and, for a launch, the command line the user typed", concluding the purge must leave it. It carries
nothing, ever.** The argument is sound and the mechanism is dead. A residual note that "none is
harmful today; each is a trap for the next slice that adds a caller" is therefore **false for this
one**: it is already load-bearing for a shipped requirement and for an amendment's reasoning.

**Three further findings recorded here rather than in their own entries:**

- **The fast-clock defect is worse than "goes deaf".** On `ErrStaleAge` the phone **acks the relay**
  and commits nothing, so frames are **permanently destroyed as they arrive** while the connection
  reads `online`. Correcting the clock recovers nothing. And the relay does not need a wrong clock:
  **withholding delivery for ten minutes and then releasing** makes the phone ack-and-discard
  everything. That is silent, permanent content destruction **performed by the victim and reported as
  health** — not the loss-of-function the residual describes.
- **B29's scope must be re-derived, not inherited.** Its acceptance rested on "one sender in v1", and
  B27's first-use clause made every never-paired target reachable by anyone. Its trigger was never
  multi-device; it is **any handset before its first connect**. B37 withdrew the premise B29 was
  accepted against.
- **The phone's drain wedges where the gateway's deliberately does not.** The gateway advances its
  cursor past a malformed item precisely so a poisoned envelope cannot wedge the loop; the phone
  returns on any open error and re-reads the same page. One unopenable frame stalls that mailbox for
  its whole retention window — and B28 relies on that behaviour without asking who can inject one.

**On the ledger's own correctness**: its Java-spelling model lowercases only the first letter, but
gobind lowercases the whole leading uppercase run (`SAS()` binds as `sas()`). No `App` verb has a
leading acronym today, so it does not fire — but the file's stated defence is that its matcher is
correct in both spellings, and it is not.

**On E15.1**: the strengthened fence is `strings.Contains(file, req)`. It moved from "the file exists"
to "the file mentions it" — **one step, not the step** — and PB-NET-2 passed it while NOT MET.

### B43 — the offline queue is withdrawn; and one more of my amendments had the unchecked shape

**PB-NET-4's queue clause and D7's "only high-level idempotent ops enter the offline queue" are
withdrawn.** The queue is not unwired — it is **unbuildable from the commands this system authors**.
A queued op is a *pre-signed* `DeviceCommandAuth`; `sealSignedCommand` stamps `ExpiresAt = now +
CommandTTLFor(action)`, one minute for an ordinary op; `opqueue.go` states it is never re-signed on
replay; `deviceauth.go` refuses it as `command expired`. **So the queue delivers nothing for any
outage longer than sixty seconds — every outage a queue exists for.** Re-signing at drain is
unavailable, because PB-SEC-2 pins the biometric gate **per use** for exactly the op list D7 names: a
drain would be a prompt, not a queue.

**Third confirmed instance of a requirement invalidated by a fix to a different one.** §6.0's
signed-horizon-by-op-class and PB-SEC-2's per-use gate landed later, and nothing re-derived PB-NET-4
against either. The phone's actual behaviour — refuse with `ErrClassOffline`, tell the user, surface
the state — satisfies PB-NET-4's remaining clauses, which is why this is a withdrawal rather than a
defect.

**PB-STATE-9's clause (3) loses its subject with it.** It reasoned that `PendingOps` carries "session
ids and, for a launch, the command line the user typed", concluding the purge must leave it. Nothing
in production ever writes that field. The **rule** survives — content-tier and non-purgeable is the
right home *if* a queue is ever built, and it is enforced — but the **justification** is false today,
and the tier test pins it over a hand-built fixture, i.e. a test over synthetic content for a field
production never fills.

**And the answer to the question I could not answer myself: one more of my amendments has the
PB-PAIR-5 shape.** **PB-PUSH-4** — I added the clause "authenticated -> a *distinguishable*
notification that still reads **no** session content", and **the added half has no assertion**: the
content-leak loop runs only against the locked path. Low severity, because the behaviour is right by
construction (the body concatenates two argument-less string resources) — but the clause I wrote to
make distinguishability safe is unchecked, which is exactly the defect I amended it to prevent.
**PB-OPS-5** is a weaker variant: its criterion was moved onto a document, the document says it, and
nothing reads the document. PB-SEC-4, PB-STATE-4 and PB-PUSH-9 were properly re-checked.

**The generalisation, now that there are three of these**: *an amendment changes what must be true,
and this phase had no step that re-checks the surface a requirement was moved onto.* Every amendment
was reviewed for whether its new wording was right, and none for whether anything now tests it.

**Ruling on the stale-age trade.** Not acking an age-refused frame leaves it uncompacted, so the
drain re-reads it until the relay's retention drops it — a bounded stall. **Accepted.** A stall is
recoverable and loud; a deletion is neither. It also gives the relay-adversary nothing new, since it
schedules delivery anyway, whereas **acking gave it destruction**, which is strictly more. The
previous behaviour let a hostile relay withhold delivery for ten minutes and then release, and have
the victim permanently delete its own inbound plane while reporting itself healthy.

### B44 — the lock RETURNS the content tier to locked; it does not destroy it. B17(b) is struck.

B35 recorded three options and took none. The implementer found a **fourth**, and it is better than all
three: **a screen lock puts the phone into exactly the state a push-woken process is already in** — a
state the design has modelled since S15 and which the load path already handles.

`PurgeKeys` now drops from **memory** precisely what a locked *load* leaves unread — epoch content
key, send-seq ceilings, receive high-waters, op queue, the three decrypted caches — and unbinds the
router; destroys **at rest** only the decrypted caches; **carries** the sealed content key verbatim,
which is what an unopened tier has always meant to every `Save`; and does not touch the wake tier.
Recovery is `App.UnlockContent`, **a fresh Keystore unwrap** — which is PB-KEY-7's own recovery
clause, and the round trip at which PB-SEC-2's 60-second window binds. The equivalence between a lock
and a locked load is fenced by test.

**Why B35's objection to option (1) does not apply here.** B35 rejected "purge memory only" as
contradicting PB-KEY-7's at-rest clause. That clause says *purge the decrypted session, snapshot and
reply caches* — and those **are** destroyed at rest, cheaply, because PB-SYNC-2 re-derives all three
by resync. **The text B35 was actually reading was `Store.PurgeKeys`'s implementation doc and B17(b),
not the requirement.** I wrote B35; that misreading is mine.

**Why destroying the sealed key at rest buys nothing.** It is sealed under an auth-gated Keystore KEK,
so a locked handset cannot open it. Destroying it helps only against an attacker who has **already
defeated Keystore** — who therefore also holds `device.key`, the recipient key and the command-signing
seed. Against that attacker the cost is total and permanent, and the benefit is zero.

**PB-SEC-11 is resolved, not traded.** The trigger is an `Application`-owned lifecycle callback plus a
**runtime-registered, non-exported** `ACTION_SCREEN_OFF` receiver. An `Application` subclass is **not
a component**: no intent filter, no manifest entry, unreachable from another process — verified, zero
manifest references. It is also strictly stronger than `onPause`: a hostile app can start the Activity
(which *foregrounds* us and purges nothing) but cannot background the process or turn the screen off.

**A brick closed on the way.** With the sealed key retained, `grantLossDetected`'s keyless test —
"no key in this process" — would have marked PB-KEY-3 **terminal at every screen lock**, the same
brick by another road. It now asks whether the phone *has* a key at rest, not whether this process
holds one.

**B17(b) is struck**, not merely marked false, and the four tests that pinned the falsified at-rest
destruction were **inverted in place** with the reasoning recorded in each — including
`the_wake_tier_is_reinstallable_after_a_purge_without_authentication`, which pinned an impossible
property twice over.

**B35's line "no `ProcessLifecycleOwner`, no `ACTION_SCREEN_OFF`, no `onStop`/`onTrimMemory`" is now
stale** — both signals exist.

**Residuals, accepted with reasons.** PB-SEC-2's mid-session lapse *while continuously foregrounded*
is deliberately not closed: every re-acquisition is Keystore-gated, so lock, background and process
death are covered and the window binds at each; a foreground timer would produce a refusal whose only
remedy is a `BiometricPrompt` **this app does not have**, and wiring a gate whose exit is unbuilt is
the failure class this phase keeps finding. Narrow by construction — continuously foregrounded means
the device is unlocked and the user is present.

**Two findings the implementer surfaced rather than buried**: `MarkGrantLost` may now have **no
production trigger** (its only one was the lock brick — a seventh uncalled-symbol instance *created by
B35's own falsified premise*), and a genuinely grant-lost phone reads `unreconciled` **before** it
reads grant-lost, so a user in a terminal state is shown a transient sync problem — PB-APP-10's
failure loop, a refusal-precedence decision still open.

**And the naming**: `PurgeKeys` no longer purges plural or destroys at rest. `LockContent` is the
honest name; it was kept only to bound gate risk across the golden surface, screen coverage and eight
test files. B35's own lesson argues for renaming it, and that is recorded as follow-up rather than
done under a security fix.

### B45 — the pairing dial runs UNVERIFIED TLS; the pin cannot bootstrap itself

Wiring the handset pin did **not** close residual 1.9, and the reason is a bootstrap paradox my own
framing missed. I wrote that "the pairing dial cannot be pinned and is covered by the cleartext
refusal". The first half is right. The second is incomplete, and fatally:

**On a pinning-only platform an unpinned `wss://` dial is REFUSED, not merely unverified** —
`Security.tlsConfig` returns `ErrPinRequired` before a packet moves. The pairing dial is unpinned by
construction, **because it is the dial that fetches the pin**. So on Android the pairing dial is
refused, the pin never arrives, and the phone can never pair over `wss://` at all. **The pin it would
have learned is unreachable through the only channel that carries it.**

**Decision: option 2 — the PAIRING dial, and only the pairing dial, runs unverified TLS.**

The argument, which is the reason this is acceptable rather than a hole: **the pairing payload is a
Noise handshake the operator authenticates by comparing a SAS.** The relay's certificate is not what
protects that exchange and never was — a hostile terminator on that hop cannot forge the handshake,
cannot learn the PSK, and cannot survive the SAS comparison. TLS on this one dial is carrying
confidentiality of *metadata*, not authenticity of *content*.

**The cost, named rather than buried**: a hostile terminator on that single hop sees the routing
metadata PB-NET-2's policy exists to hide — which routing ids are pairing, and when. It is one dial,
once per pairing, and it ends the moment the pin lands.

**The alternatives and why they lose.** (1) Verifying the pairing dial against system roots fails on
Android 14+ anyway — Go cannot see the Conscrypt store, which is this ADR's own reasoning for
`TrustRootsPinned` — and fails always for a self-signed relay, which is the runbook's own topology.
(3) An operator-provisioned pin in the app config is right for a self-hosted deployment and wrong for
a consumer flow, and adds a facade field. **Option 2 is the only one under which a self-signed relay
can bootstrap at all**, which is the deployment this project actually documents.

**Two defects this surfaced, both of which must be fixed and neither of which is this decision.**

**(a) A spinner that promises waiting is enough.** `ErrPinMismatch`, `ErrPinRequired` and
`ErrCleartextRefused` match no sentinel in `App.run`'s dial switch, so they fall through to
`reconnecting` — **"Lost the link to your machine; reconnecting", with a spinner, forever.** That is
exactly the PB-APP-10 failure `mobile/relay.go`'s own comments condemn three times, and
`ConnectionUi.kt` states the rule it breaks: *"A spinner is a promise that waiting is enough."* None
of these three conditions is ever resolved by waiting. **Pre-existing — it arrived with B37's
cleartext refusal, not with the pin** — and fixing it needs a new `ConnectionState`, i.e. Go and
Kotlin moving together, because `ConnectionState.of` errors on an unknown wire string by design.

**(b) The fail-closed branch residual 1.9's resolution rests on is REASONED, NOT MEASURED.** Nothing
exercises `ErrPinRequired`: `tlsConfig` switches on `runtime.GOOS` and the suite runs on darwin, so
the only test naming it asserts its *message text*. **A seam making the platform injectable is
APPROVED** — it is security-relevant code and the agent was right to ask first, but a fail-closed
path that has never been executed is the exact class this phase has spent itself finding.

### B46 — residual 1.12 is B37's DoS re-pointed at the PHONE, and the consent signature made it reachable

**Measured, with a vacuity control, by the round-2 threat reviewer. This supersedes residual 1.12's
"ordering wart" framing entirely.**

**The attack.** The phone operator does exactly what the design asks: compares the SAS, sees a
mismatch, **rejects**. `RunDevice` fails closed and pins nothing. But msg3 — carrying the phone's
standing relay consent — was signed and sent **six lines earlier**. The interceptor walks to the relay
with the harvested consent: `authorize_device(phonePub, consent)` accepted, `device_revoke(phoneRID)`
accepted, and the phone's next dial returns `relay-auth registration revoked`. Only the banner lifts a
ban, the banner is the interceptor, and the phone's relay-auth key is minted once per install.
**Recovery is a reinstall.** A forged consent is refused in the same run, so the gate is real and the
pass is not vacuous.

**The consent signature — the fix for B37's DoS against the machine — created the same DoS against the
phone.** That is the composition lesson again, now inside a single change.

**The author's reachability argument is FALSE, in three joined pieces nobody had put together.** It
held that a QR photographer cannot reach msg3 as responder because the machine created the rendezvous
first:

1. The shipped QR carries **no `MachineStaticPub`**, so the phone runs `AllowUnpinnedPeer` — **the QR
   secret alone suffices to hold the responder role**, no machine private key needed.
2. `RendezvousTTL` is **60s**; `defaultPairTTL` is **3 minutes**, whose own comment calls expiry
   *"advisory... the daemon's real gate is the mandatory SAS confirm, not a wall clock"*; and the CLI
   then blocks on `Pending()` with **no deadline at all**. The QR is announced valid long after the
   relay slot is gone.
3. `purgeExpiredRendezvous` **deletes without burning**, and `handleRendezvousCreate` has **no
   `requireAuth`** — so past TTL an unauthenticated stranger re-creates the same label, a claiming
   phone attaches to the stranger, and the machine sits orphaned on `Recv` with no error.

The machine created the rendezvous first — for 60 seconds of a window the product calls 180 and which
in practice never ends.

### B47 — the fifth "names a mechanism without checking what it gates": a revoke does not revoke the consent

`internal/remote/relay/routing.go`: *"There is no nonce and none is needed: the statement is a standing
grant... and **it is revoked by `device_revoke`** rather than by expiry."* Both halves true; the join
unchecked. `revokeAndPurge` deletes two `pairs` edges and writes a ban — **the signature is a durable
artifact the grantee still holds**, and `authorizePair` re-writes both edges *and clears a ban placed
by that same pairer*, in one transaction.

**Measured**: revoke the phone, dial refused; **replay the identical stored consent bytes, dial
succeeds.** The phone was never asked.

**`swarm remote revoke <phone>` is undone by bytes already sitting in the machine's state directory** —
the owner's entire remedy for a lost handset, not durable against anything that can read that
directory, i.e. against B41's own adversary.

**B47b, the purest instance of the same shape**: *"A burned or live slot is refused so the original
creator's in-flight pairing is never orphaned or hijacked"* — true of `Complete`, and exactly false
past TTL, because expiry deletes without burning.

### B48 — B45's ruling is INCOMPLETE and is amended

I ruled the pairing dial may run unverified TLS because "the certificate never protected the payload".
True of the payload; **false of two other things**:

1. **The routing metadata.** The rendezvous label rides cleartext control frames outside Noise.
   PB-NET-2 bans exposing exactly that, and TLS was the only thing hiding it. B45 priced payload
   confidentiality and never priced the clause it was exempting.
2. **The composition with B46, which is load-bearing.** The pairing dial is the dial that carries the
   phone's consent out. Under verified TLS an interceptor needs a certificate valid for the operator's
   relay; under my ruling it needs only an on-path position. **B45 lowered the cost of B46's harvest
   from "hold a valid certificate" to "be on the path"** — a widening of the one dial that must not be
   widened, ruled without B46 on the table.

**Amendment, taken from the reviewer**: dial the pairing rendezvous unverified **but capture the
presented SPKI and compare it against `MachinePayload.RelaySPKIPin` when msg2 arrives.** A pure network
attacker terminating that TLS cannot make the two agree, because the real machine authored the pin. One
comparison, no new channel, and it detects precisely the interception the ruling would otherwise
accept. It does **not** cover the QR-holder case; that is B46's job.

**Also recorded from this review**: B39's severity is **unchanged** — consent bounds *whose* routing ids
may be named, not *how many*, and an attacker signs its own; 200 identities minted again post-fix. And
`removeConn` reaps only the socket's **current** rid while re-auth overwrites it, so 25 re-auths leak 25
routing ids permanently marked connected. **B40 and B41 stand exactly as recorded**, both verified
independently rather than inherited — and the reviewer **corrected its own round-1 claim** that `User=`
was missing (the unit is a systemd *user* unit and already runs as that user).

**A clean negative, recorded as a result**: the loopback carve-out was attacked and **held** — userinfo
confusion, decimal/hex/octal literals, IPv4-mapped IPv6, hostname resolution, redirect into cleartext,
and whether `coder/websocket` replaces the caller's `CheckRedirect` (it chains it). No path to a
non-loopback cleartext dial. **And the handset pin IS consulted at the dial site** — my brief's worry
that B34's defect class had been reintroduced was unfounded.

### B49 — every `device_revoke` is now MUTUAL ASSURED DESTRUCTION; B38 deleted B40's only recovery

**The most consequential finding of round 2, and it is B25's lesson recurring verbatim.**

The ban is enforced at **registration** and is **global**, not pair-scoped (`handleAuthInit`'s
`isRevoked`). `authorizePair` is the **only** deleter of `bucketRevoked`, and only when the caller
**is** the recorded banner. So lifting a phone-placed ban on the machine requires **the phone** to
call `authorize_device` naming the machine — which was `mobile/relay.go`'s `onConnected`, and **B38
deleted it**. It is now unproducible even if restored: verification demands a signature under the
**machine's** private relay-auth key, which the phone has never held.

Measured: legitimate consented pair; phone revokes machine; machine re-dial refused; the phone's own
`authorize_device` naming the machine → `not authorized for route`. The two conditions are
individually satisfiable and **jointly unsatisfiable**.

**Before B38 this self-healed.** B25 records it in as many words — the phone authorized the machine on
every reconnect, so an attacker-placed ban self-healed at the next connect, and a recovered handset
returns on the same routing id. **That path is gone**, and the deletion's own note — *"What was
silently depending on it: nothing that survives"* — **is false.** What depended on it was B40's remedy.

**This is exactly B24 -> B25 again**: a narrowing of a permissive rule converted a second defect from
self-correcting to permanent. B38 narrowed `authorize_device` and did the same thing to B40. **Second
occurrence of the identical failure, eleven entries apart.**

**The sentence that belongs in the design docs**: *because the ban is global, only the banner may lift
it, and B38 removed the counterparty-side lift, **every `device_revoke` in this system is mutual
assured destruction — whoever fires first permanently removes the other's relay identity, and no party
can undo it.*** B40 hands a stolen phone the trigger against the machine; B46 hands an interceptor the
trigger against the phone. **They are one defect with two entry points.**

### B50 — B40's recorded remedy is FALSIFIED, and B47's is inseparable from B22

**B40's "capability scope in the signed consent" cannot work.** `bucketPairs` is **symmetric by
construction** — the only writers are `authorizePair` (two `Put`s) and `revokeAndPurge` (two
`Delete`s), and **nothing anywhere writes one edge alone**. The phone's authority over the machine is
`pairs[machine->phone]`, written by the **machine's own** call, carrying no consent. Scoping the
phone's consent constrains only what the machine may do to the phone. **`isPaired`'s doc — "authority
is asymmetric... different facts even when both hold" — is false in this implementation; they are the
same fact, byte for byte.** Implemented as recorded, B40's fix **tests green and leaves the hole
open**. The fix must scope the **pairer's own grant**, or `device_revoke` must stop reading
`mayActOn`.

**B47 is not independently fixable.** `authorizePair` restores access **and** lifts the banner's ban in
one transaction, so the owner's legitimate re-pair of a recovered handset and the attacker's replay of
stored bytes are **the same call with the same arguments**. Any remedy refusing a consent for a revoked
pairing **also breaks B22 and re-bricks PB-STATE-10**. The one separator identified: a
per-(pairer, device) **generation counter**, bumped at revoke and bound into `ConsentMessage` — correct,
and it costs relay-store-loss recovery, since the relay is the only holder of the generation.
**Whoever takes B47 must be told this before starting**, or it becomes the fourth falsified direction.

**The first-use clause deletion is safe but ENTIRELY UNFENCED** — the clause was restored verbatim and
the full relay suite stayed green. Of the remediation's claims, this is the one with no fence at all.

**B38 did NOT shrink B39's reach, and the record implies it did.** `handleAuthorizeDevice` never checks
`deviceRID != sc.rid`, so **a party can always consent to itself** — name your own key, sign with the
key you hold. Measured: 50 self-consenting identities leave 50 durable pairs rows, 50 mailbox
sub-buckets and 50 leaked rate entries after disconnect. B39's recorded reach cites B27's first-use
clause; **B38 deleted that clause, so a reader concludes the reach shrank. It did not.**

### B51 — the ninth uncalled-symbol instance: PB-SEC-2's per-use tier is unimplemented, and B44 made it bite

`KeystoreSpecs.forOperation` — the **per-use** `CryptoObject` spec for revoke, kill-switch, launch and
kill — is referenced **only from `src/test/`**. `AuthorizationLedger.beginPrompt/endPrompt/consume`
have **no production callers**. There is **no `BiometricPrompt` at all**; androidx.biometric is not a
dependency.

**So those four operations are gated by exactly what `input` is gated by**: the content KEK's
60-second **timed** window — the per-use-implemented-as-timed downgrade that `BiometricPolicy.kt`'s own
header says the file exists to make impossible. PB-SEC-2's criterion is that *a test must fail if the
implementation is an in-memory `authenticated = true` flag*; the only authorization record kept is an
**in-memory map**. **PB-SEC-2 is marked NOT MET.**

**B44 turned this from latent into live.** The content KEK carries no `AUTH_DEVICE_CREDENTIAL`
anywhere in `src/main`, so a PIN/pattern/password unlock does **not** satisfy it. B44's new trigger
drops the key on every screen-off, and the resume path asserts that "the Keystore-backed content KEK
will answer" — **false after a credential unlock (mandatory post-reboot, after biometric idle timeout,
after repeated failures, and always without an enrolled Class-3 biometric) or after 60s of idle.** On
refusal there is **no in-app way to authenticate**. Before B44 this never bit, because the key stayed
in Go memory for the process lifetime — which was B36's finding. Unverifiable in-repo by construction
(B31), which makes it a blocker rather than a measured bug.

### B52 — the consent leaves the handshake payload; it is bound to the CEREMONY, not to a generation

Both fixes I briefed for B46/B47 were falsified by measurement before a line was written. Recording
what replaces them, and why the briefs were wrong.

**(a) "Sign the consent after `DeviceSAS` returns — one site in `RunDevice`" is IMPOSSIBLE.** Measured:
`crypto.NoiseSession.binding` is captured only in `establish()`, which XXpsk0 reaches at **msg3**. So
the phone cannot derive the SAS until msg3's payload is **already fixed** — and the consent is *in*
that payload. The committee's own RED requires the `Consent` callback to be uninvoked when `DeviceSAS`
runs. Those three facts cannot hold together, and `internal/remote/crypto` is frozen, so no
intermediate binding can be exposed.

**Decision: the consent leaves msg3.** msg3 carries a **`ConsentDeferred` marker and no signature**;
the machine's pre-confirm `ErrNoConsent` check moves onto the marker — same fence, same position, and
the distinction it now draws is the honest one: *"a build that will grant a route once its operator
confirms"* versus *"a build that grants none"*, which is exactly the adversary that fence was written
against. The device derives the SAS, runs `DeviceSAS`, and **only on nil** signs and sends the consent
in a dedicated encrypted transport frame; a rejection sends an explicit **decline** frame so the
machine fails closed promptly instead of hanging. The machine's `Confirm` stays where it is — it must
display the desktop SAS *before* the phone resolves, or the human has nothing to compare against.

**No requirement pins the handshake message count** (checked: PB-PAIR-* constrain the QR payload, the
terminal states and the SAS comparison, not the frame sequence), so this is an ADR-level protocol
decision and it is taken here. Blast radius is the pairing package, the phone facade and the machine
leg — **no relay change**.

**(b) The generation counter I briefed has the SAME bootstrap contradiction** it was meant to avoid. A
relay-held per-(pairer, device) generation must be known to the **phone at signing time**, i.e. in
msg2 — but the generation is keyed by the *device's* routing id, which the machine does not learn
until **msg3**, and a first pairing has no record to read it from. That is `deliverEpochGrant`'s shape
exactly: an artifact required one step before the step that produces it. **Third time this ADR has
briefed a fix that needs a value earlier than it exists.**

**Decision: bind the CEREMONY ID — the 16-byte rendezvous id — into `ConsentMessage`, and retire
consents at the relay.** Both parties hold it from the QR **before msg1**, so there is no round trip
and no bootstrap. The relay keeps, per (pairer, device), the one live consent id plus a spent set:
`authorizePair` refuses a spent id, retires any different live id into `spent`, records the new one and
lifts the ban exactly as today; `revokeAndPurge` retires the live id in the same transaction.

Why this satisfies the three constraints that killed the alternatives:

- **B22 / PB-STATE-10 intact** — a re-pair of a recovered handset is a *fresh ceremony*, hence a fresh
  id, never spent. **No refusal is keyed on "this pairing was revoked."**
- **B47 closed** — a stored consent names a retired id and is refused, and *recording a new live id*
  retires the old one, so this does not depend on a revoke having happened.
- **`deliverEpochGrant` and `authorizeAtRelay` keep working** — both re-present the *same* stored bytes
  on every connect, and re-presenting the **live** id is idempotent-accept, not a retire.
- **It does not pay the generation counter's cost**: relay-store loss drops the spent set together with
  the bans and pairs, so the failure the counter protected against cannot arise.

Cost, named: `ConsentMessage`'s wire statement changes (breaking for any already-signed consent),
`authorize_device` gains a ceremony-id field, and the device registry persists it beside the signature.

**(c) Burning rendezvous ids on TTL expiry must not create the leak it prevents.** `Server.burned` is
an unswept `map[string]bool` and `rendezvous_create` has **no `requireAuth`**, so burning on expiry
would let an unauthenticated stranger grow it without bound. **Approved: timestamp burned entries and
sweep them, with retention longer than the announced QR window.** A fix that converts a hijack into a
memory exhaustion is not a fix.

### B53 — a machine enrolls a device whose operator REFUSED the SAS, and the deferral marker must carry no authority

Two findings from the stood-down parallel implementation, separated here because neither is about the
consent signature and both would otherwise be lost in a handover.

**(a) At HEAD, a refused SAS still enrolls the device.** Nothing after msg3 tells the machine what the
phone's operator decided: `enroll.Enroll` runs, `AddSole` commits, and **PB-STATE-10's single-device
slot is consumed by a pairing the user explicitly refused.** The machine's own confirm was affirmative,
so from its side the ceremony completed; the refusal happens on the other end and is never
transmitted.

This is **independent of the consent work** — it is a defect in what the ceremony reports, not in what
it grants. Any four-message design closes it incidentally, which is precisely why it must be **fenced
deliberately**: a defect closed as a side effect of another change is a defect that returns the moment
that change is revised. Note the user-visible shape — the owner refuses a pairing, and the machine's
one device slot is now occupied, so the *next* legitimate pairing is refused fail-fast until they
notice and revoke something they never agreed to.

**(b) The `ConsentDeferred` marker must convey NO authority whatsoever.** If the marker carries any
grant — anything the relay or the machine would act on — then **it is itself a credential released
before the SAS gate, and B46's argument restarts one level down.** The marker's entire content must be
"a build that intends to grant a route once its operator confirms", distinguishable from "a build that
grants none" and from nothing else.

This is the caveat that makes B52's marker safe rather than a re-labelling of the hole it closes, and
it was raised by the agent whose own design was superseded by it.

**A note on the process, because it is mine.** I asked one agent to *re-judge* a decision while
dispatching another to *build* it, and got two implementations of one protocol change in two
worktrees. The duplication was not free, but it was not wasted either: each design found something the
other did not — the measurement that the SAS **commits to** msg3's payload, the local-write deadlock
that looks like a free fix, the receive-ordering that removes the partial-failure window, the
empty-consent abort (measured as a 90-second package timeout when removed), and this caveat. **Both
agents flagged their own weakened fences rather than letting me find them**, which is the only reason
consolidating onto one branch without re-verifying the other was safe.

### B54 — the machine's published payload is authoritative on every completed pairing

I briefed a fix using `MachineRelayAuthPub` as a discriminator for the sticky pin. **The implementer
refused it and was right**: `differentMachine` already refuses the whole pairing before `pin()` runs,
keying on `MachineStatic`, so the discriminator would only matter for a pairing carrying a *different*
relay-auth pub with the *same* Noise static — which `machineid` cannot produce, since both live in one
identity blob. It could only have been tested against a state the product cannot construct: **the
"fence guarding a path production does not take" this phase has now found ten times, nearly built
deliberately on my instruction.**

**The reachable defect is the same-machine case and it is worse than the one I described.** The
operator re-runs `swarm remote init --relay-url X` **without** `--relay-pin` — the common case, since
the flag is optional. The machine publishes no pin; the phone keeps the stale one; every dial fails
`ErrPinMismatch` -> `relay_untrusted` -> *"Pair this phone again."* The user re-pairs; same
`MachineStatic`, so no refusal; the machine publishes no pin; **the old pin is kept again.** The
state's own remedy is a **no-op**, and there is no on-device way out.

**Decision: a completed pairing adopts the machine's published payload verbatim, including the absence
of a pin.** A completed pairing is SAS-confirmed and authenticated by both operators; treating a
missing pin as "dropped by accident" **second-guesses the machine's authority over its own relay**.
The accident case still recovers the moment the operator supplies a pin. The case that is *only*
fixable this way — a machine that legitimately has no pin — is currently **unrecoverable**.

This inverts a decision the code states deliberately, which is why the implementer declined to take it
alone. That was the right call.

### B55 — the revoked SIGNAL is asked for by the dialer; the ban becomes a fact about one relationship

B49's fix, and the reasoning is a proof about a whole class rather than a survey of options.

**B27's objection to pair-scoping was re-derived against current code and no longer stands.** It rested
on an **anonymous** party reaching `device_revoke` repeatedly. B38's consent signature removed the
anonymous party — nothing reaches that verb without the target's signature over `ConsentMessage(caller)`.
The purge is scoped anyway, using the sender already stored in each record. **This is the check round 2
said nothing in this phase performs: re-deriving a recorded remedy against the code as it is now.**

**The impossibility result.** After a revoke the machine and the handset hold **identical** relay state —
no edges, one ban — so the relay cannot tell them apart, and **every rule symmetric in (banner, victim)
either refuses both registrations (today's mutual assured destruction) or neither (PB-APP-10's signal
lost)**. Four candidate asymmetries were checked and each fails for its own reason:

- *refuse iff banned and no surviving grants* — symmetric (the machine has none either), and **reopens
  B24**, since a revoked device restores a surviving grant by self-pairing a throwaway (B50);
- *refuse iff banned by the party that was the pairer* — closes the stolen-phone entry point cheaply, but
  **B46's interceptor IS the pairer** of the pairing it forms, so it still bricks the phone. Fails the
  "one defect, two entry points" test;
- *widen the ban-lift so any consented re-pair clears every ban on the pairer* — **B24's hole verbatim**;
- *the phone re-learns at op time* — an idle revoked phone sits on `online` forever, **PB-APP-10's
  failure loop in a new costume**.

**The repair: the dialer names the peer whose verdict it wants.** `ClientAuth.Peer` rides `auth_init`;
the verdict moves to `handleAuthResp`, **after the signature verifies** — strictly narrower than the
pre-auth ban oracle it replaces. The handset names its pinned machine and gets `ErrRevoked` at the same
delivery point, with the same terminal state and grace window. **The machine names nobody, deliberately**:
no legitimate flow revokes a machine at the relay, since `swarm remote revoke` is the only production
caller and the phone's own revoke rides the sealed command plane to the gateway. So a stolen phone's ban
reaches **no verdict the machine consults**, and an interceptor's ban names the interceptor, so the phone
never consults it. **Both entry points die, and the phone-side one dies independently of B47.**

Enforcement stays server-side in the **deleted edge**; only the **signal** is advisory — which is B26's
own diagnosis, that the ban was carrying a signalling job on a global-authority mechanism. That sentence
is now in the code, not only here.

**Two findings from building it, both of which would have shipped silently.** bbolt's `Cursor.Delete`
leaves the cursor on the deleted slot, so a scoped purge deleting **adjacent** records can leave half a
revoked backlog drainable — the first fence used one frame per sender and could not have caught it. And
the grace-window fixture had a **second** modelling gap beyond its missing pairing record: it minted a
**fresh machine relay-auth key per ceremony**, so the recovery pairing re-pinned the handset onto a
machine that had revoked nobody — invisible under a global ban, incoherent under a scoped one.

**Three residuals recorded rather than fixed**: `handleDeviceRevoke` still severs the victim's live
socket unconditionally (transient, bounded by quota, and conditioning it would blur ME-1); `isPaired`'s
doc still claims an asymmetry B50 falsified and should be struck; and legacy ban rows go dead on upgrade
rather than being migrated — which **un-bricks every identity a global ban already destroyed**, at the
cost of a re-pair prompt that has by then served its purpose.

**A process note that is mine.** This agent reported the two `case false &&` disabled dial arms as an
unrecorded live defect. They were real **in its baseline** — my wholesale staging committed a mutation
in `184a7aa`, corrected in `4a4cef2`, and it branched inside that window. HEAD is clean and both fences
pass. **My `git add -A` did not merely risk shipping a mutation; it poisoned another agent's baseline and
cost it investigation time on a defect that no longer existed.** Sixth entry on this; residual 4.7 stands.

### B56 — PB-E2E-2 and PB-KEY-8 are in DIRECT CONFLICT; the app cannot start on a standard emulator, correctly

**Measured by running it, not reasoned.** The app opens on the session surface showing *"Something
failed in a way the app does not recognise"* — never the pairing step. Cause, from a temporary log at
the one catch site: `KeystoreDowngrade: dev.swarm.phone.kek.wake was generated weaker than requested:
the platform reports SECURITY_LEVEL_SOFTWARE`. **The emulator's keymaster reports software-only
backing, and PB-KEY-8's downgrade refusal fails the app closed.** Every screen is downstream of
`runtime.phone()`, so the pairing step never renders — which is exactly what the instrumented test
reports as "nothing to pair with".

**This is the product working as specified.** PB-KEY-8 requires refusing a key weaker than requested,
and the provisioning code's own comment states the condition under which it would be wrong: *"a handset
we intend to support would have to report SECURITY_LEVEL_SOFTWARE"*. **The author reasoned about
handsets. Nobody asked what an EMULATOR reports — and PB-E2E-2 requires the app to run on one.**

**So PB-E2E-2's acceptance criterion is unsatisfiable as written**, and that explains its entire
history: why the instrumented tests never ran, and why the requirement sat `shipped` unverified through
three separate investigations that each found a different proximate blocker. Six dead invocations and
the whole transport chain were repaired; **the app still cannot start.**

**RULING.**

- **Option 1 — an emulator image with a hardware-backed keystore — is to be CHECKED, and it is a
  bounded experiment rather than an argument.** It costs no security and no scope. Nobody has proven it
  impossible, and an unproven belief is exactly what this phase keeps finding underneath a stalled
  requirement.
- **Option 2 — a debug-build carve-out on the downgrade refusal — is REJECTED**, as it has been four
  times in other clothes. It weakens the control PB-SEC-1's at-rest claim rests on **so that a
  demonstration can pass**, which makes the demonstration about itself. The implementer declined to take
  it on its own judgement and was right.
- **Option 3 — re-scope PB-E2E-2 onto a physical handset, merging it into PB-E2E-5 — is the fallback
  if option 1 fails**, and PB-E2E-2 then stays **NOT MET** rather than becoming met. An exclusion is
  honest; a green produced by disabling the custody gate is not.

**The structural consequence is larger than one requirement.** PB-KEY-8 makes **every** instrumented
test of the app's startup path impossible on an emulator — not only PB-E2E-2's. `PbE2E2ResumeTest` is
in the same position, and so is any future `connectedAndroidTest` that constructs the runtime. If option
3 is taken, **that entire test tier is deferred with it, and the `androidTest` source set currently
reads as coverage that can never execute** — the same shape as residuals 4.4 and 4.6, one tier up.

### B57 — `paired` is published before the pin is durable, and `relay_untrusted` is terminal

Found while diagnosing a flaky fence, and it is a product defect in the pairing/transport seam.

`Pairing.finish()` sets the `paired` label **under `p.mu`, unlocks, and only then calls `App.pin()`** —
so the label is public before the durable write it implies. Meanwhile `App.run` retries every 250 ms
and `App.dial` reads `State().RelaySPKIPin`, so **a dial landing in that window uses the PRE-PAIRING
pin.**

That would be a self-correcting hiccup, except for what B45/B54 just built: **`relay_untrusted` is
TERMINAL.** A dial in that window against a relay the *old* pin does not match sets the terminal state
and **returns from the loop**. So a **successful pairing can be immediately followed by a permanent
`relay_untrusted`, whose remedy is "pair this phone again" — which is exactly what the user just did.**

**Same shape as B54's loop, one layer up**: a terminal state whose remedy re-enters the condition that
produced it. And **the window widens under exactly the load that broke the fence**, which is how it was
found at all.

**It is narrow** — `pin()` normally completes well inside 250 ms — and `rearmAfterPairing`'s grace
window **may already cover it**, which is unverified. **Assigned, not assumed.**

**The fix direction, to be evaluated rather than obeyed**: the `paired` label must not be published
before the durable state it implies, since every observer reasonably reads it as "the pairing's effects
are visible". If the ordering exists to avoid holding the lock across I/O, then the durable write
should complete before publication without the lock being held across it — not the label moved earlier.

**The diagnostic lesson, which is mine.** I saw a **deterministic assertion** failing 2 runs in 3 and
concluded state was leaking into it. The assertion *is* deterministic — a specific hash is or is not
present — but **the precondition it rests on is asynchronous**, and *determinism of the assertion says
nothing about determinism of what it observes*. That is a distinct trap from the one I was guarding
against, and I sent an agent hunting the wrong thing.

**Also worth recording**: `go test ./...` **without `-count=1` serves cached package results**, so any
attempt to reproduce an order-dependent failure without it proves nothing. Three "clean" runs were
meaningless for that reason.

### B58 — B57 is a SHIPPING BLOCKER: the first pairing on Android is a coin toss

B57 was recorded as a narrow race. It is not. **The worst case is the FIRST pairing on a handset**, and
it is the ordinary path.

A fresh handset holds no pin. On a pinning-only platform an unpinned dial is refused with
`ErrPinRequired`, which B45's switch maps to `connRelayUntrusted` — **terminal**. So while the user is
completing their very first pairing, the transport loop is failing **terminally** on every 250 ms
retry, and whether the handset ever connects depends on winning a race.

**`rearmAfterPairing` does not cover it — it is a lost wakeup.** It polls `dead.done` **once,
non-blocking**, and acts only on a generation already dead at that instant. Only `Start` and
`rearmAfterPairing` ever launch the loop, so a loop that dies *after* rearm has polled is **never
restarted by anything**. The losing interleaving: the label publishes, the loop dials with the
pre-pairing pin and returns terminally (the grace window is not set yet, because `rearmAfterPairing`
has not run), then `pin()` writes and rearms — and if that poll lands before the dying loop's deferred
`close(done)`, rearm sees nothing to re-arm and **the loop is gone**. In the winning interleaving the
user still sees a terminal state flash. **The grace window is not a guard here; it is a coin toss whose
outcome the user sees either way.**

**It does not reproduce on darwin, which is why nothing caught it.** With no pin and `TrustRootsSystem`
the same dial fails with a generic x509 error that falls through to `continue` rather than to a
terminal verdict. **The bug is invisible on the platform the suite runs on and ordinary on the platform
that ships.**

**RULINGS.**

**(1) Build the terminal-state half FIRST, and it stands alone.** A transport verdict reached **while a
pairing is in flight** must not be terminal — the thing that would clear it is the pairing already
running. `App` already tracks in-flight pairings, so this needs no new state, and **it removes the
brick even if the ordering is never touched.** That independence is why it goes first.

**(2) The mid-write `Cancel` question is ruled: once the durable write has landed, the pairing is
COMPLETE and `Cancel` no longer wins.** It becomes a no-op that tells the user the pairing completed
and the remedy is `revoke`. The reason is PB-PAIR-4's own principle — **never half-paired**. Letting a
late `Cancel` publish over landed durable effects would create exactly the half-paired state that
requirement forbids: a phone pinned to a machine it believes it cancelled. And rolling the write back
is a larger change than the window justifies. **`Cancel` means "stop before it lands"; after it lands,
the verb is `revoke`.** So the settled-state guard is re-checked under the lock after the write, and a
user answer arriving mid-write loses **with a message**, not silently.

**(3) Extend the platform seam to the `App` level — approved.** Today a fence can only be written
against `ErrPinMismatch` (a wrong persisted pin), while the case that actually bites is
`ErrPinRequired` (a fresh handset with none). **A fence against the reproducible error rather than the
real one is the defect class this phase has found ten times**, and it would be built deliberately here.
Keep the seam narrow and inert in release, as the existing one is.
### B59 — PB-SEC-2's per-use tier is WIRED; `AUTH_DEVICE_CREDENTIAL` is REFUSED, and B44's exit is built

B51 recorded the ninth uncalled-symbol instance. This records what closed it, and the two judgement
calls it forced.

**What was built.** `PerUseGate` runs a per-use action only after the platform hands back a
`CryptoObject` **whose key actually operates** — the cipher the prompt released is used, and a failure
to use it refuses the action. The ledger is **never consulted as the gate**: PB-SEC-2's criterion is
that a test must fail if the implementation is an in-memory `authenticated = true` flag, and the ledger
*is* an in-memory map, so it decides only whether prompting is worth doing and what is in flight. The
chain from the button to the spec is `PhoneSurface.perUseButton` → `PerUseGate` → `KeystorePerUseCiphers`
→ `CustodyProvisioning.provisionGate` → `KeystoreSpecs.forOperation`, and every link is fenced.

**Only TWO of the four operations have a production call site, and building fences for the other two
was refused.** `App.RevokeThisDevice` and `App.Kill` are reached from `PhoneSurface`. `App.Launch` is
ledgered unbound — PB-APP-6's screen does not exist — and the phone may **never set the kill switch**
(`handleRemoteSetControl` refuses the remote tier before consulting its backend, PB-SEC-6), so
`App.KillSwitchEngaged` is a READ. Gating either in production would be a fence guarding a path
production does not take, which is this phase's tenth instance of that shape. Instead the Go fence
requires **every** production call of a per-use verb — launch included — to be declared through
`perUseButton`, so the gate is mandatory the day a launch screen lands and vacuous for nobody today
(revoke and kill make it load-bearing now, and a floor fails the check if fewer than two call sites
are found).

**`AUTH_DEVICE_CREDENTIAL` is REFUSED. The argument, and what each answer strands.**

Adding it (`AUTH_BIOMETRIC_STRONG or AUTH_DEVICE_CREDENTIAL`) would let a PIN, pattern or password
satisfy the content KEK and the per-use entries. Four things decided it:

1. **With a prompt built, it rescues exactly one case.** B51 listed four states where the content KEK
   refuses. Three are fixed by the prompt alone: the post-reboot credential unlock is followed by a
   `BIOMETRIC_STRONG` prompt that *does* satisfy the key; the 60-second idle lapse is a fresh prompt;
   `ERROR_LOCKOUT_PERMANENT` is cleared by unlocking the phone at the lock screen and then prompting.
   The only case the credential authenticator uniquely rescues is **a handset with no enrolled Class-3
   biometric**.
2. **It would not rescue even that one on an existing install.** `setUserAuthenticationParameters` is
   baked into the key at generation and `KeystoreCustodyBootstrap.ensure` returns early when the alias
   exists — so a spec change reaches **fresh installs only**. To rescue a stranded handset it must be
   paired with a deliberate re-provision of the content KEK, and re-provisioning that KEK destroys the
   three content-tier device scalars it seals (`GateInvalidation`'s own note): that **is** a re-pair,
   and a re-pair rescues the handset without weakening anything.
3. **The cost lands on the declared adversary.** The threat model is a device someone else is holding.
   A device credential is shoulder-surfable and is the same secret that got the phone open, so
   "authenticated" would quietly change from *a Class-3 biometric, now* to *whatever unlocked the
   phone* — the semantic twin of the per-use-as-timed downgrade this whole slice exists to remove.
4. **The refusal has an exit, which is what makes it a refusal rather than a brick.**
   `BiometricManager.canAuthenticate` distinguishes NONE_ENROLLED from NO_HARDWARE, and those have
   opposite remedies. A handset with nothing enrolled is told to enrol a fingerprint or face unlock —
   an action the user can perform. A handset with no Class-3 sensor is told that nothing here will
   change that, which is the **one** refusal in the table with `PerUseRemedy.NONE`, asserted as the
   only one by test.

**So what is stranded, stated plainly rather than left implied**: a handset with Class-3 hardware and
nothing enrolled cannot use revoke, kill or the content tier **until the user enrols something**, and a
handset with no Class-3 sensor cannot use them at all. The rejected alternative would have admitted
both at the price of making a PIN sufficient for every install that came after it.

**And refusing had a bill that was nearly left unpaid.** Point 4 above was written before anyone
checked what a PIN-only handset actually does, and what it does is **fail during provisioning, before
any prompt can be offered**: `KeystoreSpecs.kek(CONTENT)` requests `AUTH_BIOMETRIC_STRONG`, and the
platform refuses to *generate* such a key with nothing enrolled. `DeviceCapabilities.probe` cannot see
it — USER_AUTH_PER_USE is answered from the **API level**, a fact about the platform rather than about
the handset — so the `InvalidAlgorithmParameterException` fell through `routeStartupFailure` to
`SwarmErrorTokens.UNKNOWN`: *"something failed in a way the app does not recognise"*, remedy **NONE**.
An app that will not start, for a population that is not an edge case, saying nothing they could act
on. `PhoneRuntime.refuseAHandsetThatCannotHoldTheContentKek` now asks before provisioning and routes
NONE_ENROLLED to `UserAuthenticationRequired` — whose remedy is `AUTHENTICATE`, which is what the
unlock control keys on, so the control appears, finds it cannot prompt, and shows *"add one in system
settings"*. One mechanism reached by two roads. A transient answer proceeds rather than refusing:
generation checks **enrolment**, not whether the sensor is free this second, and refusing there would
be residuals §2.8 — an app that will not start — through a new door.

The decision is **fenced in both directions**: `s20_pbsec2_peruse_test.go` fails if `KeystoreSpecs` and
`BiometricPrompts` disagree about device credentials. Half of this decision is worse than either whole
answer — a prompt that accepts a PIN over a biometric-only key is one the user can satisfy that
releases no key.

**B44's hole is closed, and the sharpest instance of it was on the STARTUP path.**
`PhoneRuntime.construct` opens the sealed store under the content KEK, so a lapsed window means no
phone can be built **at all** — every screen is downstream of `runtime.phone()`, and what the user got
was "authenticate" with nothing anywhere to authenticate with. `ContentUnlockPolicy` now decides from
the **routed remedy** whether to offer a prompt, on both the ready path and the unavailable one; a
destroyed key, an unsupported handset and a lost grant get no button, because a prompt offered for a
refusal it cannot fix is PB-APP-10's failure loop reached through the remedy.

**The residual B44 recorded is NOT closed, and its stated reason is now false.** B44 declined a
foreground `AUTH_TIMEOUT_EXPIRED` timer because its remedy "would be a `BiometricPrompt` this app does
not have". That prompt exists. `ContentLock`'s header is corrected in place rather than left standing,
and the residual now rests on a smaller claim it can carry: a continuously-foregrounded session means
the device is unlocked and the user is present, and every re-acquisition after a lock, a backgrounding
or process death is already Keystore-gated.

**What is NOT established, and may not be read out of any file added here.** No test executes
`BiometricPrompts`. Nothing claims a real prompt was shown, accepted or refused on any device, or that
a real Keystore withheld a real key. PB-E2E-5 is deferred (B31), and B56 makes the whole `androidTest`
tier unexecutable besides — an instrumented test cannot reach a prompt because PB-KEY-8 fails the app
closed before a screen renders. `androidx.biometric` is therefore confined to **one** translation file
and fenced out of every unit test, widening the `dev/swarm/phone/ui`-scoped fence in `s16_ui_test.go`
to the whole unit-test source set: a Robolectric shadow driven to "succeeded" would read as proof the
gate works.

### B60 — round 3: PB-PAIR-4 and PB-SEC-2 are NOT MET, and B40 became renewable

Four findings from the round-3 external review. **The honest count is at most 139 of 143.**

**(1) PB-PAIR-4 is NOT MET — the fifth requirement invalidated by another fix.** A post-accept
`Complete` failure **reverses the machine's interpretation after the phone has already acted**:
`internal/skeleton/pairing_transactional_test.go` requires the machine to report failure and retain no
enrollment, while the phone has pinned. **That is precisely a half-pair**, which PB-PAIR-4 exists to
forbid, and it is marked `shipped`. The repair is that final acceptance must follow a durable
machine-side commit, or the protocol needs a persisted prepare/commit either side can resume — **a
post-accept failure must not be allowed to reverse a decision the phone has acted on.**

**(2) B58's new durability fence tests DELAY, not FAILURE — and this is residual 4.9 turned on its own
author.** `App.pin` receives `Core.Mutate`'s error and **returns void** (`mobile/pairing.go`), so a
caller publishing `paired` cannot distinguish a successful write from a refused one. The new ordering
test's custody seam **only sleeps and always succeeds**, so it proves publication after a *slow
successful* write and says nothing about a *refused* one. **The success-delay fence cannot transfer to
the failure branch in the same arm** — which is exactly the generalisation an agent recorded two hours
earlier, now applied to the fence that agent was writing. `pin` must return its error, and `finish`
must publish a distinct failure state **without clearing the pairing-attempt record**.

**(3) A stale biometric callback resurrects authorization. PB-SEC-2 returns to NOT MET.**
`AuthorizationLedger.endPrompt` does not require the callback to belong to the **currently active**
prompt: after an invalidation clears `inFlight`, a queued `SUCCEEDED` callback still resolves to
`AUTHORIZED`, and the per-use gate then exercises its released cipher and runs the destructive action.
Worse: prompt A starts, a screen lock invalidates it, prompt B starts for the same operation, **A's
stale callback clears B's marker and runs A**, and B's later callback also authorizes because
`endPrompt` accepts callbacks with no matching active prompt. **The tests only invalidate an
already-completed authorization; none invalidates while a prompt is outstanding.** Each prompt needs an
unforgeable generation, and a callback must authorize only if that generation is still active.

**(4) B40 is now RENEWABLE — B47 and B55 composed to make it worse.** Consent retirement is stored in
the orientation `(machine, phone)`, but a phone-initiated `device_revoke(machine)` looks for the
**reverse** row and therefore **retires nothing**. The machine requests no peer verdict (B55,
deliberately) and **re-presents the still-live phone consent on every gateway connect**, restoring both
edges — after which the handset can revoke again. **A self-restoring, repeatable sever/purge
primitive.** Existing consent tests cover only the machine-revokes-phone orientation. B40 can no longer
be classified as "open, no replacement designed": its reach and repeatability must be re-measured
against the current composition.

**This is the third consecutive round in which the FIXES composed into something worse than their
parts** — B37 (two residuals), B49 (a fix deleting another's remedy), and now B40 renewed by the
interaction of a retirement orientation and a deliberate verdict asymmetry. **Every one was invisible
to review of either change alone.**

---

### B61 — round 3, the composition audit: self-consent is the root of two shipped defects

The round-3 composition reviewer returned seven findings with B60's four subtracted. Two were
verified by running code against a real relay with measurements, not inferred from prose. Both
of those share one root that B50 already recorded and mis-scored.

**The root.** `handleAuthorizeDevice` (`server.go:789`) verifies the caller's signature over
`ConsentMessage(ceremonyID, sc.rid)` under the pubkey the caller named, derives
`deviceRID := RoutingID(req.DevicePub)`, and authorizes the pair. **It never checks
`deviceRID != sc.rid`.** A caller may name its own pubkey and authorize itself. B50 recorded
this and scored it as identity-row and rate-row growth. That was wrong: it is a capability, and
two shipped requirements rest on its absence.

**B61(1) — PB-PUSH-9's "deletion on revoke/disable" is dead. The sixth requirement invalidated
by a correct fix to a different one.** B49 conditioned the push-token drop on
`grantsAnyone(pb, rid)` (`store.go:596`) so that one party to one relationship could not silence
a handset another relationship depends on. Correct in isolation. But one self-consent writes
`pairs[phone\0phone]`, so `grantsAnyone(phone)` is true forever and **no revoke by any party
ever drops that phone's push token again** — neither the write-through cache nor the durable
row. Measured on a real relay: after one self-consent, `machine.DeviceRevoke(phoneRID)` leaves
`cache="phone-token" durable="phone-token"`.

The sentence in `revokeAndPurge`'s own doc comment at `store.go:549-557`, citing PB-PUSH-6's
"unreachable provider-visible identifier for a device its owner disowned", is now false.
`TestB49_ARevokeSilencesThePhoneOnlyWhenItSeversItsLastRelationship`
(`b49_revokescope_test.go:321`) passes throughout, because it asserts the design and never
constructs the self-edge.

**B61(2) — an unbounded, attacker-writable, never-swept durable store. The ship blocker.**
`authorizePair` writes one `bucketRetired` row per superseded ceremony (`store.go:578`).
**Nothing anywhere deletes from that bucket**: the only references in the package are the
creation loop at line 75, the read at 384, and the write at 578. The only ceremony-id validation
is `ceremonyID == ""`; `ParseConsent` (`routing.go:121`) reads a `uint32` length out of a frame
of up to 1 MiB, and no format or length check exists.

Composed with the root, no victim, no pairing and no stolen key are required — one freshly
minted keypair and one connection. Measured: 400 self-consents carrying ~32 KB ceremony ids grew
`relay.db` from 0 to 33,714,176 bytes, linear at ~84 KB/call. bbolt never returns pages to the
OS. At the configured `OpsPerMin: 600` (`config.go:126`) that is roughly **72 GB/day of
unreclaimable relay disk from a single source**.

**This rhymes, and the rhyme is the point.** B52(c) asked exactly this question about
`Server.burned` in the same decision — *"A fix that converts a hijack into a memory exhaustion is
not a fix"* — approved a sweep for the in-memory set, and never asked it of the DURABLE bucket
the same decision was creating. The standing defect class list gains an entry: **a question
asked and answered for one store in a decision is not asked of the other store that decision
creates.**

Note the tension the remedy must hold: a sweep of `bucketRetired` is a retention mechanism, and
B47 requires a retired ceremony be refused FOREVER. A sweep that forgets a retirement re-opens
the replay B47 closed.

**B61(3) — B59's refusal of `AUTH_DEVICE_CREDENTIAL` is undone inside its own slice.**
`keys/Provisioning.kt:436` sets `setInvalidatedByBiometricEnrollment(true)` on every per-use gate
entry. `keys/BiometricPrompts.kt:263-273` answers `KeyPermanentlyInvalidatedException` with
`drop(alias)` + `provisionGate(operation)` + retry — minting a fresh key bound to the NEW
enrolment. The stated reason ("a gate entry SEALS NOTHING") is true and answers the wrong
question: the entry seals nothing, it AUTHORIZES revoke and kill. B59 refused device credentials
because a PIN is shoulder-surfable; enrolling a fingerprint needs exactly that PIN, so the
refused capability is restored by one extra step. Distinct from B60(3), which is the stale
callback in the ledger; this is the Keystore entry being reissued against an attacker's
biometric.

**B61(4) — B58's guard was added beside the defect, not over it.** Two gaps.
(a) `connRevoked` (`mobile/relay.go:259`) is still guarded by `withinPairingGrace()` ALONE while
the two arms B58 touched got `pairingInFlight() || withinPairingGrace()`. B58's ruling was about
ANY transport verdict reached during a pairing, and `ErrRevoked`'s remedy is likewise a pairing —
it is the verdict PB-STATE-10's recovery actually produces.
(b) The guard is sampled at error-handling time, not dial-start time. A dial that started before
the durable write and returns `ErrPinMismatch` after the pairing handle is gone ends the loop
terminally with rearm already spent, and `Start` is a no-op while `a.sess != nil`. The
in-flight B60(2) fix does not touch this. The fence cannot see it:
`b58_pairingterminal_test.go` measures over an in-process `httptest` TLS front where a dial
completes in microseconds, so the straddle window is structurally unreachable — 20 green runs
over an unreachable question.

**B61(5) — a second entry into PB-PAIR-4, which B60(1)'s repair makes worse.** The machine
`sendDecision(accept)` at `internal/remote/pairing/pairing.go:601` then returns and the caller
enrolls. `handleRendezvousSend` (`server.go:1339`) returns `replyOK` when the peer has detached
or the 16-slot inbox is full. A relay that simply drops msg4's decision — and the relay is the
declared adversary — leaves the machine ENROLLED and the phone UNPINNED. B52/B53's claim that
"there is no ordering in which one side is enrolled and the other is not" is false in this
direction, and B60(1)'s repair (final acceptance must follow a durable machine-side commit)
STRENGTHENS it by guaranteeing the machine has committed before it sends the frame the relay
eats.

**B61(6) — B47's durability rests on a best-effort call that swallows its own failure.**
`purgeRelayState` (`cmd/swarm/remote.go:723`) is the only production caller of relay
`device_revoke` and therefore the only thing that retires a consent on the owner's revoke.
Failure is a warning with exit code 0, and `relay.ErrNotAuthorized` is swallowed entirely at
`:733`. Composed with B60(4): a phone-initiated revoke deletes both pairs edges, so the owner's
later `swarm remote revoke` finds `mayActOn` false, gets `not_authorized`, prints nothing and
exits 0 — the machine-orientation consent is never retired either. With
`TestB47_AnUnknownCeremonyIsAccepted` required so `deliverEpochGrant` survives a store rebuild,
the stored consent stays spendable forever. B60(4)'s primitive disarms the one call that would
have retired the other orientation.

**B61(7) — the s20 gate has a receiver-shaped blind spot.**
`android/gate/s20_pbsec2_peruse_test.go:304` matches the literal `"app.revokeThisDevice("` and
its floor of 2 is met by the two existing sites, so an ungated third call site naming its
receiver anything else is invisible AND does not trip the vacuity control. The file's stated
limit ("TEXT, not types") defends against a fully broken matcher, not a partially blind one.
Also recorded: `PerUseGate.kt:305` calls `prompt.show` outside any try/catch, so a synchronous
throw leaves `inFlight` set; `AuthorizationLedger` has no synchronization while reachable from a
`BroadcastReceiver` and from lifecycle callbacks; and B54's verbatim pin adoption plus
Android-pinning-only means a machine with **no `--relay-pin` — the DEFAULT, the flag is
optional** — yields an empty pin, hence `ErrPinRequired` on every dial, hence a terminal
`relay_untrusted` the instant the pairing handle drops. ADR-007 recorded that as "currently
unrecoverable" without noting it is the default configuration.

**Clean negatives, recorded so they are not re-audited.** B55's peer-named verdict genuinely
narrows the oracle (the check runs after signature verification, so the only askable question
requires the private key). The B47 fences CAN fail (three fail by name when the retired check is
disabled). The B49 adjacent-frames fence asserts an exact depth delta AND byte-identity of the
survivors, so it catches under- and over-deletion. `authorizePair` and `revokeAndPurge` are
genuinely single transactions.

**The honest count moves to 138 of 143.** PB-PUSH-9 joins PB-PAIR-4, PB-SEC-2 and PB-E2E-2 as
NOT MET. PB-E2E-5 remains excluded.

---

### B62 — round 3, the threat audit: the daemon parks forever in the ORDINARY order of operations

Eight findings, each carrying its own provenance: executed, read, or inferred. Six are not in
B60 or B61. The reviewer independently confirmed B60(4)'s mechanism in code rather than
inheriting it, and independently reproduced B61(2)'s durable-store growth with different
numbers (200 self-consents at 32 KB ids taking `relay.db` to 16,777,216 bytes).

**B62(1) — CRITICAL. The machine parks forever on msg4 after an affirmative confirm, and the
shipped phone's only abort path is structurally incapable of sending the abort.**

No attacker is required, and this is not the tail case. The owner scans the QR, the desktop
shows the SAS, the owner presses `y` on the desktop FIRST — that prompt is in front of them —
then turns to the phone. From that moment anything that cancels the phone's context hangs the
daemon permanently. Each link read:

1. `internal/remote/pairing/pairing.go:473` — after the confirm the machine blocks on
   `recvConsent(ctx, ...)`, which the relay parks on `sc.ctx.Done()`
   (`internal/remote/relay/server.go:1372-1385`). `Conn.RendezvousRecv` (`client.go:397`) passes
   the caller's ctx straight through; there is no timeout.
2. `internal/protocol/server.go:2098` — the production ctx is
   `context.WithCancel(context.Background())`. **No deadline.** `pairWindow`
   (`internal/skeleton/pairing.go:288`) is only the ANNOUNCED `ExpiresAt`; nothing enforces it on
   the `Pair` goroutine.
3. `mobile/pairing.go:440-448` — the shipped `DeviceSAS` closure returns exactly `nil` or
   `ctx.Err()`. There is no third return.
4. `mobile/pairing.go:541-559` — `RejectSAS` and `Cancel` both call `cancelHandshake()`, which
   cancels that ctx AND `CloseNow()`s the socket. `pairingTTL` = 60 s cancels the same ctx.
5. `internal/remote/pairing/pairing.go:665` — `_ = sendConsent(ctx, sess, rt, nil)`, commented
   *"an ANSWERED refusal, not silence"*, runs on **the ctx that was just cancelled**.

So the abort frame is never sent on any production path that produces it. Measured, deterministic
over three runs against a transport faithful to `relay.Conn`: two sends attempted, one refused by
the dead ctx, machine still parked six seconds after an affirmative confirm.

**The daemon consequence is the severe part.** `Pair` never returns, so `result()` never fires, so
`clearPairing` never runs (`internal/protocol/server.go:2191`, reachable only from `result` and
the `BeginPairing` error path), so `cc.pair != nil` forever and **every later `pair_start` on that
connection is refused "pairing already in progress"** (`:2102`). There is no `pair_cancel` op.
Only dropping the whole owner connection escapes. The relay conn leaks with it.

**A composition, and no single change is wrong.** B52 introduced msg4; B46 tightened the window
to 60 s; the production ctx never had a deadline. Before B52 the machine's last receive was msg3,
which PRECEDES the confirm — afterwards it only ever sent. B52 introduced the first machine-side
receive with no clock and placed it after the point where the operator believes the pairing is
done. `RejectSAS` — which `mobile/pairing.go:886-892` calls *"the ONLY signal this protocol has
for a MITM"* — is the path most certain to hang the machine.

**B62(1a) — the fence for exactly this cannot fail, and it is the clearest instance yet.**
`b52_consent_release_test.go:223` passes `context.WithTimeout(context.Background(), 2*time.Second)`
into `Pair`. **Its green comes entirely from a deadline the TEST injects and production never
supplies.** Changing only that literal to `10*time.Minute` fails the test by name. A test that
supplies the very safety property whose absence is the defect.

**B62(1b) — and the second is worse.** `b52_consent_release_test.go:173` sets `DeviceSAS` to
return a hand-made non-nil error on a LIVE ctx. Per link 3 the shipped phone cannot produce that
shape. The fence for B52's central claim tests a rejection production cannot make and is silent
on the one it can.

**B62(2) — independent confirmation of B61(2)**, with the additional observation that this changes
the CLASS of B39's growth: durable relay state was O(identities) and is now O(operations) at up to
32 KB each, under a per-identity budget that B62(7) shows resets on demand.

**B62(3) — B41's own remediation instruction was never carried out.**
`internal/remote/supervise/unit.go:288-306` still has no `NoNewPrivileges`, `ProtectSystem`,
`ProtectHome`, `PrivateTmp`, `RestrictAddressFamilies` or `SystemCallFilter`. More consequential:
**ADR-007 line 46, the D4/R1 paragraph, is verbatim unchanged** and still reads *"Sidecar isolation
(below) limits blast radius on daemon/PTY state (defense-in-depth)"* — the sentence B41 ruled false
and demanded be withdrawn. Nineteen entries later a reader of the Decision section still gets it.
**The finding lives in an appendix that contradicts the body**, which is a new failure mode for this
record: an ADR whose later entries falsify its earlier body without amending it.

**B62(4) — B50's impossibility result has been FALSIFIED by B52, and the replacement remedy kills
B60(4) at the root.** B50 proved no remedy could exist because *"`bucketPairs` is symmetric by
construction — nothing anywhere writes one edge alone."* True when written. B52 then added
`bucketConsents`, written on exactly one key (`store.go:394`) and deleted on one key (`store.go:581`).
Measured: `consents[machine|phone]=true`, `consents[phone|machine]=false`. The asymmetric durable
fact B50 declared absent is now in the store.

`handleDeviceRevoke` (`server.go:1105`) gates on `mayActOn` alone, which a paired phone satisfies —
so a once-unlocked stolen handset, using only the wake-tier `RELAY_AUTH` key (deliberately not
user-auth-gated per B9/B16) and speaking the relay protocol directly, severs the owner's remote
access from anywhere, instantly, unattributably, and per B60(4) repeatably.

**Remedy: require the caller to be the PAIRER (`consents[caller|target] != nil`), not merely
`mayActOn`.** One bucket lookup, zero new state, zero new wire fields. Checked against every
constraint B55 enumerated: `swarm remote revoke` holds; PB-STATE-10's stranded-device recovery
holds; the stolen phone is refused (it never calls `authorize_device` — B38 deleted the one in
`onConnected`); B46's interceptor holds; B24 is untouched. **B60(4)'s renewable cycle dies with it**,
because the cycle's first step is the phone's revoke.

Also verified, because it would have changed the severity: PB-STATE-10's LOCAL recovery is not
bricked by the stolen phone's revoke — `runRemoteRevoke` treats every relay purge failure as a
warning (`cmd/swarm/remote.go:555-583`), so the device slot frees.

**B62(5) — the sixth "names a mechanism without checking what it gates".**
`cmd/swarm/remote.go:687-691`, repeated at `cmd/swarm-remote/config.go:191-194` and
`internal/remote/relay/server.go:740-744`, argues *"No legitimate flow revokes a machine at the
relay: this CLI is the only production caller."* Both halves true. The unchecked join:
**`device_revoke` is not gated on being a legitimate flow — it is gated on `mayActOn`**, a store
predicate the stolen handset's key satisfies directly. The sentence surveys the shipped app's
CALLERS; the verb's gate is a relay-side PREDICATE. And B55's conclusion is true of the BAN and
silent about the EDGE DELETION, which the same paragraph calls "the enforcement".

**B62(6) — B54's ruling is falsified for the one case it claims is only fixable that way.** B54
records *"the case that is ONLY fixable this way — a machine that legitimately has no pin — is
currently unrecoverable."* Adopting the absent pin does not make that machine reachable on Android,
the only platform Phase B ships: `TrustRootSourceFor("android")` is `TrustRootsPinned`, so
`tlsConfig` returns `ErrPinRequired` (`security.go:359-360`) → `connRelayUntrusted` → "pair again"
→ adopt no pin → `ErrPinRequired`. `handsetSecurity`'s own doc block states this outcome verbatim.
The no-op loop B54 exists to break is preserved for that case; only the sentinel changed. **Narrow
the ruling to the stale-pin half and record the no-pin machine as still unrecoverable** — noting,
per B61(7), that no `--relay-pin` is the DEFAULT.

**B62(7) — B39 unchanged; the ceremony binding does not shrink the minting reach.** Measured: 12
successful re-auths on ONE live socket; with `OpsPerMin=4`, 36 `authorize_device` calls landed in
one window and 12 leaked rate windows stayed resident. `handleAuthInit`/`handleAuthResp` have no
`!sc.authed` guard; `registerSession` overwrites `sc.rid` while `removeConn` reaps only the CURRENT
rid. Neither ceremony-bound consent nor the pair-scoped ban touches this, because both constrain
WHOSE routing ids may be named and a self-consenting attacker signs its own.

**B62(8) — an additional candidate for "invalidated by a fix to a different requirement".**
R-PAIR.6's 60 s window was tightened by B46 for a 3-message handshake containing ONE human
decision. B52 then put a SECOND human decision inside the same window and nothing re-derived the
TTL. This is the benign trigger for B62(1).

**Confirmed closes, recorded so they are not re-audited.** B49's mutual assured destruction is
CLOSED. B52(c)'s `burned` sweep is closed and correctly bounded — the durable set is the one that
got away. B53(b)'s `ConsentDeferred` marker is INERT as required: a device that sends the marker
and then nothing leaves the machine PARKED, never half-enrolled; the parking is B62(1) and the
marker itself carries no authority. B48 is wired and correct under attack — `Peer` is derived from
`MachineRelayAuthPub`, not from the asserted `MachineRoutingID`, which is the exact hazard
`ConsentMessage`'s own doc names. B47's mechanism is correct; the defect is its storage.

---

### Residual 4.10 — the third form of the fence-that-cannot-fail, stated by the agent that hit it

Recorded verbatim because it is sharper than the two forms already on the list:

> **When the failure I want to inject is reachable only through a seam the happy path also uses,
> the fence tests whichever comes first — and that is never the thing I meant.**

Earned, not theorised. The implementer of B60(2) wrote the durability fence, measured it vacuous
three separate ways against the mutation that swallows `pin`'s error — refusing both tiers after
SAS, refusing the wake tier only, and a gated custody refusing after N further calls at N ∈
{0,1,3,4} — and **deleted it rather than leave a green test standing over an unfenced fix.** The
cause is structural: the handshake and the durable write draw on the same tiers, so any refusal
reachable through `KeyCustody` breaks `RunDevice` before `pin` is ever called, and the pairing then
fails for a reason unrelated to the change.

The two earlier forms — *a fence written for one error class does not transfer to another error in
the same switch arm* (4.9), and *a success-delay fence cannot transfer to the failure branch*
(B60(2)) — are special cases of this one.

**Fencing B60(2) requires a seam that can fail the WRITE without failing the HANDSHAKE**: an
injectable `phonecore.Store`. That is new product-facing surface and was correctly not added on an
implementer's own judgement.

---

### B63 — B60(3) fixed, B60(3)'s own claim narrowed, and PB-SEC-2 stays NOT MET on two remaining halves

**Fixed (9f43244).** `endPrompt` now refuses a callback that does not belong to the prompt on
screen. The independent RED author confirmed the defect reproduces — a virgin ledger that has
never prompted for anything granted REVOKE on `endPrompt(REVOKE, SUCCEEDED, t)` — and
mutation-verified the fix in both directions. Full Kotlin suite after: 41 classes, 270 tests, 0
failures.

**My claim (b) in B60(3) was too broad, and is withdrawn.** I recorded that a stale callback
"can clear a NEWER prompt's marker while doing so", implying cross-operation clobbering. It
cannot: `if (inFlight == operation)` never matches when the operations differ, so a stale
callback for a DIFFERENT operation leaves the newer prompt's marker intact. The real hazard was
the unconditional grant beneath that check, not the marker. Left uncorrected, an implementer
would have hunted a consequence that does not exist.

**PB-SEC-2 remains NOT MET on two distinct halves, and this is the trap worth naming.** The fix
above closes the ledger's callback-identity hole. It does not close either of these, and an
implementer briefed on one alone would report the requirement met while the other still ships:

1. **Same-operation supersession.** Two prompts for the SAME operation are indistinguishable at
   `endPrompt`, because the signature carries nothing that separates them. Prompt #1 for KILL
   outstanding, invalidate, prompt #2 for KILL begun, then #1's late callback — still resolves
   against #2. Closing it needs a per-prompt token issued by `beginPrompt` and presented back.
   Deliberately NOT fenced by the RED author, because writing that test would have prescribed the
   fix's API.
2. **B61(3), the Keystore entry re-minted on an enrolment change.** Distinct file, layer and
   trigger; neither fix closes the other. Verified independent rather than assumed: the ledger
   mutation left the gate-level test green because the cipher path was never entered differently,
   and re-minting in `cipherFor` cannot affect a callback arriving after `invalidate` emptied the
   ledger.

**Caution for whoever takes B61(3):** that code carries an explicit written rationale
(`BiometricPrompts.kt:253-262` — *"unlike a KEK, a gate entry SEALS NOTHING… the honest recovery
is to make a new one"*). It is a considered decision, not an oversight, so the counter-argument
must be made on its merits: auto-re-minting discards the one signal
`setInvalidatedByBiometricEnrollment(true)` exists to raise, so an attacker who enrols their own
fingerprint passes the gate on the next attempt. B61(3) was a code read, not a mutation-verified
finding, and is not certified to the same standard as the rest of this entry.

**Adjacent, recorded, unfenced.** `invalidate` sets `inFlight = null`, so after a screen lock a
second `BiometricPrompt` can start while the first is still on screen — `REFUSE_SECOND` is
defeated across any invalidation. That is a `beginPrompt`-side property. Also: a stale callback
carrying `SUCCEEDED` maps through `PerUseGate.reasonFor` to `KEY_NOT_RELEASED`, "the unlock was
accepted but the key was not released", which is the wrong story for this case; the tests assert
only that SOME refusal reaches the caller, so a new refusal reason is free to be added.

**A methodology note that cost me twenty minutes and nearly a false attribution.** After applying
this fix, `WakeNotificationTest` failed with `java.io.EOFException` — the worker JVM dying, not an
assertion — in both a full run and an isolated one, while passing at HEAD. That pattern reads
exactly like a regression. It was not: it is the sixth distinct timing/resource failure today on a
box that hit load average 57 on 8 cores, and the same test passes with the fix in place once load
drops. **Isolation is not sufficient to discriminate a load artefact from a regression when the
load is sustained** — the discriminator is re-running BOTH arms at the same load, which is what
finally settled it. Residual 4.7's rule about the shared tree has a sibling here: the shared
MACHINE is not observable either.

---

### B65 — the app allows screenshots: PB-SEC-4 withdrawn by product decision, and inverted rather than deleted

**Decision, made by the owner on 2026-07-26:** the shipped app permits screenshots and screen
recording. `FLAG_SECURE` and `setRecentsScreenshotEnabled(false)` are removed from
`SecureWindow.protect()`.

**Why this is defensible rather than a concession.** `SecureWindow.kt`'s own documentation
already conceded the limits of what was being bought: *"FLAG_SECURE is a platform hint the
compositor honours. It does not stop a camera pointed at the screen, it is not attested, and an
accessibility service can still read the rendered screen."* PB-SEC-12's own gate says the same
thing twice in prose. So the protection stops an app that can already screenshot, and stops
nothing that has accessibility access or a second camera. Against that, it blocks users of a
DEVELOPER TOOL from sharing terminal output, which is an ordinary thing to want and which the
product exists to put in front of them.

**The two arguments that were specific, and were answered rather than overwritten.**
`android/window-security.tsv` did not argue generically; two of its seven rows carried real
reasoning, and a decision that ignored them would be worse than the original.

- **The pairing screen shows the SAS.** The row's argument stands on its own terms: a screenshot
  of the SAS hands an interceptor the one value the comparison exists to protect. What makes the
  trade acceptable is that the SAS is live for the seconds of a ceremony bounded at 60 s, the
  comparison is made against a screen the owner is looking at, and an attacker positioned to
  screenshot the handset mid-ceremony has already lost the ceremony's premise. It is a narrowing
  of margin, not the removal of a control — and it is recorded as such rather than waved away.
- **The terminal peek shows session content sealed at rest by S15 and kept out of logs by
  PB-SEC-3.** Allowing screenshots does undo one layer of that, one level up. The layers below it
  are unchanged, and they are the ones that survive device loss, which is the threat those
  requirements were written against; a screenshot requires an adversary already executing on the
  unlocked handset.

**Inverted, not deleted, and this is the load-bearing part.** Both the requirement and its gate
survive with their polarity reversed. The gate now asserts that NO source file names
`FLAG_SECURE` or `setRecentsScreenshotEnabled`, keeps the bidirectional TSV join, and asserts the
rows read `false`. Reinstating the flag therefore FAILS a gate.

That is deliberate. A requirement deleted leaves nothing behind; a requirement inverted keeps the
one property the original actually bought — that this is a DECISION and not drift. It also means
the next person to add `FLAG_SECURE` back, for what will feel at the time like an obvious security
improvement, is made to read this entry first. Both directions are mutation-proven: re-adding the
flag fails the gate by name, and flipping a TSV row to `true` fails it too.

**PB-SEC-12 is untouched.** It is UI-redress: overlay and tapjacking protection on gated actions,
clipboard hygiene, and documented IME/accessibility limits. It has no screenshot clause.
`filterTouchesWhenObscured` on the destructive and authorising controls stays exactly as it was,
and conflating the two would have removed a defence nobody asked to remove.

**A note on how this arrived, because the sequence matters.** The need surfaced as a build
problem — Play requires 2-8 phone screenshots and the flag made every capture black — and the
first remedy briefed was a debug-only guard. That would have been the wrong shape: it keeps a
control the owner does not want, adds a build-variant branch to a security-relevant path, and
requires a gate asserting the guard is exactly `BuildConfig.DEBUG` to stop it rotting. The owner's
ruling replaced a workaround with a decision. **Recorded because the workaround was already
briefed and half-built when it was cancelled** — the tempting move under sprint pressure is the
one that unblocks the immediate task without settling the question underneath it.

---

### B66 — B65's own gate was fail-OPEN, and one sentence of B65 was false

Two corrections to B65, both found by the implementer that built it and both verified by hand
against the committed artifact rather than argued.

**B66(1) — inverting an assertion re-polarised a shared helper's known bug. New defect class.**

B65's gate, as first committed, stripped Kotlin comments before scanning so it would tolerate the
remaining prose mentions of the two APIs. `stripKotlinComments` is **not string-literal-aware**: a
`//` inside a string literal blanks the rest of that line. So this line PASSED the gate while
setting the flag:

    val u = "http://e.invalid"; a.window.addFlags(...FLAG_SECURE)

Measured against both versions: `ok` under the gate as committed, FAIL under the hardened one.

**The composition is the finding, and it is a class not previously recorded here.** Under the
ORIGINAL POSITIVE assertion — *a sink must exist* — the stripper's bug was **fail-SAFE**: hiding a
real sink made the test demand one and fail. **Inverting the assertion re-polarised that same bug
to fail-OPEN**, with no change to the helper and no change to any other gate that uses it. The
stripper remains correct for every positive assertion in the tree, which is why it was not
patched; the fix removed the NEED to strip, by scanning raw source so there is no code/comment
discrimination left to defeat.

**Generalisation, recorded as residual 4.11:** *a shared helper's known limitation is safe or
unsafe according to the POLARITY of the assertion that uses it. Inverting an assertion silently
re-polarises every helper it depends on, and the helper's own tests will not notice.*

This also corrects a claim made in this session's review: the gate was verified by mutation and
reported sound, on a mutation that reinstated the flag in the obvious way. That mutation was
caught. The escape requires the literal and the flag on ONE line, and was not tested. **A single
mutation proves a gate fires; it does not prove the gate cannot be evaded** — the two are
different questions and only the first was asked.

**B66(2) — B65's "keeps the bidirectional TSV join" was FALSE when written.**

There was no bidirectional join at HEAD. The gate had one direction only — the two roles PB-SEC-4
names must have rows — plus the sink count. The implementer built the missing direction (a row
naming a screen the app no longer has now fails, mutation-proven) but could **not** honestly build
the fully general converse, *any new screen with no row fails*: this module has no screen registry,
`ui/` mixes screen models with row models, error routing and the facade bridge, and every naming
rule tried is escapable by calling the next screen something else.

**So B65's sentence is amended: the join is bidirectional FOR THE TWO NAMED SCREENS, not in
general.** The limit is written into the gate file so the pair is not over-read. B65's
mutation-proof sentence stands; only the join claim needed the qualifier.

**B66(3) — `docs/verification/remote-phaseB-s18-evidence.md` was stale and is now marked.** It
described `SecureWindow.kt` as "the one sink: `FLAG_SECURE` + `setRecentsScreenshotEnabled(false)`",
asserted the window CARRIES the flag after `onCreate`, and recorded mutation evidence for a
function B65 deleted. A superseding banner was added at the top; the body is left unedited,
because it is signed-off evidence for a closed slice and rewriting history to match a later
decision is how an evidence trail stops being one. This is the second instance of B62(3)'s shape —
a record whose later parts falsify its earlier parts without amending them — and the first where
the stale artifact was an EVIDENCE file rather than the ADR body.

---

### B64 — the daemon parked forever on msg4, in the ordinary order of the ceremony

**WRITTEN LATE, ON 2026-07-30, AND THE LATENESS IS ITSELF THE FIRST THING TO RECORD.** The fix
shipped in `f8b8634` with only a commit message behind it, and the number B64 was assigned to an
agent and used in file names and citations while no entry existed. A round-4 reviewer found the
gap by grepping: the record ran `B63` straight to `B65`. This project's own convention is that a
decision change requires an ADR, and the most severe non-adversarial defect of the phase had none
— which is B62(3)'s shape again, one entry later, and this time the missing artifact was the
entry itself.

**The defect.** The machine parked forever on pairing msg4 **with no attacker and in the ordinary
order of operations**. The owner scans the QR, the desktop shows the SAS, the owner answers the
desktop prompt FIRST — it is the one in front of them — then turns to the phone. From that moment
anything ending the phone's leg wedged the daemon.

Two causes, neither wrong alone:

1. **The pairing window was announced but never enforced.** `pairWindow` produced the `ExpiresAt`
   in the `PairView` and nothing else; the handshake ran on the connection-lifetime ctx
   (`internal/protocol/server.go`'s `context.WithCancel(context.Background())`), which carries no
   deadline. Past the announced instant the goroutine stayed parked in `recvConsent` indefinitely.
2. **The abort was sent on the context whose cancellation was the only way to reach it.**
   `internal/remote/pairing/pairing.go:665` called `sendConsent(ctx, …)`, commented *"an ANSWERED
   refusal, not silence"* — but mobile's `DeviceSAS` returns only `nil` or `ctx.Err()`, `RejectSAS`
   and `Cancel` both go through `cancelHandshake()`, and the 60 s `pairingTTL` is a deadline on
   that same ctx. So the frame that existed to unpark the machine was the one frame that never
   left.

**Why it was severe rather than merely slow.** The handshake goroutine is the only thing that ever
calls `result`, so no result meant no `clearPairing`, so the connection's single pairing slot was
held forever and **every later `pair_start` on that connection was refused "pairing already in
progress"**. There is no `pair_cancel` op; dropping the owner connection was the only exit.

**A composition, and B52 introduced the window it lives in.** Before B52 the machine's last receive
was msg3, which PRECEDES the confirm — afterwards it only ever sent. B52 introduced the first
machine-side receive with no clock and placed it after the point where the operator believes the
pairing is done. B46, one entry earlier, had tightened the announced window to 60 s for a
three-message handshake containing ONE human decision; B52 then put a SECOND human decision inside
it and nothing re-derived the TTL (B62(8)).

**Both fences for this were vacuous.** `b52_consent_release_test.go:223` passed
`context.WithTimeout(context.Background(), 2*time.Second)` into `Pair` and took its green
**entirely from a deadline the test injected and production never supplied** — changing that
literal to ten minutes failed it by name. And `:173` drove a rejection shape the shipped phone
cannot produce (a hand-made non-nil error on a live ctx). The clearest instance this project has
produced of a test supplying the very safety property whose absence is the defect.

**The fix.** The window is enforced as well as announced, bounded by the DURATION rather than the
`expiresAt` instant so an injected clock cannot skew it — what is promised and what is enforced
stay the same value. `abortConsent` detaches with `context.WithoutCancel` (not `Background`, so
the ctx's values survive and only cancellation is dropped) under its own short timeout, because
the usual reason that ctx is dead is that `RejectSAS` already `CloseNow()`'d the socket.

**THE DEADLINE IS THE FENCE; THE ABORT IS A COURTESY.** Stated in the code so the courtesy is not
mistaken for the mechanism: a phone that loses the network sends nothing at all, and the machine
must still recover. A remedy resting on the abort would have been a fence that cannot fail for the
most common failure it faces.

---

### B67 — round 4: evidence files are checked for EXISTENCE, never for CURRENCY

The round-4 committee's bookkeeping reviewer found no eighth requirement of the familiar
invalidated-by-another-fix shape. It found a different and arguably worse one.

**B67(1) — "Evidenced (measured on disk)" does not mean current.** `scripts/phaseb-traceability.py`
distinguishes *shipped* (asserted by hand) from *evidenced* (measured on disk) and says so
prominently, so a reader trusts the second number. But `evidence_path()` checks only that the file
EXISTS. Two requirements now have evidence artifacts that are fossils — true of an earlier commit,
false of HEAD — and nothing catches it:

- **PB-SEC-4** — `remote-phaseB-s18-evidence.md` described `SecureWindow.kt` as the sink for the
  screenshot block and cited a mutation result for a test name that no longer exists. B65 deleted
  both. That file is what the traceability table cites for **all ten** of S18's requirements.
  Marked superseded in B66(3).
- **PB-PUSH-9** — `remote-phaseB-s17-evidence.md` predates both the defect that made the
  requirement NOT MET (B61's self-consent) and the fix that restored it. The test that is
  load-bearing for its CURRENT status is not in the file that is supposed to prove it.

**The generalisation, residual 4.12:** *an evidence file is a claim about a commit, and a
traceability check that tests only for its existence silently re-dates every claim it contains to
now.* The honesty machinery this project built to separate asserted from measured has a third
category it did not name: **measured, but measured against the wrong version.**

**B67(2) — an OPEN finding went moot and nobody recorded it.** B61(6) held that a phone-initiated
`device_revoke` deletes both pairs edges and thereby defeats the owner's later CLI revoke through
`purgeRelayState`'s swallowed `ErrNotAuthorized`. B60(4)'s `isPairer` gate means a phone can no
longer reach that verb at all, so the precondition is gone. Verified by running the B60 suite.

Recorded because the direction is the interesting one: this record has been repeatedly warned that
a "this is closed" claim is a claim with a timestamp. **The converse is equally true and had not
been stated — an OPEN finding can be silently closed by an unrelated fix, and a reader who trusts
the open list will chase something that no longer exists.** Three work directions died this phase
re-deriving a stale impossibility proof; the same cost is available in the other direction.

**B67(3) — B62(3) confirmed still open at HEAD.** `internal/remote/supervise/unit.go` still has
none of `NoNewPrivileges`, `ProtectSystem`, `ProtectHome`, `PrivateTmp`, `RestrictAddressFamilies`
or `SystemCallFilter`, and the Decision body's D4/R1 paragraph still asserts verbatim the
defence-in-depth B62(3) ruled false and demanded withdrawn. Two entries have now recorded this and
neither remediation was carried out. **It is a production blocker and is listed as one.**
