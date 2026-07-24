# A7 Cross-Model Review Evidence (2026-07-24)

Per DoD §0, A7 (renderer + input data-plane) is a security-critical slice requiring cross-model
review (codex + independent opus) recorded as an evidence file. This is it. Panel: codex
(GPT-5.6 sol, read-only) + an independent opus agent, both given the same threat model + the full
A7 diff (eddf356~1..f06fcc9), both told to assume the work is flawed and read the actual code.

## Verdict

**A7 is NOT sound as committed — a fix-pack is required before A7 closes.** The 15 A7 slices are
individually correct (each RED->GREEN, per-slice reviewed), but the holistic adversarial review
found real CROSS-SLICE and INTEGRATION defects that per-slice review missed. Both models
independently converged on the same top findings. This is exactly the value of the gate.

**The core security properties I built HOLD** — both models tried and found NO bypass on: peek
input-injection (three read-only layers), pump raw-byte suppression to a remote controller, R7
lifetime binding, the single-Accept seq gate (no double-Accept), and replay/reorder/dup rejection.
Frozen crypto unchanged. The defects are in cross-slice routing, renderer composition/fidelity,
concurrency, and one un-composed E2E path — not in the crypto or the authz gates.

## Reconciled findings (consensus unless noted)

### A — CRITICAL, LIVE: cross-session input misroute via a dropped take_control + ignored Gap
Both confirm. Input frames carry NO session id (`internal/phonecore/input.go`); the gateway routes
them by the mutable `currentSession` (`internal/remotegw/command_loop.go` routeInput), and
`OpenMailboxFrame` DISCARDS `res.Gap` (`internal/remotegw/mailbox_in.go`). Attack (relay is the
adversary, no forging): phone controls A (focus=A), then take_control B (different sessions coexist
in the LeaseManager map — Begin only supersedes same-session). The relay DROPS the take_control B
envelope and delivers B's next input frame. `Accept` passes (seq>hi) and sets Gap=true, but Gap is
ignored; `currentSession` is still A; the input rides A's LIVE lease. The daemon gate cannot catch
it — the frame legitimately matches A's lease (controlGateOpen clause 4). B's keystrokes execute in
A. **Fix (two independent, do both): bind the target namespaced session id into the signed input
frame and route by it; AND honor `res.Gap` — a gap clears focus and drops input until a
successfully-processed take_control re-establishes it.**

### B — HIGH: render loop starts an UNSEEDED emulator (loses initial screen)
Both confirm. `internal/daemon/terminalrender.go`: `renderInitial` pushes the initial snapshot, then
`vt.NewEmulator(cols,rows)` creates an EMPTY grid never seeded with the initial content. The first
live frame renders a near-blank grid; the phone's latest-wins cache overwrites the good initial
render. Masked by tests (initial-snapshot test sends no live frames; burst test uses an empty
initial). **Fix: seed the emulator from the initial snapshot (`emu.Feed(vt.RenderSnapshot(initial))`),
exactly like `seedMirror`.**

### C — HIGH: peek survives a mid-stream kill-switch flip
Both confirm. The kill switch is checked ONLY at subscribe (`server.go` handleTerminalSubscribe
first gate); the render push closure never re-reads `RemoteControlEnabled()`. So `swarm remote off`
(or revoking the last device) does NOT blank an established peek — contradicting the handler's own
comment. Contrast controlGateOpen clause 1 (re-checked every keystroke). **Fix: re-check
`cc.killSwitch()` in the push closure and terminate the peek when disabled.**

### D — MEDIUM: tap teardown TOCTOU race
Both confirm. `internal/skeleton/sessiontap.go` `removeSub` computes `last` under t.mu, RELEASES
t.mu, then calls `teardown` (which re-locks + sets closed). A `subscribe` interleaving in that window
(t.closed still false) registers a NEW sub, which teardown then evicts + closes the upstream under.
User-visible as a fresh attach whose channel closes immediately (self-recovers via subscribe's
retry-on-closed). **Fix: set `closed` atomically with the last-check under one t.mu hold (or teardown
re-checks `len(subs)==0`).**

### E — MEDIUM (latent): RelaySink.seal releases the lock before append -> out-of-order seq
Both confirm. `internal/remotegw/relaysink.go` `seal` holds `s.mu` for `seq++` then RELEASES before
`MailboxAppend`. Once RunJournal + RunTerminal run concurrently (they share one RelaySink + one phone
seq stream), concurrent seals can append out of seq order -> the phone drops the lower-seq frame as
ErrStaleSeq + spurious gap/resync. Latent because RunTerminal isn't composed yet (see #2). **Fix:
hold `s.mu` across the append.**

### #2 — CRITICAL (wiring): the terminal PEEK is not composed end-to-end
Both confirm (opus: "REFUTE — not wired"). `Service.Run` starts only runJournal + command bridge;
nothing calls `RunTerminal` in production; `phonecore.MailboxRouter` is constructed only in tests;
the phonesim decodes journal only. The renderer/gateway/decoder are correct unit-islands but not a
working capability (unlike the INPUT path, which the phonesim proves E2E). Opus nuance: `RunTerminal`
DOES namespace its forwarded snapshot session; the gap is that the subscribe REQUEST needs a
watch-session and Service must supervise the loop. **Fix: Service runs RunTerminal for the phone's
requested watch-session; RunTerminal sends the session id in terminal_subscribe; the phone uses
MailboxRouter; add a phonesim observe-terminal E2E.**

### #7 / J — MEDIUM: peek write-failure doesn't terminate + unchunked snapshot can exceed MaxFrame
Both. The peek push closure ignores `writeFrameDeadline` errors (repeated 5s stalls, retained
renderer/tap), and the peek snapshot is a SINGLE unchunked TControl frame — a large grid (up to
1000x1000, forwardResize-reachable) can exceed `wire.MaxFrame` and be silently dropped (the
interactive snapshot IS chunked; the peek isn't). **Fix: cancel the render ctx on the first
write/encode error; cap or chunk the peek snapshot.**

### F — MEDIUM (design/ADR): owner supersede seeded from the lossy mirror
Opus. When a peek or a remote lease keeps the tap alive, an owner supersede becomes a LATE tap
subscriber seeded from `mirror.Snapshot()` (the lossy `RenderSnapshot` projection drops SGR pen +
title + scrollback), NOT a fresh shim re-dial — so the owner can be repainted missing pen/title vs
the ADR-002 fresh-dial guarantee. Only the sole-subscriber case is byte-identical. **Fix or
ADR-accept: for personal v1 the fidelity loss on a concurrent-peek supersede is minor; document it
(ADR) or re-dial a fresh snapshot for an owner supersede when the tap has other subscribers.**

### G — MEDIUM (design/ADR/spec-drift): concurrent multi-tier control vs P-5
Opus. Owner (d.srv) + remote take_control (d.remoteSrv) hold independent read-write leases on the
SAME PTY via the shared tap; input interleaves; neither supersedes nor notifies the other. system-
spec P-5 + ADR-002 say "exclusive controller, one per session in v1" — no ADR records the shift. The
local user isn't locked out or notified when a phone drives their shell. **Decision needed (ADR):**
for the PERSONAL single-owner v1 (owner == phone user, same person) concurrent control is acceptable
and is documented as such; owner-notification when a phone holds a lease is a recommended hardening.
Multi-user exclusivity is a later concern. (Recorded as an operator-facing decision; personal-v1
default is accept-with-ADR.)

### H, K — LOW / note
- H: peek requires no per-device signature (gated by global kill switch + cap, like journal_read) —
  consistent with the read tier + gateway-owner-uid residual; matters more given C, addressed by the
  C fix (kill switch severs peeks).
- K: SnapText keeps Unicode U+2028/2029/202E (not terminal escapes; a phone renderer could
  line-break/bidi-spoof) — very low; a phone-UI hardening, not a terminal-escape.

### #8 — MEDIUM: journal-eviction test residual flake
codex saw `TestProtocol_JournalSubscribeOrderedAndEvictsWedged` fail 4/5 under its heavy load
(the HEALTHY subscriber's drainer starves -> its queue overflows -> it also evicts -> remaining==0).
Opus's run passed; my re-run passed 5/5 under 4-core `yes` stress (could NOT reproduce codex's
profile). Real-in-principle residual: the healthy sub's survival depends on winning a scheduling
race under sufficient starvation. **Fix: make the healthy sub's survival load-independent (bound the
flood / synchronize the healthy drain so its queue can't overflow), not a wall-clock/throughput
race.** Lower priority than A-E.

## Fix-pack plan (order)
1. **A7-fix-A (CRITICAL, LIVE):** session id in the signed input frame + route by it + honor Gap.
2. **A7-fix-render (HIGH):** B (seed emulator) + C (per-emission kill-switch) + #7/J (write-error
   termination + chunk/cap the peek snapshot) — all in the render/peek path, land together.
3. **A7-fix-concurrency (MEDIUM):** D (tap TOCTOU) + E (RelaySink seq order).
4. **A7-fix-wiring (#2):** compose the terminal peek E2E (Service RunTerminal + subscribe session id
   + phone MailboxRouter + phonesim observe-terminal test) — after render fixes land.
5. **A7-fix-flake (#8):** harden the journal-eviction test.
6. **ADR amendments (F, G):** document the personal-v1 concurrent-control + supersede-seed decisions.
7. Re-verify (full -race) + a focused re-review of the fixes.

Core authz/crypto/injection/pump/R7 properties confirmed sound by both models; the fix-pack is
integration + renderer composition + concurrency + one live routing bug, not a crypto/gate rework.

## Fix-pack landed (2026-07-24)

Every consensus finding is fixed, each RED->GREEN with independent review:
- **A (CRITICAL, LIVE)** — `5abd036`: input frames now carry the target session id sealed INSIDE
  the frame (AEAD-protected — the relay can drop/reorder but not alter it) and route by it, not
  by mutable focus; `res.Gap` is honored (a gapped/empty-session frame is dropped). RED reproduced
  the misroute (a keystroke sealed for A delivered to B); GREEN routes by sealed session.
- **B/C/#7/J (HIGH/MED)** — `50f7785` (+ tests in `de59343`): the render loop seeds the emulator
  from the initial snapshot; the peek re-checks the kill switch before every emission (cancels on
  flip-off); terminates on the first write error; and clips the render to a 300x200 viewport so it
  can't exceed MaxFrame.
- **D/E (MED)** — `f8ae70d`: the tap sets `closed` atomically with the last-detection (TOCTOU gone,
  a concurrent subscribe re-dials); RelaySink holds the lock across seal+append (no out-of-order seq).
- **#8 (MED)** — `ba1ef77`: the journal-eviction test's healthy-sub survival is now load-independent
  by construction (lockstep flood/drain bounds its queue < cap); 10/10 under 8x load, mutation-checked.
- **#2 (CRITICAL, wiring)** — `de59343`: the terminal peek is composed end-to-end (RunTerminal carries
  the session id; a supervised TerminalWatcher runs it per watched session; the phone requests a watch
  via an unsigned terminal_watch and decodes snapshots via MailboxRouter). `TestPhonesim_ObserveTerminalE2E`
  proves a server-rendered marker reaches the phone AND that `swarm remote off` blanks an established
  peek (the C fix validated E2E).
- **F/G (design/ADR)** — `a6b4971`: ADR-007 amendment resolves concurrent owner+phone control
  (allowed in the personal single-owner v1, relaxing P-5; TUI indicator recommended) and the
  lossy-mirror supersede seed (accepted v1 residual on the narrow concurrent-peek path).

Both models found NO bypass on peek input-injection, pump raw-byte suppression, R7 authorization,
double-Accept, or replay/reorder/dup — unchanged by the fix-pack.

## Second review cycle — re-review (2026-07-24)

The full -race sweep passed (40 pkgs), and BOTH re-reviewers (codex + opus) confirmed the fix-pack
CLOSED A, B, #7, J, D, E-ordering, #2-composition, #8, F/G — and found NO regression on the security
core (misroute closed, no injection, no post-OFF content stream, sanitization intact, R7/authz/replay
unchanged). BUT both returned **NOT SOUND** on the kill-switch TEARDOWN/RECOVERY path (one root):
- **C incomplete + new HIGH:** the kill-switch re-check only fired on an emission, so an IDLE peek
  never terminated on `swarm remote off` (renderer + tap lingered); and on cancel the daemon didn't
  close/detach, so Gateway.RunTerminal polled a silent conn forever and the TerminalWatcher never
  recovered (OFF->ON did not restore). Fails CLOSED (no leak) — a safety-intent + availability gap.
- **new MED (E-liveness):** RelaySink held its mutex across an UNBOUNDED MailboxAppend.
- **new LOW-MED:** Unwatch didn't join the cancelled goroutine (rewatch overlap).
- non-blocking: the input plane is best-effort under relay reorder (drop-on-Gap) — documented in
  protocol.md (`87d5b9a`).

**Fixed (`7de9515`):** RenderTerminal polls a `stillAllowed` predicate every ticker tick (idle peek
terminates within ~4ms); the handler writes OpError on peek end (peekGen-guarded) so RunTerminal
returns and the watcher reconnects — refused at the subscribe gate while OFF (bounded retry),
resumed on ON; the gateway blanks the phone on cutoff + re-checks ctx before forwarding; Unwatch
joins; RelaySink bounds the append (5s). `TestPhonesim_ObserveTerminalRecoversAfterKillSwitchToggle`
proves OFF->ON recovery end to end. Also hardened the sibling load-sensitive
`TestFanout_WedgedSubscriberDisconnectedWithinBound` (same class as #8).

Remaining: full -race sweep under load + a focused confirmation that the teardown blocker is closed;
then A7 is SOUND. The security core was confirmed sound across BOTH review cycles.
