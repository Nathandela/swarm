# Audit Committee Report 001 — system-spec.md (Draft 1)

**Date**: 2026-07-16
**Members**: GPT-5.6 sol (codex), Claude Sonnet, Claude Opus. Gemini 3.5 Flash unavailable (quota).
**Verdict**: **REVISE** — architecture direction is sound, but four foundational decisions must be made before decomposition, and three requirements as written are technically impossible.

## Consensus (raised by 2–3 members — treat as fact)

1. **Daemon death kills every live session as specced.** The daemon holds each PTY master fd; daemon crash or upgrade closes them, SIGHUPs every agent, and a restarted daemon cannot re-adopt a PTY. "Survives terminal close" holds; "survives daemon crash/upgrade" does not — and `brew upgrade` makes upgrades frequent. Options: (a) per-session shim process owning the PTY master + its own socket, daemon reconnects (true survival, +1 epic); (b) accept and document the limitation (Agent View has the same one) with an explicit warned restart UX + resume; (c) tmux as the persistence layer (contradicts chosen constraint).
2. **Byte-buffer scrollback replay cannot restore a full-screen TUI (A-4 impossible as written).** All five agent CLIs use the alternate screen with in-place redraws; replaying recent raw bytes lands mid-escape-sequence on the wrong buffer. The daemon must maintain a real VT emulator (grid, cursor, modes) per session; attach = serialize grid snapshot, then live stream. Largest single work item in the system; missing from the spec entirely.
3. **One JSON channel cannot carry the binary PTY stream (P-1/P-2 contradiction).** PTY bytes are not valid UTF-8; base64-in-JSON breaks N-2 latency. Split control plane (NDJSON) from data plane (length-prefixed binary frames), specify framing, max sizes, backpressure, and that a slow client never blocks PTY draining.
4. **Status detection strategy is based on a stale capability assumption.** Typed signals exist beyond Claude: Gemini CLI has structured hooks (BeforeAgent/AfterAgent/Notification), OpenCode exposes `permission.asked`/`session.idle` plugin events, Codex has an app-server protocol with turn lifecycle events. PTY parsing must be the last resort, not the default for 4/5 agents. Also: `needs_input` vs `review` is unobservable from screen content alone — precisely the two states that drive notifications.
5. **Status model should be orthogonal dimensions**, not one enum: process (running/exited/lost) × turn (active/idle/unknown) × interaction (none/prompt/permission/unknown). View groups derive from these. Fixes the state-machine gaps found (no starting→ended, unknown→ended, interrupted-not-deletable).
6. **roster.json is a corruption single-point-of-failure.** Per-session metadata files are the source of truth (atomic temp+rename writes, schema_version field); the roster is a rebuildable index. PID liveness checks must pair PID with process start time (PID reuse).
7. **V2 "protocol is mobile-ready" is overstated.** Remote needs identity, auth/pairing, E2EE/relay trust, reconnect cursors, idempotency — none present. Keep schemas evolvable + add endpoint/session namespacing cheaply now; drop the claim; remote gets its own ADR + threat model.
8. **Multi-client attach authority undefined.** v1: exclusive controller lease, others read-only observers; resize authority follows the lease.
9. **Environment capture bug.** Workers inherit the daemon's frozen env (from whichever terminal first started it), not the launching client's. The launch RPC must carry the client's environment.
10. **No resource bounds.** Transcript rotation/caps (spinners write GBs overnight), session count cap, fd budget, per-client outbound queue bounds, disk-full behavior.
11. **Security below the daemon's authority.** State dir 0700, transcripts 0600 + retention (they capture secrets), argv-array-only spawning (no shell interpretation, daemon re-validates client input), hook callback authentication, terminal-escape hygiene (OSC 52).
12. **Worktrees underspecified.** Branch naming, base ref, teardown on delete, non-repo error path. Committee: cut from first slice, ship in a later epic.
13. **Lifecycle plumbing gaps**: singleton via flock acquired before bind + stale-socket unlink; setsid + stdio redirect on daemonize; version-skew handshake behavior + explicit `swarm daemon restart`; process-group kill (agents spawn MCP servers etc., killing one PID leaks children); CGO_ENABLED=0 for static builds.

## Divergence

- **Opus** argues the tmux-backed clones are the correct reference class (Anthropic supervises first-party sessions and never needs to emulate a foreign TUI; swarm takes on tmux's hardest job to save a dependency). Codex and Sonnet accept the daemon path if the shim + VT emulator + framing are budgeted. Resolution: user decision at Gate 2, with the cost stated honestly.
- **Opus** flags that TUI-only notifications hollow out the headline use case (background agent needs input → silence until the user happens to reopen swarm). The user chose TUI-only for v1; committee recommends pulling minimal OS notifications back in.
- **Codex** uniquely surfaced the typed-event capabilities of Gemini/OpenCode/Codex with sources — upgrades the whole detection design.
- **Opus** uniquely: env capture, Ctrl+Q collides with XON flow control (detach default changed to Ctrl+\, raw mode with IXON off required).
- **Sonnet** uniquely: PID-reuse start-time check, Claude hook install scope (per-invocation settings, never mutate the user's global config non-atomically).

## Blind spots (no member raised)

- macOS sleep pauses everything regardless of architecture (ccmux's caffeinate trick is the eventual answer; document as limitation).
- AGY adapter is committed but uncharacterized anywhere — needs a capability-matrix entry or deferral (Codex/Opus raised adjacent points).

## Prioritized fix list (feeds spec Draft 2)

1. ADR-001: session process ownership (shim vs accept-limitation vs tmux) — user decision.
2. ADR-002: streaming protocol — control/data split, framing, backpressure, attach lease.
3. ADR-003: persistence — per-session metadata as truth, atomic writes, schema_version, retention caps.
4. ADR-004: security baseline — dir perms, argv-only spawn, hook auth, env capture.
5. Rework status model to orthogonal dimensions; detection = typed events first (Claude hooks, Gemini hooks, OpenCode events, Codex app-server), VT-grid parsing fallback, `unknown` on staleness heuristic.
6. Add the VT emulator as a first-class epic feeding both attach replay and detection.
7. Stage adapters: prove the contract with Claude + one typed-event CLI; others follow behind fixtures. Characterize or defer AGY.
8. Soften F-2; add endpoint/session namespacing + auth envelope field now, remote ADR later.
9. Move worktrees out of the first slice. Reconsider OS notifications for v1. Measurable perf targets.
