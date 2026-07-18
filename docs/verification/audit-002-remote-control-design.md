# Audit 002: Remote-Control Design Proposal — Committee Report

Target: docs/research/remote-control-design.md (v1 draft). Committee: GPT-5.6 sol (codex,
read-only repo access), Gemini 3.5 Flash (brief-only), Sonnet (source-verified), Opus
(source-verified, security focus). All four reviewed independently against the same brief.
This report is the cross-examined synthesis; the design doc v2 incorporates every accepted
amendment.

## Verdict

Revise (unanimous direction-approval, unanimous rejection of the v1 draft's cost claims).
The E2EE-relay + typed-events + push direction matches prior art and the codebase's real
seams. What failed audit: every write-capable feature was priced as "additive" when the
source shows new capture, new schema, new data-plane fan-out, or a new approval mechanism;
the crypto was one scheme where two are needed; remote launch was under-scoped as an RCE
surface.

## Consensus (raised independently by 2+ members; all accepted)

1. Observer attach is a data-plane fan-out epic, not a capability flag (all four).
   Shim serves exactly one connection (internal/shim/server.go:38, serial acceptLoop); the
   daemon lease has one controller and one upstream; supersede works by close-and-reopen.
   Multi-subscriber needs new fan-out, backpressure, eviction, and invariants.
2. transcript_delta has no safe source today (all four). The transcript file is raw, lossy
   (repaint-collapse + drop-under-pressure) PTY bytes; ADR-002 explicitly rejected transcript
   replay for alt-screen TUIs. Grid-to-chat derivation is per-CLI screen scraping — the exact
   brittleness that killed Omnara's wrapper. Gate behind a spike on real CLI output.
3. interaction_request content does not exist at ingest (codex, sonnet, opus). Hook parsing
   keeps top-level string fields only (cmd/swarm/main.go parseHookStdin); adapters map
   PermissionRequest to a 4-value enum; tool_input (command, diff) is discarded. The adapter
   boundary is frozen (Epic 9) — extending it is its own ADR-level change.
4. Approval must bind to request identity, never keystrokes (codex, gemini, opus). A delayed
   approve must reference (machine, session, agent-instance, request-id, content-hash) and
   fail if stale. Opus C3 goes further: the CLI prompt is a synchronous in-PTY keystroke menu
   and hooks are fire-and-forget, so minutes-later approval may be architecturally unavailable
   without owning the runtime — mechanism must be resolved by spike before any approval UX is
   promised. "Approve-and-remember" removed until a scoped policy model exists.
5. Durable event journal belongs in the daemon, on disk (codex, opus). A gateway-held log
   dies with the process; subscribe is live-only; resume must survive daemon crash/upgrade
   (D-5) with an atomic snapshot-plus-cursor contract. Idempotency keys likewise persist
   daemon-side with cached outcomes (codex, gemini).
6. Remote launch is remote code execution and needs an authority model (codex, sonnet, opus).
   Existing revalidation proves argv safety, not authorization. Required: allowed cwd roots,
   forbid dangerously-skip-permissions/full-access remotely, no phone-supplied env (also fixes
   the ADR-006 billing-env incident sonnet flagged: LaunchReq.Env comes from the desktop
   shell today — a phone has none), worktree-by-default, per-device policy, confirm step.
7. Option B was strawmanned by omission (codex, gemini, sonnet). The honest comparison is
   Option A vs "Tailscale transport + minimal blind push broker" (now Option D). Option A's
   relay is also not "stateless-ish" once mailboxes, cursors, device registry, APNs custody,
   and abuse controls exist.
8. Two crypto schemes, not one (codex, opus). Noise XX (or equivalent) covers the live
   interactive transport only; offline mailbox delivery and APNs payloads need an async
   envelope scheme (sealed-box family) keyed to per-device long-term keys, with key epochs,
   history-across-rotation, and revocation semantics defined. "Noise XX vs NaCl" was a
   category error.
9. Push is an untrusted hint (codex, gemini). Ciphertext-only payloads; no sensitive fallback
   text in the outer payload (NSE can time out); every notification-launched action re-syncs
   authoritative state before enabling Approve. Self-hosted relays cannot hold the APNs
   signing key if the app is ever distributed — design the blind push gateway seam now
   (gemini; moot for a personal-only build, which also makes the $99/yr Apple account a hard
   Phase 0 dependency, not an open question).
10. Relay metadata is a real exposure (all four). E2EE hides payloads, not presence, timing,
    sizes, routing pairs, push cadence. The ADR gets an explicit metadata section; the
    "managed hosting leaks nothing" claim is withdrawn.
11. Pairing hardened (codex, gemini, opus). Single-use short-lived QR; mandatory local
    desktop confirmation (fail closed); pinned semantics of what the QR actually carries;
    relay-mediated remote revocation plus on-device biometric gate before mutating ops
    (device loss currently equals RCE via finding 6).
12. Scope contradiction resolved (sonnet). v1 goal includes spawn, but Phase 1 as drafted
    deferred it silently; phasing now states explicitly what ships when and why.
13. "Tamper-evident" audit log downgraded to signed local activity log unless checkpoints
    are anchored off-machine (codex).
14. Gateway defaults to a supervised sidecar, not in-process (opus, gemini): the one
    remotely-reachable parser of attacker-influenced bytes should not share an address space
    with the PTY-owning, agent-spawning daemon. In-process is the justified exception.

## Divergence

- Gemini argued transcript_delta requires building a stateful terminal emulator; swarm
  already has one — the true gap (grid state to semantic chat text) was identified by the
  source-reading members. Kept the source-verified framing.
- Opus (N4) considers SAS emoji verification marginal for a single owner scanning their own
  screen; gemini wanted maximal ceremony. Resolution: the mandatory desktop-side confirm is
  the anti-photograph control; SAS display is kept as a cheap visual check, not a separate
  ritual.
- Platform: codex and opus both call SwiftUI defensible; opus alone stresses that NSE/Live
  Activities need native modules under Expo anyway, so native's real cost is a second UI
  codebase for a solo maintainer. Recommendation stands (SwiftUI, iOS-first) with the
  tradeoff stated and Live Activities deferred.

## Blind spots (found by none; added by synthesis)

- Laptop sleep: N-7 means a sleeping machine pauses agents and drops off the relay. The
  phone must render "machine asleep" as a first-class state, and the relay should emit a
  "machine went silent mid-run" push, else away-usage silently dies with the lid.
- Relay abuse surface: pairing endpoints and mailboxes need rate limits and quotas from day
  one (codex listed adversarial tests, but no member scoped relay-side DoS).
- The mock itself: scenario UX (60 s approval expiry, stale rejection) is now load-bearing
  design; it must stay in sync with the spike outcomes or it over-promises exactly like the
  v1 draft did.

## Per-member signal

- GPT-5.6 sol: broadest sweep (14 findings); the two-protocol crypto split, durable
  idempotency semantics, push-as-hint, and the required adversarial test list.
- Gemini 3.5 Flash: APNs custody catch-22 and the blind push gateway; desktop confirm on
  pairing; traffic-shape obfuscation.
- Sonnet: source-verified the shim single-connection pin; the launch-env / ADR-006 billing
  regression; the v1-scope-vs-phasing contradiction; Option D naming.
- Opus: the approval-mechanism impossibility argument (C3) — the single most consequential
  finding; Noise-XX-is-interactive; daemon-side journal; sidecar default; the honest
  minimal v1 (remote grid attach + push).

## Disposition

All 14 consensus items and the three blind spots are incorporated in
docs/research/remote-control-design.md v2, which now: re-scopes Phase 1 to the provable core
(relay, pairing, E2EE, inbox from subscribe, remote grid attach, push on Group transitions),
gates chat/one-tap-approval UX behind three named spikes (S-A transcript derivation, S-B
interaction capture, S-C approval mechanism), adds Option D, splits the crypto design,
hardens launch policy, and moves the journal into the daemon.
