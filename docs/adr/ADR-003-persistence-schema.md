# ADR-003: Per-session metadata as source of truth

**Status**: Accepted
**Date**: 2026-07-16

## Context

Draft 1 made a single `roster.json` the registry. A crash mid-write corrupts state for every session at once — the exact scenario the persistence layer exists to survive (audit-001, finding 6). Sessions also need identity that survives PID reuse, and resume needs the agent-native conversation id, which a raw transcript does not provide.

## Decision

- Source of truth: `$XDG_STATE_HOME/swarm/sessions/<id>/meta.json`, one per session, written atomically (temp file + rename), carrying `schema_version`, shim PID + process start time, captured environment, and agent-native conversation id when available. Native identity is write-once. Codex persists the canonical id announced by its structured backend; Claude persists the canonical top-level `session_id` from an authenticated hook before shaping that event.
- Resume performs no implicit fresh launch. For an ended/lost Codex or Claude row created before durable identity capture, an explicit resume may lazily recover one unique native id from the daemon user's provider history and persist it before argv composition. Recovery is rooted at the trusted user home, bounded by entries/files/record bytes/total bytes, validates canonical UUID + cwd + launch-time evidence, and fails closed on ambiguity or malformed history. Symlinks are rejected except for the stable first provider component (`~/.codex` or `~/.claude`) when it is an absolute alias to a clean strict descendant of that same trusted home; the target component depth is capped, traversal stays within the anchored root, the alias is revalidated, and every link below it remains forbidden. OpenCode and AGY have no speculative history scanners.
- `roster.json` is a rebuildable index; if missing or corrupt, the daemon reconstructs it by scanning session dirs.
- Transcripts: 0600, size-capped, rotated; spinner redraw frames collapsed before disk.
- State dir 0700. Retention: completed sessions persist until user deletion.

## Consequences

### Positive
- Crash can corrupt at most one session's metadata, never the registry.
- Upgrades get a migration primitive (`schema_version`) from day one.
- Resume is grounded in provider-owned conversation ids, not transcript hope, and old rows migrate only when the owner explicitly resumes them.

### Negative
- Directory scan on cold start (negligible at human session counts).
- Two artifacts (truth + index) to keep coherent — index is disposable by design.

## Alternatives Considered

### Single roster.json as truth
Rejected: single point of corruption; global-file locking contention.

### Embedded database (SQLite/bbolt)
Robust; rejected for v1 as heavier than needed for tens of records — JSON files are greppable and diff-friendly (agentic-codebase value). Revisit if the model grows relations.
