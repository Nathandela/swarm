# Lightweight supervisor storage verification

2026-10-17 · `agents-tracker-bdp7`, `agents-tracker-sb4m`, `agents-tracker-cyhm`

The owner-selected [fixing plan](../specifications/lightweight-storage-plan.md)
and [ADR-033](../adr/ADR-033-live-only-supervisor-storage.md) replace duplicate
discussion archives with live supervision. This is a source implementation
checkpoint, not evidence of deployment or production space reclaimed.

## Implemented boundaries

| Resource | Result |
|---|---|
| Generic interaction journal | Payloads stay in an 8 MiB encoded-event RAM window. No generic conversation body is written to control segments. Current pending approvals are pinned until resolution/expiry. |
| Durable control journal | Existing lifecycle/WAL, status, capability and gap records use a 64 KiB rotation target, 16-segment retention and a 1 MiB control budget. Existing accepted-operation/security/delivery stores retain their own custody. |
| Stream cursors | Reserve a durable ceiling before transient publication. Durable controls burn unused reservations so legacy on-disk replay remains ordered. Failed controls never expose an unpersisted candidate through `Cursor` or `ReadFrom`. Exhaustion fails closed. |
| Legacy journal migration | Stream records with a 1 MiB per-record limit, atomically commit compact control segments and cursor custody, then remove old segments. Large files are not loaded whole. Torn final lines are tolerated; oversized/corrupt/future-schema evidence fails closed and remains available. |
| Raw output | New shims write only a 64 KiB ring plus a 32-byte header. Generations and CRC32C reject concurrent/torn reads. Partial writes latch failure; queue pressure keeps newest evidence. No raw log rotations or final-screen files. |
| Native identity | Preserve authenticated hook/backend capture and legacy-tail readers. The new ring is generation-gated, including constant-size wraps, and quiet evidence retries failed metadata writes. Ended tails retire after the native identity is durable. |
| Agent workspaces | Refuse requested and hook/finalizer-resolved cwd paths inside supervisor state, including aliases. Codex resume explicitly uses the saved/resolved project; legacy backend configs without a separate cwd also default to the agent project. |
| Explicit deletion | Existing complete-family removal remains; deletion also erases that session's cached RAM discussion. Independent launches, handoffs and external projects retain their ownership. |
| Test waste | Shared per-run binary cleanup now matches kernel executable paths on macOS/Linux, preserves unrelated same-name/argv processes, and removes TUI/skeleton helper binaries. |

The RAM event bound is not an RSS cap: structs, current approval pins, live VT
screens and other existing bounded caches have additional memory overhead.
Whole-item eviction avoids replaying a message tail as a complete message.
After 4,096 unfinished evictions, a session stops caching ordinary prose until
its terminal sweep; live fanout and approval/control progress continue. This
cache pressure fallback adds no command authority or provider behavior.

## Validation evidence

Host: macOS ARM64, Go 1.26.9, golangci-lint 2.12.2 (official native asset,
checksum verified). Tests use shared compiler/module caches, `TMPDIR=/private/tmp`,
private umask and no inherited `SWARM_*` variables. Final broad checks use four Go
CPUs. No provider inference or existing-session deletion was used.

| Check | Evidence |
|---|---|
| `go build ./...`, `go vet ./...` | Pass |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` | Pass |
| Full `-race`: journal, transcript, shim, Codex adapter, e2e, protocol, worktree | Pass; final journal/transcript rerun also covers candidate-cursor failure, RAM deletion and torn-payload checksum |
| Focused daemon `-race`: real kill/reconnect, 50-restart soak, side-files, state-workspace refusal, live-only journal and deletion | Pass |
| Focused skeleton `-race`: identity generations/quiet retry, resume, deletion, hook draining and live-history floors | Pass |
| Real CLI/role `-race`: pilot workers and shim launch/re-exec | Pass |
| Final skeleton integration `-race`: installed CLI refresh, real backend topology, all-stage hook kill matrix, tap latency | Pass; measured tap latency 335.5 ms against 1 s |
| Real shim legacy backend cwd fallback/override and large no-consumer output `-race` | Pass; exact tail file bound 65,568 bytes |
| macOS test-binary sweep, same-name non-target survival, TUI 50-session gate; Linux compile-only checks | Pass |
| `golangci-lint run --new-from-rev=f87bf66b` | Pass, zero new issues |
| Full lint | Existing 11 unused account-native declarations on macOS; tracked in `agents-tracker-nsxw` |
| Full `go test ./...` | Not green on this host. The broad run identified obsolete archive/cwd fixtures, which were adapted and rerun successfully; existing account/platform and toolchain failures remain. |
| Production reachability gate | Only the existing macOS Linux-specific process-containment exports remain after an explicit test-fixture exemption for the old rotating transcript constructor. |
| `git diff --check` | Pass |

Connected restart tests now distinguish durable ingress from a discussion
archive: accepted but unfolded hooks replay once; already folded hooks remain
durably deduplicated; each daemon's live content is checked before retirement;
reopening loses prior prose and reports an honest resync/history floor. The
kill-stage matrix checks both crash seams, durable `hook.applied` membership,
duplicate rejection after restart and a fresh complete stage batch.

Independent agent review covered cursor reservation/replay, failed rotation,
partial migration, whole-item eviction, pending approvals, saturation progress,
session-end ordering, fixed-tail failure handling and native cwd behavior.
Reported blockers were corrected with direct regressions. The final source
review found no remaining material issue within the chosen host live-only cut.

## Remaining full-plan work

- Source fixes have not been deployed, and old live shims still use their old
  logging behavior until they retire. Native provider history and existing phone
  caches are unchanged. Host history after restart/eviction is unavailable;
  native CLI history remains the owner's history surface.
- The [Mac cleanup inventory](storage-cleanup-inventory.md) measured 75.14 GiB
  under Swarm, including 13.16 GiB of recognizable cache candidates. It proves no
  candidate writer-free: several exited/lost rows still have native process cwd
  references, and the largest thread's original project directory is missing.
  Existing workspaces were not moved or purged (`agents-tracker-3bxu`).
- The VM was unreachable over SSH during this follow-up. Its earlier figures
  are historical; current Linux runtime and production before/after growth
  measurements remain unrun.
- Managed Claude diagnostics remain an uncapped native writer. Its 16 MiB reader
  limit is not a disk bound. `agents-tracker-f9vv` records the required bounded
  ingestion characterization without discarding exact model-recovery proof.
- Rate/count-pressure proof for still-valid operation guards remains the separate
  `agents-tracker-aivi` investigation. Existing sealed outbound envelopes and
  ingress/control receipts were preserved; paired delivery still performs writes.

No global quota manager, background janitor or native-history backfill service was
added. This change bounds Swarm's duplicate capture; it does not cap arbitrary
agent builds in user projects or establish a whole-machine disk guarantee.
