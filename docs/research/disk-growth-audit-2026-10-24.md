# Swarm disk growth audit

2026-10-24 · source `8a50ecda` · investigation `agents-tracker-n8xa`

Swarm coordinates native coding agents and preserves live terminals across
dashboard and daemon restarts. This audit assumes one owner, many retained
discussions, concurrent agents, repeated upgrades and development/test runs.
Three investigation agents covered runtime, native/workspace and auxiliary
storage; two independent Astra agents challenged their conclusions. An additional
investigation pass covered development caches and aborted tests.

**The concern is real, but there are several different causes.** On the Mac,
ordinary Swarm state is small today; development caches dominate the identified
Swarm-related temporary footprint. On `tf_nathan`, managed native histories are
the largest component inside Swarm state, and sampled history duplication is
confirmed. Several smaller runtime leaks also remain. Existing live-only
capture fixes work in the tested paths and should be retained.

This is an investigation and option comparison, not a cleanup or implementation.
No production files were deleted, sessions restarted, provider prompts sent,
or runtime configuration changed. Measurements are live snapshots, not a fixed
workload growth rate or a promise of reclaimable bytes.

## Measurements on both hosts

Sizes below use allocated KiB from `du`, converted to binary units. Subcategories
are included in their parent and must not be added twice. Native paths were
resolved on the VM: its original provider homes are on the separate data volume,
while Swarm state is on root. Transient/unreadable entries make the VM native and
temporary totals incomplete. APFS sharing and concurrent activity limit exact
physical-space conclusions on the Mac.

| Category | Mac | `tf_nathan` |
|---|---:|---:|
| Installed `swarm version` | 0.19.0 | 0.22.0 |
| Swarm state | 21.8 MiB | 10.65 GiB |
| Managed login storage, inside Swarm | Not present in sampled state | 7.49 GiB |
| Managed Claude project histories, included above | — | 6.13 GiB |
| Managed Codex session histories, included above | — | 0.75 GiB |
| Older account storage, inside Swarm | 4 KiB | 1.15 GiB |
| Original native Codex home | 10.9 GiB | Approximately 6.38 GiB |
| Original native Claude home | 1.56 GiB | Approximately 8.69 GiB |
| Temporary directory | 83.7–84.4 GiB across scans | At least 5.59 GiB |
| Swarm-prefixed immediate temporary directories | 31.26 GiB | Not classified by this naming rule |
| Largest identified Swarm development Go cache | 14.02 GiB | — |

The Mac had 14 persisted attempts and approximately 18.2 MiB of legacy raw
captures in one attempt. The VM had 223 metadata rows; known hook spools totaled
only 2,579 content bytes, and legacy raw captures about 19.3 MiB. Thus saturated
hook spools and terminal archives do **not** explain today's measured VM GiBs.
The VM had about 12 GiB available on root and 22.9 GiB on its data volume;
the Mac reported about 202 GiB available. Neither was full during these scans.

Six bounded JSONL samples, three per provider, were hashed in place on the VM.
All six managed/source pairs were byte-identical, had distinct inodes and single
links, and resided on different filesystems. The six managed copies contain about
88 MiB, duplicated by their source counterparts.
This establishes real duplicated history, without claiming the entire managed
history set is redundant. There were 2,489 Claude and 197 Codex matching-size
candidates within the sampling size range; those counts are not full hash proof.
No conversation contents or credential values are included in this report.

## Confirmed mechanisms and their limits

| Finding | Evidence and falsification | Smallest complete response and tradeoff |
|---|---|---|
| **Managed native context duplicates data and keeps growing** | [Bootstrap](../../internal/loginmigration/context.go#L229) copies selected histories and complete selected plugin/skill trees; it permits 64 GiB of selected input per provider. [Source preservation](../../internal/loginmigration/migrate.go#L238) is deliberate. [Launch environment](../../internal/logins/native.go#L337) redirects native homes into Swarm. Existing tests and six VM hashes confirm independent copies. Completed bootstrap markers falsify copying on every switch. | Estimate space and reserve headroom before import; prefer an existing native home in a simpler native-login mode, or make managed isolation/import explicit. No-copy saves duplication but gives up exclusive managed ownership unless external native writers/auth are coordinated. Ongoing native history still needs its own policy. `agents-tracker-qv44`. |
| **Development workflows multiply large caches** | Mac Swarm-prefixed temporary roots total 31.26 GiB; large isolated Go/Gradle/module caches dominate. Names establish a development association, not safe deletion ownership. The shipped runtime does not create these bespoke compiler-cache roots. | Reuse existing compiler/module caches; isolate only tests that need it. Give exceptional scratch roots an owner, budget and retirement boundary. Preserve selected evidence rather than complete build environments. Shared caches reduce isolation; cleanup may cause rebuilds. `agents-tracker-zrb5`. |
| **Aborted tests bypass cleanup** | [TestMain](../../internal/skeleton/main_test.go#L11) sweeps after `m.Run`. An isolated normal/panic/timeout comparison created its cleanup marker only on normal return. Existing days-old test shims were observed, but their originating failures are unknown. All `swsk*` roots total only 0.175 GiB, much smaller than caches. | Run tests under external ownership-aware supervision, reap exact children and report failed cleanup. Per-test stop-and-wait helps ordinary exits. `defer` cannot repair a process killed by timeout panic or SIGKILL. Existing unrelated processes must remain untouched. `agents-tracker-zrb5`; related `agents-tracker-gzhf`. |
| **Linux nightly updater leaves temporary payloads** | [Script](../../packaging/systemd/swarm-upgrade.sh#L45) installs an EXIT trap, then finishes with `exec`; the trap is skipped. Three isolated successful executions of the unchanged script retained one, two and three directories. The VM installed unit and script use this exact pattern. A bounded production scan found no recognizable leftover release bundle, so current retained bytes are unproved. | Finish cleanup before final exec, preserving existing failure behavior; cover success and failed-download paths with one shell fixture. Normal-exit cleanup alone cannot handle SIGKILL. `agents-tracker-flxd`. |
| **Shared daemon log has no lifetime bound** | [Append writer](../../internal/daemon/client.go#L215), [shim inheritance](../../internal/daemon/launch.go#L729). Isolated production-function probe wrote 12 MiB without rotation; unlink/reopen left 13 MiB on the original descriptor while the replacement pathname was empty. Current host logs are small. | Bound every writer or deliberately drop diagnostics through an owned sink. Same-inode truncation is a smaller best-effort option, with lost history and burst overshoot. Renaming/unlinking or restarting only the daemon cannot release surviving shim descriptors. Existing `agents-tracker-b3df`. |
| **Stopped hook payloads can remain after folding** | [Offline drain](../../internal/skeleton/hookdrain.go#L568) deliberately reads without compaction. Exact 8 MiB synthetic spool stayed 8 MiB after reading with a cursor beyond all records; that probe did not persist a fold cursor. Connected stopped-drain tests passed separately. The bound is per attempt; historical attempt count is not bounded. Production spools were tiny in this scan. | Retire only the durably folded prefix after writer quiescence, preserving incarnation/floor/sequence controls and uncertain tails. A stopped row alone is insufficient deletion proof. Existing `agents-tracker-j6bv`. |
| **OpenCode handoff exports accumulate** | [Export writer](../../internal/skeleton/opencode_history.go#L230) creates a fresh immutable file, at most 8 MiB plus newline. Four identical synthetic history requests left four files. [Caller](../../internal/skeleton/api.go#L1578) writes before later model/cwd/launch validation. Regular resume and phone history do not create these files; none were seen in the host scans. | Delete newly owned exports on failed handoff before any successor can reference them. Successful exports need reference-aware retirement or deduplication that excludes capture timestamp. Reusing one mutable filename can change history under an existing successor. `agents-tracker-x2ef`. |

Additional source paths have narrower relevance. The Mac scheduled updater writes
an unrotated [upgrade log](../../packaging/launchd/com.swarm.upgrade.plist#L56)
and disables Homebrew automatic cleanup; its installed execution and accumulated
cache contribution were not measured. Worktrees are opt-in, share Git objects,
and still duplicate checkouts/build outputs. An isolated Git fixture proved
worktree deletion retains committed data through the retained topic branch;
that is preservation behavior, not permission to delete unique work.

Phone content has item-count limits without a complete global byte bound, and
gateway sender/epoch checkpoints need lifecycle accounting. These are separate
remote product surfaces with deployment gates still open. The ordinary gateway
delivery path retries pending custody before advancing; an unbounded low-level
outbox API alone does not prove an offline production backlog leak. Legacy
hosted database file retention cannot explain ordinary desktop storage.

## Hypotheses rejected or narrowed by challenge

- New shim capture is a 65,568-byte identity ring, not an ever-growing terminal
  archive. Generic discussion content is RAM-only; durable journal control
  retention is approximately 1 MiB. Focused production tests passed. Old live
  shims and legacy files remain a separate migration issue.
- Current source removed the old `CLAUDE_CODE_DIAGNOSTICS_FILE` injection in
  `99d63cbd`; older storage docs still describe it as open. Existing old writers
  are not stopped by this source change. Four small diagnostic files remain
  on the VM, without proof of current writers.
- Audit-only remote activity is capped at 1 MiB in current source.
- The Go updater uses one staging and one rollback slot; it is not an unlimited
  release archive. This does not fix the separate deployed Linux shell updater.
- Enrollment directories have a retirement path after terminal, stopped,
  credential-cleaned jobs reach deadline plus 24 hours. The VM's approximately
  316 MiB of enrollment material cannot be called a leak without ages/status and
  cleanup-error checks.
- Idempotency storage already compacts repeated transitions and refuses new
  operation IDs at its count limit. Unresolved effects intentionally do not
  expire. Deleting those receipts to recover space can repeat real actions.
  Count limits still do not establish a small aggregate byte budget.
- Neither moving scratch onto another disk nor moving the agent cwd outside
  Swarm state reduces total storage by itself. Neither a periodic janitor nor a
  per-file cap proves a whole-machine ceiling.

## Architectural choices

| Option | What it can honestly guarantee | Cost and failure mode |
|---|---|---|
| **Finish the live-only supervisor** | A small total Swarm control/diagnostic budget, with byte and count admission, safe retirement and reserved transaction space. | At capacity, refuse new work before effects. Retained attempts cannot grow forever. Native histories and arbitrary tool output remain separately accounted. Least invasive option. |
| **Use native homes without copying** | Avoid redundant native-history/plugin imports. Existing ordinary native launch is already a path in the product. | Simplest form leaves login switching to native tools. Retaining Swarm-managed switching requires redesigning shared auth/writer ownership. Existing copied state needs a deliberate migration. Does not cap original history. |
| **Own disposable scratch and share bounded caches** | Predictable retirement and reduced duplicate build state for participating tools. | Tools can ignore environment paths; cleanup after exit is not a hard peak bound. Preserve source, unknown plugin changes and useful outputs. More cache misses/rebuilds at smaller budgets. |
| **Confine agents to quota-backed storage** | A hard ceiling for the whole local workload, if every writable path is inside the boundary and supervisor recovery has separate reserved space. | Major product change: filesystem/VM sandbox, controlled source export, auth/tool integration and handling failed writes at quota. Writable host mounts, elevated helpers or a Docker socket can escape the bound. |
| **Stateless terminal dashboard** | Near-zero additional persistent Swarm state; native tools own their history and login. | Removes saved roster/managed switching and durable orchestration or remote replay/delivery guarantees, unless custody moves elsewhere. Live shims can survive dashboard restart; host reboot loses RAM. Native workload disk still grows. |

The quota option has real platform primitives: [APFS volume quota and reserve](https://support.apple.com/en-euro/guide/disk-utility/dskua9e6a110/mac)
and [Linux enforced filesystem quotas](https://docs.kernel.org/filesystems/quota.html).
Confinement is an additional design requirement, not something quotas provide
automatically. [Docker volumes survive container removal and bind mounts write host paths](https://docs.docker.com/engine/storage/),
so putting a provider in a container alone does not establish the guarantee.

The ideal “never grow beyond installation” is possible for a **finite** supervisor
budget: fixed reserved storage, bounded sessions/payloads and refusal at capacity.
It does not require giving up all durability. A preallocated storage arena would
add complexity, and fixed logical size does not guarantee fixed physical blocks
under copy-on-write snapshots. No local design can permit unlimited useful output,
unlimited retained history and unlimited uncertain operations within fixed space.

The smallest useful direction is to finish bounded supervision, stop implicit
bulk copies where native ownership suffices, and put development/scratch lifecycle
under control. Choose confined execution if “agents cannot fill this disk” must
be a hard product promise. A whole supervisor rewrite is unnecessary to fix the
confirmed leaks, but the confinement or stateless options deserve explicit product
decisions rather than being ruled out by the current architecture.

## Verification and acceptance boundaries

Executed on native ARM Go 1.26.9 using existing caches: focused identity-ring,
live-only journal, spool capacity/compaction, stopped-drain, remote-activity,
login-migration, worktree and upgrade tests; the complete idempotency package;
and `TestSaveDiskFullNeverTears`, which injects ENOSPC and passed. Isolated probes
covered shared open log descriptors, saturated stopped spools, repeated OpenCode
exports, three shell upgrades, Git worktree/branch retention and normal versus
panic/timeout TestMain cleanup. All ultimately executed selected tests/probes
passed. Initial invocations through the default Go executable failed because it
was the wrong CPU architecture; these are not passing test evidence.

A future implementation needs a fixed-workload before/after run on both hosts,
including repeated launches/exits, refresh/resume, failed handoffs, failed and
successful upgrades, offline delivery, native history growth and aborted tests.
Measure peak allocated bytes as well as final directory size. Inject disk/quota
exhaustion during append, atomic replacement and compaction; verify preserved
resume identity, pending custody, exact retries and absence of hidden open-deleted
writers. Fewer bytes after manual cleanup alone is not acceptance.

Not checked: full/race suites, exhaustive file-level attribution, all native CLI
retention behavior, real quota confinement, physical handset/hosted gates, or a
long-running fixed production workload. No claimed directory total is an approved
cleanup target. Existing source, native histories, unresolved receipts and live
writers must survive any later implementation or cleanup.
