# Storage lifecycle implementation contract

2026-10-24 · `agents-tracker-5jmx` · draft for Sol and Astra review

Work is isolated on `feat/bounded-supervisor-storage`, based on `179073ab`.
Beads tracks execution; this document defines scope, ownership and acceptance.
Evidence comes from the [disk audit](../research/disk-growth-audit-2026-10-24.md).

## Owner intent and product policy

Keep the live-only supervisor and its daemon/shim survival architecture. Remove
unnecessary accumulation, make storage visible, and clean only demonstrably
disposable owned data. Native histories, projects, credentials and unresolved
operation receipts remain preserved. There are no new history copies.

The owner explicitly selected **warn only; allow continued growth** at the
overall supervisor storage threshold. This supersedes the proposed aggregate
admission refusal. Do not reject launches, user operations or retries merely
because an advisory threshold is exceeded. Existing correctness limits, safe
write failures and fail-closed recovery guards remain unchanged. Local diagnostic
retention can discard old logs; it must never discard accepted control custody.
This program cannot promise a fixed total footprint or prevent arbitrary agent
tools from filling a disk.

Production-ready means reviewed source, passing applicable quality gates and
controlled integration evidence. It does not mean installed on the live Mac/VM,
published as a release, or permission to purge existing storage. No release tags,
main merge, native-history deletion, old temporary-directory sweeps or deployment
are part of this task.

## Ownership and integration

`feat/native-login-handover` exclusively owns login/native-home selection,
credential migration/handover, execution/continuation records and their small
typed `internal/persist` helpers. Do not change `internal/logins`,
`internal/loginmigration`, `internal/loginworker`, `internal/skeleton/logins*`,
or execution/continuation persistence. Existing history copies stay in place.

This branch owns general storage diagnostics, runtime diagnostic log retention,
hook-payload retirement, OpenCode handoff artifact lifecycle, updater temporary
cleanup, and generic test-process/scratch supervision. Shared idempotency retention
is unchanged. No `internal/persist` production changes are planned; any newly
required overlap must be coordinated before editing. Narrow edits to shared
`daemon/launch.go`, `shim/shim.go`, `skeleton/serve.go` and `skeleton/api.go` must
remain independent of auth/execution logic and be documented for integration.

Use the existing standard library and project process/locking primitives. No
storage service, generic garbage collector, history importer, new dependency or
replacement storage engine. Use shared existing Go/module/Gradle caches. Run
negative controls in fixtures or isolated subprocesses, never by temporarily
breaking the shared source tree. Stage and commit exact owned paths.

## Implementation waves and TDD contracts

### 1. Updater temporary payloads

Luna owns `packaging/systemd/swarm-upgrade.sh` and its existing tests under
`internal/remote/supervise`. Run the fetch function in a subshell so its EXIT trap
finishes before the final exec. Keep unconditional unattended convergence, exact
restart exit status, checksum verification and install sequencing.

Red test executes the unchanged script using an isolated release fixture: repeated
successful upgrades leave temporary payloads today. Green requires zero retained
owned payloads after three successes and download/checksum/install failures,
with restart still attempted and its status preserved. No network, real sudo or
production binary modification. SIGKILL remains outside an EXIT-trap guarantee.

### 2. Read-only accounting and warning semantics

Sol owns `cmd/swarm/doctor.go` and a focused storage helper/test in that package.
Reuse doctor findings/JSON rather than add a command or UI. Report allocated and
logical bytes beneath the configured state root, with non-overlapping categories
for supervisor state, managed login/account data and other retained material.
Include managed native homes in the visible total; do not pretend they disappear
because another component owns their format. External native homes/projects and
temporary caches are explicitly outside this scan.

The scan is metadata-only: no file contents, startup, migration, locks, writes,
deletion, or symlink traversal. Deduplicate hard-linked files within the scan;
state-root filesystem availability is distinct from per-category usage. Bound
work (entry budget and elapsed deadline); incomplete traversal, stat failures
and unsupported allocation reporting yield an explicit partial/unknown result.
Never report partial measurements as exact totals or reclaimable bytes.

Initial advisory defaults: warn at 256 MiB of total state-root allocation,
including managed login/account and other retained material, or
less than 1 GiB available on its filesystem. Report managed/other categories
regardless of threshold. These are diagnostic defaults, not capacity promises;
no new configuration knobs are needed for the first version. A warning preserves
doctor's exit-zero semantics when no independent failure exists and changes no
launch/operation path. Document on-demand diagnosis, not proactive monitoring.

Red/green tests: known nested allocations and category totals; hardlink dedupe;
symlink outside-root exclusion; absent state remains absent; scan limit/error
shows partial; injected low-space and over-threshold values yield warn/exit zero;
JSON and text retain units/coverage; large native data remains visible without
being mislabeled disposable. Read-only inspection never requires world traversal.

### 3. Shared diagnostic logs

Sol owns the diagnostic log helper and narrow wiring in daemon/shim entry points.
All existing writers/callers must be traced. Maintain the recognized regular-file
inode; never rename/unlink a log under inherited descriptors. Validate file type,
ownership/link safety and mode using the open descriptor. Use same-inode
truncation above 8 MiB, checked at opening and periodically by each new long-lived
owner, including surviving new shims while the daemon is absent. Carry the
configured diagnostic log path in the private shim launch config; verify the
inherited descriptor is the same owned inode before maintaining it. Never trim
arbitrary redirected stderr. Unsafe initial log targets remain rejected; failure
of maintenance on a validated descriptor is best effort. Retention work
is independent of PTY draining. Maintenance errors are visible and cannot turn a
healthy launch into a storage-budget refusal.

This is intentionally best-effort retention: old diagnostic history is lost,
bursts can overshoot between checks, old binaries are unchanged, and a stopped
maintainer cannot promise a bound. Do not call the threshold a hard ceiling.
The Mac updater template opens its existing upgrade log once per invocation with
truncate and redirects both streams to that owned shell descriptor; remove the
competing launchd StandardOutPath/StandardErrorPath file descriptors. Preserve
the existing command sequence and last command exit code. Validate private-path
and existing-file safety before truncation. Prove the rendered command and a
task-owned temporary launchd job; never modify the deployed unit during tests.
Do not alter Homebrew global cache cleanup or update security. This retains the
latest run, without a strict size bound on one invocation; state that explicitly.

Red/green tests: two inherited append descriptors continue reaching the same
linked inode after truncation; a live new shim maintains retention with daemon
absent; mode hardening; symlink/hardlink/foreign-file refusal; injected maintenance
failure; stop joins maintainer; no PTY dependency; multiple busy intervals settle
under the threshold. Updater log fixture proves repeated invocations do not
accumulate past-run logs and preserves final status. Production unit reinstallation
remains a deployment requirement.

### 4. Stopped hook payload retirement

Sol owns `internal/shim/hookspool.go`, `internal/skeleton/hookdrain*.go` and narrow
shim/end-session wiring. Reuse the existing `HookSpool.Compact` crash-safe
floor/rename algorithm. No whole-session deletion or custody expiry.

Retirement needs three proofs: the producer is quiescent, the durable applied
cursor belongs to the exact spool incarnation, and no writer/reopen can race
compaction. An exit report is insufficient: current shim reports exit before hook
handler shutdown/join. New shims acquire an exclusive lifetime producer lock
before opening the spool and persist capability proof bound to its incarnation
under that lock. All new producer opens use the same lock. Shutdown joins hook
handlers and closes the spool BEFORE publishing the terminal report, retaining
the lock until the close is complete. Retirement nonblock-acquires that lock,
verifies the capability/incarnation and cursor certificate, and holds it through
reopen/compaction. On crash, OS lock release plus exact shim identity/death proof
permits the same path. Legacy writers without bound capability proof are retained.

Keep final drain before engine token retirement, after the new shim's joined
shutdown ordering. A busy/uncertain lock leaves payload untouched. Register only
that exact attempt for bounded retirement retries through the existing lifecycle
loop; after restart, terminal new-writer records with certified cursors get the
same conservative retry. Retries compact only already-certified prefixes and
never need to resurrect engine authentication or accept new old-session hooks.

Keep the existing numeric fold cursor backward-compatible. Add a small atomic
incarnation/certified-sequence association if needed; publish it only after the
applied cursor is durable. A missing/stale/failed certificate means no retirement.
Never certify an old numeric cursor merely because it is below the current last
sequence. Maintain incarnation, floor, high-water and gap/torn-tail evidence.
Persistence failures retain accepted data. Retry is limited to still-provable
lineage; a lost physical capability after replacement conservatively retains
payload instead of manufacturing disposal authority.

Red/green: real final drain of a stopped new writer retires fully folded payload;
partial fold preserves later records and torn bytes; reopen continues sequence;
live/unknown/legacy producer refuses retirement; replacement incarnation does not
reuse old certification; cursor/certificate/compaction ENOSPC and crash seams
preserve either old or safe new state; replay and gap behavior unchanged. A final
drain test must cover the actual end-session path, not only a helper.

### 5. OpenCode handoff artifact ownership

Sol owns `internal/skeleton/opencode_history.go`, handoff tests and narrow
`coreAPI.Launch`/composition changes. Validate everything possible before export.
Carry explicit newly-created artifact ownership through composition and launch;
never infer deletion authority from a prompt, basename or arbitrary resolver path.
Store new exports under a private state-level handoff-export directory independent
of the source attempt directory, so deleting the source cannot remove the
successor's input. Preserve existing legacy paths; before deleting a source with
legacy exports, reject deletion if a retained successor's launch configuration
references them, including unreadable/ambiguous dependency evidence. Do not move
legacy files or rewrite saved prompts.

Remove only a newly owned export when launch definitely failed before a successor
could reference it. Retain on ambiguous acceptance, persisted successor or cleanup
failure. Existing provider history is never a cleanup target.

Successful exports are immutable inputs to successors. Avoid duplicate successful
exports for identical native history where safe by canonical content identity that
excludes capture timestamp; validate an existing candidate before reusing it and
never overwrite it. A reused file is not newly owned and cannot be removed by a
failed later launch. Serialize all production hands-off composition through
launch outcome with a dedicated coreAPI handoff lock: a creator cannot fail and
delete a shared artifact after another handoff has reused it. Acquisition precedes
export lookup/publication; release follows definitive cleanup or retained launch
outcome. Do not hold general lifecycle/auth locks during provider export. Test
the creator-fails/concurrent-reuser-succeeds interleaving explicitly. Native export
calls outside launch remain retained and must participate in publication ownership
if they can race the production path. Keep distinct history versions intact.
Do not invent a new durable reference database or automatic successful-history
retirement in this
wave; count/report retained exports and state this limit. Native histories and
successful handoff inputs remain preserved rather than time-expired.

Red/green: repeated identical export reuses immutable content; changed history
gets a distinct file; prelaunch validation and definite launch failure leave no
new unused export; reused/exported provider path is never deleted; ambiguous or
partially accepted launch preserves its input; concurrent identical exports and
hostile existing path are handled safely; history reads/resume still remain RAM
paths; restart preserves exact referenced content. Existing source/child deletion
semantics must not be weakened by this change; any discovered dependency flaw
must be resolved or explicitly block this wave's production-ready verdict.

### 6. Test-process and scratch supervision

Sol owns `internal/testbin` and process-heavy package TestMain wiring. Luna can
handle mechanical short-path temporary-root migrations after the helper contract
is approved. Integrate into normal `go test` and race runs, not an unused wrapper.

Use a parent outside the `m.Run` process: a unique short run root, one copied test
executable for exact ownership, child execution, and cleanup after child success,
panic or timeout. Reuse existing platform executable/PID-start primitives; never
kill by name/argv or sweep historical prefixes. Register/contain all run-created
test binaries and temporary roots; migrate hardcoded `/tmp` roots in the covered
suites so they do not silently escape. Preserve fake-agent dispatch modes.
The covered process set is the copied test executable and registered run-owned
executables. Arbitrary detached system programs outside that set require their
fixture to retain exact child custody and cleanup; an executable-path sweep is
not a universal process sandbox. Audit the covered suites for such fixtures and
give them explicit stop/wait, without killing unrelated system binaries.
Privileged or nondumpable fixtures also need explicit PID/start or process-handle
custody; Linux kernel-owned executable metadata is outside generic user-level
path inspection. Unknown errors within inspectable ownership still fail cleanup,
after verified owned children have been stopped.

Stop and wait for verified owned processes before removing their files. Surface
kill/remove/enumeration failures; retain evidence and fail an otherwise successful
test run on incomplete cleanup. Preserve child failure status, output, coverage
and race behavior. Shared compiler caches remain outside run-owned scratch and
are neither copied nor removed. Parent SIGKILL/host crash remains an explicit
limit; no speculative persistent janitor is required.

Red/green: real child starts a detached owned survivor, then normal/panic/timeout
paths all leave no owned processes/root; same-name/argv unrelated process survives;
TERM-ignoring registered child is reaped; an identity mismatch observed before
signaling is never signaled (portable check-then-signal still has a residual PID
reuse race); cleanup failure
is visible and retains its root; no recursive supervisor; existing fake helpers,
full test invocation and race coverage still work. A tiny working-agreement note
documents shared caches and exceptional scratch ownership, without a new test that
merely mirrors prose.

## Review and qualification

Before implementation: independent Sol and Astra must review this complete
contract against owner intent, call sites and failure modes. Both must explicitly
approve the same revision; blockers revise the plan before dependent work starts.
Implementation agents own disjoint files. Every nontrivial change starts with a
behavioral failing test, implements the smallest complete fix, then receives
independent Astra review. Root resolves interface/file conflicts and performs a
separate connected-code review. Beads records work, failures and disposition.

Record compact red/green commands and outcomes under `docs/verification/`; do not
retain build environments as evidence. Shared cached native ARM Go is used on the
Mac. Native Linux tests use an isolated temporary tree or controlled container;
do not install the candidate or disturb existing VM sessions. Do not run multiple
heavy whole-repo suites concurrently.

Final gates: build, vet, lint, full normal suite and required race coverage;
platform-specific log/process/updater tests on Darwin and Linux; existing replay,
survival, deletion and native-history regression coverage. Run a controlled fixed
workload with repeated launches/exits/handoffs and aborted test runs, measuring
peak and settled owned bytes. Warning-only growth past the advisory threshold is
expected; growth of known disposable payloads after their retirement boundary is
not. Injection at accepted-state/atomic replacement boundaries verifies old-or-safe
new records and zero lost custody. Physical full-disk experiments are unnecessary
where injected failures establish the relevant path; claims must match evidence.

Sol and Astra perform final independent integrated reviews; iterate until no
unresolved correctness/safety blocker remains. Baseline platform failures must be
reproduced separately and reported, never relabeled green. Commit and push the
feature branch and issue records; leave unrelated worktrees, stashes, caches,
production files and native processes alone. Update the original storage plan's
stale diagnostic/retention claims only with the implementation evidence.
