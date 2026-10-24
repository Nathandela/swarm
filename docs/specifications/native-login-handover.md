# Native login handover implementation contract

Draft for Sol and Astra review · 2026-10-24 · `agents-tracker-h6sc`

Work is isolated on `feat/native-login-handover`, based on `179073ab`. Beads owns
task status; this document defines the implementation and acceptance contract.
The baseline evidence is [the native rotation research](../verification/provider-logins/seamless-rotation-research.json).

## Intended behavior

There is one selected saved login for Claude and one for Codex. A healthy native
execution loop keeps its process, conversation, model, tools and pending human
decisions while authentication changes. Confirmed quota exhaustion or fresh
near-limit evidence initiates a handover without waiting for every discussion to
be idle or detached. Already admitted requests may finish on the old account.

Selection, native authentication acknowledgement and observed request adoption
are different facts. A disconnected or unresponsive native process remains
visibly pending; host input fencing cannot stop its internal model requests.
Successful fleet handover is not claimed until every included live process has
qualified adoption evidence or independently ends. Other healthy discussions
must still progress. A timeout never authorizes killing tools or answering a
question. Strict instantaneous absence of old-account requests is not promised.

Automatic continuation belongs only to a specific unfinished native turn that
terminated because of quota. An alive native retry/loop receives no extra input.
Completed answers, post-answer maintenance, cancellation, pending input or
permission, unknown activity and ambiguous submission outcomes receive no
automatic continuation. A real model can independently repeat an action: the
contract is preservation of native history and exclusive host dispatch, not
universal exactly-once external side effects.

The supported user flow remains Add login / Make active / automatic rotation.
Enrollment remains available independently of advanced handover qualification.
Codex continues to show only weekly usage; Claude shows 5-hour and weekly usage.

## Ownership and storage boundary

The disk-audit worker was consulted through Swarm CLI. This branch owns login
credential placement/migration, native handover, execution evidence and recovery
records. The separate effort owns generic disk accounting, `internal/testbin`
and test-process supervision. Changes to shared idempotency retention or generic
TestMain cleanup are outside this branch. Small typed persistence additions for
execution/continuation are reserved here; further overlapping edits require
another bounded coordination exchange.

Keep existing common native configuration/history in place. Create no new
history, plugin or transcript copies. Saved-login refresh homes contain only
authentication and the minimal native configuration required for renewal.
Helpers are lazy and exit after use; their outputs and scratch have explicit
bounds and exact process ownership. Reuse the established Go/module caches.

Execution records contain identities, event positions, small enums and bounded
sets of live actors/tools/requests, never message bodies or credentials. Retain
one current execution record and at most one unresolved continuation per native
session. Bound both each record and the aggregate number/bytes of retained
records; capacity failure marks evidence incomplete and refuses new automatic
effects. Retire resolved records using exact native/session ownership, never
expire unresolved outcomes to recover space. Reserve atomic-replacement
headroom; ENOSPC before durable intent causes zero external effects.

## Components and contracts

### Native credential ownership

Retain the saved-login registry and revision CAS in `internal/logins`. Introduce
a versioned handover record alongside the existing stopped-writer migration
journal. Do not reinterpret an old pending switch as a hot handover. A schema
change must be rejected by old binaries rather than silently downgraded.

For Codex, qualify a short-lived native-managed app-server as the sole refresh
writer for a saved login. Serialize refresh, relogin, removal and credential
capture for that grant. Native code performs OAuth renewal; Swarm validates the
resulting identity, expiry and credential generation and distributes access
tokens only. A successful `account/read` alone does not establish fresh auth.
Expired/revoked credentials and partial writes remain explicit failures.

Managed discussions use Codex external-token authentication. Reuse their existing
authenticated app-server connection for login updates and refresh callbacks.
Intercept the exact auth request method before the generic backend event pump;
secret parameters/results never enter content, logs, journals, environment,
argv, persisted session metadata or the phone protocol. Do bounded asynchronous
refresh work and respond on the requesting connection. Coalesce concurrent
requests for the same grant; reject late results for an obsolete desired epoch.
Test callback ownership with multiple native clients and daemon disconnection.
The shim/native processes survive daemon loss; an unavailable refresh owner
must fail visibly and recover when reconnected without a fabricated user turn.

For Claude, qualify a stable native secure-storage authority and cooperation
with the installed native refresh locks/CAS. Current live processes are pinned
to their saved-login directory through their environment and need a one-time
owned migration. Preserve account-independent auth/config fields. File install
alone is not adoption acknowledgement. The actual native model loop, children,
quota/account-dependent settings and concurrent refresh must be tested before
enabling this path. A native capability failure changes the design before
dependent integration; helper tests do not substitute for this gate.

### Durable execution evidence

Use the existing authenticated hook/backend ingress and lifecycle identities.
One record per session/conversation identifies native incarnation, current turn,
owner-input revision, observation completeness, active actors/tools, pending
human decisions and terminal/final-answer cause. Preserve the current status
projection as presentation; automatic effects consult execution evidence.

Claude hook evidence is actor-aware and restored across daemon restart. A
missing start, gap, corrupt snapshot, unrecognized native lifecycle or overflow
makes the relevant work unknown. Native background tool completion is distinct
from returning a background task handle. Codex records exact thread/turn/item
identity, child relationships and outstanding requests; parent idle cannot
erase independently active child work. Reconciliation must use native state
where available and preserve uncertainty otherwise.

Persistence precedes acknowledgement of control-relevant evidence. Backend
reading and PTY draining stay independent of disk/refresh latency. Duplicate,
reordered and previous-incarnation events cannot advance a different turn.
Dropping a control-relevant event invalidates execution completeness before any
later automatic effect; a bounded queue must never silently authorize on loss.
Existing owner input, native decision, stop/delete and ContextGuard fences
remain the shared admission points; do not create another general scheduler.

### Handover and recovery

A handover records desired account/epoch and included native incarnations.
Install or distribute fresh auth, then record each native acknowledgement and
qualified adoption outcome. Newly admitted host turns require their process's
current adoption; laggards do not embargo healthy processes. Old-generation
usage/errors cannot exhaust or relabel the new account. Display pending/partial
adoption honestly. Retry bounded native auth operations; preserve live tools.

An independent continuation obligation binds original actor/turn, exact
terminal quota evidence, native incarnation, owner-input revision and adopted
account epoch. Its phases distinguish prepared, submission-uncertain, accepted
and retired. Revalidate all eligibility at the existing composer/lifecycle
write boundary. Durable intent must precede the native operation.

Each provider's capability gate must establish a positive machine-observable
unfinished-work discriminator. Quota plus absence of an observed final answer
is insufficient: lossy history, unclassified assistant questions and unknown
final/maintenance phases are ineligible. If a provider cannot distinguish these
states, its automatic continuation path is blocked until the design resolves it.

Codex's characterized operation is empty-input `turn/start` on the same loaded
thread after the failed turn is terminal. It starts a new turn; it neither
resends the original prompt nor replays a tool call. `turn/start` can steer an
active turn, so independently establish no active turn before dispatch.
Correlate the returned native turn and reconcile a lost acknowledgement only
with authoritative native evidence. A client-generated ID is not assumed to
provide deduplication. An unresolved outcome stays visible and unsent.
Reconciliation must identify the successor belonging to this exact obligation
and owner-input revision; the existence of any newer turn is insufficient.

Claude needs a separately proven native continuation primitive and exact
unfinished-turn binding. A generic Enter, last-prompt resend or blanket
"continue" is not a qualified replacement. A failed capability experiment is
an implementation blocker for this part of the requested outcome, not grounds
to label a disabled placeholder complete.

Proactive selection consumes fresh account-scoped observations, validates the
destination, and uses one conservative reserve with cooldown/hysteresis. It
does not permanently mark a merely near-limit account exhausted. Known reset
times restore eligibility; stale/unknown readings do not invent capacity.
If no better candidate exists, retain usable capacity and fall back to confirmed
quota handling. Bounded retries and a visible all-exhausted state avoid loops.
No per-discussion routing policy, balancing score, inference proxy or custom
OAuth implementation is part of the initial design.

## Implementation phases and exit contracts

| Phase | Work and principal seams | Evidence required before dependent work |
|---|---|---|
| Native capability qualification | Reusable isolated native probes beside existing verification tooling; Codex refresh owner, built-in route/WS and request adoption; Claude stable-store loop and continuation. | Actual installed binaries, synthetic loopback first, then owned Linux native canaries. Preserve tool identities/results, auth identity and human-decision state. Failed hypotheses revise this contract. |
| Execution evidence | `internal/engine`, `internal/persist`, `internal/daemon` registration/reconcile, authenticated hook/backend ingress. | Red regression for lost child across daemon restart; no false-idle destructive authorization; actor/turn/gap/overflow/disk-full/permission tests and race checks. |
| Credential ownership and migration | `internal/logins`, narrow native refresh lifecycle, login launch/shim auth boundary and versioned ownership records. | One refresh writer, no secret disclosure or extra history copies, A-B-A/expiry/relogin/removal/crash tests, old-schema and downgrade refusal. |
| Live handover | `internal/skeleton/logins*`, dedicated backend auth dispatch, per-incarnation adoption and input admission. | Busy/attached healthy loops continue; no fleet idle barrier; partial adoption explicit; stale A results cannot affect B; daemon restart and concurrent owner input checked. |
| Exact quota recovery | Failure/transcript observers, bounded continuation records, existing composer/lifecycle admission. | Terminal unfinished quota only; zero dispatch for final/maintenance/human waits/cancel/unknown/newer turn; accepted-but-unacknowledged crash never blindly retries; native tool results retained. |
| Proactive selection and UI | Existing usage/availability plumbing, owner-only login projection and compact TUI status. | Freshness, capacity reserve, cooldown, exhausted/reset behavior, no flip-flop; Codex 5h remains hidden; no new setup workflow. |
| Integration and removal | Replace qualified path's global stop/restart dependency, update ADR-034 and operator/verification docs. | Legacy journals still finish safely. Retain only required legacy migration/fallback code; remove unreachable scaffolding and obsolete claims. Full quality gates and independent final reviews. |

Every production change begins with a failing behavioral test, receives a
separate Astra review, and is revised until that phase's contract passes. Luna
handles bounded fixtures/docs/simple tests; Sol handles native ownership,
concurrency and recovery. Root integrates and performs a separate connected-code
review. Agents receive exclusive file ownership before editing shared seams.

## Final qualification

Build, vet, lint and the full suite must pass; run race tests for concurrent
packages and the required wider race gate. Use external run ownership for native
test processes and deadlines, preserving only bounded evidence. Do not clean
unrelated test processes, caches or worktrees.

Native acceptance covers multiple sessions during long tools/subagents, attached
terminals, pending approvals/questions, A-B-A, actual expiry/renewal, stale quota,
all-exhausted/reset, daemon crashes and ambiguous native acceptance. Inject
ENOSPC before intent, during credential replacement and before completion;
preserve credentials, pending custody and exact conversation identity.

Migration separately exercises existing live legacy sessions and pending old
switch journals. Their immutable environment and original ownership remain
intact until a stopped-writer migration reaches a proven actor/tool-free safe
boundary. Human decisions remain usable and crash obligations durable; timeout
does not authorize a stop or hot reclassification.

Race new process launch against handover, destination B rejection against C
selection, and continuation against owner Send/Stop/Delete and ContextGuard.
Recovery preserves native model, reasoning, approval, sandbox, cwd and thread
configuration. If B cannot serve the original model, refuse visibly without
silently changing configuration or losing the continuation obligation.

Root and independent Sol/Astra final reviews assess the integrated implementation
against this contract. Record native version/platform and remaining limitations
explicitly. Work is committed and pushed on the feature branch; main integration,
release tagging and live fleet migration are not performed by this branch task.
