# v0.26 audit remediation

Baseline a1da607e. Scope covers the eight audit issues for the terminal dashboard, supervisor, and local integration fixtures. The intended deployment remains a per-user daemon on macOS and Linux with sessions surviving daemon replacement.

| Work | Acceptance and regression spike |
|---|---|
| Operation store (7ssa, 94v8) | Reads expose errors before opening a writable store. Repair only a provably incomplete final record, retaining valid unterminated records. Keep write and replay limits consistent, account for existing large records, and prove restart cannot lose later records or repeat a transition. |
| Live journal (sd7c) | Publish replacement segments and durably retire old segments before reporting success. Inject partial retirement and retry across both recovery and restart while preserving cursor ordering and subscriptions. |
| Native session identity (6h9n, xqdu) | Keep newer manual names through restarts, serialize timestamp checks with writes, retry failed persistence, honor disabled agy presentation, and reject conflicting Codex archive state before any batch launch. |
| Discord pruning (w039) | Visit only publications for each expired request, preserving guard retention, independent requests and unresolved deliveries. |
| Cleanup (cpnj) | Untrack the old executable, retire unused simulator helpers, correct requirement evidence and test the shipped board renderer. Preserve release builds, actual recovery tooling, and useful test fixtures. |
| Qualification (q44t) | Isolate inherited hook endpoints and use canonical fixture roots. Scope source gates to the checkout. Recheck race tests and relay approval through the real local Worker harness. |

Review the strategy and challenge failure paths before merging implementation. Cross-review changes with agents who did not author them. Run focused failures first, then build, vet, lint, full race tests and local relay integration. Record unresolved platform or release gates. Push reviewed source, with release publication a separate operation.

## Implementation and independent review

The storage, identity and qualification workstreams reviewed their initial strategy and exercised failures with temporary stores and session fixtures. Review by agents outside the authoring workstream changed the implementation in four places.

- A zero-byte write failure can retry once the filesystem recovers. A partial append or uncertain sync blocks further writes and compaction until replay.
- Newly appended rows fit the previous reader’s 4 MiB ceiling. Replay and compaction preserve existing rows up to 16 MiB. The [operator notes](../ops/storage-clean.md#operation-log-recovery) cover failures and rollback.
- Segment retirement proceeds newest-first and syncs the directory after each removal. A partially completed cleanup leaves a replayable prefix of the original segments and the complete replacements.
- Repeated agy titles advance an in-memory timestamp on the session without writing metadata on every tick. A daemon replacement ignores ticks stamped before it opened. Tests cover a delayed different title both before and after replacement.

Codex bulk reattachment aborts before launch when the same thread appears in both searched roots. Default discovery still reads active history. Discord cleanup seeks by encoded request prefix and verifies key identity. A regression puts an unreadable publication under a different request and proves the expired request can still be pruned.

The old gateway artifact was removed from the tree and the checkout. Release packaging continues to build it from source. Dashboard rendering, section sizing and layout assertions now exercise the shipped board. The legacy push gateway backup and restore implementation remains available under its existing retention decision.

## Verification

- macOS build, vet and golangci-lint passed. The strict Phase B manifest check passed, and all 162 OpenCode bridge tests passed.
- The local Workerd suite passed with the release workflow’s 300-second budget, including the approval, pairing, revocation and terminal cases.
- Linux arm64 with Go 1.26.0 passed the complete idempotency, journal and Discord package race suites and focused native-title, Codex reattach, shim and board race tests. Linux amd64 cross-build also passed during integration.
- The previously failing CLI, persistence, hook, source-walk and shim checks passed under the race detector with an inherited shim endpoint and an aliased temporary root. The two attach-snapshot fixtures passed ten consecutive race runs after replacing fixed sleeps with a socket observation barrier.
- The exact 4 MiB append boundary was checked against the previous scanner. It read both the maximum accepted record and its suffix. The writer rejected one additional byte.
- The real-PTY smoke clients restrict provider discovery to their fixture binaries. Six repetitions passed. The daemon fixture clears the enclosing remote-socket override and reports its last failed dial. An invalid inherited socket reproduced a startup timeout before isolation and passed three repetitions after it. The original timeout did not retain enough evidence to establish the same cause.
- The containment fixture passed ten race repetitions after replacing a sleep with a parent-child handshake. Three runs with the parent's readiness wait bypassed failed as expected.
- Final macOS runs of `go test -p 2 -timeout 35m ./...` and `go test -race -p 2 -timeout 35m ./...` both exited successfully. Each reported 82 passing packages. These runs included the production reachability check and the full supervisor assembly suite. Independent review identified no remaining blockers in the final changes.

This record covers local release-candidate qualification. Hosted push, physical Android devices and release publication were outside this change.
