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
