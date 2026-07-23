# Remote Control v1 — Work Breakdown (locked 2026-07-23)

Dependency-ordered plan to reach "drive and type into a real session from a phone."
Governed by ADR-007 (+ its 2026-07-23 amendment). Sizes: S = day, M = few days,
L = 1-2 weeks, XL = multi-week.

## Locked decisions (2026-07-23)

1. **First on-phone target: Android handset** (buildable here, no Apple account). iOS
   follows when Xcode + an Apple account exist. One shared gomobile phone-core, two thin
   native UIs (ADR-007 amendment §1).
2. **Full remote input in v1** via signed `take_control` + lease (amendment §2). v1 input
   surface = the designed terminal-peek + take-control screen, not the chat composer.
3. **Safety hardening lands in Phase A**, with the input backend, not deferred
   (amendment §3).
4. **The existing UI/UX design is the binding client spec** (amendment §4): design §8
   eight screens = phone-core output contract; Substrate/Void skin + tokens; mock pairing
   flow.

## What already exists — do NOT rebuild (verified in-tree)

- **Crypto** (`internal/remote/crypto`): frozen, cross-model reviewed. Noise pairing, SAS
  (36-bit), sealed mailbox, `MailboxReceiver`. Changes need an ADR.
- **Relay** (`internal/remote/relay` + `cmd/swarm-relay`): full server WITH a binary —
  rendezvous, ciphertext mailboxes, APNs push seam (`apns.go`), rate limiting.
- **Daemon remote tier**: `protocol.ServeRemote*` wired in `internal/skeleton/serve.go`
  (opens `remote.sock` when `RemoteSocketPath` set), `requireRemoteAuthz` choke point,
  durable kill-switch (off-until-paired), launch policy (env-drop + option denylist +
  allowed-cwd-roots, fail-closed), two-phase idempotency store.
- **Gateway runtime** (`internal/remotegw.Service`): journal-OUT bridge (ack-gated cursor,
  no silent event loss) + command-IN loop. **Library only — no binary, nothing starts it.**
- **Phone-core** (`internal/phonecore`): command builder, journal receiver (MailboxReceiver
  + monotonic SessionCache), op queue, accept. **Library, incomplete — no snapshot
  renderer, no gomobile surface, no client.**
- **Pairing / enroll / device registry** (`internal/remote/{pairing,enroll,device}`):
  libraries; no ops/CLI/TUI drive them yet.

Still-open safety items (beads / consolidated §5): relay round 3, kill/delete idempotency,
gateway GW-H2/M1/M2 — all pulled into Phase A below.

## Phase A — machine backend + control plane + input + hardening (proven with phonesim)

All on this machine; no app, no Apple account. Dependency-ordered.

- **A1. Daemon stands up the remote tier in production.** `swarm daemon` sets
  `RemoteSocketPath`; supervise the gateway process. (S)
- **A2. `cmd/swarm-remote` gateway binary** hosting `remotegw.Service` + launchd/systemd
  unit + `swarm remote init`. Includes remaining gateway reliability: GW-H2 (RelaySink seq
  = journal cursor, ADR D6), GW-M1 (MailboxAck + durable cursor), GW-M2 (inbound envelopes
  through `MailboxReceiver`). (M)
- **A3. Control-plane wire ops** (R-PROT.4/.5): `device_list`, `device_revoke`,
  `policy_query`, `pair_pending` / `pair_confirm` events. Prerequisite for A4. (S-M)
- **A4. `swarm remote {init,pair,devices,revoke,off,on,status}` CLI + TUI pairing-confirm.**
  SAS compare, byte-matching the mock flow (QR -> check both screens -> paired). (M)
- **A5. Full-input backend** (R-GW.6/.7): signed one-shot `take_control` op + input riding
  the lease; reopen remote `OpDataIn`/`OpAttach`/`OpResize` ONLY behind a valid take_control
  session (device sig + biometric gate token + current lease gen + authz). **Cross-model
  review (security-critical).** (L)
- **A6. Safety hardening** (pulled in per amendment §3): relay round 3 — CR-1 (per-source
  conn cap + cumulative handshake deadline), CR-4 (mailbox depth cap ON by default), HI-3
  (device-consent pairing proof + machine allowlist), ME-1 (atomic revoke that closes the
  live socket) — plus kill/delete through the idempotency store. **Cross-model review.** (M)
- **A7. Phone-core completion.** Snapshot renderer/sanitizer (GATING — a phone cannot render
  a terminal today; hostile-PTY-safe), pairing state machine, machine registry/presence,
  on-device persistence, launch builder, biometric gate token, capability negotiation,
  reconnect backoff+jitter. **Exported surface designed gomobile-bind-safe + a structural
  test that fails if the surface drifts off gomobile rules.** (L)
- **A8. `cmd/phonesim`** — drives the REAL phone-core end-to-end over a live relay. The 8
  R-E2E.3 adversarial scenarios (replay/reorder/dup, stale approval, revocation, QR theft,
  cross-machine substitution, daemon/shim crash mid-op, cursor compaction, hostile PTY at
  the renderer, APNs dup/expiry, concurrent desktop/phone control) are the acceptance
  floor. (M-L)

**Phase A exit:** phonesim pairs (SAS), observes (inbox + journal + snapshot cards),
launches (policy-gated), AND types (take-control) — the entire wire proven, no UI.

## Phase A execution status (live log)

Slices land RED->GREEN through the sonnet/opus TDD swarm; each verified (targeted tests +
build/vet + `-race`) and pushed. Order is by CRITICAL PATH to a working `phonesim`, not the
listed A1..A8 order.

**Done (pushed):**
- A1 — daemon binds the remote-tier socket in prod (opt-in, secure default OFF).
- A2/GW-M2 — command-IN through `crypto.MailboxReceiver` (replay/reorder rejected).
- A2/GW-M1 — command mailbox ack + durable cursor (no cross-restart command replay).

**Decisions:**
- **GW-H2 design LOCKED, deferred to pre-gate.** Live events seal `Seq = rec.Cursor` (ADR
  D6). The roster snapshot (deliberate `Cursor=0` records) must seal **boundary-anchored**
  seqs: for a snapshot as-of journal cursor `N` with `K` roster items, item `i` (0-indexed)
  seals `Seq = N-K+1+i` (a contiguous block ending at `N`; first live event is `N+1`). This
  is restart-consistent (D6) and collision-free. Requires the daemon snapshot to expose `N`,
  so it spans daemon + gateway + phonecore + a fixture update to the committed phonecore
  replay tests (asserted properties preserved). Reliability polish, NOT on the phonesim
  critical path -> implemented as a dedicated careful slice before the phase gate.
- **A2 `cmd/swarm-remote` binary deferred to after A3/A4** (it needs a paired device's
  content key + relay routing to do anything).

**A3 sub-slices + design decisions (from scouting):**
- A3.1 read ops (`device_list`, `policy_query`) — new `Control` fields `Devices []DeviceView` /
  `Policy *PolicyView`, new optional interfaces `DeviceLister`/`PolicyDescriber` on `coreAPI`
  (reuse `device.Registry.List()` / `remoteLaunchPolicy.roots`), cap-gated, non-mutating (no
  `requireRemoteAuthz`). New direct `Control` fields REQUIRE `protocol.md` field-table rows
  (GG-7 `TestProtocolMDBidi`). Cleanest; first.
- A3.2 `device_revoke` — mutating; uses a NEW `TargetDeviceID` field (NOT `DeviceID`, which is
  the caller's own auth id), new `ActionDeviceRevoke`, full `requireRemoteAuthz`, `coreAPI.
  RevokeDevice` -> `Registry.Remove`. Auto-off on last device is already automatic. KNOWN GAPS
  (document, do not claim done): does NOT purge the relay-side registration (-> A6 ME-1 atomic
  revoke), and any CapFull device can revoke any other (no admin tier).
- A3.3 pairing flow — DESIGN LOCKED (ADR-007 amendment 2026-07-23, "Pairing host: Option A").
  The DAEMON hosts pairing: owner-tier ops `pair_start` (trigger->QR) / `pair_pending`
  (SAS+name push) / `pair_confirm` (allow/deny) / `pair_result` (terminal), via a
  `PairingHost{BeginPairing(ctx,req,confirm,result)}` seam running `pairing.Machine.Pair` in a
  background goroutine over an injected `RendezvousTransport`. Sub-slices:
  - A3.3-a wire types (Op consts + Control fields + protocol.md rows) — now.
  - A3.3-b tier/cap gating (remote-tier refuses; CapPairing) — now.
  - A3.3-c ConfirmFunc<->wire bridge (PairingHost iface + Server closures; fake host + memRendezvous) — now.
  - A3.3-d enroll wiring (coreAPI implements PairingHost; real Machine.Pair->enroll->Add, keys injected) — now.
  - A3.3-e live-relay RendezvousTransport adapter (relay.DialRaw) — DOABLE NOW (see reconciliation).
  Deferred to A2/A7: sealed-grant delivery over the relay mailbox. Frozen pairing/enroll/crypto untouched.

**RECONCILIATION (2026-07-23, after A4 scoping) — supersedes earlier "blocked on A2" notes:**
- The pairing-host ADR decision (daemon makes its own ephemeral rendezvous `DialRaw`)
  **decoupled A3.3-e from the A2 gateway binary**: the pairing rendezvous is the daemon's
  transient dial, NOT the gateway's steady-state relay connection. A3.3-e needs only a
  RendezvousTransport adapter over `relay.DialRaw` + `relay.Client.Rendezvous*` + the relay
  URL/auth key from `swarm remote init` — all doable now, testable against an in-process relay
  (`cmd/swarm-relay` exists). So real relay-backed pairing is reachable without the gateway binary.
- **`swarm remote init` is FOUNDATIONAL, lands in A4** (not deferred to A2/A7): it provisions +
  persists the machine pairing identity (Noise-static + recipient X25519, grant-signing +
  relay-auth Ed25519) + relay URL under StateDir (0600, temp+rename+Sync), and serve.go loads it
  into `coreAPI.pairing` (tri-state: missing => nil/pairing-off; corrupt => daemon refuses to
  start). Needs a NEW machine-identity bundle type in internal/remote/crypto (no existing bundle
  fits both the 4-key shape and enroll.Enroll's raw-grant-key need). Follow-up: BeginPairing must
  populate QRPayload.RelayURL once init provides one (one line, easy to lose).

**A4 sub-slices (dependency-ordered, from scouting):** A4-0 CLI scaffold (`swarm remote` dispatch);
A4-1 `swarm remote init` (keystone, above); A3.3-e rendezvous adapter (now unblocked); A4-3
`devices` + A4-4 `revoke` (thin protocol.Client extensions, owner-tier no-sig); A4-6 kill-switch
manual override (NEW owner-tier-only wire op + durable ManualOff field, winning under device-count
auto-off; `off` process-sever half is a TODO until A2); A4-5 `pair` + A4-7 TUI confirm modal (share
a new async pairing-session protocol.Client API for the pair_pending/pair_result pushes — the one
"bigger than it looks" piece; TUI modal is mock-flow-licensed by the DoD); A4-2 `status`.

**A5 design (from scouting, 2026-07-23) — take_control input backend:**
take_control is a signed mutating op through the UNCHANGED requireRemoteAuthz (add
ActionTakeControl -> device.ActionControl in actionClass; CapFull only). It establishes a
lease through the UNCHANGED s.attach (inheriting genCounter + supersede), records a
per-connection `controlSession{deviceID,target,leaseGen,expiry}` on clientConn (mirrors
pairSession). DECISIONS: (1) the gate token binds via ContentHash = SHA256(gate_token) --
same mechanism handleLaunch uses for the launch spec, so the FROZEN crypto layer is NOT
edited (a first-class signed field would need an ADR); (2) single-use via the existing
idempotency.Store keyed by operation_id (crash-safe, no new store). Reopened remote-tier
OpDataIn/OpResize pass a four-clause gate: kill-switch on -> cc.control != nil -> now <
expiry -> control.target==attSession && control.leaseGen==attGen -> existing forwardInput
gen check. Bare remote OpAttach STAYS refused (only take_control attaches internally), so
the item-3 fail-close tests stay green. TTL daemon-clamped on coreAPI.now(); OpTakeControlEnd
clears the session. No new ADR (reopening is ratified by the 2026-07-23 amendment sec 2).
Server-side "biometric gate" = the daemon verifies a fresh device-signed single-use token
bound to this grant; the phone-side biometric UI is A7/deferred. GG-7 rows + a system-spec
invariant required. Sub-slices: A5-a take_control op+session+authz (now); A5-b reopen
OpDataIn/OpResize behind the session; A5-c gate token (ContentHash + idempotency single-use);
A5-d adversarial suite (14 attacks incl. -race) -> cross-model review gate. All doable
against the in-tree protocol/skeleton harnesses (E2E-over-relay is A8).

**A5 STATUS: COMPLETE + cross-model reviewed (2026-07-23).** A5-a (op+authz), A5-b (input
reopen behind the four-clause gate), A5-c (gate token + single-use), A5-d (14-attack
adversarial suite), A5-e1 (review hardening R3/R4/R5). codex + opus both verdict the gate
TRUSTED (docs/verification/remote-phaseA-a5-review.md). Tracked follow-ups from the review:
- **R2 (next hardening slice):** make the remote tier fail-CLOSED when OperationClaimer /
  KillSwitch are absent (today they fail open; production wires them, so NOT a live exploit,
  but a future misassembly via a DaemonAPI decorator would silently lose single-use +
  kill-switch). Requires a test-contract refactor (stub implements the now-mandatory
  interfaces; take_control helper sends a gate token) preserving all assertions -- a careful
  dedicated slice, not a rushed one.
- **R1 + R7 -> A7 BLOCKERS:** decide the end-to-end keystroke-transport trust boundary (the
  input producer doesn't exist yet; the relay can't forge; the gateway data-plane integrity
  is the owner-uid residual -- decide before real phones type) and have the phone sign the
  session lifetime (bind to the signed ExpiresAt).
- **R6 -> L3/DME-1 constraint:** idem-GC TTL must exceed max command validity; persist ExpiresAt.

**Reprioritized critical path (next):** A3 control-plane ops -> A4 pairing CLI/TUI ->
A2 gateway binary -> A7 phone-core (pairing SM + snapshot renderer + gomobile surface) ->
A5 full-input backend -> A8 phonesim. A6 hardening runs in parallel with A5 (both touch the
input blast radius). GW-H2 slots in before the end-of-phase gate.

**Known GG-4 blocker:** pre-existing `TestProtocol_JournalSubscribeOrderedAndEvictsWedged`
timing flake (see `docs/verification/remote-phaseA-dod.md` §2b) — not Phase A's, must clear
before the gate.

## Phase B — Android handset (the v1 milestone)

- **B1. gomobile bind** phone-core -> Android AAR; enforce the surface contract from A7. (M)
- **B2. Shared design tokens** extracted from `remote-control-design-directions.html`
  (Substrate/Void, light+dark) into one source both clients consume. (S)
- **B3. Thin Android UI** implementing the design §8 v1 screens: pairing (SAS), triage
  inbox (4 Groups + machine switcher), machines pane (presence/paired/revoke/kill-switch/
  activity), session detail (journal + snapshot cards), terminal peek + take-control
  keyboard, settings (push toggles, quiet hours, biometric gate). (XL)
- **B4. Android push (FCM)** wake on Group transitions, ciphertext only. Relay has the
  APNs seam (`apns.go`); add the FCM path beside it. (M)

**Phase B exit:** your Android phone pairs, observes, launches, and types into a real
session over the untrusted relay.

## Phase C — hardening-to-connect remainder, ops, iOS

- **C1.** Remaining R-ADV floor as an evidenced suite + R-E2E rollup + R-GATE.3. (S-M)
- **C2. Relay ops:** Dockerfile / systemd unit, TLS termination runbook, VPS provisioning,
  key-backup UX, onboarding docs. (M)
- **C3. On-device cross-language SAS KAT** (Android first, then Swift): both clients must
  produce the identical six emoji from the same channel binding. (S, on-device)
- **C4. iOS:** SwiftUI app + notification service extension + design system, gomobile
  xcframework, D12 on-device release gate (archive, killed-app push, NSE timeout, biometric
  cancel, Keychain-after-reboot). (XL — needs Xcode + Apple account)

## Held for Phase 2+ (NOT v1)

Chat transcript view (spike S-A), structured one-tap approval sheets (S-B/S-C), live launch
execution polish, voice, quick replies, quiet hours, activity-feed depth, Live Activities,
optional tsnet direct path (Option D accelerator). An `approve` capability class exists as
scaffolding — there is NO approve wire op or daemon workflow yet; do not mistake the
scaffolding for the feature.

## Execution rules

TDD with evidenced failing-first (RED) runs under `docs/verification/`; never weaken a test
to make it pass. Cross-model review (codex + independent opus) on A5 (input) and the A7
gomobile surface. Implementers are sonnet/opus subagents (never fable/haiku for this work).
`-race` on every package that spawns goroutines. Beads is NOT used in this worktree (its
bd config is broken here — do not `bd init`); this doc is the tracked breakdown.
