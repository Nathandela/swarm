# Phase A — Definition of Done (validation goals, audit-committee-aligned)

Locked 2026-07-23. Phase A = the swarm remote **machine backend + control plane +
full-input backend + safety hardening**, proven end-to-end by `cmd/phonesim` WITHOUT any
mobile app. This document is the measuring stick: Phase A is DONE only when every slice
criterion below is demonstrably true (evidence under `docs/verification/`) AND a full
`/audit-committee` pass returns no unresolved blocker. Governed by ADR-007 + its
2026-07-23 amendment; work items map to `docs/research/remote-v1-roadmap.md` Phase A.

## 0. Standing quality gates (every slice, non-negotiable)

- **GG-4**: `go build/vet/test ./...` green; `-race` on every package that spawns goroutines.
- **GG-5 (TDD)**: failing-first (RED) evidenced — ordered commits + recorded output — before
  GREEN. Never weaken a test to make it pass; if a test seems wrong, stop and discuss.
- **Process**: independent roles (separate test-writer / implementer / reviewer subagent
  instances); sonnet/opus only for this work (never fable/haiku).
- **Cross-model review** (codex + independent opus, recorded as an evidence file) on the two
  security-critical slices: **A5** (reopening remote input) and **A7** (gomobile surface +
  hostile-PTY renderer).
- **No drift**: any deviation from ADR-007 produces an amendment; protocol changes are
  field-drift-checked against `protocol.md` (GG-7).

## 1. Audit-committee alignment (the adversarial bar)

Phase A is authored to survive the committee mandate: *"assume this is flawed; find
correctness bugs, edge cases, false assumptions, security and performance risks, missing
tests, and simpler alternatives."* Every slice must answer all five:

1. **Correctness** — does it do exactly what the criterion says, including the failure path?
2. **Security vs the threat model** (a phone drives code-editing agents on a personal
   machine through an *untrusted* relay) — does the slice widen the attack surface, and if
   so is every new byte authorized, bounded, and fail-closed?
3. **Edge cases** — crash/restart mid-op, disconnect, replay/reorder/dup, hostile input.
4. **Tests** — is the property asserted by a test that fails if the property breaks?
5. **Simplicity** — is there a smaller correct design?

The end-of-phase gate (§3) runs the full committee; individual slices bake these five in.

## 2. Per-slice validated criteria

**A1 — daemon stands up the remote tier in production**
- `swarm daemon` binds the dedicated remote-tier UDS when (and only when) the operator
  opts in via env; unset => no remote socket (**secure default: remote control OFF**).
- The bound socket is the remote tier (every connection remote-origin, authorized against
  the pinned device registry) — never the owner-trusted socket.
- Tests: env-set => `Config.RemoteSocketPath` wired to it; env-unset => empty.

**A2 — `cmd/swarm-remote` gateway binary + gateway reliability**
- A runnable binary hosts `remotegw.Service` (journal-OUT + command-IN) over one relay
  connection; a gateway crash leaves the daemon and its sessions untouched (S1) and resumes
  from the last durable cursor.
- launchd/systemd unit + `swarm remote init`.
- GW-H2 (RelaySink seq = journal cursor, ADR D6), GW-M1 (MailboxAck + durable cursor),
  GW-M2 (inbound envelopes through `MailboxReceiver`) closed with tests.

**A3 — control-plane wire ops**
- `device_list`, `device_revoke`, `policy_query`, `pair_pending`/`pair_confirm` events
  implemented, authorized at the remote choke point, field-drift-checked against
  `protocol.md` (GG-7). Prerequisite for A4.

**A4 — `swarm remote` CLI + TUI pairing confirm**
- `swarm remote {init,pair,devices,revoke,off,on,status}` drive the REAL registry/pairing.
- TUI pairing-confirm shows the SAS; it equals the SAS the peer shows (mock flow). `off`
  severs the gateway; `on`/`off` flip the durable kill switch.

**A5 — full-input backend (SECURITY-CRITICAL, cross-model reviewed)**
- A signed one-shot `take_control` op establishes a bounded (TTL + explicit end) lease-bound
  control session; keystrokes ride it (no per-keystroke signature).
- Remote `OpDataIn`/`OpAttach`/`OpResize` reopen ONLY inside a valid take_control session
  (device signature + biometric gate token + current lease generation + `requireRemoteAuthz`);
  outside it they stay fail-closed.
- Adversarial tests: replay/reorder/dup of take_control, expired session, wrong lease
  generation, missing gate token, kill-switch-off — each refused with the stable error taxonomy.

**A6 — safety hardening (cross-model reviewed)**
- Relay: per-source concurrent-connection cap + cumulative handshake deadline; mailbox depth
  cap ON by default; atomic revoke that closes the live socket; device-consent pairing proof
  + machine allowlist.
- kill/delete routed through the two-phase idempotency store (a replayed op returns the
  cached outcome exactly once).
- Tests assert each bound: over-cap rejected, revoke drops the live connection, replayed
  kill is idempotent.

**A7 — phone-core completion (SECURITY-CRITICAL surface, cross-model reviewed)**
- Snapshot renderer/sanitizer: turns a live VT stream into a phone-safe snapshot; hostile
  PTY content cannot escape the render (no control-sequence injection at the phone).
- Pairing state machine, machine registry/presence, on-device persistence, launch builder,
  biometric gate token, capability negotiation, reconnect backoff+jitter.
- The exported surface obeys gomobile bind rules; a structural test FAILS if the surface
  drifts off those rules (no generics/unsupported types on the boundary).

**A8 — `cmd/phonesim` + acceptance floor**
- phonesim drives the REAL phone-core end-to-end over a live relay + gateway + daemon:
  pair (SAS), observe (inbox + journal + snapshot cards), launch (policy-gated), type
  (take-control).
- The adversarial scenarios (replay/reorder/dup, stale approval, revocation, QR theft,
  cross-machine substitution, daemon/shim crash mid-op, cursor compaction, hostile PTY at
  the renderer, APNs dup/expiry, concurrent desktop/phone control) pass as the acceptance floor.

## 2b. Known GG-4 blockers discovered during Phase A (must clear before the phase gate)

- **RESOLVED 2026-07-24 (commit 3824a7a).** `TestProtocol_JournalSubscribeOrderedAndEvictsWedged`
  flaked on loaded machines because it asserted the healthy subscriber received
  >= eventQueueCap+64 frames within a 15s wall-clock window — a throughput/rate gate that
  false-fails under CPU starvation (the healthy sub is alive but slow; ~190-310 of 320).
  Fixed by observing eviction DIRECTLY at its source of truth: `distributeJournal` removes a
  wedged subscriber from `srv.jsubs` on queue overflow, and the test is `package protocol` so
  it polls `len(srv.jsubs)` under `srv.jsubMu` (2->1) instead of the frame-count proxy. Both
  guarded properties are asserted directly and more strongly (eviction: map removal +
  `remaining==1` healthy-survives + wedged conn torn down; ordering: unchanged strictly-
  increasing cursor check). Test-only change, zero production changes. Verified 6/6 `-race`
  on the box the old test failed on; mutation checks (eviction disabled -> FAIL; concurrent
  `go distributeJournal` -> ordering FAIL) confirm the assertions retain teeth. The full-suite
  GG-4 blocker for the phase gate is cleared.

## 3. End-of-phase gate (iterate until all pass)

1. Walk every A1-A8 criterion -> test/artifact in a per-slice evidence file (mirroring the
   existing fix-pack RED/GREEN evidence style).
2. Full GG-4 sweep across all touched packages.
3. Full `/audit-committee` (codex + agy + sonnet + opus), brief = this DoD + the Phase A
   diff. Any consensus blocker or unresolved divergence => fix and re-run. **Phase A closes
   only on a clean committee verdict**, then iterate to Phase B.

## 4. Remaining work + decisions (2026-07-24, code-grounded)

Status: A1/A3/A5 DONE; A2 mostly (GW-H2 boundary-anchored seq design-locked pre-gate; G3
launchd unit off-path); A6 core DONE (HI-3 deferred, zero live call sites); A7 CODE COMPLETE
(renderer eddf356..2bf0fd7 + input data-plane e55c26f..5c30d51, + input Slice 7 E2E). A4 and
A8 remain, plus the required gate artifacts.

**A4 slices (order: off/on -> status -> pairing-API -> pair CLI -> TUI modal):**
- **A4-off/on (durable manual kill switch):** add `ManualOff bool` to skeleton `remoteState`;
  `RemoteControlEnabled()` becomes `devices.Count()>0 && !manualOff`; new owner-tier
  `OpRemoteSetControl` (refused on the remote tier, CapPairing-gated) + `Client.SetRemoteControl`
  + a coreAPI setter over the existing `writeRemoteState` durable path; `serve.go` loads it.
  **Decision — "off severs the gateway":** scope `off` as durable-disable + DAEMON-SIDE
  severance (the daemon re-checks the flag as the first gate on every remote mutating op AND
  input-gate clause-1, so `off` immediately halts all remote ops + in-flight keystrokes at the
  daemon choke point). Killing the separate `swarm-remote` PROCESS is a follow-up gated on G3
  supervision — recorded, not blocking; the security-meaningful severance is at the daemon.
- **A4-status:** `swarm remote status` reads the same durable state (devices via ListDevices,
  manualOff + Count, config-file presence). No new read op.
- **Async pairing client API (prerequisite for pair CLI + TUI):** `Client.StartPairing()` ->
  `{SAS <-chan, Confirm(bool), Result <-chan}`, with new `dispatchControl` cases routing
  `OpPairPending`/`OpPairResult` pushes to the pairing session (server side already exists).
- **A4-pair CLI** + **A4-TUI pairing-confirm modal** (mock-flow test licensed) over that API.

**A8:** the adversarial acceptance floor is ALREADY pinned at the unit/integration level for
every R-E2E.3 scenario (evidence rollup will cite them) EXCEPT **#9 APNs dup/expiry** —
**DECISION: defer to Phase C** (Phase A ships no live push; recorded here). Net-new A8 work =
phonesim `DriveLaunch` + `DriveTakeControl`/type (Slice 7) so the phonesim E2E proves the full
pair/observe/launch/type DoD sentence, + the evidence rollup.

**Required gate artifacts (before the §3 committee):**
- **A7 cross-model-review evidence file** (DoD §0 requires codex + opus on A7) — MISSING, run next.
- Per-slice A1-A8 RED/GREEN evidence walk files (only a5 exists today).
- **GW-H2** boundary-anchored roster seq (design-locked, deferred) — pre-gate slice or an
  explicit deferral note.
