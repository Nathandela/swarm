# ADR-002: Control/data plane split and in-shim VT emulation

**Status**: Accepted
**Date**: 2026-07-16

## Context

Draft 1 put "all communication" in newline-delimited JSON, including the attach stream. PTY output is arbitrary bytes (not valid UTF-8), high-volume, and latency-critical; base64-in-JSON is lossy-adjacent and slow (audit-001, finding 3). Separately, replaying raw historical bytes cannot reconstruct a full-screen TUI's current state — all target CLIs use the alternate screen with in-place redraws (audit-001, finding 2).

## Decision

- One UDS connection multiplexes two planes: **control** (NDJSON messages: handshake, list, launch, kill, delete, attach/detach, resize, subscribe) and **data** (length-prefixed binary frames: PTY input/output, grid snapshots), with a defined max frame size.
- Each shim runs a **VT emulator** (established Go library of the vt10x class, evaluated at implementation) maintaining the session's grid. Attach = serialized grid snapshot, then live frames. Status heuristics read the grid, never the raw byte stream.
- Attach uses an **exclusive controller lease** with generation ids; stale input/resize is rejected. Backpressure: bounded per-client outbound queues; slow subscribers are disconnected; PTY draining never blocks.

## Consequences

### Positive
- Correct bytes end-to-end; single-digit-ms attach latency stays plausible.
- Snapshot-attach is instant and correct even on the alternate screen.
- One emulator serves both attach replay and status detection.

### Negative
- The VT emulator is the largest single work item in the system.
- Binary framing needs careful fuzz/property testing (split escape sequences, partial frames).

## Alternatives Considered

### Everything JSON (base64 PTY payloads)
Simple; rejected for ~33% overhead + allocation cost on the hottest path and UTF-8 fragility.

### Replay transcript tail on attach
No emulator needed; rejected as technically wrong for alt-screen TUIs (starts mid-escape, wrong buffer, wrong size).

### Two sockets (control vs data)
Cleaner separation; rejected as more connection lifecycle for no measured gain — revisit if profiling disagrees.

## Amendments

### 2026-07-17 — Snapshot framing, supersede re-snapshot, and bounded eviction (audit-006)

The Epic 6 protocol review (audit-006) surfaced three wire/liveness gaps in the attach path; the resolutions refine (do not reverse) the decision above.

- **Snapshot chunking.** A single `TSnapshot` frame cannot carry a full grid snapshot: with `maxDim = 1000`, a styled snapshot is far larger than `wire.MaxFrame` (1 MiB). The snapshot is now delivered as a **sequence of one or more `TSnapshot` frames** carrying raw ordered chunk bytes. The preceding `lease` control carries `snapshot_len` (the snapshot's total byte length); the client concatenates chunk payloads until it has that many bytes before painting. A snapshot that fits in one frame is still sent as a single raw `TSnapshot` frame, so the common path and the S10 ordering (`lease` → snapshot → live `TDataOut`) are unchanged. This adds one field (`snapshot_len`) to the control schema; no frame type changes.
- **Re-attach on supersede (supersedes the earlier "re-snapshot" resolution).** The new controller must see the **current** grid, not the snapshot captured when the stream first opened. Rather than splice a fresh snapshot into a *reused* live stream (racy: queued pre-snapshot frames can be replayed after the new snapshot), a supersede **re-attaches**: the daemon releases the prior controller, closes the old shim connection, and opens a **fresh** one. The shim serves one connection at a time and delivers snapshot-then-stream atomically under its hub lock (Epic 4, S10), so the fresh stream's snapshot and first live frame share the shim's own boundary — no daemon-side splice, no boundary race. A re-attach failure is a hard error (the supersede fails cleanly; it never shows a stale screen). The single-upstream-per-lease framing of L3 is refined: exactly one upstream connection is held per controller, and a supersede is a clean close-then-open (the prior connection is released before the fresh one is opened). The whole attach is serialized per session so the controller and pump are published only once a real pump is running (no wait on a not-yet-started pump).
- **Bounded controller eviction + total snapshot deadline.** The attach output path writes to the controller under a per-write deadline, and the lease + all snapshot chunks share a single TOTAL deadline with a stop-check between chunks. A wedged/slow controller's write fails at the deadline and the controller is evicted (its lease released, its connection closed); a supersede/detach concurrent with an in-progress snapshot send is never blocked. This makes supersede and detach **always** proceed within a bound — a wedged client can never hold the lease or block the daemon (S9), consistent with the original "bounded per-client outbound queues; slow subscribers are disconnected" decision. The client reassembles the chunked snapshot up to `snapshot_len` under a hard size cap, rejecting a negative/oversized declared length or an overshooting chunk stream.

### 2026-07-18 — Chunked snapshot on the shim->daemon hop + hello capability (item 1.2, agents-tracker-mlm)

The 2026-07-18 audit surfaced the mirror gap on the *inner* hop: the shim served its attach snapshot to the daemon as a **single** `TSnapshot` frame, which `wire.WriteFrame` rejects past `MaxFrame-1`. A 200x50 styled grid (~1.06 MiB) therefore made the daemon's snapshot read hang until its deadline and then fail, silently starving the grid-tap heuristic. The resolution extends the audit-006 daemon->client chunking to the shim->daemon hop; it refines (does not reverse) the decision above.

- **Chunked shim-hop encoding.** When negotiated, the shim delivers its snapshot as a `shimwire` **`snapshot_info` control preamble** carrying `snapshot_len` (the snapshot's total byte length, declared UP FRONT — mirroring the daemon->client `lease.snapshot_len`), followed by the snapshot as a sequence of one or more `TSnapshot` chunk frames of at most `MaxFrame-1` bytes each. An empty snapshot is the preamble alone (zero chunks), so the reader completes without waiting for a following frame and an idle session never hangs. The daemon reassembles chunk payloads until exactly `snapshot_len` bytes arrive, under the **same** `maxSnapshotBytes` cap the client hop uses, so a bogus declared length cannot OOM the daemon; overshoot, a live `TDataOut` before completion, a duplicate preamble, an over-cap/negative length, and a short/stalled stream are all protocol errors bounded by a single TOTAL attach read deadline. S10 is unchanged: the whole snapshot precedes any live frame.
- **Capability negotiation via an optional hello field, no version bump.** Chunking is negotiated at the G2 hello through a new **OPTIONAL** `shimwire.Control` field, `snapshot_chunking`; `shimwire.WireVersion` **stays 1** (bumping it would mark every running shim lost on daemon upgrade — an S1 break). The daemon advertises `snapshot_chunking` in its hello, and the shim chunks **only** when that peer advertised it; the shim advertises its own support in the hello reply, and the daemon reassembles **only** on receipt of the `snapshot_info` preamble (which only a chunking shim sends). `shimwire.Decode` tolerates unknown fields, so an old-build shim (or old-build daemon) simply never sets or reads the field and the pair degrades to exactly the single-frame path above — including the oversized-single-frame failure, which is no worse than prior behavior. No frame type changes; the only schema additions are the `snapshot_chunking` and `snapshot_len` fields and the `snapshot_info` control type.
- **Observable tap failures.** A grid-tap attach/snapshot failure is now counted and rate-limit-logged rather than silently skipped, so the heuristic can no longer die unnoticed.
