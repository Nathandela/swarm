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
