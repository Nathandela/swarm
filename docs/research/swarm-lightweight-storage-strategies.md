# Lightweight Swarm Storage Strategies

*2026-10-16*

## Abstract

This survey compares ways to make Swarm a small supervisor rather than a second conversation archive. It combines the two-host disk investigation in `agents-tracker-rybv`, an audit of current storage consumers, provider documentation, installed Codex protocol schemas, and an independent design review. It presents alternatives and trade-offs, not an implementation decision. Source inspection is pinned to Swarm commit `e284976571080294c77d7f6684ca77c8c5d4e761`; external documentation was checked on the date above. Research is tracked in `agents-tracker-acuk`.

The incident has two distinct causes to address: agent work accumulated in Swarm's state directory, while Swarm also retained independent terminal and structured-history copies. On the Mac, session storage measured approximately 306 GiB and later 226 GiB during concurrent external activity; terminal logs plus the journal accounted for approximately 0.75 GiB. Eliminating those captures alone would address less than 0.4% of that snapshot. Workspace ownership and build-artifact growth therefore remain separate from the choice of conversation-history architecture.

Provider CLIs already offer much of the discussion history and resumability Swarm needs. Swarm still owns process identity, session metadata, remote-operation outcomes, replay protection, and delivery obligations. The main architectural choices are a smaller bounded mirror, a live-only supervisor, history fetched from native stores, a bounded recent cache with native backfill, and an explicitly separate archive. Each can make Swarm-owned payloads bounded; none alone limits arbitrary tools writing to projects, provider homes, or temporary directories.

## 1. Introduction

After this survey, the owner selected live-only supervision. The subsequent
[fixing plan](../specifications/lightweight-storage-plan.md) and
[ADR-033](../adr/ADR-033-live-only-supervisor-storage.md) record the implementation
direction; the alternatives below remain a research comparison.

### Problem statement and evidence

The desired property is sustained low overhead, rather than periodically recovering space after growth. Four resources matter independently: retained disk bytes, write volume, resident memory, and startup/read cost. Moving data between directories or putting it in RAM does not necessarily improve all four.

The previous investigation recorded these approximate snapshots, not atomic or reproducible benchmarks:

| Evidence | Mac | VM |
|---|---:|---:|
| Swarm session/state tree | 306 GiB initially; 226 GiB in a later session-tree scan | 15.3 GiB |
| Terminal captures plus structured journal, included above | 0.75 GiB | About 1.85 GiB |
| Agent-created workspace/artifact share | Dominant; largest directory about 204 GiB | About 79% of Swarm storage |
| Native Codex and Claude state, separately | About 24 GiB | About 16.4 GiB |

The Mac's free space changed from 4.3 GiB to 124 GiB without deletion by this investigation. Consequently, cross-scan differences cannot be attributed to a proposed storage policy. Directory totals use allocated-space measurements; the exploratory file-name counts did not deduplicate hard links and must not be summed as physical usage.

The old Codex backend-cwd defect placed native threads and their sandboxes in Swarm's session directory. The fix in v0.17.8 sets the backend's cwd to the project. Existing native threads and previously generated work are not thereby migrated. One affected native thread continued using its workspace after Swarm marked the original session exited. See the [current launch boundary](../../internal/daemon/launch.go#L672) and [explicit resume-directory override](../../internal/adapter/codex/codex.go#L161).

### Scope

Covered: desktop/VM app-owned state, conversation copies, native-history integration, workspace and cache ownership, storage enforcement, migration, and phone-history consequences. Excluded: implementation, production cleanup, live thread migration, provider-history modification, and performance claims unsupported by measurements. Gemini CLI is included as a provider comparison, not evidence about Swarm's distinct AGY adapter. Installed protocol checks are not end-to-end integration tests.

### Definitions

- **Control state:** facts required for safe supervision, restart, authorization, or command execution.
- **Conversation content:** messages, tool results, plans, and file-change descriptions already substantially retained by a provider.
- **Custody:** accepted data that Swarm must retain until a defined acknowledgement, outcome, expiry, or revocation boundary.
- **Cache:** material that can disappear with a truthful unavailable/floor response and no repeated side effect.
- **Workspace:** source files, uncommitted changes, generated deliverables, and associated build outputs. It is user work unless explicitly declared disposable.
- **Bounded:** limited by a continuously enforced byte/count/admission rule, including peak temporary space; a periodic cleanup target alone is weaker.

## 2. Foundations

### 2.1 Ownership comes before retention

The XDG specification distinguishes persistent state, nonessential cache, and runtime objects. It also warns that the runtime directory may be memory-backed and unsuitable for large files. This vocabulary separates lifetimes; it does not impose quotas or establish permission to delete project work. [XDG specification](https://specifications.freedesktop.org/basedir/0.8/)

| Class | Present examples | Why it exists | Expiration boundary |
|---|---|---|---|
| Session metadata | Native ID, cwd, options, account binding, lineage, names/tags | Listing, resume, reconciliation | Explicit identity-retention policy |
| Live execution state | Shim/backend identity, launch record, exit report | Surviving daemon restart; safe process handling | Durable outcome merge and custody release |
| Security/control state | Device keys, epochs, sequence high-waters, operation outcomes | Authorization and prevention of duplicate execution | Protocol-defined validity/retirement |
| Ingress/delivery custody | Hook spool, exact sealed outbox envelope | Recovery across uncertain delivery or crashes | Fold/commit acknowledgement or explicit revocation |
| Derived display data | Structured recent chat, excerpts, historical details | Phone presentation and reconnect | Cache budget and truthful history floor |
| Terminal evidence | Raw PTY bytes, final grid | Identity fallback and diagnostics | Small purpose-specific retention policy |
| Provider history | Native rollout files and databases | Provider conversation ownership and resume | Provider/user policy |
| Workspace/build data | Worktrees, source copies, Cargo targets, fixtures | Actual task execution | Explicit workspace/artifact ownership policy |

A cache cleaner may delete a known derived payload. It cannot treat every file beneath an ended session directory as a cache. Current whole-session deletion calls `RemoveAll`, and managed worktree teardown invokes `git worktree remove --force`; these are unsuitable primitives for automatic payload expiry without narrower ownership semantics. [Session deletion](../../internal/persist/persist.go#L327), [worktree teardown](../../internal/worktree/worktree.go#L146)

### 2.2 What current Swarm actually reads

Source tracing changes the apparent cost of removing duplicate captures:

| Artifact | Observed production dependency | Architectural implication |
|---|---|---|
| `meta.json` | Startup roster, native resume, account and process identity | Compact durable metadata remains necessary |
| `transcript.log` | Fallback extraction of a native conversation ID from its newest tail | Full terminal archive is unnecessary for this reader |
| Rotated raw transcripts | No production history reader found | Retention currently exceeds the observed reader's needs |
| `final-snapshot.bin` | Written on exit; no production reader found in the audited tree | Removing it needs contract reconciliation, not a claim that no requirement exists |
| Live VT grid | Shim supplies snapshot, then subsequent PTY frames | Live attach and daemon-crash survival do not replay raw disk logs |
| Native transcript | Hands-off handoff points to the provider's file | Removing Swarm's raw archive need not remove supported handoff |
| Structured journal | Remote reconnect, phone history and lifecycle stream | Shrinking/removing it affects actual shipped product behavior |
| Full-detail store | In-memory, 64 MiB daemon-wide | It is a RAM budget, not an additional disk archive |

Evidence: [identity-tail reader](../../internal/skeleton/serve.go#L1043), [live snapshot ordering](../../internal/shim/server.go#L624), [handoff resolver](../../internal/skeleton/api.go#L1534), [journal-backed paging](../../internal/skeleton/chat.go#L1340), [detail store](../../internal/skeleton/chat.go#L1544), [final-state writer](../../internal/shim/shim.go#L578).

The audited Mac had 47 immediate session metadata files totalling 123,036 apparent bytes, with a median of 2,649 bytes. This is evidence that the listing/resume layer is small, not an upper bound for future metadata, filesystem overhead, or other control stores.

### 2.3 Durable control is different from a conversation archive

The provider cannot establish which device command Swarm accepted, whether a side effect may already have happened, which session incarnation was authorized, or which encrypted envelope may already have reached the relay. The outbox preserves exact sealed bytes before append; regenerating content later is not equivalent. Operation records are durable before effects. [Outbox contract](../../internal/remotegw/outbox.go#L12), [idempotency store](../../internal/idempotency/store.go#L1)

Conversation cache eviction must not evict unresolved approvals or still-valid replay guards. A finite budget can refuse new acceptance before taking custody; it cannot silently forget obligations already accepted. Approval preservation is a contract, not a verified property of every current retention path: journal segment deletion has no approval-aware check. The present count cap on idempotency records also needs separate admission/rate analysis: compaction keeps the newest over-cap records even when they have not expired. These are verification gaps, not demonstrated exploits. [Journal trimming](../../internal/journal/journal.go#L494), [idempotency compaction](../../internal/idempotency/store.go#L278)

### 2.4 Native history is available, but not permanent or uniform

| Provider checked | Supported history surface | Evidence and limits |
|---|---|---|
| Codex 0.162.1 | App-server reads and paginated turns/items | Installed static schemas contain `thread/read`, `thread/turns/list`, `thread/items/list`, and resume `cwd`. Persisted full items are not a lossless recording of every live event. Runtime/store compatibility remains to be tested. [Official API](https://learn.chatgpt.com/docs/app-server) |
| Claude Code 2.1.296 | SDK session listing/message reads over native transcripts | Readers need no model turn. A Go implementation would need a maintained adapter or helper/runtime; direct JSONL is an internal interface. [SDK sessions](https://code.claude.com/docs/en/agent-sdk/sessions), [CLI sessions](https://code.claude.com/docs/en/sessions) |
| OpenCode 1.18.15 | Native server session/message endpoints; CLI export | Useful history surface; a storage-wide cap was not established from the inspected docs. [Server](https://docs.opencode.ai/docs/server/), [CLI](https://docs.opencode.ai/docs/cli/) |
| Gemini CLI 0.46.0 | Saved sessions, ID resume, list/delete, interactive export | No equivalent paginated read API was established in this survey. It documents 30-day default retention and configurable limits. [Session management](https://geminicli.com/docs/cli/session-management/) |

The installed Codex schemas were emitted with static `generate-json-schema` tooling, without starting an app-server, opening an account, resuming a thread, or requesting inference. They confirm protocol availability, not reconstruction fidelity or memory cost. Temporary schemas are not a required project artifact; their reproducible command is recorded under Practitioner Resources.

Claude documents default 30-day cleanup with exceptions; its exact age clock was not established here. An optional setting can remove pre-compaction material for sufficiently large `-p`/SDK transcripts while preserving effective resume. That setting does not document the same behavior for Swarm's interactive PTY sessions. Native resumability therefore does not promise indefinite verbatim traceability. [Retention](https://code.claude.com/docs/en/claude-directory), [environment controls](https://code.claude.com/docs/en/env-vars)

Codex `history.max_bytes` applies to prompt history in `history.jsonl`, not to rollout files, thread databases, caches, or total `CODEX_HOME`. It cannot be presented as a complete native-storage limit. [Configuration schema](https://learn.chatgpt.com/docs/config-schema.json)

## 3. Taxonomy of Approaches

The first decision is who supplies historical conversation content. Checkpointing, compression, expiry, workspace separation, and quotas are independent enforcement choices.

| Approach | Historical content owner | Swarm content retained | Main product trade-off |
|---|---|---|---|
| A. Smaller bounded mirror | Provider plus Swarm within a window | Uniform recent structured history; optional tiny terminal evidence | Earlier history floor; least disruption to current history path |
| B. Live-only supervisor | Provider | Live state and minimal reconnect/custody buffers | Long-term discussion browsing stays in native tools |
| C. Native history on demand | Provider | No durable conversation cache; bounded request memory | Historical UI depends on provider availability and adapters |
| D. Bounded hybrid | Provider; Swarm owns a recent derived cache | Small recent window with native older-history fetch | Some duplication in exchange for fast common reads |
| E. Separate explicit archive | Archive plus provider | Bounded local working set | Storage/availability/administration move to the archive tier |

All five retain the compact control records required by the features they continue to offer. “Delete all durable state” is not another equivalent design: it abandons restart, replay, and custody guarantees.

## 4. Analysis

### 4.1 Approach A: smaller bounded mirror

**Theory & mechanism.** Keep the current normalized interaction pipeline but enforce one continuous machine-wide payload budget, age expiry, bounded record sizes, and a defined retention floor. Retain minimal metadata after content expires. Terminal evidence can have a separate small purpose-specific budget instead of three large historical files per session.

**Literature evidence.** Swarm already defines truthful `history_floor`, unavailable detail, and roster reconstruction independently from journal history. This provides a compatible vocabulary for shorter retention. Its current journal is approximately bounded to 16 MiB ×64 segments while running; 512 MiB is a startup target, and an oversized newest legacy segment is retained. It is not a continuous strict 512 MiB cap. [ADR-014 A12](../adr/ADR-014-paged-interaction-history.md#L330), [journal retention](../../internal/journal/journal.go#L244)

**Implementations & benchmarks.** This builds on existing `internal/journal` and phone paging rather than a new history backend. Current disk settings, multiplied across historical sessions, model about 23.4 GiB for 1,000 filled raw transcript sets. A global content budget stops that multiplication. No smaller-budget implementation or measured replay benchmark was produced here.

**Strengths & limitations.** Uniform recent phone history and existing cursor semantics remain. Compatibility cost is comparatively small. A busy session can consume another session's historical allowance, so fairness and protected active state matter. Eviction must preserve whole folded items or explicitly mark incomplete history: dropping arbitrary records can leave streamed deltas without their opening item, including interleaved items. The mirror still duplicates provider content and cannot offer all historical conversations inside a small fixed budget. Smaller logs do not address the observed workspace trees. [Whole-item paging](../../internal/skeleton/chat.go#L1480)

### 4.2 Approach B: live-only supervisor

**Theory & mechanism.** Swarm owns session identities, current process/control state and necessary custody, while native CLIs own historical discussion browsing. The phone displays current interactions and whatever short reconnect window is available. It explicitly stops promising long-term chat reconstruction after those buffers expire.

**Literature evidence.** Current live terminal attach already uses the surviving shim's VT state. Native identity and history pointers already support resume and handoff. R-3 requires completed rows to remain listed, rather than requiring every captured payload to remain. [Attach](../../internal/daemon/attach.go#L16), [R-3 and R-5](../specifications/system-spec.md#L145)

**Implementations & benchmarks.** This removes historical capabilities rather than adding a native-history import layer. It can reuse the current metadata store and small bounded delivery ring. Zero persistent conversation bytes would still require replacing terminal-derived identity capture and revising the capture contracts. Metadata measurements suggest small overhead, but no full-control-store budget has been validated.

**Strengths & limitations.** Fewest history-format dependencies and little duplicate content. Long history is read where it already exists. The phone loses independent historical browsing and some deep links; reopening after eviction may show only current state. Operator debugging loses past terminal detail unless separately enabled. This changes product scope, not merely a numeric retention setting.

### 4.3 Approach C: native history on demand

**Theory & mechanism.** Preserve provider conversation IDs and fetch historical pages only when opened. Normalize and redact the result in bounded request memory, then discard it. Current live interactions and Swarm-origin receipts remain distinct from imported historical items.

**Literature evidence.** Native APIs in Section 2.4 make this feasible for several providers. Existing anchored native-history resolution shows Swarm already has a trust boundary for locating provider records. That resolver does not yet implement a complete cross-provider historical UI. [Resolver contract](../../internal/adapter/adapter.go#L262), [native resolution](../../internal/skeleton/resume_history.go#L175)

**Implementations & benchmarks.** Codex can use installed paginated protocol shapes; Claude can use its supported reader interface or a characterized adapter; OpenCode can use its native server. Unsupported providers need a named limitation rather than speculative parsing. An API `limit` alone does not prove bounded underlying file reads, decoded memory, or latency. Those require measurement on large native histories.

**Strengths & limitations.** Avoids a persistent second conversation store while retaining rich historical browsing. Complexity shifts to provider compatibility, identities, pagination and sanitization. Native cleanup, account/profile retirement or an unavailable host can make old pages unavailable. Historical imports must never recreate live approval authority. A full conversation read must not send a new prompt or resume execution merely to obtain history.

Native records may contain fuller tool bodies than the current clipped/redacted stream. The same account scoping, anchored reads, untrusted-content handling, redaction and size limits must therefore apply before serving history. Provider/account namespaces belong in identity mapping. A helper SDK or history server also costs startup time, memory and packaging; adding one persistent helper per historical session could undermine the lightweight goal even when disk improves. Existing read-only backend connections may be reusable where their lifetime and authority permit it.

### 4.4 Approach D: bounded recent cache with native backfill

**Theory & mechanism.** Keep a small global byte-bounded cache for recent normalized items and fetch older pages or details from the provider. Cache entries are derived and evictable; delivery/control obligations are held separately. Content may expire while the session row remains.

**Literature evidence.** The current phone already distinguishes recent and explicitly backfilled items. Host detail bodies are already transient. These establish that not every historical body is permanently guaranteed, but native imports add a new provenance/identity problem. [Phone windows](../../internal/phonecore/interaction.go#L55), [detail retention](../../internal/skeleton/chat.go#L1544)

**Implementations & benchmarks.** This combines the existing live normalization path with bounded native readers. Cache membership cannot serve as an authority check. Native message identities need stable mapping to display identities, including fork/resume and subagents. The current history protocol uses Swarm item identity and journal ordering; injecting provider pages into it unchanged could duplicate or concatenate messages incorrectly. No integration prototype is claimed.

**Strengths & limitations.** Recent reads and reconnects remain fast, and older discussion can stay available without permanent duplication. It has more moving parts than either a short mirror or live-only design. Cached data can disagree temporarily with provider edits/compaction; source provenance and invalidation rules matter. A phone's downloaded cache may remain usable while offline, but cold history cannot be fetched from an unreachable machine.

### 4.5 Approach E: explicit separate archive

**Theory & mechanism.** Separate the lightweight supervisor from a deliberately retained archive. The archive may store provider exports, normalized history, selected terminal evidence or control audit records under its own policy. Swarm retains only a bounded local working set and references.

**Literature evidence.** This is a design alternative, not a feature implemented by the inspected Swarm. Its value follows from separating resume/current control from historical retention; the existing pointer-based handoff demonstrates one such boundary without establishing an archive service.

**Implementations & benchmarks.** A user-managed export directory and an automated encrypted archive service have very different cost. Neither was prototyped. A directory on the same disk merely relocates the bytes; an archive needs another storage boundary to protect that disk. Upload queues, local staging and failed transfers also need budgets.

**Strengths & limitations.** Can preserve selected history independently of provider expiry while keeping supervisor state small. Adds archive administration, restore latency, privacy/key management and potentially cost. Remote storage is not a free capacity guarantee. A retained archive must also have a total retention policy if indefinite growth is unacceptable.

### 4.6 Orthogonal choice: checkpoints versus event history

**Theory & mechanism.** Store authoritative current state in a compact checkpoint and retain a bounded event tail. Conversation projections, accepted operations and security high-waters have separate lifetimes. Historical timeline loss then does not require replaying years of events to recover current state.

**Evidence and implementation.** Session metadata is already the roster's source of truth. The current journal additionally multiplexes lifecycle and chat under one monotonic cursor. Splitting it is more than changing file limits: reconnect, full resync, sequence mapping and phone reducers must agree on the new boundary. Reusing the existing store is a smaller structural change than introducing a database solely for a few small records; bbolt is already installed if measured store needs eventually justify reuse. [Persistence ADR](../adr/ADR-003-persistence-schema.md), [journal construction](../../internal/daemon/journal.go#L11), [dependencies](../../go.mod)

**Strengths & limitations.** Reduces replay and memory proportional to historical events. Atomic checkpoint/tail transitions, high-water preservation and crash tests become central. Compaction needs temporary space, and failing halfway must preserve the old recoverable state. It does not itself limit workspace or native-provider storage.

### 4.7 Orthogonal choice: workspace and build-cache ownership

**Theory & mechanism.** Keep application state, disposable scratch, project work and shared tool caches separate. Validate the effective provider cwd, including saved native cwd on resume. Unknown legacy files remain user work; only positively identified generated payloads are automatically disposable.

**Evidence and implementations.** Modern managed histories already use aliases rather than complete copies. Sharing appropriate tool caches can avoid repeated dependency trees: Go documents that its standard cache is safe across concurrent invocations. Cargo distinguishes final outputs from intermediate artifacts and documents a compiler-cache integration; arbitrary mutable build directories are not thereby interchangeable. Cargo's automatic global-cache cleanup does not currently clean build artifacts. [Go cache](https://go.dev/src/cmd/go/alldocs.go), [Cargo layout](https://doc.rust-lang.org/cargo/reference/build-cache.html), [Cargo cleanup scope](https://doc.rust-lang.org/cargo/reference/config.html#cache)

**Strengths & limitations.** Addresses the incident's dominant storage category. Avoiding complete per-review source/dependency copies can save more than reducing terminal logs. Shared caches can contend, and tests deliberately varying toolchains or permissions may need isolated outputs. User-controlled workflows can ignore scratch/cache conventions. Lifecycle cleanup of test-owned binaries must be deterministic; a generic machine-wide janitor is not equivalent.

### 4.8 Orthogonal choice: application budgets versus enforced quotas

**Theory & mechanism.** Application accounting can constrain Swarm's own writes. A filesystem/storage boundary is needed for a hard cap on arbitrary writes within an execution area. These are different promises.

**Evidence and implementations.** Linux quota facilities provide enforced block/inode limits; XFS project quotas can apply to a directory tree when supported and configured. APFS offers volume quota sizes, not the same portable per-directory interface. This survey did not establish quota support or provisioning on the two hosts. [Linux quotas](https://docs.kernel.org/filesystems/quota.html), [XFS project quotas](https://man7.org/linux/man-pages/man8/xfs_quota.8.html), [APFS quota sizes](https://support.apple.com/guide/disk-utility/add-delete-or-erase-apfs-volumes-dskua9e6a110/mac)

**Strengths & limitations.** An enforced quota can stop a confined workspace from exhausting its storage allocation. Builds then receive allocation failures; that changes their behavior and may leave partial outputs. A quota on one volume does not cover writable locations elsewhere. Same-user permissions, cwd selection, process groups and instructions are not filesystem confinement. Isolating tools sufficiently to cover every writable location can affect provider authentication, native history, plugins, network tools and project access. A fully documented hard guarantee requires both a quota and a confinement boundary.

A shared quota can also block native-history writes, security checkpoints and operation-outcome persistence. Control/staging headroom and tested `ENOSPC`/`EDQUOT` handling therefore belong in the design. A small hard limit without application admission and recoverable failure handling can turn a disk-capacity problem into a supervision or data-loss problem.

## 5. Comparative Synthesis

### 5.1 Product and engineering trade-offs

Relative costs below are architectural assessments, not measured implementation estimates.

| Property | A. Bounded mirror | B. Live-only | C. Native on demand | D. Bounded hybrid | E. Separate archive |
|---|---|---|---|---|---|
| Duplicate conversation bytes | Limited window | Minimal | None durably | Small recent cache | Deliberate archive copy |
| Recent phone experience | Current behavior within shorter horizon | Live/current only | Cold fetch may delay opening | Warm recent reads | Depends on local cache |
| Older phone history | Stops at Swarm floor | Native tool only | Provider-dependent | Provider-dependent after cache floor | Archive-dependent |
| Provider purge tolerance | Only retained Swarm window survives | No independent history | No independent history | Only retained cache survives | Selected archived material survives |
| Provider-format maintenance | Existing capture adapters | Lowest historical coupling | High | High | Export/import-specific |
| Independent Swarm action receipts | Preserved separately | Preserved separately | Preserved separately | Preserved separately | Preserved; archive optional |
| Approximate implementation disruption | Lower | Product scope change | Larger reader/UI changes | Largest combined history path | Additional storage tier |
| Controls arbitrary build growth | No | No | No | No | No |
| Can coexist with workspace quotas | Yes | Yes | Yes | Yes | Yes |

Native discussion access, terminal fidelity and Swarm audit provenance are three different retention promises. No row provides all of them indefinitely with a fixed local budget.

### 5.2 What a storage ceiling must count

For a design with `N` retained metadata records and `A` active sessions, a useful accounting model is:

```text
Swarm-owned bytes = retained metadata(N)
                  + security and control records
                  + accepted ingress/delivery custody
                  + conversation cache
                  + terminal evidence(A or N, depending on lifetime)
                  + diagnostics
                  + compaction/staging reserve
```

Each term needs a lifecycle and a capacity rule before the sum is a guarantee. Provider homes, project workspaces, shared compiler caches and archives are additional categories, not bytes that vanish when excluded from this equation. Admission is necessary when protected obligations exhaust their budget; eviction is appropriate only for dispensable content.

| Model | Approximate bytes | Qualification |
|---|---:|---|
| 1,000 metadata records at this Mac's median apparent size | 2.53 MiB | Model; excludes filesystem overhead and other state |
| 1,000 full current terminal sets | 23.4 GiB | 24 MiB/session model, excluding small write overshoot |
| 128 active 64 KiB identity tails | 8 MiB | Proposed tail size; RAM or disk trade-off, not implemented |
| Current 128-active-session limit ×8 MiB hook spools | 1 GiB | Outage/full-spool upper model, separate from chat cache |
| 32 MiB structured cache at 100 B/s | About 3.9 days | Illustrative rate, not guaranteed time retention |
| Same cache at 1 MiB/s | 32 seconds | Demonstrates why byte bounds cannot promise a fixed horizon |

The present active-session default is 128. A “64 MiB total” cannot simply be asserted while retaining the existing per-session spool maxima, unlimited historical identities and unbounded diagnostic streams. Conversely, replacing conversation archives with tens of MiB of cache does not require losing session listings. The control/custody terms need their own measured envelope. [Active-session default](../../cmd/swarm/main.go#L43), [spool limit](../../internal/shim/hookspool.go#L83)

TTL bounds age, not burst size. Per-session caps do not bound the number of retained sessions. Compression and deduplication reduce typical usage without limiting worst-case incompressible data. Periodic sweeps permit overshoot; continuously enforced admission/rotation provides a stronger promise. Unlinked open files retain their storage until the final descriptor closes, so a payload counter is not automatically a disk-free-space guarantee. [Unlink semantics](https://man7.org/linux/man-pages/man2/unlink.2.html)

### 5.3 Contract and failure-mode comparison

| Boundary | Required behavior or explicit product change |
|---|---|
| R-3 historical listing | Tiny metadata can remain after content expires; unlimited identities still grow with `N` |
| S-5/R-1 terminal persistence | Default-off terminal recording requires an ADR/spec amendment and replacement identity capture |
| G3/E4.5/E8.4 final snapshots | No reader found, but remove or revise the existing promised behavior explicitly |
| D-9/remote control | Retain durable control, monotonic cursors and accepted operation outcomes; content cache is not their replacement |
| S9 disk stalls | Expendable capture can degrade with an honest gap; PTY draining must remain nonblocking |
| Phone offline beyond horizon | Restore current state and report the historical floor; no invented complete history |
| Daemon crashes | Surviving shim remains authoritative; acknowledge/fold boundaries prevent silent replay or loss |
| Provider history missing/changed | Named unavailable/unsupported result; never silently start a fresh conversation |
| Imported approval history | Display-only; never creates a current actionable request |
| Dirty worktree or unknown legacy files | No automatic whole-directory deletion based on a session status |

Sources: [system requirements](../specifications/system-spec.md#L70), [exit criteria](../specifications/implementation-goals.md#L64), [system invariants](../invariants/system-invariants.md), [history contracts](../adr/ADR-014-paged-interaction-history.md).

### 5.4 Common enforcement boundaries across the alternatives

Independent of the historical UI choice, a complete strategy must account for effective cwd on fresh and resumed threads, narrow ownership of expendable files, total rather than solely per-session budgets, safe behavior when a budget is exhausted, writer-aware log rotation, test-process cleanup, and legacy recovery. A compact checkpoint does not justify dropping accepted commands; native traceability does not justify deleting unknown workspace files.

Cleanup of known generated files can be automated without deleting the session identity. Legacy contamination needs a separate preservation process: inspect native use and uncommitted work, establish the intended workspace, use supported explicit relocation when needed, verify resulting native context, and remove only data whose disposition is established. Archiving a row is not reclamation, and copying hundreds of GiB onto the same nearly full filesystem is not an effective offload.

## 6. Open Problems & Gaps

1. **Provider fidelity and cost.** Static APIs are verified, but complete normalized reconstruction, large-file read amplification, cancellation, concurrent writers and cold-page latency were not benchmarked.
2. **Identity/provenance mapping.** Native items and current Swarm journal items have different IDs, ordering, source attribution and capture boundaries. Backfill must avoid duplicate messages and false approval authority.
3. **Remaining provider coverage.** OpenCode, AGY and Hermes currently use terminal-derived identity fallback. A small shim-owned tail or authoritative provider identity path must preserve short-lived/daemon-outage cases. A fixed-size tail also needs a generation counter: the existing reader skips unchanged file sizes. [Current cache](../../internal/skeleton/serve.go#L1111)
4. **Strict control admission.** Budget safety needs bounds on active sessions, accepted operations, unresolved approvals and custody. The existing idempotency count-cap assumption is not proved by TTL arithmetic alone; actual rate limits, expiry and restart behavior need reachability analysis before claiming a failure or a guarantee. Follow-up: `agents-tracker-aivi`.
5. **Legacy journal migration.** The newest oversized segment can survive the startup target, and recovering dropped high-water state may read a whole old segment. Current host sizes do not demonstrate the pathological case. Follow-up: `agents-tracker-cyhm`.
6. **Native evidence.** Quota/auth failure detection reads a bounded native transcript tail and correlates conversation and prompt identifiers. Read bounds do not cap native writers. [Consumer](../../internal/skeleton/logins_transcript.go)
7. **Legacy workspace recovery.** Native threads can continue using directories after the original Swarm process exits. The exact space safely reclaimable requires per-workspace writer and preservation checks. Follow-up: `agents-tracker-3bxu`.
8. **Test cleanup portability.** The shared sweep's process termination is Linux-specific; separate TUI test binaries still lack complete cleanup. Follow-up: `agents-tracker-uh9e`.
9. **Quota deployment.** Filesystem type, privilege requirements, macOS provisioning, writable escape paths and provider compatibility need host-specific validation before claiming a machine-protection guarantee.
10. **Metadata cardinality.** Indefinite listed history cannot fit in a fixed finite total. An explicit count/admission/export contract is required if even metadata must have a hard ceiling.

These are research/validation boundaries, not an implementation checklist or a decision to change existing retention.

## 7. Conclusion

The provider CLIs can supply much of the historical discussion that Swarm currently duplicates. The existing implementation already relies on native identity and native transcript pointers for key continuation features. What Swarm uniquely needs to retain is substantially smaller: identities, process/recovery facts, and its own control and delivery obligations.

The alternatives exchange historical phone behavior, independence from provider deletion, compatibility work, read latency, and duplicate storage. Separately, workspace ownership and enforced writable-storage boundaries determine whether arbitrary agent builds can fill a machine. A lightweight supervisor footprint and a bounded total agent workload are distinct achievable properties with different costs. The appropriate product contract depends on which history and offline guarantees are retained; this survey makes no selection among the alternatives.

## References

All external sources were accessed 2026-10-16. Local source findings refer to commit `e284976571080294c77d7f6684ca77c8c5d4e761`.

- Swarm, [documentation index](../INDEX.md), [system specification](../specifications/system-spec.md), [implementation goals](../specifications/implementation-goals.md), [invariants](../invariants/system-invariants.md). Repository: [Nathandela/swarm](https://github.com/Nathandela/swarm/tree/e284976571080294c77d7f6684ca77c8c5d4e761).
- Swarm, [ADR-003: persistence](../adr/ADR-003-persistence-schema.md), [ADR-007: remote access](../adr/ADR-007-remote-access.md), [ADR-013: mirror capture](../adr/ADR-013-mirror-capture-architecture.md), [ADR-014: paged history](../adr/ADR-014-paged-interaction-history.md).
- Swarm, Beads `agents-tracker-rybv`: measured two-host disk investigation; `agents-tracker-acuk`: this survey. No conversation bodies are reproduced here.
- OpenAI, [Codex App Server](https://learn.chatgpt.com/docs/app-server); [Codex configuration schema](https://learn.chatgpt.com/docs/config-schema.json). Installed Codex 0.162.1 static schemas independently checked.
- Anthropic, [SDK sessions](https://code.claude.com/docs/en/agent-sdk/sessions), [CLI sessions](https://code.claude.com/docs/en/sessions), [Claude directory and retention](https://code.claude.com/docs/en/claude-directory), [environment controls](https://code.claude.com/docs/en/env-vars).
- OpenCode, [server API](https://docs.opencode.ai/docs/server/), [CLI commands](https://docs.opencode.ai/docs/cli/).
- Google, [Gemini CLI session management](https://geminicli.com/docs/cli/session-management/).
- freedesktop.org, [XDG Base Directory Specification 0.8](https://specifications.freedesktop.org/basedir/0.8/).
- The Linux kernel, [quota subsystem](https://docs.kernel.org/filesystems/quota.html); XFS project, [xfs_quota manual](https://man7.org/linux/man-pages/man8/xfs_quota.8.html).
- Linux man-pages project, [unlink lifecycle semantics](https://man7.org/linux/man-pages/man2/unlink.2.html).
- Apple, [APFS volume size options](https://support.apple.com/guide/disk-utility/add-delete-or-erase-apfs-volumes-dskua9e6a110/mac).
- The Go project, [build and test cache documentation](https://go.dev/src/cmd/go/alldocs.go).
- The Rust project, [Cargo build cache](https://doc.rust-lang.org/cargo/reference/build-cache.html), [Cargo configuration and cleanup scope](https://doc.rust-lang.org/cargo/reference/config.html#cache).

## Practitioner Resources

### Existing Swarm boundaries

- [Native history resolution](../../internal/skeleton/resume_history.go): reuse its anchored, budgeted trust boundary; it is not a generic history importer.
- [Native context import](../../internal/loginmigration/context.go) and [Codex history dependencies](../../internal/loginmigration/codex_history.go): preserve the referenced conversations and child/base history before changing execution context.
- [Hook spool](../../internal/shim/hookspool.go), [outbox](../../internal/remotegw/outbox.go), [idempotency store](../../internal/idempotency/store.go): custody/retirement semantics a smaller design must preserve.
- [Test sweep](../../internal/testbin/sweep.go): per-run unique executable ownership; current process cleanup portability remains limited.

### Provider inspection

- `codex app-server generate-json-schema --out <temporary-directory>`: static installed-version protocol evidence; does not establish runtime history fidelity.
- Claude SDK session readers: supported access to native messages; assess helper/runtime cost before adding a dependency to a Go supervisor.
- OpenCode server history endpoints: native message retrieval when the server/store is available.

### Capacity and workspace tools

- `du -x` with explicit resolved roots and `df`: distinguish app ownership from mount capacity; avoid adding overlapping totals or following shared aliases repeatedly.
- Provider/compiler cache controls: distinguish reusable dependencies from source work and generated deliverables; custom per-task caches need their own lifetime.
- Filesystem quota facilities: platform-specific enforcement primitives, not a portable Swarm setting and not proof of confinement outside the capped area.

### Common pitfalls

- Reducing logs while leaving hundreds of GiB of generated workspaces untouched.
- Treating a directory move, symlink or RAM-backed temporary area as a reduction in total resource use.
- Describing age expiry, compression or per-session caps as a global worst-case bound.
- Disabling native persistence to save space while still promising native resume and history.
- Assuming resumable history preserves all old verbatim messages after compaction or cleanup.
- Counting RAM-only detail bodies as another disk archive.
- Evicting command receipts, unresolved approvals or sealed pending deliveries with an ordinary content LRU.
- Reusing automatic cache expiry to invoke whole-session or forced worktree deletion.
- Claiming a native reader is bounded merely because its returned page is small.
- Dropping raw capture without preserving provider identity fallback and final-exit cases.
