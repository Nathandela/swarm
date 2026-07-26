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

**Threat-model scope (residual R1, the honest boundary).** A `0600` socket does not isolate two processes running as the same owner uid, and the gateway must run as the owner (it holds the machine identity key and reads the 0700 state dir), so SO_PEERCRED cannot distinguish a compromised gateway from the local TUI. Therefore the cryptographic containment boundary is the **untrusted relay** and the **semi-trusted phone**: a process compromised while running as the owner (the gateway included) already holds the machine identity key and can act as the owner directly, without the daemon — the same status as a compromised shell on a single-owner machine, and outside the cryptographic boundary by construction. Sidecar isolation (below) limits blast radius on daemon/PTY state (defense-in-depth); it is not a cryptographic barrier. This ADR adopts the scoped threat model for the personal-deployment default and records the stronger option (a dedicated non-owner service uid with its own key custody, or an OS sandbox/MAC profile denying the gateway the main-socket path) as an available hardening if multi-user isolation is later required. Revisiting ADR-004's deferred SO_PEERCRED question: it does not help here because both trusted and untrusted processes share the owner uid.

### D5. Gateway: supervised sidecar

`cmd/swarm-remote` runs as its own process under an external supervisor (macOS launchd LaunchAgent, Linux systemd user unit), never spawned by the daemon; it dials the daemon's remote socket in and the relay out; it holds no state a restart loses except live connections and the persisted `(K_epoch, epoch_id, relay-acked journal cursor)`. It is the only component parsing attacker-influenced relay bytes and must not share an address space with the PTY-owning, agent-spawning daemon.

### D6. Durable journal + two-phase idempotency (in the daemon)

A single daemon-wide append-only journal under `<stateDir>/journal/`, versioned records `(schema_version, cursor uint64, ts, session_id, type, payload)`, written at the `saveMetaLocked` **choke point** (covering `SetStatus`, `finalizeTerminal`, `Launch`, and the two `reconcile.go` startup transitions) plus a separate `Delete` tombstone hook — enumerate the choke point, never the callers. The journal append is a WAL-style step in the same recoverable commit as the meta write (no meta-without-journal or vice versa across a crash), fsync'd before its cursor is acked (D-5). Resume contract: "snapshot as of cursor N, then events after N," atomic. Flap debounce lives at the delivery layer (push-wakes + coalesced snapshots), never in the durable journal.

Idempotency is **two-phase** (residual R2 / audit-003 CRITICAL): a durable `prepared -> executing -> completed/failed` record keyed by `operation_id`, fsync'd **before** the side effect; for launch the `operation_id` is persisted as part of the existing two-phase session reservation (same fsync), so a crash between spawn and commit is resolved by reconcile against the reserved id, not by re-spawning. Replay returns the cached outcome and executes nothing. `interrupt` is **at-most-once** (SIGINT delivery is not verifiable from terminal state): its record resolves to `completed` or, after a mid-interrupt crash, to a terminal `outcome-unknown` state the phone surfaces — never a claimed exactly-once; `kill` (SIGKILL + terminal-state-verifiable) stays exactly-once-verifiable.

The async mailbox `seq` is the durable journal cursor (one coordinate; a gateway restart holds no independent counter). `recipient_key_id` is a routing hint **outside** the AEAD AAD, so the ciphertext under the shared K_epoch is identical for every recipient and the relay's per-device mailbox does the routing. EpochGrants are not in the journal-seq stream; they carry their own `(epoch_id, grant_seq)` per-device anti-replay coordinate and are mailboxed (so an offline-at-rotation device receives its grant on reconnect).

### D7. Input and approval semantics (residuals R3, R4 folded into D6)

Raw `input`/`resize` are **live-only** — they require a live connection holding the current lease generation and are never durably queued or replayed; on disconnect a queued keystroke resolves to an explicit "delivery unknown / not sent." Take-control opens a **signed one-shot `take_control` op** (device signature + a single biometric gate token) establishing a bounded authenticated control session (TTL + explicit end); keystrokes ride that session, not per-keystroke signatures. Discrete ops (interrupt/kill/approve/launch) each carry their own signature + gate token. Only high-level idempotent ops enter the offline queue.

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

**B17(b) is now FALSE.** It states that after a purge "the Android side must re-install it, without any
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
