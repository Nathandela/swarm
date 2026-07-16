# ADR-003: Per-session metadata as source of truth

**Status**: Accepted
**Date**: 2026-07-16

## Context

Draft 1 made a single `roster.json` the registry. A crash mid-write corrupts state for every session at once — the exact scenario the persistence layer exists to survive (audit-001, finding 6). Sessions also need identity that survives PID reuse, and resume needs the agent-native conversation id, which a raw transcript does not provide.

## Decision

- Source of truth: `$XDG_STATE_HOME/swarm/sessions/<id>/meta.json`, one per session, written atomically (temp file + rename), carrying `schema_version`, shim PID + process start time, captured environment, and agent-native conversation id when available.
- `roster.json` is a rebuildable index; if missing or corrupt, the daemon reconstructs it by scanning session dirs.
- Transcripts: 0600, size-capped, rotated; spinner redraw frames collapsed before disk.
- State dir 0700. Retention: completed sessions persist until user deletion.

## Consequences

### Positive
- Crash can corrupt at most one session's metadata, never the registry.
- Upgrades get a migration primitive (`schema_version`) from day one.
- Resume is grounded in real conversation ids, not transcript hope.

### Negative
- Directory scan on cold start (negligible at human session counts).
- Two artifacts (truth + index) to keep coherent — index is disposable by design.

## Alternatives Considered

### Single roster.json as truth
Rejected: single point of corruption; global-file locking contention.

### Embedded database (SQLite/bbolt)
Robust; rejected for v1 as heavier than needed for tens of records — JSON files are greppable and diff-friendly (agentic-codebase value). Revisit if the model grows relations.
