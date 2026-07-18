# swarm — System Invariants

**Created**: 2026-07-16
**Framework**: Lamport safety/liveness properties
**Source**: STPA analysis during decomposition (see build-plan.md). Every invariant maps to at least one test assertion; the Integration Verification epic asserts all of them.

## Safety properties (must never be violated)

**S1 — SURVIVAL**: While a session's process is running, no daemon termination event ends the agent. *Assert*: `kill -9` the daemon → every agent PID still alive; restarted daemon lists and reconnects the same sessions. [D-2, D-3, D-5, ADR-001]

**S2 — SINGLE-CONTROLLER**: At most one client holds a current-generation attach lease per session; the shim applies input/resize only under the current generation. *Assert*: two concurrent attaches → applied input from a stale generation == 0. [P-5]

**S3 — IDENTITY-SAFETY**: Signals are sent and shims adopted only on (PID, process-start-time) match; mismatch → `lost`, never a signal. *Assert*: PID-reuse fixture → zero signals sent. [D-4]

**S4 — INJECTION-FREE SPAWN**: Every spawn is `exec(argv[])`; no user-supplied field is ever interpreted by a shell. *Assert*: metacharacter-laden cwd/prompt/options arrive as single argv elements; no `/bin/sh` exec observed. [S-1, ADR-004]

**S5 — PROCESS-GROUP CONTAINMENT**: Kill terminates the whole session process group (TERM → grace → KILL). *Assert*: scenario 17 — no descendant survives. [S-4]

**S6 — AUTHENTICATED STATUS**: A status callback mutates a session only with that session's live token and a fresh sequence number; tokens die with the session. *Assert*: tokenless, foreign-token, replayed, and post-end callbacks are all no-ops. [T-2, ADR-004]

**S7 — NEVER-CONFIDENTLY-WRONG**: turn is never `active` after (no output ∧ no CPU) beyond the staleness threshold; heuristics never override a fresher typed signal. *Assert*: staleness fixture → `unknown`. [T-2, T-3, T-4]

**S8 — ATOMIC-DURABLE STATE**: meta.json is only ever observed complete and schema_version-tagged; a crash corrupts at most one session's meta; the roster is always rebuildable by scan. *Assert*: crash injection mid-write → old-or-new file, never torn. [R-1, ADR-003]

**S9 — DRAIN-NONBLOCKING**: No slow client or disk stall ever blocks PTY draining. *Assert*: wedged subscriber + stalled disk → PTY still drained, subscriber disconnected within bound. [N-3, P-3]

**S10 — SNAPSHOT-CONTINUITY**: Attach delivers exactly one grid snapshot before any live frame, with no lost/duplicated bytes at the boundary; hostile escapes (OSC 52) filtered. *Assert*: attach under output load → client grid == shim grid. [A-4, N-6]

**S11 — SHIM↔META BIJECTION**: After reconciliation, every live shim maps to exactly one meta.json and every `running` meta maps to a live shim or is reclassified `lost`. *Assert*: crash injection in spawn/exit windows → no orphan shims, no phantom sessions. [D-4]

**S12 — SINGLETON DAEMON**: At most one process holds the flock + bound socket; stale sockets are unlinked only under the lock. *Assert*: two simultaneous daemon starts → exactly one survives, client reaches it. [D-6, D-7]

## Liveness properties (must eventually happen)

**L1**: After any status-dimension change, the session's latest committed state reaches every still-connected subscriber within 1 s (or that subscriber is disconnected for slowness). Intermediate states MAY coalesce to the latest (ADR-008). [V-2, P-3]

**L2**: After a daemon restart, every still-live shim is eventually reconnected and re-registered. [D-4, D-5]

**L3**: After a mid-attach client disconnect, the lease and stream are released so a subsequent attach succeeds. [P-4]
