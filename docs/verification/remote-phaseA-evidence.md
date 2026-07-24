# Phase A — DoD Walk / Evidence (2026-07-24)

The measuring stick is `docs/verification/remote-phaseA-dod.md`. This file walks every A1-A8
criterion to the test/artifact that demonstrates it (DoD §3.1), so the end-of-phase
`/audit-committee` (DoD §3.3) has the map. Standing gates: `go build/vet/test ./...` green with
`-race` (verified under 6x CPU load), TDD failing-first evidenced per slice (ordered commits),
cross-model review on A5 + A7.

## A1 — daemon stands up the remote tier (opt-in, secure default OFF)
- `swarm daemon` binds the dedicated remote-tier UDS only when the operator opts in; unset => no
  remote socket. The bound socket is the remote tier (every conn remote-origin).
- Evidence: `internal/skeleton` remote-socket wiring tests; `Config.RemoteSocketPath` env-gated.
  Committed pre-session (A1 done, roadmap live log).

## A2 — cmd/swarm-remote gateway binary + gateway reliability
- `cmd/swarm-remote` hosts `remotegw.Service` (journal-OUT + command-IN + input plane) over one
  relay connection; a gateway crash leaves the daemon + sessions untouched (S1).
- GW-M1 (mailbox ack + durable cursor) + GW-M2 (inbound envelopes through `MailboxReceiver`)
  closed. `swarm remote init` provisions machine keys.
- Evidence: `cmd/swarm-remote` (config.go/main.go + service_test.go); `internal/remotegw`
  command_in / ackcursor tests; the phonesim E2E stands up the real Service.
- **DEFERRED (design-locked, tracked): GW-H2** boundary-anchored roster seq (live events seal
  Seq=rec.Cursor, ADR D6). NOT a v1 correctness blocker: journal resume works today via the
  MailboxAck durable cursor (GW-M1) + full-resync-below-floor; GW-H2 is a resume-robustness
  refinement and would re-touch the just-hardened RelaySink seq scheme (finding E), so it is
  deferred to avoid destabilizing it. The **launchd/systemd unit + daemon-supervised
  swarm-remote (G3)** is off the phonesim critical path, deferred. Flagged for the committee.

## A3 — control-plane wire ops
- `device_list`, `device_revoke`, `policy_query`, `pair_start/pending/confirm/result` implemented,
  authorized at the remote choke point, field-drift-checked (GG-7).
- Evidence: `internal/protocol` device/policy/pairing tests; `TestProtocolMDBidi_FieldSetMatchesStructs`
  + `TestProtocolMD_DocumentsEveryOp` green. Pairing host = the daemon (ADR Option A).

## A4 — swarm remote CLI + TUI pairing confirm
- `swarm remote {init,pair,devices,revoke,off,on,status}` drive the real registry/pairing/state.
- Evidence (this session): `178d3c5` off/on (durable manual kill switch — ManualOff wins; owner-tier
  only; off severs at the daemon choke points live); `d1a276d` status; `4013860` async pairing
  client API (StartPairing, fail-closed); `b14c41e` pair CLI (real StartPairing round trip, local
  desktop confirm, fail-closed); `3ddf87a` TUI pairing-confirm modal (mock-flow, renders the SAS =
  peer's SAS). Tests named in each commit; -race green across skeleton/protocol/cmd/tui.

## A5 — full-input backend (SECURITY-CRITICAL, cross-model reviewed)
- Signed one-shot take_control establishes a bounded lease-bound control session; keystrokes ride
  it (no per-keystroke signature). Remote OpDataIn/OpResize reopen ONLY inside a valid session
  (device sig + gate token + lease generation + requireRemoteAuthz); outside it fail-closed.
  Adversarial: replay/reorder/dup, expired, wrong generation, missing gate token, kill-switch-off
  each refused with the stable taxonomy.
- Evidence: `internal/protocol` takecontrol_* tests; `docs/verification/remote-phaseA-a5-review.md`
  (codex + opus, verdict TRUSTED) + `remote-phaseA-a5-adversarial.md` (14 attacks -> mechanism ->
  test). R2-R8 follow-ups closed (fail-closed construction guard, R7 signed-ExpiresAt binding, etc.).

## A6 — safety hardening (cross-model reviewed)
- Relay: per-source concurrent-conn cap + cumulative handshake deadline (CR-1); mailbox depth cap
  ON by default (CR-4); atomic revoke that closes the live socket (ME-1). kill/delete through the
  two-phase idempotency store — a replayed op returns the cached outcome once (DHI-3).
- Evidence: `internal/remote/relay` + `internal/protocol` idempotency tests; the DHI-3 owner-tier
  regression (idempotency leaked onto the owner tier) was caught by the full-suite gate and fixed
  (`b50292e`, remote-tier-gated).
- **DEFERRED (tracked): HI-3** device-consent-proof artifact + machine allowlist — UNDER-SPECIFIED
  (needs an ADR amendment) AND has ZERO production call sites today (daemon-hosted pairing; the
  relay's authorize_device is unused), so no live attack surface to harden. Defensible deferral;
  flagged for the committee.

## A7 — phone-core completion (SECURITY-CRITICAL surface, cross-model reviewed) — VALIDATED SOUND
- Snapshot renderer/sanitizer turns a live VT stream into a phone-safe snapshot; hostile PTY cannot
  escape (no control-sequence injection at the phone). Pairing SM, machine registry, persistence,
  launch builder, gate token, capability negotiation, reconnect backoff — the input data-plane
  (typing) + terminal peek (observing).
- Evidence: 15 slices (`eddf356`..`f06fcc9`) + fix-pack (`5abd036`, `50f7785`, `f8ae70d`, `ba1ef77`,
  `de59343`, `a6b4971`) + teardown fix (`7de9515`) + flake hardening (`8044fdb`).
  `docs/verification/remote-phaseA-a7-review.md`: TWO full cross-model cycles (codex + opus) +
  a focused confirmation. Cycle 1 caught a CRITICAL live relay-driven cross-session misroute + 7
  more; cycle 2 caught the kill-switch teardown/recovery incompleteness; ALL fixed. The security
  core (no injection, no content leak, R7/authz/replay, sanitization) confirmed sound across BOTH
  cycles. VERDICT: A7 SOUND. Full suite green under 6x load.

## A8 — cmd/phonesim + acceptance floor
- The phonesim drives the REAL phone-core end-to-end over a live in-process relay + gateway +
  daemon: pair (SAS/enroll), observe (journal cards + terminal snapshot), launch (policy-gated),
  type (take-control), observe-terminal (server-rendered peek), kill.
- Evidence (`internal/skeleton/phonesim_e2e_test.go`): TestPhonesim_PairObserveKillE2E,
  TakeControlTypeE2E (+ end-to-end replay drop), ObserveTerminalE2E, ObserveTerminalRecovers-
  AfterKillSwitchToggle (OFF->ON), LaunchE2E (+ disallowed-cwd refused). `d766626`.
- **Adversarial floor**: every R-E2E.3 scenario is pinned at the unit/integration level (matrix in
  the A4/A8 grounding): replay/reorder/dup (crypto seq gate + kill_idempotency + the phonesim
  end-to-end replay drop), stale approval/expired (takecontrol_expiry), revocation (ME-1 +
  devicerevoke), QR theft/SAS (pairing/*), cross-machine substitution (takecontrol_forgedtarget +
  the A7 sealed-session-id misroute fix), daemon/shim crash mid-op (launch_crash_replay +
  idempotency), cursor compaction (journal + ackcursor), hostile PTY at the renderer (vt SnapText +
  daemon terminalrender + the A7 hostile-PTY E2E), concurrent desktop/phone control (lease +
  takecontrol_input + the A7 tap). **APNs dup/expiry: DEFERRED to Phase C** (Phase A ships no live
  push) — recorded in the DoD.

## Deferred, with justification (for the committee)
- **GW-H2** (A2): design-locked resume-robustness refinement; v1 resume works via GW-M1 + full-resync;
  deferred to avoid re-destabilizing the hardened RelaySink seq.
- **HI-3** (A6): under-specified + zero live call sites; needs an ADR amendment; no attack surface.
- **APNs dup/expiry** (A8): no live push in Phase A; Phase C.
- **G3** (A2): launchd/systemd supervision; off the phonesim critical path.
- A7 non-blocking follow-ups (per-backoff re-blank tidy; latent-unreachable peekGen window; bounded
  teardown latency) — logged in the A7 review evidence.

If the committee rejects any deferral as a v1 blocker, it is implemented and the gate re-runs.
