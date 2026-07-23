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

- **`internal/protocol` `TestProtocol_JournalSubscribeOrderedAndEvictsWedged` fails on this
  machine even in isolation** (healthy subscriber receives ~190-310 of the required 320
  frames within the 15s deadline; count varies run to run). The test comment
  (`remote_journal_test.go:235-241`) documents it as inherently CPU-scheduling-sensitive
  and "reliable in isolation and on an unloaded CI box" — but it does NOT hold in isolation
  here. NOT caused by Phase A (no Phase A slice touches `internal/protocol`; the wedged-
  eviction property is a pre-existing fix-pack concern). Resolution options for the gate:
  make the eviction test deterministic (drive the fan-out via an injected clock / a
  synchronous overflow signal instead of a wall-clock throughput threshold), or run GG-4 on
  an unloaded CI box as the authors intended. Tracked here; must be green before Phase A closes.

## 3. End-of-phase gate (iterate until all pass)

1. Walk every A1-A8 criterion -> test/artifact in a per-slice evidence file (mirroring the
   existing fix-pack RED/GREEN evidence style).
2. Full GG-4 sweep across all touched packages.
3. Full `/audit-committee` (codex + agy + sonnet + opus), brief = this DoD + the Phase A
   diff. Any consensus blocker or unresolved divergence => fix and re-run. **Phase A closes
   only on a clean committee verdict**, then iterate to Phase B.
