# ADR-001: Per-session shim processes own the PTYs

**Status**: Accepted
**Date**: 2026-07-16

## Context

swarm's core promise is that agent sessions survive the terminal. If the daemon itself holds each PTY master fd, daemon crash or upgrade (`brew upgrade` + restart) closes the masters, SIGHUPs every agent, and kills all running work — and a restarted daemon cannot re-adopt a PTY. Audit committee consensus (audit-001, finding 1). Anthropic's Agent View accepts this limitation; we chose not to.

## Decision

Each session runs under a dedicated **shim** process that the daemon spawns and then does not own.

- Shim: setsid'd; owns the PTY master; runs the VT emulator (grid); appends the capped transcript; serves a per-session UDS socket (snapshot, stream, write, resize, signal).
- Daemon: connects to shims as a client; rediscovers them from `sessions/<id>/meta.json` (PID + start time) after restart.
- The single distributed binary contains all three roles (client, daemon, shim).

## Consequences

### Positive
- Sessions survive daemon crash and upgrade — strictly better than Agent View.
- `swarm daemon restart` becomes a safe, ordinary operation (enables painless releases).
- Failure isolation: a panicking shim loses one session, not all.

### Negative
- One extra process per session and a second internal socket protocol (daemon⇄shim).
- Two-phase spawn (daemon→shim→agent) complicates launch error reporting.

## Alternatives Considered

### Daemon owns PTYs directly
Simplest; ships faster. Rejected: turns every daemon restart into "all your agents die", the exact failure the product exists to remove.

### tmux as persistence layer
Solves persistence, emulation, and resize for free. Rejected: contradicts the zero-runtime-dependency and Agent View-fidelity decisions; the daemon protocol (needed for V2 anyway) would still have to exist alongside tmux scraping.
