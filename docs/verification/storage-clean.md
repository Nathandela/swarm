# Manual storage cleanup implementation evidence

Source commit: [`56899d97`](https://github.com/Nathandela/swarm/commit/56899d97ab6b9a644735604345adfda7668396de)  
Source tree: `f13f8c45d205584bcd0c0fcea9851e693d1ae4d0`  
[Reviewed plan](../specifications/storage-clean-plan.md) · [Operator guide](../ops/storage-clean.md) · [Machine-readable gate evidence](storage-clean-quality-gates-2026-10-24.json)

Sol and Astra approved the plan and corrected source. Build, vet and lint pass
for both targets; full race suites pass on native macOS and Linux. Linux normal
testing passes outright. macOS normal coverage required a successful, unchanged
package retry after one unrelated cancellation timeout. That failed invocation
remains recorded as failed; this is not a claim that every command passed first try.

## Scope and intent

`swarm clean` previews; `--apply` reassesses and applies proven cleanup. `--json`
uses the same results. It neither starts/stops a daemon nor creates state or a
report file. It trims only the configured diagnostic log above the existing
8 MiB threshold and compacts certified prefixes of stopped hook spools. Hook
cleanup needs custody of the existing configured daemon singleton and producer
locks. No new protocol, background job, dependency or persistent cleanup index.

Native histories and old copies, credentials/accounts, projects/worktrees,
handoff inputs/staging, session metadata, replay/idempotency state and unresolved
recovery receipts remain protected. Warning-only budgets still permit growth.
No production cleanup or deployment was performed. No rotator-owned paths or
production `internal/persist` files changed. Integration/rollout remains tracked
separately in `agents-tracker-ih2v`; scratch ownership is `agents-tracker-r9vj`.

## Qualification

Native macOS: Darwin 27.0.0 arm64, Go 1.26.9. Native Linux: Linux
5.15.0-198-generic x86_64, Go 1.27.3. Both used existing shared compiler caches,
`GOTOOLCHAIN=local`, `GOMAXPROCS=2`, supervised temporary roots and umask `0022`.
The Linux checkout's Git tree matched the source above. Linux lint used the
existing macOS golangci-lint 2.12.2 binary with
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0`; it was a target check, not native Linux
linter execution.

| Gate | macOS | Linux |
|---|---|---|
| `go build ./...` | Pass, 7 s | Pass, 7 s |
| `go vet ./...` | Pass, 3 s | Pass, 4 s |
| golangci-lint | Zero issues | Zero issues, Linux target |
| Full normal tests | 5,979 passed; 64 skipped; 1 timeout, 921 s | 5,982 passed; 62 skipped; 0 failed, 741 s |
| Unchanged failed-package normal retry | 28 passed; 1 skipped; 0 failed | Not needed |
| Full race tests | 5,980 passed; 64 skipped; 0 failed, 1,110 s | 5,982 passed; 62 skipped; 0 failed, 909 s |

Counts are top-level tests. Full normal/race commands were
`go test [-race] -json -p 2 ./... -count=1 -timeout=30m`. Lint used
`--timeout=10m --max-issues-per-linter=0 --max-same-issues=0`. The JSON evidence
retains exact commands, log hashes, package counts, skipped tests/reasons and
original failures; raw task logs are retired after preserving that evidence.
No release, hosted-provider or physical-device qualification is implied.

## Falsification and review corrections

CLI tests first failed because `clean` did not exist. Initial implementation
`1a88823c` passed focused macOS race checks for `cmd/swarm`, `diagnosticlog`,
`shim` and `skeleton` (4.803 / 1.369 / 10.105 / 12.341 s). Native Linux then
falsified a safety assumption: `TestHookCleanupRefusesReplacedTemporaryInode`
failed in both full normal and race runs. Closing the temporary descriptor let
Linux reuse its inode, so `os.SameFile` accepted unrelated recreated bytes.

The correction keeps the temporary descriptor open through rename, replacement
reopening, identity validation and deferred cleanup. Close errors stay visible,
including after partial progress. Sol and Astra independently reviewed this fix.
The regression now passes in the final full Linux normal/race runs. An overlay
run also exposed an umask-dependent fixture; the intended private producer
fixture now explicitly uses `0700`, and the corrected focused Linux run passed
under the VM's default `0002` umask. Production ownership checks were preserved.

Other tested review corrections covered raw `symlink/../log` paths, physical
containment of custom singleton paths, and a reproduced macOS case-alias lock
collision. Unsafe aliases, hard links and FIFOs are retained. A mutation followed
by failure is reported as an error, and unknown replacement identity never
produces a reclaimed-byte claim.

Coverage includes unchanged preview snapshots/no daemon startup, missing-root
no-creation, existing-mode/inode preservation with append writers, exact applied
prefix removal with byte-identical unapplied/torn suffixes, busy/unknown producers,
stale/malformed/oversized proofs, scan limits, stdout failure after lock release,
ENOSPC/proof-write failure, source/temp/parent replacement and protected receipts.
Existing ingestion-persistence and compaction crash regressions also pass.

## Disclosed timeout follow-ups

The macOS normal failure was unchanged
`internal/loginworker.TestStaleWorkerIdentityCannotCancelOrReadDevice`, at final
valid cancellation under its shared five-second context. The exact delayed phase
is unproven. The same test passed in isolation (0.40 s), its complete unchanged
package passed on retry, and it passed in the final full race run (1.76 s).
Follow-up: `agents-tracker-moos`; rotator ownership remains reserved.

The initial Linux race run also timed out in unchanged
`TestReconnect_HydratedBannerExpires` at its three-second hydration wait, before
the expiry assertion. The final full Linux race run passed that test (6.17 s).
Follow-up: `agents-tracker-lrgf`. No TUI source was changed; neither test directly
executes the new cleanup path. The original failures are preserved in the JSON.

## Limits and artifact retention

Discovery is limited to 10,000 immediate entries and a cooperative two-second
budget; in-progress filesystem calls are not preempted. Hook reads stop at
64 MiB, retaining larger spools. Busy, legacy or uncertain data stays retained.
Compaction needs temporary space; interruption can leave unproven staging that
this command deliberately does not sweep. Observed allocation changes do not
guarantee physical free space. Identity checks protect cooperative lifecycle
operations and accidental aliases, not a malicious same-UID filesystem actor.

The task reuses the existing isolated worktree and shared caches. Task-private
caches created during an early test attempt were removed and their absence was
verified. After evidence review and commit, the task-owned Linux checkout (167,858,176
allocated bytes) was removed with no observable process references; three
unrelated process FD lists were opaque and no processes were signaled. The
reviewed local overlays and 28 task-owned evidence/helper files were also
removed. Compact evidence remains committed, and the source worktree is retained
for review. User state, histories, unrelated worktrees and shared caches are not
part of that cleanup.
