# ADR-004: v1 security baseline

**Status**: Accepted
**Date**: 2026-07-16

## Context

The daemon can type into agents and approve their permission requests: compromising it equals interactive code execution as the user. Draft 1 hardened only the socket mode (audit-001, findings 8, 11; codex H5). Transcripts capture whatever agents print — secrets included. Free-text launch fields flow into process spawning. V2 will eventually expose the protocol beyond the local user.

## Decision

v1 baseline, enforced by tests:

1. **Filesystem**: state dir 0700; sockets, meta and transcript files 0600.
2. **Spawning**: argv arrays only, via exec (never a shell string); the daemon re-validates every client-supplied field server-side (P-6); session ids validated against path traversal.
3. **Singleton**: flock taken before socket bind; stale sockets unlinked only under the lock.
4. **Hook callbacks**: per-session random token; the daemon rejects status callbacks without it. Hook installation is per-invocation (flags/env/project-local), never a non-atomic mutation of the user's global CLI config.
5. **Terminal hygiene**: OSC 52 (clipboard write) and comparable hostile sequences filtered in the snapshot path.
6. **Environment**: launch env is allowlist-filtered before persistence (S-2) so meta.json does not immortalize every secret in the launching shell.

Remote access (V2) gets its own ADR: identity, pairing, E2EE/relay trust, idempotency, audit logging. Nothing in v1 may assume the peer is trusted beyond UDS + filesystem permissions.

## Consequences

### Positive
- Injection class eliminated by construction; local multi-user machines safe.
- Hook channel cannot be spoofed by other local processes.

### Negative
- Transcripts remain plaintext (readable only by the user); full at-rest encryption deferred — documented exposure.

## Alternatives Considered

### At-rest transcript encryption
Deferred: key management for a local single-user tool adds real complexity for marginal v1 threat reduction.

### SO_PEERCRED checks per request
Redundant with 0700 dir + 0600 socket for v1's local threat model; reconsidered in the V2 remote ADR.
