# Native login handover implementation contract

Implementation contract reviewed by Sol and Astra · 2026-10-24 · `agents-tracker-h6sc`

Work is isolated on `feat/native-login-handover`, based on `179073ab`. Beads owns
task status; this document defines the implementation and acceptance contract.
The end-to-end behavior below is an unreleased target. Retained-account and
execution-evidence foundations have targeted verification. Bounded actual Linux
Claude profile-birth and CWD-launch evidence now exists; integrated authentication
and admission, workflow parity, migration and automatic continuation still have
open gates.
The baseline evidence is [the native rotation research](../verification/provider-logins/seamless-rotation-research.json).
The [Linux native capability receipts](../verification/provider-logins/native-handover-capabilities-2026-10-24.json)
record version-pinned scenarios using synthetic credentials. They characterize
native behavior; they do not certify the integrated feature. Receipt counts and
coverage evolve with the qualification work.
The [finite continuation source review](../verification/provider-logins/native-continuation-source-gates-2026-10-25.json)
records the current native API gaps and distinguishes source hypotheses from
executed canaries.

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

The storage branch inspected at `8c93f8a9` preserves native histories, saved
credentials, handoff inputs, session metadata and unresolved recovery records.
Its discussion-deletion changes add `handoffMu` before the resume/workspace locks
and check retained handoff dependencies before each deletion. A future merge
must preserve those checks together with this branch's exact bootstrap
owner-delete authorization.

The source coordination review of main `98e74010` (`v0.23.1`) adds an explicit
integration gate, tracked in `agents-tracker-h6sc.24`. Both branches added a fourth
`sendMessage` boolean, but it means `manual` here and `pasteText` on main. Preserve
both meanings and their admission checks independently. Main’s new `PasteInput`
path must cross this branch’s `foreignWrite` and `beforeWrite` ownership fences,
including fallback, while retaining paste framing, sanitization and input embargoes.
Keep manual input available after `InteractionError`; newer owner input still
invalidates stale automatic recovery. Retain main’s latched journal storage-failure
refusal and physical handoff-worktree validation. Agent handoff creates delegated
work; account rotation preserves the existing conversation and CWD.
Frozen constructor source3534/productdc90 predates these changes. Its receipts
remain historical; they cannot qualify the eventual merged source.

Keep the existing common native directory as the history and shared user-asset
source, and as the location of legacy incarnations. Create no new history, plugin
or transcript copies. Future owned Claude incarnations use a deterministic native
profile path derived from account and organization identity. Their saved native
secure-storage authority remains in its existing saved-login location; do not
copy grants into profiles. Retain old virtual profile aliases while referenced
histories need them. Profiles are mutable and may contain secrets, so retire an
alias only after exact history and live-process references end. Saved-login
refresh helpers use an empty private config and native auth authority. Helpers
are lazy and exit after use; their outputs and scratch have explicit bounds and
exact process ownership. Reuse the established Go/module caches.

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

Retain the saved-login registry and revision CAS in `internal/logins`. Codex's
versioned handover record remains separate from the stopped-writer migration
journal. Claude's new-profile birth path uses the existing provider selection
and epoch, without another handover or per-session adoption ledger. Do not
reinterpret an old pending switch as a hot handover. A schema change must be
rejected by old binaries rather than silently downgraded.

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

Managed Codex launches scrub inherited authentication selectors and reject
`OPENAI_FEDERATION_RULE_ID`, `OPENAI_IDENTITY_TOKEN_FILE`,
`OPENAI_WORKLOAD_IDENTITY_CONTEXT`, `OPENAI_API_KEY`, `OPENAI_ORGANIZATION` and
`OPENAI_PROJECT` assignments in the common native home’s `.env`, including empty
assignments. Workload inputs can select auth before external-token installation.
The other three are refused consistently with the selected-subscription policy:
optional API-key authentication and conflicting organization/project headers
are outside that policy. This does not assert a default Responses bypass or a
server-side account change. Check fresh birth and normal/switch restart paths
again at final shim preparation. Reuse the private bounded file reader and native
record/header grammar; never evaluate or report values. Ordinary tool and
transport preferences remain native inputs. Validation does not lock the file
against a later owner edit. API-key-dependent realtime/Guardian behavior is not
qualified for subscription-managed sessions.

For Claude, bind native refresh to the selected saved authority using the
installed native refresh locks/CAS. Every future owned identity, including the
currently selected account, gets its own deterministic account-and-organization
profile. Existing live processes keep their original account and common profile
until that specific incarnation reaches a separately qualified safe transition;
the current implementation has no hot rebinding. A healthy old-A process may
finish under A while new work uses B. No fleet-wide idle barrier is allowed. A transition needs durable
source ownership, proof its native writers stopped, one launch claim and an exact
successor binding before readiness or continuation. Do not restart a healthy
process merely to make adoption counts uniform.

The common root remains the physical history and ordinary user-asset source.
Provision only stable aliases to existing assets; never copy history, plugins,
transcripts or caches. Preserve normal user/project/local precedence, launch
flags, model, effort, permissions, cwd and absolute native transcript path. Do
not use `--settings` or `--mcp-config` to simulate user scope. Before creating an
owned profile, provision the explicit user and exact-CWD local MCP
definitions from `.claude.json`'s `mcpServers` and `projects[exactCwd].mcpServers`.
Use a supported private native input if available; otherwise atomically edit only
those documented maps in a private destination after all config writers stop.
Never put definition JSON or secrets in argv. Verify the resulting scopes against
native-writer fixtures. Reject conflicting destination definitions unless
identical or explicitly resolved. Pre-provision known incoming working
directories. Established profiles can add a bounded exact CWD under the existing
registry fence while discussions remain live when the complete validated MCP
merge changes no native config. That path publishes registry membership only;
it still settles the profile directory before the registry save. Actual config
changes require positive writer exclusion followed by fresh source and
destination reads. Preserve unrelated native fields. Both initial launch and
resume callers must reach this path. Conflicts or unavailable writer-stop proof
for a required config change leave that profile pending without blocking healthy
discussions. The [write-free extension checks](../verification/provider-logins/claude-cwd-extension-2026-10-25.json)
cover source and connected Linux callers; native two-discussion qualification
remains separate. Do not migrate MCP OAuth, trust, account state or approvals.
The only other initial config fields are the limited setup state described below.

Native refresh helpers use a fresh empty private config bound to the existing
saved native authority. Accept auth only after independently observing a native
written profile with the exact target account and organization IDs and a
`profileFetchedAt` within the helper attempt, plus a usable resulting grant,
expiry/scopes and current credential generation/selection. Initialize success,
file existence, `SessionStart`, or logged-in status alone is not an auth receipt;
never seed identity or infer success. This receipt establishes account identity
only. Keep auth, discussion startup, local config readiness, ordinary native
trust/policy enforcement and operational adoption distinct. Preserve native
policy enforcement; do not fabricate a policy verdict or run a hidden model
preflight. A normal native operation supplies operational evidence. Mac and Linux native
config and fresh-auth suites each pass ten selected cases. Production credential
CAS/crash integration and end-to-end handover qualification remain open. These receipts do not authorize production activation.

### Claude helper caller integration

The [retained-helper verification](../verification/provider-logins/claude-retained-auth-2026-10-25.json)
covers helper recovery and retry authority with synthetic native Linux cases.
It does not establish the daemon caller or ordinary profile-launch behavior.

The reviewed integration boundary is fresh authentication for new owned-profile
births. Existing `ProviderState.ActiveID/Epoch` records the desired account;
it does not assert authentication success. One Make active request persists B
even while B's authentication is pending. A later C request supersedes it.
Every birth still requires an exact current receipt, profile and CLI identity.
A fresh ready warm B receipt is validated against prospective B within the
selection transaction; the public validator must still refuse B while A is
selected. A structurally valid exact-target receipt with positively stale
canonical generation or expiry permits desired B pending renewal. It is retained
and cannot authorize a birth until renewed. Malformed, unreadable or conflicting
authority still refuses selection; generic validation errors are never treated as
staleness. This avoids a second Make active request or slow authentication work
on the owner operation path. The [Store checks](../verification/provider-logins/claude-desired-selection-store-2026-10-25.json)
cover this distinction, CWD extension and helper admission; daemon callers remain
subject to the native constructor gate below. The
[caller checks](../verification/provider-logins/claude-account-manager-callers-2026-10-25.json)
cover the owner API, completion worker, usage readers, typed-input gates and
final working-directory preparation with synthetic Linux processes.

Reuse the one-second auth watcher for confirmed-exhaustion selection. The existing
two-second login-completion worker chooses the selected and at most one warm
login and owns slow Start/Recover calls,
outside the login, admission and discussion lifecycle locks. Only the successful
creator of PREPARED receives start authority. Reopen, a missing PID and a timeout
never recreate it. A locked bounded census in the shared Begin/Replace creation
path admits at most two unresolved helpers across daemon replacements. Terminal
labels or absent PIDs do not release a slot; exact positive settlement does.
Native authentication already runs asynchronously. Bound external maintenance
with its context, outside application locks, and revisit enrollment after each
return. Context cancellation does not make Store locks or filesystem calls
interruptible. No new worker thread, queue, scheduler or index is needed.
Alternate cleanup-first passes with candidate-first passes so repeated capability
probe timeouts cannot starve retained jobs. Before the first owned-mode selection,
qualify the platform, manager and exact native image outside the owner mutex;
the transaction only checks the cached installation identity. Start revalidates
the installation. The launch finalizer also provisions the actual CWD returned
by the existing worktree hook, with exact selected-runtime binding and ordinary
before-fork rollback on refusal.

Recovery collects exact helper custody and retains its terminal intent. A ready
receipt stays usable while its canonical generation and expiry remain valid.
A failed or lost job keeps its consumed budget across ticks and restart. An
explicit new desired epoch permits one atomic replacement of an exactly settled
failed intent; that completion worker owns the new start. Warming changes and
reboot supply no retry authority. A successfully completed ready receipt may
renew after positive canonical generation/expiry evidence proves it stale,
including a warm B under A. Replace that exact settled receipt atomically; a
still-valid receipt, unreadable grant or selection mismatch alone grants no new
attempt. Failed replacement retains the previous receipt. Explicit removal/
relogin owns retirement through its existing durable transaction. Bounded enumeration reads the existing
intent directory, including issuing selection and partial cleanup evidence.

The implementation dependency cuts are:

| Cut | Existing seams | Required evidence |
|---|---|---|
| Retained helper lifecycle | `logins/claude_auth*`, `loginworker/claude_auth*` | Retained ready/failed outcomes across reopen; no repeated budget; exact new-epoch replacement; cleanup races and ENOSPC preserve the prior authority. |
| Birth and mutation custody | Store registry lock, shim native start/record, session replay/delete, saved credential/relogin operations | Selection-first admits zero old births; birth-first publishes exact ownership before selection; lost/unpublished reservations cannot be deleted or replayed; old nonselected accounts retain their grants until exact writer-stop proof. |
| Desired selection and runtime | Existing login switch entry point, exact-CWD profile provisioning, runtime/environment and both usage readers | One pending selection survives restart, C supersedes B, receipt/config gate every birth, owned-profile usage is accepted and old-account usage rejected. |
| Daemon caller and projection | Existing auth tick/completion worker and relogin completion | Slow auth leaves PTY/hooks/list responsive; current and one warm job coalesce; selected relogin avoids legacy fleet restart; desired/auth/config/startup/old-account pending remain distinct. |
| Native integration | Actual daemon and command constructor | Normal trust/policy, native history/config and an ordinary operation work through the real caller. Helper-only evidence cannot enable this path. |

The shim first claims the existing exclusive zero-PID native record under a
short custody guard. Slow environment and image preparation follows outside the
registry lock; an ordinary refusal can then publish its own contained stop proof.
The final guard holds the existing interprocess registry lock from exact
reservation/current receipt/profile validation through PTY start and durable
native-process publication. Revalidate the original unused claim and consume one
scope-local Start attempt immediately before PTY Start, including error outcomes.
The [refused-birth checks](../verification/provider-logins/claude-no-start-custody-2026-10-25.json)
include meaningful negative controls, duplicate/crash retention and Linux tests.
Catchable cancellation is buffered before custody and remains captured through
deferred containment and proof publication. Pending cancellation is checked
before preparation and again inside final admission before Start. The
[startup cancellation checks](../verification/provider-logins/claude-startup-cancellation-2026-10-25.json)
cover actual Linux TERM/INT/HUP, proof-sync timing and a final-check omission
control. SIGKILL remains unresolved; a signal arriving after the final check may
start a child and then uses the existing containment and Wait path.
Do no network or native initialization under the final lock. Daemon replacement cannot open a gap between
selection and start. A crash after start but before publication retains the
existing reservation as unresolved; no extra birth ledger is introduced.
Destructive Store mutations and relogin recovery use the same fence and exact
bounded native custody, including hidden/lost and nonselected old-A sessions.
The display roster's best-effort scan is not a complete negative writer proof.
The [Linux owner Stop regression](../verification/provider-logins/claude-birth-owner-stop-2026-10-25.json)
records the shared-mutex failure and independent-reader fix; it does not qualify
the complete birth or migration path.

Operational profile selection requires registry schema 10, so older writers
cannot ignore this fence. New profile reservations use metadata schema 5 and
native scope schema 4. Legacy common-home reservations remain schema 4 while
unresolved and block destructive cleanup and ambiguous replay at every registry
version. Only exact native schema 3 stop proof or the original before-fork issuer
may retire them. Under the same registry lock, retirement first durably raises
the registry to schema 10, then writes the terminal schema 5 marker, before any
workspace hook or payload removal. The marker preserves deletion authority after
partial cleanup. This does not classify common homes as profiles or permit live
common-home schema 5 births. A visible registry promotion or identical marker
may follow a failed directory sync; retries settle both directories before
allowing effects, without rewriting the marker or advancing the revision.
An operation omitted from the roster remains unresolved while its session
directory exists or cannot be inspected; a skipped metadata row is not absence.
The [Store foundation checks](../verification/provider-logins/claude-store-custody-2026-10-25.json)
cover mutation/admission fencing and the first-creation race. The
[custody transition checks](../verification/provider-logins/claude-custody-transitions-2026-10-25.json)
cover all registry versions, partial deletion and the actual selection boundary
with synthetic processes on Linux. Refused-birth cleanup is covered separately;
historical rows without Runtime provenance, live-session daemon restart/rejoin and integrated
native authentication/admission remain gates.

Old A may finish internal work after B is desired. Typed composer and daemon-authored
turns on a stale incarnation remain pending. The explicit owner terminal-input
path keeps approval keys and cancellation usable; opaque terminal bytes cannot
be classified as approvals versus new turns. This separate input path does not
grant automated continuation authority or prove human origin. Both paths retain
the common custody checks and final write fences. A failed selected Claude login
can be retried with Enter in Accounts; healthy/pending Claude selections and
active Codex selections retain their existing behavior. This slice
does not implement existing-discussion transition or automatic continuation.
Keep unsupported platforms/pins explicit and enrollment independently usable.

The [ordinary native startup checks](../verification/provider-logins/claude-normal-startup-2026-10-25.json)
pass on both pinned platforms with synthetic authority and normal settings
discovery. The native trust dialog needs a settled selection before confirmation;
the [staged launch gate](../verification/provider-logins/claude-launch-gate-2026-10-25.json)
now uses the existing 500 ms sampler to separate selection from confirmation. Each
step needs an unchanged recognized frame observed at least 250 ms apart, bound
to the same native incarnation. Foreign input, controller takeover, incomplete
snapshots and uncertain writes abandon that attempt. The gate takes no queued
composer ticket and uses a bounded 100 ms control write; normal input keeps its
existing behavior. Current-frame checks reduce stale input but cannot make a
terminal read and native key adoption atomic.

The isolated native checks establish trust, SessionStart and the initial composer
only. The [post18 V7 constructor canary](../verification/provider-logins/claude-profile-setup-2026-10-25.json)
adds bounded actual Linux Claude 2.1.310 evidence with synthetic saved grants:
B1 starts on one CWD, registry-only membership adds a second CWD while B1's exact
native/shim identity remains unchanged, and B2 is a fresh Launch reusing that
profile rather than a conversation resume. B3 is another fresh Launch on the
same profile after B1/B2 stop. After B3 stops, C starts a distinct profile; its stopped-B profile hash remains unchanged. The expected
negative N case stays not-ready with zero model requests. All cases have exact
inner native-stop and outer empty/gone cleanup evidence; Astra independently
reviewed 39 positive artifacts. V6's extra-art-glyph observation-cap failure
remains recorded as historical evidence; V7 uses the retained CLI identity
instead of decorative banner text.

V7 does not establish real saved-account authentication or account admission,
fresh-user onboarding, Mac native constructor behavior, full workflow/plugin/MCP
parity, task execution, automatic continuation or production activation. Fresh
users still complete native setup manually, and existing profile files are
never backfilled. The [staged launch gate](../verification/provider-logins/claude-launch-gate-2026-10-25.json)
and [CWD extension record](../verification/provider-logins/claude-cwd-extension-2026-10-25.json)
retain those boundaries and the older failed attempts.

For an existing configured user, the first write of an unborn profile can inherit
the common native configuration's actual `hasCompletedOnboarding: true`, its
original canonical stable `lastOnboardingVersion`, and a supported built-in
`theme`. This is Swarm's policy for preserving existing native setup state, not
proof of a human security acknowledgment or a native cross-directory guarantee.
Only these fields and the explicit MCP maps transfer. Credentials, account
identity, trust and approvals remain separate.

Any existing destination file suppresses setup inheritance, including an empty
file or `{}` on a pending retry. Established profiles never backfill or reset
setup. Missing or false completion leaves manual native setup necessary;
malformed selected fields and unsupported legacy themes produce an actionable
refusal without exposing their values. Modern settings keep their existing
shared alias. The [setup inheritance checks](../verification/provider-logins/claude-profile-setup-2026-10-25.json)
record source/test coverage and the bounded V7 Linux fresh and second-profile
constructor runs from an actual configured setup source. Live-session daemon
restart/rejoin and fresh-user enrollment setup remain outside this qualification; fresh users
still complete native setup manually while the managed interface is evaluated.

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
For the pinned Codex single-feed path, retain live items and fold exact matched
completions into bounded phase/order summaries. Native source has one serialized
event drain and does not replay item lifecycles on that connection; reconnect or
unknown custody still invalidates automatic effects. Retaining every completed
item would make ordinary long turns overflow. Child actors retain exact parent
and turn edges; an unlinked child remains ineligible until a native spawn receipt
connects it. A parent terminal or wait/close snapshot never clears descendants.
Sol and Astra approved this source-qualified reduction on 2026-10-24; it does not
claim deduplication of arbitrarily forged native lifecycle sequences.
Existing owner input, native decision, stop/delete and ContextGuard fences
remain the shared admission points; do not create another general scheduler.

Global Codex thread/status notifications do not establish a lifecycle
subscription. The [R12 diagnostics](../verification/provider-logins/codex-fresh-constructor-2026-10-27.json)
and pinned native source show that ordinary thread creation subscribes its
creator. Rejoining after materialization can miss earlier turn/item events.
Canonical paginated history is separately sourced state; never manufacture
past lifecycle notifications from it or use an empty snapshot as actor proof.
Native `thread/resume` can also replay requests and invoke idle goal/queue
callbacks. An active-state precheck is not an atomic passive-attachment guard.
Inspected 0.164.0, 0.165.0 and pinned main offer no suitable passive subscribe
method. Keep automatic effects fenced while qualifying reconciliation or
creator-stream observation; no Codex UI replacement is authorized by this work.


### Handover and recovery

A handover records desired account/epoch and included native incarnations.
Install or distribute fresh auth, then record each native acknowledgement and
qualified adoption outcome. Newly admitted host turns require their process's
current adoption at admission; laggards do not embargo healthy processes. Old-generation
usage/errors cannot exhaust or relabel the new account. Display pending/partial
adoption honestly. Retry bounded native auth operations; preserve live tools.

An independent continuation obligation binds original actor/turn, exact
terminal quota evidence, native incarnation, owner-input revision and adopted
account epoch. Its phases distinguish prepared, submission-uncertain, accepted
and retired. Revalidate all eligibility at the existing composer/lifecycle
write boundary. Durable intent must precede the native operation.

For qualified Codex, the adoption receipt is explicitly **account-API adoption**:
after `account/login/start` acknowledges, a guarded `account/rateLimits/read`
must return the target account ID and an explicit `ordinaryUsageAllowed` boolean.
The native API validates the account and user against its current authentication;
the Linux synthetic probe confirms A→B→A and rejection of stale or mismatched
identity. Separate model-loop probes establish subsequent model-request behavior.
This avoids requiring an initial model request before admitting a host turn.

Bind native authentication/read dispatch and receipt publication to the same
native connection, incarnation, desired epoch and credential revision; owner
input admission checks that same tuple at its admission point. A false permission proves
account-API adoption but prohibits new host turns and continuations; approvals
and cancellation remain usable. Null, malformed, cancelled, mismatched or stale
reads invalidate admission readiness and discard all returned quota values.
Historical adoption may remain recorded separately from current readiness.
For explicit owner submissions, epoch, credential revision and readiness are
checked at admission, not leased through the request write. A later readiness
invalidation does not revoke an already admitted submission on that same physical
feed/incarnation; a fresh submission must pass the gate again. Existing physical
connection identity and Stop/Begin write fences still apply: replacement or
disconnect never rebinds an admitted operation to a successor. Automatic
continuation independently requires its exact task/turn and unfinished-work
claim, including exclusion of human waits, at its final admission boundary.
Unscoped native usage/error notifications only trigger bounded, coalesced scoped
reads; their contents cannot label the newly selected account exhausted. An old
turn's terminal quota remains bound to its original generation. Ordinary usage
permission does not establish model-specific capacity or continuation eligibility.
Sol and Astra approved this refinement on 2026-10-24; integrated qualification
remains required.

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

Six synthetic native scenarios pass on both Mac and Linux, including the quota
successor using B without prompt resend or repeated completed tool. The exact
ephemeral native title helper is outside the main-thread wake path. These facts
do not close the reserved `active_turn` task-none window, actual fresh managed
TUI birth or loaded-thread configuration/refresh gates. Public config reads
re-read files rather than attest the loaded thread's full configuration. Do not
build or enable a dispatcher whose eligibility predicate ordinary production
sessions cannot positively satisfy. Preserve user hooks, MCP, plugins and
workflows; do not disable them silently to pass a canary.

Repeated quota recovery must retain the same original task across A→B→A. Each
successor requires its own exact native start, unfinished-work and quota evidence;
an immediate quota with no new qualifying work cannot inherit permission from
the previous turn. Use at most one unresolved obligation and bounded lineage,
with durable uncertainty before effects and no resend after a lost ACK.

Claude's ordinary hook observer has not established a complete actor census or
final-result barrier across plugin transformations and completion work. A
completed generator return alone does not prove the final native answer. Thus
the earlier proposed host-composer continuation remains unqualified and is not
an implementation prerequisite for the authentication helper or new births.
Generic rate-limit text, missing Stop hooks and the last available records are
insufficient to establish unfinished subscription-quota work.

A separate native interrupted-turn resume capability is under finite
qualification. Bare `--reply-on-resume` fails the source hypothesis: a quota
assistant row can prevent replay, while cancellation stripping can expose an
earlier prompt. The distinct native `CLAUDE_CODE_RESUME_INTERRUPTED_TURN=1`
route uses native transcript repair and a vendor-owned recovery message. The
[environment variable is documented for SDK use](https://code.claude.com/docs/en/env-vars);
pinned Linux 2.1.310 source and the finite observation below also establish an
interactive startup path. This does not qualify a stable interactive recovery
contract. It receives
no host continuation prompt, transcript surgery or input replay. Qualification
must prove quota-after-tool recovery and zero automatic inference for completed
answers, unanswered questions, unapproved tools and explicit cancellation, with
positive startup evidence for negative cases. Any human-wait/cancel requery
fails this contract. Process custody, original owner intent and exact successor
binding remain required even if those native cases pass. No frontend/mode change
or global preference change is implied.

The owner authorized structured Claude exploration on 2026-10-25. Terminal
resume experiments were paused at that checkpoint while a direct native stream-JSON baseline tested
trust recording, normal relaunch, request-bound human input and durable native
history. Preserve the normal CLI prompt, settings, tools and project features;
do not spoof a cloud session origin or assume cloud-only pending-action recovery
exists locally. This investigation adds no SDK dependency or live/default mode
change. Quota recovery, parked decisions and workflow/background parity remain
separate gates after the baseline.
The [structured baseline](../verification/provider-logins/claude-structured-baseline-2026-10-25.json)
passes on the pinned macOS and Linux binaries. It verifies native history and
one question callback with normal settings; it does not enable recovery. The
[quota continuation canary](../verification/provider-logins/claude-structured-quota-2026-10-25.json)
also passes on both pins: a completed Read survives a typed quota failure and
native initialize-only continuation finishes the original task without another
host user message. A completed final-answer case also stays inactive on both
pins. The [pending-question counterexample](../verification/provider-logins/claude-structured-pending-question-2026-10-25.json)
fails on both pins: after a durably unanswered native question, initialize-only
recovery attempts another model request and appends a native continuation row.
The resume flag therefore cannot determine eligibility. Swarm must prove exact
interrupted work and exclude human waits before constructing a successor.
That host gate, cancellation, queued input, background work and workflow parity
remain unqualified.

The first [structured background-actor case](../verification/provider-logins/claude-structured-actors-2026-10-25.json)
on Linux observed parent success while its exact child was still active. After
the child completed and background membership became empty, native Claude made
another model request from its task-completion notification, without host input.
The three-response fixture refused that fourth request, so the case remains
failed and no final idle was observed. Parent result, command completion, child
terminal and empty membership therefore cannot establish whole-session completion.
Keep session activity, known-child settlement and original-task recovery
eligibility distinct. A native notification is not human input; it grants no
permission to answer a pending question or resume unfinished work. Global actor
completeness remains unqualified even when the known child settles.

The later R4 case passes that finite Linux 2.1.310 cycle: exact notification
provenance and request continuity authorize one scripted follow-up response,
followed by a distinct native result and then native `idle`. Normal instructions,
settings, the project hook and native history remain intact. This establishes a
tested boundary for this one child-and-notification cycle; cancellation, pending
human decisions, background shell tools and workflow completeness remain separate
gates. No automatic successor was constructed or authorized.

If structured Claude becomes the selected managed-session interface, use one
shim-owned primary native process and its typed input/control stream. The
existing `BackendConfig` describes a separate Codex side process; it is not a
second Claude execution loop. Reuse the execution record, operation journal and
native decision protocol. Ordinary launch and manual resume never inherit the
interrupted-turn flag. Automatic eligibility must be durably claimed and checked
before constructing the successor, because initialization itself can trigger
native recovery.

The host proof must include newer queued owner input, not merely input already
written to the native process. The current `UnobservedOperation` excludes
`prepared` operations, and the Codex owner revision advances at its native write
boundary. Neither alone establishes that no newer input is waiting in the
composer lane. A Claude recovery design therefore needs a durable owner-intent
fence at admission and a final check under the existing lifecycle/input locks.
Begin qualification with the recorded pending-question counterexample requiring
zero stop and zero successor construction. Native background actor completeness,
normal workflow/command/MCP behavior, and the managed chat interface remain
separate acceptance gates. On 2026-10-26 the owner explicitly approved
implementing a new opt-in Swarm chat for new Claude sessions only. This does
not authorize converting existing sessions or replacing the default
native-terminal interface.

Released main `6324374c` includes Claude's native sender and independent queue guard,
with native permission and conversation-change checks. The
[reviewed feature-branch integration](../verification/provider-logins/main-integration-2026-10-27.json)
preserves those paths, Hermes ownership and message provenance. Their submitted/started
receipts prove message admission, not task completion or safe quota recovery.
The `h6sc.30` investigation evaluates passive native mod events through the
installed pinned CLI. The documented
[turn, tool and session hooks](https://code.claude.com/docs/en/plugins/mods/reference)
provide finite observations, but unfinished-work, human-wait, cancellation and
background-work discrimination remain unqualified. This investigation grants no
automatic continuation authority and does not replace the approved structured
chat runtime.

The [first passive native mod observation](../verification/provider-logins/claude-native-mod-observation-2026-10-27.json)
now joins one Linux Claude 2.1.310 owner prompt to its main turn, a completed
Read and an exact-prompt typed quota failure in durable native history. A
completed-answer capture also satisfies the corrected offline oracle; its
original command failure is retained. These results characterize the native
terminal interface; they do not gate implementation of the approved Swarm chat.
Further passive-terminal assembly work is parked after the
[isolated startup failures](../verification/provider-logins/claude-native-control-assembly-2026-10-28.json).
That runtime still has unknown actor coverage and refuses all automatic effects.
The later [human-state observations](../verification/provider-logins/claude-native-human-state-2026-10-27.json)
observe an exact live question and one human-Esc cancellation. Native history
does not durably record either exclusion in these cases: both question commands
failed their physical-row gates, and the cancelled turn has no abort marker.
Persist human-wait and cancellation exclusions in Swarm's existing execution
record; missing native history cannot clear them or authorize a successor.
Those human-state cases sent no successor or automatic continuation. The API
abort operation, background actors, queued input and account adoption remain
separate gates.

The [known-child observation](../verification/provider-logins/claude-native-known-child-2026-10-27.json)
retains a strict frozen-prefix result: the parent answered while its exact local
child remained running. The full command failed during unit shutdown; actor
census remains unknown. An `ask/person` check alone is not a human wait.

The [native startup-resume evidence](../verification/provider-logins/claude-native-linux-startup-resume-2026-10-27.json)
adds one synthetic A→B continuation. A reached quota after a completed Read and
all A writers were retired before B construction. B resumed the same conversation,
used B's synthetic credential/account context, made one main request with zero
terminal input, and produced a native final without repeating the Read. A's original
physical history prefix remained unchanged; native wire formatting trimmed one trailing tab and
added refreshed B context. The full command still failed on B's unit shutdown
timeout. Startup/actor completeness, real accounts, policy coherence and
production recovery remain unqualified.

Full standard-login hot adoption remains unknown. The earlier negative replaced
both credentials and account metadata in macOS SDK mode: the next request used
B's bearer but retained A metadata. It did not exercise native login and its
post-login resets or interactive sibling adoption. Those results justify retaining
immutable account profiles as the current candidate; they do not disprove every
native login transition. The later Linux same-process probe below confirms
bearer reload from credentials alone while again observing stale A metadata.

Proactive selection consumes fresh account-scoped observations, validates the
destination, and uses one conservative reserve with cooldown/hysteresis. It
does not permanently mark a merely near-limit account exhausted. Known reset
times restore eligibility; stale/unknown readings do not invent capacity.
If no better candidate exists, retain usable capacity and fall back to confirmed
quota handling. Bounded retries and a visible all-exhausted state avoid loops.
No per-discussion routing policy, balancing score, inference proxy or custom
OAuth implementation is part of the initial design.

## Opt-in Claude chat implementation

**Unreleased; installed-native qualification and deployment remain open.** The
feature branch implements an explicit fresh local Linux Claude2.1.310 caller. New Claude
sessions can choose **Swarm chat (experimental)** through the launch
form or `swarm spawn --interface swarm-chat`. Ordinary launches remain native
terminal sessions; existing sessions are unchanged. Initially refuse resume,
handoff/import, remote/preset use, unsupported runtimes and platforms. Persist an
exact `claude-chat` runtime discriminator so old readers fail closed.

The [caller checkpoint](../verification/provider-logins/claude-native-chat-caller-2026-10-28.json)
connects explicit launch, managed stdio, deterministic initial owner input,
version 2 chat reads and independent Stop. Seven Linux race-tested packages pass
529 checks with one explicit native-image gate still skipped. Cold restart
retains Stop/read custody without restoring input. Native decisions, complete
actors/workflows, long history and automatic recovery remain unqualified; this
source checkpoint grants no deployment or release claim.

The [regression follow-up](../verification/provider-logins/claude-chat-regression-checkpoint-2026-10-28.json)
records terminal teardown fixes and passing race suites for all five affected
packages. The original full-run failure is retained; native qualification remains open.

The [installed-native preflight](../verification/provider-logins/claude-native-host-preflight-2026-10-28.json)
passes synthetic AUTH and observed workspace trust. Native setup under umask
`0002` created absent settings with mode `0664`; three subsequent native writes
preserved a precreated `0600` file. Provisioning now creates only missing private
settings, preserving existing preferences and the strict validators. A separate
initial-instance ordering fix passes Linux component race checks. The fresh
combined public native attempt still fails before initial-message delivery in
both subcases. Fixed-field diagnostics show successful idle initialization and
matching process identity, but the host input-readiness check refuses; no model
request or positive writer-stopped receipt follows. The later
[large-metadata fix](../verification/provider-logins/claude-native-large-metadata-2026-10-29.json)
keeps oversized native JSON in the existing full-detail store while fitting its
compact projection. It passes 49 Linux race checks. The subsequent
[metadata compatibility check](../verification/provider-logins/claude-native-history-metadata-2026-10-29.json)
filters six source-qualified internal record types from displayed
history while retaining file contents, message identities and scan limits. Both
fresh Linux synthetic launch subcases now pass,
including owner echoes, completed receipts, correlated results, history, replay
protection and verified native-writer Stop. The proxy's 16-error buffer is full;
this finite flow does not qualify clean transport, kernel-level egress isolation,
real subscriptions or broader workflows. The complete changed-package
[race rerun](../verification/provider-logins/claude-chat-skeleton-regression-2026-10-29.json)
passes 2,253 tests, with 82 explicit skips and no race report.
A separate
[same-process synthetic probe](../verification/provider-logins/claude-same-process-token-adoption-2026-10-29.json)
demonstrates A1→B1→fresh A2 bearer adoption across three model requests in one
native task, preserving both Read results without a restart or another owner
message. It synthesizes one auxiliary title response and blocks two ancillary
connections. During B1, native account metadata still identifies A and no B
profile fetch is observed. Coherent account/policy handover, refresh races and
real subscription acceptance remain unqualified; the result does not activate
hot rebinding or automatic recovery.

The private [native refresh-slot canary](../verification/provider-logins/claude-native-refresh-slot-2026-10-29.json)
then passes one native refresh and A2→B1→saved A2 within one native process and task.
It proves a busy refusal while native refresh owns the locks, positive native
storage contention, byte-exact parking of the actual refreshed grant, preservation
of unrelated credential keys and positive cleanup. Native removed the seeded
unknown provider field; the writer preserved what native actually wrote. This
synthetic experiment uses minimal granted scopes and does not qualify MCP APIs,
two-client refresh overlap, production refresh ownership, account/policy coherence
or automatic quota recovery.

#### Lean stable-slot integration boundary

The single-client proof removes account-specific profiles and a chat interface
as mechanical prerequisites for healthy token adoption. After the two-native-client
overlap gate, the proposed Claude path uses one stable secure directory for all
new managed native births. Saved logins keep their own parked grants. This is one
provider-level selection and publication operation, without a per-discussion
account ledger. The existing opt-in chat work remains independent.

Reuse the login store's anchored readers, registry lock, durable replacement and
uncertain-write handling. Desired selection and actual slot ownership are distinct
durable facts. Fence both auth admission and delayed worker claim: the actual
slot owner cannot also be refreshed as a parked login, and an incoming login's
helper must settle and remain excluded throughout promotion. Record a bounded
publication intent before mutation. Legacy native processes are refresh writers
too: an account cannot enter the stable slot while any unresolved process can
refresh its inherited saved authority. Require positive retirement or a separately
qualified authority transfer for that account before promotion; unknown custody
refuses promotion without stopping unrelated healthy discussions. Under native
OAuth, legacy OAuth and storage
leases, recheck desired generation, ownership, grant hash and the two-second
budget; durably park the actual outgoing provider subtree before merging the
incoming subtree. Preserve unrelated native credentials. A crash between slot
publication and the registry receipt requires hash-based reconciliation with
refresh authority still fenced, never blind rollback or republishing.

Current-grant identity evidence is a separate proposed boundary from the native
helper auth receipt above. One read-only GET to the fixed native profile endpoint
can identify the snapshotted access token without granting another process refresh
authority. Use bounded normal HTTPS, no redirects, cookies, refresh, retries or
environment-selected proxy; do not log credentials, response bodies or provider
error text. Parse the exact account and organization fields and return only their
identity digest, the snapshot's grant hash and observation time. Obtain this
evidence outside registry/native locks and recheck its freshness and grant hash
under those locks before parking. A changed token, foreign identity or failed
lookup means unknown/refusal. This evidence does not replace enrollment, native
auth readiness, native policy enforcement or the existing helper receipt. Its
response interpretation and negative cases still require qualification.

New births need stable authority custody rather than the selected account's
identity/epoch as their immutable binding. Preserve final birth admission, CLI
identity, private settings, native history and exact Stop custody. Only after
publication, crash recovery and migration pass may the stable path delete
account-specific config projections and selection-epoch invalidation used solely
for isolation. Existing processes retain their inherited secure-storage paths;
keep their history aliases, writer inventory and retirement checks until migration
has positively retired them. No symlink or metadata edit can rebind those live
processes. This plan grants neither production activation nor automatic quota
continuation; account/policy coherence and safe interrupted-turn recovery remain
separate release gates.

The [pinned native login propagation study](../verification/provider-logins/claude-native-login-propagation-source-2026-10-29.json)
finds that normal login separately writes credentials and account configuration.
Siblings watch shared configuration and request identity reads that cache. The
credential-only canary did not exercise this path. Config installation is not
proof of every local-login cache invalidation or quota-wait effect, and the two
files are not one atomic native transaction. Qualification must use genuinely
native-written incoming account state and observe healthy siblings, policies and
waits; this source result does not authorize copying account fields or bypassing
the existing account-state migration rule.

A2 adds passive quarantine for the exact managed metadata schema 6 plus
`claude-chat` discriminator pair; ordinary schema 5 sessions retain their
existing behavior. Retained running or ended chat discussions are fenced from
the old terminal paths for raw attach, input, resize, drain and automatic
recovery. The old production consumer passed four synthetic Claim/Start cases;
this is not native qualification. At that quarantine checkpoint, chat launch,
stdio shim, UI and capabilities were inactive. See the
[checkpoint evidence](../verification/provider-logins/claude-chat-quarantine-2026-10-27.json).

The earlier constructor checkpoint was development-only: one shim-owned stdio primary, bounded
R3 capture, real config/metadata binding, replay UUID, and signal/Stop/EOF cleanup.
At that checkpoint, message sending, history control, decisions, recovery and
public routing were inactive. This was not native or release qualification. The
[constructor evidence](../verification/provider-logins/claude-chat-constructor-2026-10-27.json)
records passing focused and ordinary regression checks. These checks did not
close the overall release gates; Beads records their current status.

The earlier development slice connected that fake primary to the existing TUI
chat view, one durable semantic owner message, exact native-style echo/result,
bounded history and owned Stop. Reader publication, execution revision and
process identity fence input and evidence; Stop completes while the test holds
persistence Sync returns. Any different message operation refuses in this one-turn fixture.
Workflow custody remains explicitly incomplete. Managed/native/public launch,
native decisions, runtime restart/rejoin, ordinary subsequent turns and automatic
continuation remain unavailable. See the
[thin-message evidence](../verification/provider-logins/claude-chat-message-2026-10-27.json)
for passing focused checks, deliberate source-removal controls and the failed
broader regression run; this is an in-progress checkpoint.

The storage-only prerequisite permits strict development `fake6`/`claude-chat`
metadata for structured child tests. Ordinary `fake1` and managed schema 5/6
remain supported; saved `fake6` mode is immutable, and malformed or mixed rows
are retained and refused. This prerequisite does not grant managed-login, CLI, retirement,
native-writer-proof, OpenCode-binding or input-embargo authority, and does not
enable managed/native/public launch admission, public chat/UI or capabilities.
The passing storage checks are recorded in
[`claude-chat-fixture-storage-2026-10-27.json`](../verification/provider-logins/claude-chat-fixture-storage-2026-10-27.json).

Use one shim-owned structured Claude primary. Reuse selected-login admission,
profile custody, the authenticated shim connection, composer lane, operation
journal, hook spool, execution record, native request/decision handlers and
anchored transcript history. Do not create a Codex-style backend or a second
transcript store. Native request decisions require their own positive replay and
correlation gates; unsupported requests stay visible and unanswered.

The reviewed native Exit A implementation first shares the existing managed
birth fence between PTY and stdio launch: held image, final selected-profile
admission, exact reservation and one Wait. Register child containment before
publication can block on storage. At that prerequisite checkpoint native chat admission stayed
disabled. Linux 2.1.310 is the first target; macOS remains a separate gate.

Native readiness comes from an exact initialize control response bound to the
owned process, version and human-request state. Native `system/init` can arrive
after the first user message. Consume the actual replay, assistant, tool and
result shapes: an assistant frame UUID differs from its message ID, and a result
with subtype `success` can still have `is_error=true`. An own UUID/text replay
proves admission only. Keep hook-origin and primary-origin envelopes distinct,
with one spool producer and separate spool and primary-stream sequences.

After request validity and identity checks, persist each valid prepared or
queued owner intent before FIFO waiting or native delivery readiness. That
revision revokes older automatic authority even if delivery waits or refuses;
never roll it back. An initialized conversation stops qualifying for pristine
empty history once such an intent exists. Read history through the existing
anchored profile resolver; missing history after admission is unavailable.

Managed owner Stop needs a narrow containment path independent of the consumer,
engine, coordinator queue and persistence writers. Retain exact managed
reservation, session/conversation, profile, shim birth and scope authority; reuse
the negotiated authenticated control socket with one absolute operation deadline.
Linearize Stop against Start and provider writes, including a child born while
publication sync is blocked. Missing or mismatched proof sends no signal and
returns unconfirmed. Delivery proves neither death nor retirement, release or
successor authority. Tests must hold actual execution/semantic sync and
coordinator/store access while proving containment and no late input. The
existing coordinator `OwnerEnd` and core `Kill` paths alone can block before
signaling and do not establish this contract.

The hook transport now bounds complete DRAIN replies before application. DRAIN
is unframed JSON, with a 64 MiB encoded-response cap independent of
`wire.MaxFrame`; default JSON escaping can expand the 8 MiB production spool
sixfold. Reads are bounded before parsing and compaction. Incarnation and
producer-certificate metadata are limited to 1,024 and 2,048 bytes. An invalid or
truncated response cannot advance cursor, certificate or gap state. DRAIN refuses
custom spools above 8 MiB while retaining their bytes; general custom spool APIs
and the existing 64 MiB stopped-cleanup inspection bound remain unchanged.
This prerequisite does not activate the chat primary or qualify native behavior.

Deliver the connected exits test-first. **A** proves fresh launch through shim,
owner protocol, one message/echo/result, history, and authenticated Stop/cleanup;
negative origin, identity, write, storage and overflow cases must still allow
Stop. Root qualifies the exact native user-UUID echo and finite flow before
enabling it. **B** adds request-bound decisions only after separate native
decision-replay and question-answer-correlation gates. **C** adds known actor
retention, bounded history, reconnect and restart through the same shim; unknown
workflow activity remains incomplete. Sol and Luna own their reviewed seams;
Astra reviews each package and Root runs serial RED/GREEN and quality gates.

Automatic recovery/continuation and workflow completeness remain excluded at
every exit and require separate positive native gates. Qualify Linux Claude
2.1.310 first and macOS separately. Existing native feasibility evidence does
not qualify this connected implementation or activate the runtime.

## Implementation phases and exit contracts

| Phase | Work and principal seams | Evidence required before dependent work |
|---|---|---|
| Native capability qualification | Reusable isolated native probes beside existing verification tooling; Codex refresh owner, built-in route/WS and request adoption; Claude profile, fresh-auth, stable-store loop and continuation. | Actual installed binaries, synthetic loopback first, then owned Linux native canaries. Preserve tool identities/results, auth identity and human-decision state. Failed hypotheses revise this contract. |
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
