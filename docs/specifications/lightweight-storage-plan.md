# Lightweight Swarm fixing plan

2026-10-16 · owner-selected direction · implementation tracked in `agents-tracker-bdp7`

Swarm is a live supervisor. Provider CLIs own discussion history. Swarm retains
the small facts needed to supervise processes, resume native discussions, and
avoid repeating accepted actions. This is the implementation plan following the
[two-host investigation and strategy comparison](../research/swarm-lightweight-storage-strategies.md),
governed by [ADR-033](../adr/ADR-033-live-only-supervisor-storage.md).

The [2026-10-17 source checkpoint](../verification/lightweight-storage.md) records
implemented prevention and deletion behavior. [Release and rollout evidence](../verification/lightweight-storage-rollout.md)
records v0.18.0 through corrective v0.18.2 deployed on the owner's Mac and VM,
with checked legacy cleanup. The Mac retains only this operation's controlling
discussion; the earlier trust-blocked discussions were subsequently deleted
under the owner's explicit deep-clean instruction, without changing trust.
Every VM WIP discussion is preserved. Three idle legacy terminals were handed off
to bounded shims; one was already bounded, and two remain held. The owner approved
curated Git recovery, then deferred the final workspace cleanup and requested
only obvious disposal for now. That final batch removed 1.28 GiB of unused
environments, dependencies, caches, previews and a compiled binary. Bulk
relocation was never applied; its automation and temporary tooling are removed.
Completion of retained WIP recovery and old/native diagnostic writer qualification remain open;
the whole program is not complete. The operation-guard pressure flaw is also
corrected and covered by pressure/restart regressions.

The [2026-10-24 storage lifecycle follow-up](storage-lifecycle-plan.md) defines
the warning-only aggregate policy and remaining source work. Its
[implementation evidence](../verification/storage-lifecycle.md) records component
reviews and focused tests; final integrated/platform gates remain pending. This
follow-up is not deployed and makes no fixed-total-footprint promise. Native
histories and successful or uncertain handoff inputs remain retained.

## Stop new accumulation

| Work | Required result | Tracking |
|---|---|---|
| Remove duplicate discussion archives | Generic interaction content lives in a short, bounded RAM window. Phone reconnect/history responses report the actual available boundary. Native CLI history is unchanged. | `agents-tracker-bdp7` |
| Reduce raw terminal persistence | Keep only a small fixed-size identity-discovery tail for adapters that need it, with explicit generation tracking; remove historical rotations and final-screen persistence from new shims. Live attach continues using the shim's in-memory VT screen. | `agents-tracker-bdp7` |
| Enforce the workspace boundary | Refuse agent working directories under supervisor state, including aliases and hook-resolved paths. Explicitly align Codex resume to the saved/resolved project so a legacy native cwd cannot silently re-enter state. Worktree creation remains opt-in. | `agents-tracker-sb4m` |
| Stop test-generated waste | Source implemented and component-reviewed in the 2026-10-24 follow-up: parent supervision cleans copied/registered run-owned executables and migrated scratch after success, panic, timeout and parent TERM. Arbitrary detached system executables need fixture custody; parent SIGKILL/host crash remains outside the guarantee. Reserved login/auth fixtures retain explicit roots. Shared compiler caches are preserved; final gates and rollout remain separate. | `agents-tracker-uh9e` |
| Close live-orphan launch custody | Still open: the existing W4 crash window can lose shim PID custody before native side effects are known. Add a real crash/orphan harness and prevent blind duplicate re-drive. A skipped test is not proof. | `agents-tracker-69qr` |
| Bound native Claude diagnostics | Historical prevention complete: Swarm removed automatic `CLAUDE_CODE_DIAGNOSTICS_FILE` injection in `99d63cbd`, confirmed by the [2026-10-24 audit](../research/disk-growth-audit-2026-10-24.md). Existing old writers and upstream diagnostic behavior still need host-specific qualification; source removal neither stops already-running writers nor proves retained files disposable. | `agents-tracker-f9vv` |
| Bound shared debug output | Source implemented and component-reviewed in the 2026-10-24 follow-up: new daemon/shim owners maintain the validated shared inode, truncating above 8 MiB at open and every five seconds. Bursts can overshoot and old binaries are unchanged. The Mac updater keeps its latest invocation, which can itself grow without a ceiling. Final gates and rollout, including unit reinstallation, remain separate. | `agents-tracker-b3df` |
| Retire folded stopped-hook payloads | Source implemented and component-reviewed in the 2026-10-24 follow-up: new cooperating shims join writers before exit publication; retirement requires exact producer/incarnation proof and a durable fold certificate under the lifetime lock. Compact only certified prefixes, preserving floor/sequence controls. Legacy or uncertain proof retains payload. Final integrated/platform gates and rollout remain separate. | `agents-tracker-j6bv` |
| Define audit retention | Implemented in the 2026-10-21 follow-up: audit-only remote launch activity is capped at 1 MiB, retaining up to 512 KiB of recent complete records on overflow. Larger individual records are omitted. Older forensic history is lost; durable operation/replay custody is unchanged. Host rollout remains separate. | `agents-tracker-85yb` |
| Retire obsolete native execution/profile assets | Still open: distinguish unused executable slots and regenerable cache from native history, credentials, active bindings and plugin work with local-only commits. Directory names alone do not prove disposability. | `agents-tracker-b0o6` |

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

The v0.18.2 production follow-up closes canceled account-alias cleanup,
fully hidden terminal families, unknown shared-state IDs and physical-path
resolution during shared-workspace checks. The release passed all 17 jobs;
both installed supervisors use that release.

Deleting an owned attempt/worktree also removes work stored inside it. External
projects, provider-native history, security receipts and previously delivered
phone copies retain their own ownership and lifetime. No silent secure-erasure
promise is made.

## Reclaim existing buildup

| Existing data | Treatment |
|---|---|
| Generic structured journal history | Stream the legacy records with bounded memory, preserve durable cursor/control coordinates first, then remove duplicate conversation payloads. Startup interrupted at any point must remain retryable without cursor reuse. `agents-tracker-cyhm` joins this migration work. |
| Terminal logs from old shims | Retire the old writer before reclaiming its captures. Unlinking a live file can hide its bytes while the process keeps consuming disk. |
| Build-heavy legacy session workspaces | Identify each discussion's useful code, tests and documents; prefer reviewed Git commits and remote verification before removing disposable workspaces. Existing committed topic branches need no rebase or integration merely to preserve them. Keep active work and unresolved useful/private outputs until classified. Swarm's exited status alone is insufficient writer proof. `agents-tracker-3bxu`, `agents-tracker-3x3c`. |
| Previously hidden attempts | Discover them explicitly. Apply validated family/workspace ownership and stopped-writer checks; do not turn a version upgrade into an indiscriminate recursive deletion. |

Produce a concrete per-host cleanup inventory before deleting existing workspace
contents. Development tests use temporary data. The source fixes themselves do
not reclaim the hundreds of GiB already accumulated on the Mac or VM.
The [initial cleanup inventory](../verification/storage-cleanup-inventory.md)
separates measured cache candidates, live writers and unresolved ownership.
The [rollout checkpoint](../verification/lightweight-storage-rollout.md) records
the executed batches and retained material; its cleanup results supersede the
read-only inventory. Native test results remain a separate verification record.

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
pending requests across ordinary RAM-history eviction. The pressure fix refuses
new operation IDs at the 100,000-entry admission ceiling while preserving exact
retries, all still-valid guards and legacy over-cap records. Only the existing
24-hour expiry releases their slots; signed commands expire within one hour.
Repeated transitions compact the log. Pending approvals and exact sealed outbound
custody have pressure/restart regressions (`agents-tracker-aivi`).

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
