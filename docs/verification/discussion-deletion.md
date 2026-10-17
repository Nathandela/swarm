# Discussion deletion verification

2026-10-16 · `agents-tracker-fxsl` · baseline `79e7bd3d`

The owner requires explicit deletion to remove a discussion's retained Swarm
attempts, rather than hide and archive earlier attempts. This amends R-3 and
[ADR-024](../adr/ADR-024-auth-recycle-on-relogin.md). The broader storage
investigation is in [Lightweight storage strategies](../research/swarm-lightweight-storage-strategies.md).

## Behavior checked

- Deletion removes every on-disk attempt in the validated explicit resume family,
  including hidden predecessors, and its owned Git worktrees. Independent launches
  and handoffs remain separate, even with the same native conversation ID.
- A multi-attempt family must be stopped first. Outside sessions using a cleanup
  directory, a nested directory, or a symlink/case alias refuse deletion before
  mutation. Ordinary launch registration cannot race workspace cleanup.
- Partial failures return errors and preserve ownership metadata. Farthest-first
  deletion keeps the selected attempt and remaining lineage reachable for retry.
  A failed worktree cleanup remains retryable across supervisor restart.
- The durable deletion record precedes directory removal. A filesystem failure
  produces a compensating metadata record. The writer fence and final tombstone
  prevent a concurrent natural exit from recreating a deleted attempt.
- Payloads are removed before `meta.json`, so a filesystem failure does not leave
  a large directory without discoverable cleanup ownership.

Tests use temporary sessions, real temporary shim/agent processes, a temporary
Git repository/worktree, injected cleanup and journal failures, actual filesystem
permission failures, and concurrent lifecycle operations. An independent agent
reviewed the connected launch, resume, cleanup, persistence, and journal paths;
the final review reported no remaining material issue within this scope.

## Validation

Host: macOS ARM64, Go 1.26.9. `TMPDIR=/private/tmp` avoids `/tmp` symlink fixture
mismatches. Test processes use a private umask and remove inherited `SWARM_*`
variables: a real parent shim's hook socket otherwise contaminates hook tests.
No provider inference or existing user-session deletion was used.

| Check | Result |
|---|---|
| `go build ./...` | Pass |
| `go vet ./...` | Pass |
| `go test ./internal/persist` | Pass |
| `go test ./internal/worktree` | Pass |
| Focused race command below | Pass, all selected packages |
| Independent daemon deletion/journal race suite | Pass |
| `golangci-lint run --new-from-rev=79e7bd3d` (v2.12.2) | Pass, zero new issues |
| Full `golangci-lint run` | Fails on 11 existing unused declarations in account-native code on macOS |
| Full `go test -race ./...` | Not green; account/platform fixtures, inherited hook environment, macOS reachability, and an installed x86 gobind binary surfaced |
| `git diff --check` | Pass |

```sh
go test -race ./internal/daemon ./internal/skeleton ./internal/persist \
  ./internal/worktree ./internal/protocol \
  -run 'TestDelete|TestDiscussion|TestCleanupContains|TestProjectDiscussions|TestJournal_DeletedRecordOutlivesSessionDir|TestResume' \
  -count=1
```

Representative unrelated failures were reproduced against HEAD's unchanged
daemon/persistence sources using a temporary Go overlay, in the clean test
environment: `TestWritersStoppedForBindingRefusesUnknownRetainedCustody`,
`TestRestrictedClaudeCheckDropsInteractiveConfiguration`, and
`TestLaunchManagedClaudeInjectsDiagnosticsAndFallbackAfterPolicyFilter`.
The inherited-hook tests passed when rerun with that environment cleaned.
Baseline macOS quality failures are tracked in `agents-tracker-nsxw`.
The full run also rejected two Linux-specific process-containment exports in the
macOS production-reachability gate, and the mobile binding gate could not execute
the installed x86 `gobind` on this ARM host.
Two test-only portability corrections let vet compile on macOS: move the shared
`captureLog` helper out of a Linux-only file, and give a Claude enrollment test
the Linux constraint required by its existing fixture.

## Boundaries

This is attempt-directory and owned-worktree deletion. The live-only supervisor
conversion is tracked separately in `agents-tracker-bdp7`. Provider-native history, external project directories,
daemon-wide journal/control stores, and previously delivered phone/relay copies
retain their existing ownership and retention semantics. It is not secure erase.
The initial source verification did not touch existing discussions. The later
owner-authorized production rollout and cleanup are recorded below.

## Production cleanup follow-up — 2026-10-17

Owner-authorized cleanup on v0.18.1 exposed additional deletion boundaries:

- A canceled account alias could acknowledge deletion of an already absent
  successor while retaining the requested source. Deletion now completes the
  durably pinned target first, then removes the explicitly requested alias family.
  Kill retains its existing target semantics; cancellation receipts remain intact.
- Cancellation must be confirmed durable before either effect. After-rename
  directory-sync failure retries repeat the same persistence barrier, and a
  frozen recovery ledger refuses account owner effects.
- Fully hidden, terminal families can be permanently deleted without temporarily
  revealing metadata. A hidden predecessor of a visible discussion still refuses.
- Unknown IDs with existing state payloads, including shared stores, refuse
  without a deletion journal write. A genuinely absent ID remains an idempotent
  no-op, including after restart.
- Shared-workspace checks resolve physical `symlink/..` semantics before path
  cleaning, including when trailing workspace descendants are missing.

Each boundary has a failing-before/passing-after regression. Connected native
macOS ARM64 race checks passed, including actual attempt-directory removal,
injected partial deletion/retry, durable cancellation faults, running-family
exclusion and unchanged cancellation target/receipt checks. The Linux-target
pinned linter passes. Native Mac full lint still reports the same 11 unused
Linux-only account declarations recorded above; broader Mac baseline failures
remain tracked in `agents-tracker-nsxw`.

[v0.18.2](https://github.com/Nathandela/swarm/releases/tag/v0.18.2), source
`2f54b3e2835fbbd50247a7b722d0bf7ad88882a6`, passed all 17 release jobs and was
deployed on both hosts. The corrected API permanently removed all 20 reviewed
non-WIP VM attempts, including the previously retained account alias. All 220
original protected WIP attempt directories remained. The Mac cleanup removed
53 reviewed attempts and retained the discussion controlling this operation.
Executed cleanup and retained WIP migration evidence belong in the
[rollout record](lightweight-storage-rollout.md).
