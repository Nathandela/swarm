# Lightweight Swarm fixing plan

2026-10-16 · owner-selected direction · implementation tracked in `agents-tracker-bdp7`

Swarm is a live supervisor. Provider CLIs own discussion history. Swarm retains
the small facts needed to supervise processes, resume native discussions, and
avoid repeating accepted actions. This is the implementation plan following the
[two-host investigation and strategy comparison](../research/swarm-lightweight-storage-strategies.md),
governed by [ADR-033](../adr/ADR-033-live-only-supervisor-storage.md).

The [2026-10-17 source checkpoint](../verification/lightweight-storage.md) records
implemented prevention and deletion behavior. Deployment, verified per-host
legacy cleanup, native diagnostic bounding and the remaining pressure proof are
still open; the whole program is not complete.

## Stop new accumulation

| Work | Required result | Tracking |
|---|---|---|
| Remove duplicate discussion archives | Generic interaction content lives in a short, bounded RAM window. Phone reconnect/history responses report the actual available boundary. Native CLI history is unchanged. | `agents-tracker-bdp7` |
| Reduce raw terminal persistence | Keep only a small fixed-size identity-discovery tail for adapters that need it, with explicit generation tracking; remove historical rotations and final-screen persistence from new shims. Live attach continues using the shim's in-memory VT screen. | `agents-tracker-bdp7` |
| Enforce the workspace boundary | Refuse agent working directories under supervisor state, including aliases and hook-resolved paths. Explicitly align Codex resume to the saved/resolved project so a legacy native cwd cannot silently re-enter state. Worktree creation remains opt-in. | `agents-tracker-sb4m` |
| Stop test-generated waste | Remove run-owned temporary binaries and reap only processes using those unique executables on macOS and Linux. Preserve unrelated processes and shared compiler caches. | `agents-tracker-uh9e` |
| Bound native Claude diagnostics | Still open: the native diagnostic writer is uncapped, while the reader stops at 16 MiB. Preserve exact current prompt/request/model proof; characterize bounded shim-owned ingestion rather than silently dropping evidence or guessing a model. | `agents-tracker-f9vv` |

The implementation uses small bounds inside existing stores. It does not add a
global quota manager, a background janitor, a new archive service, or native
history import/backfill. Agent builds in real project directories remain agent
work; this plan does not promise to cap every tool on the machine.

## Make deletion mean deletion

`agents-tracker-fxsl` is implemented in `f87bf66b`: explicit deletion of a stopped
discussion removes all retained attempts in its validated resume family and
their owned worktrees. It no longer hides predecessors while preserving their
files. Shared workspace use refuses removal; cleanup errors remain retryable.
[Verification](../verification/discussion-deletion.md).

Deleting an owned attempt/worktree also removes work stored inside it. External
projects, provider-native history, security receipts and previously delivered
phone copies retain their own ownership and lifetime. No silent secure-erasure
promise is made.

## Reclaim existing buildup

| Existing data | Treatment |
|---|---|
| Generic structured journal history | Stream the legacy records with bounded memory, preserve durable cursor/control coordinates first, then remove duplicate conversation payloads. Startup interrupted at any point must remain retryable without cursor reuse. `agents-tracker-cyhm` joins this migration work. |
| Terminal logs from old shims | Retire the old writer before reclaiming its captures. Unlinking a live file can hide its bytes while the process keeps consuming disk. |
| Build-heavy legacy session workspaces | Inventory each affected native thread and actual writer, identify the intended project, and preserve any unique work before relocation/removal. Swarm's exited status alone is insufficient proof that no native writer exists. `agents-tracker-3bxu`. |
| Previously hidden attempts | Discover them explicitly. Apply validated family/workspace ownership and stopped-writer checks; do not turn a version upgrade into an indiscriminate recursive deletion. |

Produce a concrete per-host cleanup inventory before deleting existing workspace
contents. Development tests use temporary data. The source fixes themselves do
not reclaim the hundreds of GiB already accumulated on the Mac or VM.
The [current cleanup inventory](../verification/storage-cleanup-inventory.md)
separates measured cache candidates, live writers and unresolved ownership;
unreachable VM measurements remain historical.

## Keep the small safety state

Keep process/native IDs, paths, names and resume links; exact accepted-operation
outcomes; replay/sequence high-waters; uncertain direct-input and ContextGuard
holds; account/credential ownership; and exact sealed outbound envelopes still
awaiting delivery. Hook spools remain temporary ingress custody until safely
folded. These are different from a conversation archive.

Live approval authority remains tied to current CLI evidence. The current
implementation does not reconstruct provider approval RPC authority from saved
chat records after a supervisor restart. The lightweight change must never turn
an old card or old connection's request ID into new authority. Protect current
pending requests across ordinary RAM-history eviction; verify command guards and
delivery custody under pressure (`agents-tracker-aivi`).

## Finish on both machines

Implementation order: enforce workspace/test cleanup boundaries, remove duplicate
capture and add the bounded legacy-journal transition, verify connected lifecycle
and phone behavior, then perform the per-host legacy cleanup and rollout.

Acceptance requires large-output tests showing flat capture size, monotonic
cursors after crashes/restarts, native resume and exit-only ID capture, truthful
history floors, current-only approval authority, retryable deletion, and unchanged
accepted-command replay protection/exact outbound custody. Measure the Mac and
VM before and after cleanup, then repeat a fixed workload and verify that the
same class of files no longer accumulates. A smaller initial `du` is insufficient
evidence that the cause is fixed.

The tradeoff is explicit: older conversation detail may be unavailable from the
host after a supervisor restart or RAM-window eviction; the provider CLI remains
the history source. Existing phone caches are outside this host-storage change.
Pending outbound custody can still require disk writes while a gateway is paired.
