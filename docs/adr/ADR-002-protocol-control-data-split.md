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
- **Re-snapshot on supersede.** A supersede reuses the single upstream stream (L3) but the new controller must see the **current** grid, not the snapshot captured when the stream first opened. On supersede the daemon re-fetches a fresh snapshot from the shim (which always holds the current grid) and sends that. The shim already re-snapshots on a repeated attach over the same connection, so no shim-protocol change is needed.
- **Bounded controller eviction.** The attach output path now writes to the controller under a per-write deadline; a wedged/slow controller's write fails at the deadline and the controller is evicted (its lease released, its connection closed). This makes supersede and detach **always** proceed within a bound — a wedged client can never hold the lease or block the daemon (S9), consistent with the original "bounded per-client outbound queues; slow subscribers are disconnected" decision.
