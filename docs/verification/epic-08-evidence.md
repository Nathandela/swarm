# Epic 8 — Evidence

**Epic**: Attach path — WALKING SKELETON MILESTONE (`agents-tracker-ddp`)
**Commits**: bc64444 (assembly + attach), 43b9b12 (review fixes: runTUI + demux + GG-1), ce92b08 (final: version bump + GG-1 rigor + stdin).

## The milestone (GG-1)

**The walking skeleton is real and human-runnable.** `swarm` (no args) opens the real TUI (EnsureDaemon auto-starts the daemon → protocol.Dial → Bubble Tea general view → attach passthrough over the real terminal). The GG-1 end-to-end proof (internal/e2e, a real `swarm daemon` subprocess): launch a fake agent → grouped in List → attach paints the snapshot → type → detach → **`kill -9` the daemon → both shim AND agent PIDs still alive → restart → session reconnected (PID+start-time identity match, not relaunched) → re-attach's decoded client grid equals the pre-kill grid EXACTLY, and the transcript is intact**. Demo: `scripts/demo-walking-skeleton.sh` (exit 0). This is the architecture's headline invariant proven end-to-end.

## Criterion walk (E8.1 – E8.7)

| Criterion | Evidence |
|---|---|
| E8.1 raw passthrough | internal/attach: raw mode (IXON off via x/sys/unix termios), snapshot-paint-then-live, keystrokes→Input |
| E8.2 detach + restore | detach Ctrl+\ (configurable, not forwarded); termios restored on detach/panic(main-loop)/SIGINT/TERM/HUP (handler registered before MakeRaw); SIGKILL not claimed (documented); darwin PTY tests read termios while-alive (kernel revokes on session-leader exit) |
| E8.3 resize under lease | Session.Resize on SIGWINCH |
| E8.4 chrome + read-only (G3) | one-line toggleable chrome; completed/lost rows render the persisted final snapshot read-only, no input |
| E8.5 latency (N-2) | client-side added echo p95 well under 10 ms |
| E8.6 scenarios + failure injection | scenarios 2,3,7,8,9,10,16 green; daemon-killed-mid-attach |
| E8.7 GG-1 demo | scripts/demo-walking-skeleton.sh runs the real kill-9 survival, exit 0 |

## The daemon assembly (the composition the earlier epics deferred)

`internal/skeleton.Serve` composes daemon.Open (singleton) + engine.Run + `protocol.Serve` on the daemon's socket (via an additive `daemon.Config.ConnHandler` — the daemon keeps flock/singleton/reconnect) + a **deterministic first-byte demux** ('V' version-probe / '{' hook JSON / 0x00 protocol frame — no timing window after the review) + a reserved "fake" agent launch path. `runDaemon` runs it. ProtocolVersion bumped 1→2 for the demux wire change so D-8 skew detection is correct.

## Committee (audit-008) — productive divergence

codex returned 3 CRITICAL + 3 HIGH; Opus (ran the FULL -race suite + demo, verified constants) downgraded 2 criticals (the demux is provably unambiguous for real clients; agent-PID survival IS tested in daemon/realkill). Both agreed on the runTUI stub. Fixes: runTUI wired to the real TUI (PTY smoke test); demux made deterministic with the 'V' tag (only the version-vs-frame ambiguity needed it; 'F'/'H' left untagged, pinned by frozen tests); GG-1 strengthened to self-contained (exact grid + transcript + agent PID); ProtocolVersion bumped; stdin+stdout TTY guard. **codex APPROVE** on the final round.

## Carry-forwards (recorded on beads)

- Engine→daemon full status wiring (register session with the minted token, OnOutput tap, Emit→persist+fan-out) → Epic 11 (becomes real with live adapter hooks). The transport is live but sessions unregistered, so status detection is inert with no false claim (documented).
- LaunchReq.Worktree end-to-end smoke → Epic 14. Pump-panic self-restore + endpoint-id wider hash → documented (informational/V2).

## Quality gates (GG-4)

**Whole module green under `-race`** (all 24 packages incl. e2e/skeleton/attach/tui/cmd), gofmt + vet clean, GOOS=linux build clean, demo exit 0.
