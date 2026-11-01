# Provider login integration plan

2026-11-01 · implementation target, not released behavior · `agents-tracker-h6sc`

This is the current integration design for the owner's provider-wide login
manager. It resolves the earlier account-profile direction in
[native-login-handover.md](native-login-handover.md); that document retains the
existing implementation contracts and historical verification. Beads tracks
work and acceptance status. Section order below defines dependencies, not a
second task ledger.

Work stays on `feat/native-login-handover`. At planning time its head is
`81e8d0ff`; current main, `b609dfa6`, is already an ancestor. There is no current
main merge to redo. Verify ancestry again before landing. The existing branch
also contains chat, terminal/renderer, dependency and extensive verification
work; review that scope explicitly rather than merging it blindly or removing
another effort's changes.

## Product contract

One saved account is selected for Claude and one for Codex. All managed clients
of a provider converge on that selection; there is no per-discussion account
picker, routing score or permanent account-assignment ledger. Requests already
admitted under A can finish under A while later work adopts B. Selection is not
an instantaneous server-side revocation of A.

Users sign in once per saved account through the native flow. Native code renews
credentials when possible. Expiry maintenance covers every renewable parked
login fairly, including quota-limited accounts. Revocation or a provider's
interactive challenge can still require sign-in again; “saved” is not a promise
that authentication will remain valid forever.

Healthy native processes keep working through qualified handover. A slow client
does not impose an idle/detach barrier on the fleet. A replacement process is
allowed only at an independently qualified boundary for that incarnation; a
timeout, alive PID or coarse `done` status is not permission to stop work.

Automatic continuation is a separate operation. It may act only on the exact
unfinished task terminated by subscription quota, after usable account adoption.
Idle, completed, maintenance, canceled, human-waiting, already retrying and unknown
work receives no automatic inference. Preserve conversation, CWD, model, effort,
permissions, sandbox, settings and completed tool results. Never replay a tool or
invent a generic “continue” prompt.

Claude's native terminal remains the default. The separately authorized opt-in
Swarm chat work is independent of credential switching. Its decision, actor,
workflow, history and restart gates are not automatically satisfied by choosing
that interface. No Codex UI replacement is included.

## Evidence and unresolved decisions

| Finding | What is established | What integration must still establish |
|---|---|---|
| [Store ownership foundation](../verification/provider-logins/claude-slot-foundation-2026-11-01.json) and [protected-home inventory](../verification/provider-logins/claude-slot-inventory-2026-11-01.json), plus [settled-target reservation](../verification/provider-logins/claude-promotion-reservation-2026-11-01.json) | Inactive schema/hold records and helper/mutation fences pass focused and complete Store race checks. | Actual publication/reconciliation, staged same-account relogin, complete authority inventory and every native activation gate. |
| [Claude token adoption](../verification/provider-logins/claude-same-process-token-adoption-2026-10-29.json) | One pinned Linux native process retained its task and Reads across synthetic A→B→A request tokens. | Multiple clients, real account acceptance, policy/cache coherence and quota waits. |
| [Claude refresh slot](../verification/provider-logins/claude-native-refresh-slot-2026-10-29.json) | Native A refreshed to A2; a compatible candidate writer refused busy refresh; actual A2 was parked and restored without losing unrelated native credentials. | Production Store transaction, helper exclusion, reconciliation, platforms and migration. |
| [Native login propagation source](../verification/provider-logins/claude-native-login-propagation-source-2026-10-29.json) | Native login separately writes credentials and shared account config; siblings have a config-watch path. | A qualified unattended operation from a parked login and healthy-sibling account, policy, model/tool and wait behavior. |
| [Two-client qualification](../verification/provider-logins/claude-two-native-refresh-2026-10-29.json) | Telemetry attempt failed before ARM; cleanup passed. A harmless Python-only observation setup passed. | No two-Claude refresh/switch result exists. The subsequent native-tracing path is stopped and supplies no qualification. |
| [Current Claude chat caller](../verification/provider-logins/claude-native-history-metadata-2026-10-29.json) | Two fresh synthetic native launch subcases reached history and authenticated Stop. | Decisions, complete actors/workflows, long history, reconnect/input custody and automatic recovery. |
| Codex native account APIs and execution model | Existing native refresh owner, access broker, connection-bound handover and exact quota-candidate foundations exist. | Real managed constructor/feed ownership, multi-client/reconnect integration and authoritative continuation. See the [earlier contract](native-login-handover.md#handover-and-recovery). |

The stopped native-tracing packet is excluded. Do not retry it under another
agent, name, tool or observation mechanism. Use normal supported CLI/API
operations and their observable outcomes for the integration's native tests.
Successful concurrent tasks establish those finite outcomes; they do not prove
an unobserved internal lock interleaving or universal plugin/actor coverage.
Retain the stronger unobserved claim as open, or explicitly revise its acceptance
with independent review; never silently mark it passed.
This distinction does not excuse a reachable native fallback that writes outside
the qualified leases. Establish supported exclusion of that path or a proven
no-lost-generation serialization/reconciliation contract. Stress without a
failure is not that proof; otherwise common-slot activation remains closed.

An early decision is mandatory for Claude: identify an unattended native-supported
account/config adoption operation, then qualify its behavior. Token-file
replacement alone is not that operation. Copying `oauthAccount`, fabricating a
native profile/policy verdict or forcing a cache is not an established solution.
A native login demonstration requiring a new browser dance on each switch also
does not meet the saved-login product contract. If no suitable operation is
qualified, implement the independent foundations but keep hot Claude activation
unavailable and report the full feature incomplete.

## Architecture and invariants

Reuse `internal/logins` as the owner of selection, credentials and publication;
the existing completion worker handles bounded maintenance. Provider-specific
code stays explicit: Claude uses a proposed common native authority; Codex keeps
its native refresh owner and authenticated app-server access-token distribution.
Do not add a general provider framework, OAuth implementation, request proxy,
scheduler service or second transcript store.

Four facts remain separate:

| Fact | Meaning and owner |
|---|---|
| Desired selection | Existing provider account, epoch and registry CAS; user C may supersede pending B. |
| Actual authority | Which account owns the observed Claude slot, or Codex grant/distribution authority; persists independently of desired state. |
| Native readiness | Exact incarnation/connection's account/config acknowledgment and usable policy state. Existing bounded handover receipts are temporary convergence evidence, not account routing. |
| Continuation authority | Exact task, terminal cause, complete relevant custody and unchanged owner intent; owned by execution/input admission, not by account selection. |

Credentials and identity responses stay in private native credential storage.
Registry, execution, UI and remote records contain only bounded identifiers,
hashes, generations, enums and timestamps. No token goes into argv, environment,
journals, logs, phone protocol or review evidence.

For Claude, there is one native refresh authority for the actual slot. Multiple
native processes may participate through its qualified native serialization.
The same account must not also refresh through a parked copy or a legacy saved
directory. Desired B may persist while its helper completes every allowed attempt
and fully settles. Under one registry lock, revalidate the exact current ready
ref, generation, usable identity/expiry and already-recorded native/unit/worker
retirement, then durably reserve B before unlocking. A changed ref, remaining
launch authority or live legacy writer refuses reservation. Later admission,
Replace and delayed Claim see that reservation and refuse. This lets B finish a
legitimate refresh-driven retry without an exception after reservation. No stop,
network operation or wait runs under that lock. Keep B reserved through promotion;
apply the same custody to relogin, capture, removal and crash recovery. Pending
desired B does not forbid native A's legitimate refresh while A is still actual.

Account identity includes organization. Obtain current-grant identity outside
registry/native locks, bound to the captured grant hash and freshness. Recheck it
under final publication custody. A read-only fixed-endpoint profile GET is a
candidate identity witness, not enrollment, auth readiness or policy approval.
Qualify parsing and transport first: normal verified HTTPS, bounded response and
deadline, no redirects, cookies, refresh/retry or environment-selected routing;
never retain provider bodies/errors in ordinary logs. Changed grants or foreign
identity produce unknown/refusal.

The active authority can itself need renewal when no discussion is issuing
requests. If a native helper is needed, bind it to the same active authority and
the same native coordination, not to the stale parked seed. Reuse the existing
worker lifecycle and derive its authority path internally. An active-account
parked-helper prohibition must not deadlock first launch after an idle expiry.

### Claude publication transaction

Use the existing Store lock, anchored readers, durable replacement, bounded
records and uncertainty latch. Define the new ownership mode in the next registry
schema after the current maximum of 10, with old-reader/downgrade refusal. Do not
reinterpret old `Switch` phases as hot publication.

| Durable state | Required meaning |
|---|---|
| Desired pending | Target is recorded; actual A and its native refresh remain authoritative. |
| Prepared | Source/target identities, desired generation, grant generations/hashes, helper/legacy exclusions and storage headroom are reserved. |
| Outgoing parked | The actual native-written A2 provider subtree is durably preserved before B can replace it. |
| Publication uncertain | Persist before any native credential/config publication can escape; restart must reconcile rather than retry or roll back blindly. |
| Published | Actual credential publication is reconciled; client policy/config readiness remains separate. |
| Settled | Qualified account-coherence requirements hold and temporary transaction custody can retire. |

Under the qualified native OAuth→legacy OAuth→storage lease order, re-read the
actual grant and check desired generation, identity/hash, parents, lease ownership
and elapsed budget after acquisition and immediately before rename. Reuse the
two-second bounded writer model; no network, native startup or whole-fleet wait
under these leases. Preserve the complete actual provider subtree and unrelated
native/MCP credentials. Never rebuild A2 from a normalized subset or OAuth reply.

The account/config operation and the credential write are not made atomic by a
registry enum. Their intermediate state and native-client behavior require their
own qualification. Fencing host input cannot freeze a live CLI's internal model
requests; do not use that fiction to claim a coherent switch.

Park the raw captured provider grant and its hash. If encoding the published
document changes JSON bytes, record the actual encoded published subtree hash
and verify that its complete content matches the incoming grant. Do not confuse
input-byte hashes with publication receipts or copy unrelated account credentials.

Recovery classifies unchanged A, exact B publication, a later native renewal of
A/B supported by fresh identity, and unknown/foreign state. Hash equality alone
cannot classify refreshed B2. Unknown state retains intent, both possible owners
and helper exclusions. ENOSPC or sync failure after rename means uncertainty.
If B escaped before desired C won, record actual B honestly and converge B→C.
Cancel/supersession never proves that no external mutation happened.

## Implementation sequence

Each slice starts with behavioral RED tests, has a separate review and ends with
recorded acceptance evidence. Production implementation can proceed behind the
existing admission/capability fences while native gates run in parallel. A
foundation-only slice is not the delivered rotator.

### 1. Reconcile the branch and implement the Store transaction

Implementation record: `agents-tracker-h6sc.39`.

**Scope:** `internal/logins/{types,store,native,files,credentials,capture,relogin,erase}.go`,
selection/custody callers and focused slot tests. Keep mutation inside Store;
do not expose a test-candidate writer as a second production manager.

Add actual Claude authority and one bounded publication intent. Connect selection,
cancellation, relogin/removal and restart reconciliation to the same generation
and exclusions. Implement the real private-file, lock, park/merge and uncertain
outcome paths with activation still refused until slice 3's native gate passes.

RED cases cover refreshed A2 versus stale seed, unrelated/unknown fields,
superseding C, lease loss, delayed helper claim, parent replacement, wrong identity,
park failure and cuts before/after rename/sync/receipt. Use real temporary storage
and deterministic fault seams; mock the network/native boundary, not business
state. Concurrent packages receive race checks. Verify current input/paste,
failed-turn manual input, journal failure, handoff deletion and bootstrap custody
regressions; main is already integrated, so do not redo historical merges.
Test both Claim/Replace-first and reservation-first race orders, stale ready-ref
refusal, and a genuine helper retry→ready→retired sequence before reservation.
Before activation, actual/source/target holds must also reach `ProtectedHomes`
and every writer-inventory/deletion consumer, not just direct mutation methods.

**Exit:** newest grant and publication facts survive every tested cut; no external
publication before durable intent; no stale helper can claim either authority.

### 2. Keep every parked login fresh

Implementation record: `agents-tracker-h6sc.40`.

**Scope:** `internal/skeleton/logins_protocol.go` (`maintainClaudeAuth`, completion
worker), `internal/logins/claude_auth*.go`, `internal/loginworker/claude_auth*.go`,
Codex grant leases/refresh owner and relevant relogin/removal callers.

Replace “selected plus first warm candidate” with a fair bounded pass over all
renewable saved grants, independent of inference-quota eligibility. Prioritize
near expiry, then round-robin the rest; reuse one bounded external action per
existing pass and existing helper/concurrency bounds. A limited subscription can
still have its authentication renewed. An explicit needs-sign-in account does
not get a browser dance or a new failed worker on every tick.

Parked-job identity follows login revision, attempt and authority custody, not an
unrelated change of desired selection. Keep retry deadlines/backoff consumed
across restart. Add the active-authority renewal case described above. For Codex,
reuse ID-specific `GrantLease`/`StartCodexRefresh`; do not weaken the selected-token
broker to turn it into a parked maintenance worker.

**Exit:** A/B/C all receive due maintenance; quota-limited A can renew; one canceled
caller cannot cancel another's shared refresh; admission and delayed claim reject
active/promoting/legacy-owned copies; no tick loop recreates failed authority.

### 3. Connect Claude selection, coherent native adoption and stable births

Implementation record: `agents-tracker-h6sc.41`; native gates remain in
`agents-tracker-h6sc.36` and `agents-tracker-h6sc.37`.

**Scope:** `logins/{claude_selection,claude_birth,claude_custody,native}.go`,
`skeleton/{logins_launch,logins,logins_protocol,authwatch}.go`, final shim birth
admission, native settings/history readers and the provider-login view.

Use a short, decision-focused qualification through ordinary supported native
operations: parked B→native B account/config, healthy sibling propagation, two
clients with expiry/renewal, A→B→saved A2, active tools, idle, question, permission
and quota-wait cases. Retain original traffic restrictions and finite owned test
processes. Do not expand the abandoned telemetry fixture or restart the stopped
instrumentation path. Record private lock timing as unobserved when it is.

Integrate the qualified account operation into slice 1's transaction. New births
use stable provider authority custody, with the exact CLI/process, final
reservation-to-Start fence, private settings, actual post-worktree CWD and native
history location preserved. Their immutable runtime identity is not the mutable
selected account/epoch. Update authwatch and input gates so a normal selection
does not recycle healthy stable-authority processes.

Qualify Linux file storage and macOS Keychain separately. Linux R14 is not a Mac
receipt. Installed-version capability refusal applies to the unqualified advanced
operation, not to ordinary Add login or a fabricated requirement to sign in again.

**Exit:** the actual public constructor and Make active path perform coherent
native adoption without profile copies, broad restarts or fabricated readiness.
If the account operation remains unsupported, this exit remains open.

### 4. Finish Codex's existing native account path

Continue the existing implementation record: `agents-tracker-h6sc.19`.

**Scope:** `skeleton/{backend_auth,logins_codex_access,logins_codex_handover}.go`,
existing bootstrap/migration callers, `internal/appserver` and shim final auth
environment validation. Reuse existing `Handover`, broker and connection fences.

Keep the exact token-refresh callback ahead of generic event ingress. Coalesce
per-grant refresh and return secrets only on the requesting authenticated feed.
After native `account/login/start` acknowledgment, require the guarded scoped
`account/rateLimits/read` target account and explicit `ordinaryUsageAllowed` result.
False means adopted but unusable; null, stale, malformed or mismatched means no
current readiness and no usable quota reading.

Bind token delivery, reads and receipt publication to physical connection,
incarnation, desired epoch and grant revision. Test multiple live clients,
disconnection, daemon restart, obsolete callback, lost ACK, replacement feed and
selected relogin. Preserve final subscription-selector scrubbing and `.env`
refusals. Transient pending members do not block already-ready members.

**Exit:** one selection converges through the actual managed TUI constructor,
with native thread/model/settings preserved and no invented user turn.

### 5. Add conservative automatic selection

Implementation record: `agents-tracker-h6sc.42`.

**Scope:** `skeleton/{logins_usage,logins_failure,logins_transcript,logins}.go`, Codex
scoped reads, Store availability and existing protocol/TUI projections.

Keep the current display contract: Claude 5-hour/weekly, Codex weekly only, unknown
shown simply. Introduce a separate internal rotation observation containing the
proven provider/account/credential generation, native source, observation time,
window/reset and usable headroom. The 15-minute display cache is not authorization.

Initial fixed policy: source low-water at 5%, target above 10%, scoped observation
age at most 60 seconds, and a 60-second proactive cooldown. Any applicable,
qualified source window at or below 5% can trigger consideration. Every applicable
target window must be known, qualified, fresh and above 10%: Claude requires both
5-hour and weekly readings; Codex requires weekly only. A missing target window
refuses proactive selection even if another window has capacity.
These constants are heuristics, not a guarantee against concurrent/model-specific
limits. Test exact boundaries; expose no new tuning UI. Unknown target capacity
does not justify a proactive switch from usable A. Refresh/read a candidate on
demand through a qualified native route or stay on A.

A confirmed subscription rejection bypasses the proactive cooldown. It can try a
fresh-auth candidate not known exhausted, once per bounded episode; unknown
capacity is not advertised as known capacity. Auth failure, spend/credit denial,
network failure and quota are distinct. Near-limit never marks an account
exhausted. Old A observations cannot exhaust newly selected B. Manual C always
wins against stale automatic B. Reset restores eligibility; all exhausted leaves
an honest visible state with decisions, cancellation and manual input usable.

Persist the provider's automatic episode, source generation/reset, attempted
targets and proactive not-before time in the existing Store under selection CAS.
Restart or cancellation cannot mint new attempts or erase cooldown. Confirmed
quota bypasses cooldown, not target deduplication. An episode ends only on
qualified usable adoption with subsequent native progress, explicit owner
supersession/cancellation, or a proven relevant window reset. Publication can
settle while its attempted-target episode remains retained; never send a model
request just to manufacture progress. Retire the episode durably, retaining a
bounded consumed-evidence watermark. Token renewal, a new PID or cancellation
cannot rearm the same failure; immediate destination rejections join the current
bounded episode rather than mint fresh attempts.

**Exit:** proactive and hard-limit decisions share the same publication path,
cannot flip-flop on stale readings and never wait for fleet-wide idle/detach.

### 6. Finish execution/input truth and exact continuation

Implementation record: `agents-tracker-h6sc.43`; retain the related actor/chat
records rather than duplicating their qualification status here.

**Scope:** `engine/{execution,claude_execution,codex_execution}.go`,
`persist/execution*.go`, `skeleton/execution*.go`, existing composer/sender/native
decision/Stop/Delete/ContextGuard admission and one bounded continuation record.

First add durable owner-intent invalidation at admission, before FIFO waiting,
including prepared messages and every local/remote/direct/paste path. Keep the
existing write revision semantics; they do not represent a newer queued intent.
Persist control-relevant evidence before acknowledgment; gaps, overflow, lost
custody and reconnect invalidate automatic authority before another effect.

Keep presentation separate: `working`/`done` is not a scheduler permission. A main
completion does not drain children; a background tool handle is not completion.
Track complete relevant actors/tools/requests and human waits, or say unknown.
Codex creator-feed custody and mutating `thread/resume` behavior must be resolved
through a supported path. History pagination cannot recreate missed events.
Claude's current observation subset/unknown actors must not be promoted by a
boolean. Finish any needed opt-in chat decisions/actors/restart work as its own
dependency, with native workflow parity tests; never silently change interfaces.
Full default-native delivery requires exact recovery qualification for each
provider's native TUI. Opt-in Claude chat receipts can qualify that opt-in surface
only; they cannot satisfy or silently replace the default-native recovery gate.

A continuation requires positive unfinished subscription-quota work, no native
retry already running, no final/maintenance, no human wait, no pending children or
unknown custody, no cancellation/newer owner intent and usable adopted auth.
Bind original task/turn, native incarnation/feed, owner-intent revision and
successor identity. Persist prepared→submission-uncertain before **any** potentially
inferential native operation, including startup/initialize, then exact accepted
successor→retired. A lost ACK remains unresolved until native evidence identifies
this successor. Newer unrelated work is not that receipt.

Use only qualified native continuation: characterized Codex empty-input start on
the same loaded thread where all eligibility gates hold; a separately qualified
Claude path. Never restart a quota-waiting CLI just because a process is alive.
When replacement is genuinely necessary, reuse exact lifecycle/Stop/resume
custody and preserve the original conversation. Each repeated quota needs fresh
eligibility; a new immediate rejection cannot borrow the last turn's permission.

**Exit:** a quota-after-tool original task completes across A→B→A, while final,
maintenance, question, permission, cancellation, queued input, alive retry and
unknown-child controls cause zero automatic inference. Authentication switching
alone is not this exit. Unsupported evidence keeps dispatch closed and the full
unattended feature incomplete.

### 7. Migrate existing state and remove the obsolete paths

Implementation record: `agents-tracker-h6sc.44`.

**Scope:** `internal/loginmigration`, `logins/{claude_profiles,
claude_settings,claude_history,claude_custody,claude_birth}.go`, old switch/retry
callers, shim metadata, upgrade/downgrade gates and operator docs.

Existing incarnations keep their inherited authority and immutable metadata.
Inventory old writers and unresolved switch/auth journals before new promotion.
An account cannot enter the common slot while an unresolved legacy process can
refresh its saved copy. Positive retirement or an independently qualified transfer
comes first. Never rewrite `LoginRuntime`, point a symlink elsewhere, expire an
unresolved receipt or delete a grant to manufacture convergence.

New births use the qualified new mode. Existing pending operations finish or
receive an explicit tested conversion; ordinary idle/human-waiting discussions
are not auto-resumed. Migration does not kill unrelated healthy work. Preserve
exact history/CWD/assets and unresolved recovery across disk and daemon failures.

| Remove from the qualified new path | Keep until the stated dependency ends |
|---|---|
| Account-derived Claude config/profile provisioning and MCP/CWD projection for isolation. | Shared native settings, native precedence, actual CWD validation, history access and aliases referenced by old incarnations. |
| Selected-account epoch comparisons used as stable-session identity; healthy authwatch recycling on selection. | Selection CAS, credential generations, exact birth/CLI/input/Stop custody and old-runtime decoding. |
| Whole-fleet idle/detach/stop/restart action for new account selection. | Old journal recovery and separately required Codex/bootstrap/legacy migration transitions until no supported state uses them. |
| Duplicate experimental account managers, unreachable integration scaffolding and obsolete present-tense docs. | Proven native helper containment, writer inventory, uncertainty handling and historical qualification receipts. |

Search both callers and tests before deletion. Replace obsolete behavior tests;
retain wrong-identity, stale generation, unknown-writer, history, Stop, crash and
migration regressions. Remove old code only after its persisted states/references
have a tested disposition, not merely because new births no longer call it.

**Exit:** no obsolete production path is reachable after supported migration;
no live/recoverable credential, history or owner intent was deleted. Update
ADR-034 with an explicit amendment and rewrite released operations guidance at
cutover, preserving historical provenance.

### 8. Connected review, qualification and release

Implementation record: `agents-tracker-h6sc.45`.

Slices 1→2 supply authority and maintenance. Claude slice 3 and Codex slice 4 can
then proceed independently. Slice 5 depends on qualified provider adoption and
scoped readings. Slice 6's input/evidence foundation can run in parallel, but its
dispatch depends on provider readiness and positive native task semantics.
Migration/deletion in slice 7 and full acceptance here close last.

Run the real daemon→shim→native path, first synthetic then owned real-provider
accounts and disposable discussions. Include fresh launch, normal resume, two
and several concurrent clients, long tools/subagents/workflows, attached clients,
all human waits, A→B→A with rotated refresh tokens, parked renewal, idle expiry,
relogin/removal, daemon restart/rejoin, stale quota, all-exhausted/reset, policy/
model changes, native settings/MCP/hooks, disk pressure and every publication/
continuation crash cut. Zero leaked owned workers and preserved user data are
acceptance results, not just test cleanup instructions.

| Cross-component contract | Integration evidence |
|---|---|
| Selection→Store→native authority | Real private files/Keychain, concurrent CAS, native renewal, actual owner reconciliation and fault cuts. |
| Maintenance→promotion/removal/relogin | Delayed claim races and positively settled writer ownership; no duplicate refresh domains. |
| Native adoption→host input | Same physical feed/incarnation/generation and actual policy readiness, including disconnect and supersession. |
| Execution/owner intent→continuation | Exact admitted intent and native effect boundary, all negative states, lost ACK and preserved tool history. |
| Legacy migration→new births→cleanup | Immutable old writers retained until proven retirement; exact data preservation and downgrade refusal. |
| Provider state→owner UI | Desired/actual/pending/unknown are distinct; no credential or fabricated capacity exposure. |

Run build, vet, lint, full Go tests and race checks for all changed concurrent
packages and required wider gates on the final integrated source. Use shared
caches and per-run scratch; do not repeat an expensive suite for documentation
changes alone. Mac and Linux receipts identify exact native versions/platforms;
fake/native skips remain open coverage. Review the full existing branch diff,
including independent chat/renderer/vendor work, before main integration.

Root performs connected-code review plus independent Sol and Astra final reviews.
Each critical finding gets a failing regression and a correction. Native
hypotheses get at most two bounded attempts before an explicit supported,
unsupported or unresolved disposition; don't grow another probe framework.
Proceed with independent product work while a gate is unresolved, without
claiming its behavior or enabling its automatic effects.

Only when the complete contract is green: update user docs, qualification map,
ADR and release notes; push the reviewed main integration; publish the next
minor version (+0.1.0). Public version values use plain `x.x.x`, with the existing
`v` release-tag prefix only. Keep tag-only CI and existing release/security gates.
Verify published artifacts and the installed VM/Mac behavior, not just a tag.
The current standing implementation authorization is not evidence that any gate
has passed. No partial auth-only slice is the completed unattended rotator.

## Execution ownership

Root owns shared Store schema, cross-package integration, Beads, Git, VM tests and
release. Sol implements native authority, concurrency and recovery. Luna handles
bounded fixtures, UI text, link/documentation reconciliation and mechanical
cleanup once its deletion conditions hold. Astra reviews critical interfaces
independently; Root also reviews connected callers, not only diffs.

Assign exclusive file ownership before dispatching parallel edits. Components
without shared files may run concurrently; dependencies wait for a reviewed
contract. Start with slice 1's smallest complete transaction and its tests, then
fair parked maintenance. Native experiments answer a concrete integration
question and stop at their bound. No production activation, deployment or release
is implied by completing this plan.
