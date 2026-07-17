# Audit 008 — Epic 8 (walking skeleton)

**Date**: 2026-07-17
**Committee**: codex GPT-5.6 sol (cross-model; couldn't run gates — sandbox) + Opus (independent, ran the FULL -race suite + demo). agy quota-blocked.
**Verdict**: FIX REQUIRED → **RESOLVED (codex APPROVE)**. Sharp divergence resolved by Opus's actual gate runs + constant-level verification: codex's 3 CRITICALs largely downgraded, but both agreed on the real gap (runTUI stub). All fixes landed across commits 43b9b12 + ce92b08; codex confirmed on final re-review: runTUI opens the real TUI (PTY smoke test), the demux is deterministic/timer-free with the ProtocolVersion bump for the wire change, and GG-1 is self-contained (exact grid == shim grid + transcript continuity + agent-PID survival across a real kill -9).

## Consensus — FIX
1. **runTUI is an unwired stub** (codex CRITICAL, Opus MEDIUM): cmd/swarm/main.go:88 returns "tui: not implemented"; bare `swarm` doesn't open the TUI. All three layers (skeleton daemon, internal/attach, internal/tui) are built + tested in isolation but never assembled into the no-arg binary — no tui.New, no Client.Attach, no terminal handoff. The scripted GG-1/E8.7 demo passes (proven programmatically via protocol.Client), but a human running `swarm` gets the stub. Not tracked as a deferral. FIX: wire runTUI (EnsureDaemon → protocol.Client → tui.New(WithAttachRunner(NewAttachRunner(...))) → run the program; graceful no-tty error).

## codex criticals DOWNGRADED by Opus's verification
2. **Socket demux "ambiguous/timing"** (codex CRITICAL → Opus robust): Opus verified via constants — wire.MaxFrame=1<<20 so a frame length's MSB is ALWAYS 0x00 (never '{'=0x7B); WriteFrame emits the whole frame in ONE Write so a real frame's 5th byte co-arrives; a version handshake stops at 4 bytes. The 250ms classify window disambiguates the two 0x00-starting shapes (handshake vs frame) safely for ALL real clients; the only misroute (a crafted 4-bytes-then-stall protocol client over a local 0600 UDS) is unreachable + benign. STILL: it IS a timing-based demux — replace with an explicit 1-byte discriminator to remove the timing + the 250ms/probe cost (cleaner, and closes codex's edge). FIX (hardening).
3. **GG-1 doesn't prove agent survival** (codex CRITICAL → Opus proven-elsewhere): the e2e asserts SHIM survival + genuine reconnect (non-empty snapshot can only come from the surviving shim's live emulator; reconcile requires PID+start-time+live-hello). Agent-PID survival IS asserted in daemon/realkill_test.go + survival_test.go (real agent PIDs from pidfiles, kill-9 real daemon, every agent alive, reconnect, re-alive). STILL: the HEADLINE e2e should be self-contained — strengthen it to also assert the agent PID survives + client-grid == shim-grid + meta continuity. FIX (test strengthening).

## Minor / accepted
- Latency test comment falsely claims an e2e latency assertion (there is none); N-2 measures client-side added latency (valid reading). FIX the comment; true e2e latency is E14.4.
- Detach on coalesced reads (n>1 not treated as detach): DELIBERATE paste-safety, correct under raw VMIN=1 (a lone keypress reads n==1). Accepted.
- Pump-goroutine panics recovered locally but don't self-restore (terminal restored on main-loop exit): non-wrecking. Low-priority hardening (propagate to main loop). Optional.
- LaunchReq.Worktree flag untested end-to-end (machinery fully tested): trivial smoke → Epic 14. endpoint-id 32-bit truncation: V2 forward-compat, opt-in, low impact → note.

## Cleared by both
Real subprocess kill-9; ConnHandler doesn't break singleton/flock/reconnect (S12/S3); reconcile identity match; engine wiring inert-but-safe (documented Epic 11 carry-forward, no false claim, no nil-deref); IXON off + restore on detach/panic(main)/signal, signal registered before MakeRaw; S10 snapshot-exactly-once-before-live; banner-in-view migration legitimate. Whole module green under -race; demo exit 0.
