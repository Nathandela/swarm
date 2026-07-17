# Audit 006 — Epic 6 (client protocol + daemon API)

**Date**: 2026-07-17
**Committee**: codex GPT-5.6 sol (cross-model) + Opus (independent, ran -race). agy quota-blocked.
**Verdict**: FIX REQUIRED. Sharp but productive divergence (codex 7 HIGH + 5 MEDIUM; Opus 1 HIGH + 2 MEDIUM + LOW). Opus ran the race detector and traced interleavings, clearing several codex HIGHs; orchestrator adjudicated the disputed ones by reading the code.

## Consensus (both) — FIX
1. **Stale snapshot on supersede** (codex#1, Opus#1, both HIGH): supersede reuses the upstream stream (correct) but hands the new controller the ORIGINAL cached snapshot + only post-supersede frames → corrupted alt-screen (old screen + gap + live). Contradicts ADR-002's whole rationale. Fix: fetch a FRESH snapshot on supersede (re-snapshot path on SessionStream). Verified: fromdaemon.go Snapshot() returns a fixed st.snap captured at stream creation.

## codex-only, verified real by orchestrator — FIX
2. **Snapshot exceeds MaxFrame + write-failure deadlock** (codex#4, HIGH): a single TSnapshot frame; with maxDim=1000, a large styled grid snapshot vastly exceeds wire.MaxFrame (1 MiB) — even Epic 2's worst-case 200x60 ≈ 1.3 MB. writeFrame(snapshot) then fails, and the attachment state (newDone) was installed before the write, so cleanup/Close waits on pumpDone forever. Fix: CHUNK the snapshot across frames (reassembled client-side) so any grid size transmits, AND roll back attachment state on write failure. May need an ADR-002 amendment (snapshot framing).
3. **Unbounded attach pump / slow controller blocks supersede** (codex#5, HIGH): the pump writes to the client socket with no deadline/bounded outbound queue; a wedged controller blocks its pump, and supersede waits on that pump → a wedged controller can't be evicted (liveness). Fix: bounded outbound queue + write deadline + evict slow controller so supersede/detach always proceeds.
4. **L1 event loss in FromDaemon** (codex#7, HIGH): watch advances `seen` before a non-blocking send; on full 64-queue the event is dropped and never retried, and the subscriber is NOT disconnected → a status change is lost (violates L1's "reach subscriber OR disconnect"). subscribe also sends ok before registering. Fix: disconnect on full queue (like the server fan-out) or don't advance seen; register before ok.
5. **Stale-input TOCTOU** (codex#2, HIGH — DISPUTED, Opus cleared): forwardInput checks (controller,gen) under s.mu, RELEASES, then calls stream.Input — verified: a keystroke validated at gen N can apply after a supersede to N+1 (Opus's test only covers input arriving after supersede, not this window). Real but narrow (one stray keystroke, rare steal-flow). Fix with a bounded approach (per-lease serialization of gen-check+send without holding s.mu across shim I/O).
6. **DialSession S3 recheck** (codex#6, HIGH — DISPUTED, Opus cleared via the G2 hello): add the (PID, start-time) recheck before dial for consistency with Kill/Delete — cheap hardening; the hello confirms a serving shim but not THE session's shim identity.
7. **Multiple attaches on one connection cross-route** (codex#3, HIGH): Client.Attach replaces c.att without detaching; server clientConn tracks one attSession. Defensive fix: reject or auto-detach a second attach on the same connection.

## Consensus MEDIUM/LOW — FIX
8. initial_prompt accepted+documented but dropped (both): daemon.LaunchSpec has no carrier. Plumb it through.
9. GG-7 drift test is substring-presence, no reverse check (both): make it a bidirectional field-set diff with teeth.
10. D-8 server-side d8Message has SWAPPED version numbers (Opus#5); client returns arbitrary daemon prose verbatim (codex#11). Fix the swap; client synthesizes/validates the restart+safety text.
11. Detach generation ignored (codex#9): a delayed old-gen detach releases the current lease. Add a gen check.
12. summary documented but never populated (Opus#4): V-4 summary always "". Populate a grid-derived last-line, or defer to Epic 7/10 with a note.
13. lease/`seen` map growth over lifetime (codex#7): clean up on session delete.

## Deferred (noted)
Agent-name allowlist → Epic 9/11 (the adapter validates valid agents). GG-5 evidence file → orchestrator at close.

## Cleared by both / by Opus-with-race (not re-litigated)
Generation monotonic; normal detach/EOF releases lease+stream (L3); superseded Frames() close vs send race clean; server event fan-out bounded + wedged-sub evicted outside the lock (S9); status Groups server-side only; no UDS/fd fields, foreign namespace rejected; codec MaxFrame-before-alloc, malformed/unknown handled without panic; first-attach snapshot ordering (S10) correct.
