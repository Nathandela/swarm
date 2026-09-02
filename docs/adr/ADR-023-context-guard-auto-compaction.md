# ADR-023: ContextGuard auto-compaction

## Status

Accepted

## Date

2026-08-31

## Context

Long-running agent sessions can exhaust their model context before a user notices. Some
harnesses expose a manual compaction command or a native compaction operation, but their
telemetry, action semantics, and lifecycle evidence differ. Swarm must not infer context
pressure from terminal text, replace a harness-owned setting, type into a dirty prompt, or
repeat a non-idempotent compaction after a crash.

The feature is an owner guardrail, not a replacement for a harness's own compaction policy.
It is configured from the session-board options window but must keep working when that TUI is
closed, so the assembled daemon owns it.

## Decision

### D1. Product contract

Auto-compaction is daemon-global, opt-in, and disabled by default. Its remembered integer
threshold defaults to 80 and is valid from 40 through 95 inclusive. Reaching the threshold
triggers (`used >= threshold`). A completed or otherwise observed compaction latches the
crossing; the guard re-arms only after fresh usage falls to the threshold captured for that
crossing minus ten percentage points.

Swarm changes no harness launch option, native auto-compaction setting, status-line setting,
or harness configuration file. Unsupported or untrusted telemetry causes no provider action.

### D2. Ownership and extension boundaries

Core owns settings, provider I/O, workers, persistence, timers, policy, and observability.
Adapters remain pure and stateless and may expose optional context-event parsers and action
descriptors; the frozen `adapter.Adapter` interface is not widened. The protocol's frozen
`DaemonAPI` is not widened: settings are exposed through a feature-specific optional backend.

### D3. Settings and protocol

`<daemon-state-dir>/context-guard-settings.json` is a versioned, owner-only `0600` document
containing a revision, enabled state, and threshold. A set is compare-and-swap: validate,
write and sync a temporary file, rename and sync the parent directory, then publish memory.
Missing settings mean disabled/80/revision 0. Corrupt or future settings disable the guard
without preventing daemon startup and are never silently overwritten.

The additive protocol capability is `context-guard-settings`; owner operations are
`context_guard_get` and `context_guard_set`. Remote-tier connections are refused before the
request body or backend is consulted. Protocol version 1 does not change.

### D4. Exact, provenance-bound observations

An actionable observation carries session id, session instance, backend connection epoch,
provider thread id, settings revision, local ingest sequence, observation time, used tokens,
and a positive provider-authored context limit. It must be exact, current for the complete
identity, observed after the active settings revision, and at most 30 seconds old at dispatch.
Threshold comparisons use overflow-safe integer token arithmetic, never a rounded display
percentage. Samples are not persisted.

Callbacks only copy state and wake a per-session worker. Usage samples may coalesce to the
latest value; compaction lifecycle, settings, replacement, backend-loss, and input-generation
edges may not disappear. Detected edge loss enters a no-dispatch hold.

### D5. At-most-once lifecycle

Each session directory may carry a low-frequency `context-guard-state.json`, bound to its
session instance. A dispatch is prepared and revalidated in the shared per-session semantic
action lane. Immediately before provider bytes are written, the guard durably records
`executing` through the existing provider write-boundary callback. `executing` or
`awaiting_confirmation` recovered after a daemon crash becomes `outcome_unknown_hold` and is
never automatically resent. Corrupt lifecycle state blocks automation. A new session instance
starts a new cycle; a new backend connection invalidates samples but cannot clear an unresolved
action.

Only a characterized response that proves no side effect is retryable, and only after a new
telemetry/idle edge. Timeout, EOF, connection loss, or process death after the write boundary is
an unknown outcome. Safety wins over liveness.

### D6. Shared semantic action lane

Automatic compaction is ordered with prompt/steer, Stop, approval resolution, terminal input,
and manual/native compaction. At the queue head it revalidates configuration, full provider
identity, fresh usage, `process=running`, `turn=idle`, `interaction=none`, no pending approval,
no uncertain operation, an unchanged input generation, and a live matching sink. Work observed
before the write boundary wins and cancels a pending compaction. Once the boundary is crossed,
the request is tracked but never blindly repeated.

### D7. Harness rollout

Harness action support is independently version-fenced.

- Codex is observed from `thread/tokenUsage/updated`, using
  `tokenUsage.last.totalTokens / modelContextWindow`; lifetime cumulative totals are forbidden.
  Its native action is `thread/compact/start`. Production dispatch remains disabled until an
  authoritative contract or enforceable serialization plus adversarial evidence proves both a
  concurrent turn versus compaction and a concurrent manual/native compaction versus Swarm
  cannot cancel work or compact twice. Until then it is observe-only.
- Claude is unavailable until an authoritative, non-invasive numerator and runtime context
  limit are proven, along with strict transactional submission and hook/race gates. Swarm does
  not set `--autocompact` or `statusLine`.
- OpenCode is unavailable pending an authenticated, session-bound HTTP/SSE backend and validated
  occupancy/concurrency semantics.
- AGY and Hermes remain unavailable pending stable telemetry, action, and completion contracts.

### D8. Observability

An optional additive `SessionView` body exposes sanitized support, integer usage percentage,
phase, last result, and stable error code. Supported background mutations may not be silent.
Roster publication coalesces by displayed percentage and phase; transcript content, paths, and
raw provider errors never cross this surface or enter logs.

## Consequences

### Positive

- The guard survives TUI closure and daemon restart without repeating an ambiguous action.
- Harness configuration remains wholly user-owned.
- Provider upgrades and stale connections fail closed.
- Pure policy and adapter parsers can be exhaustively unit-, fuzz-, race-, and fault-tested.
- Additive protocol fields preserve old-client/new-daemon compatibility.

### Negative

- The first release may be observe-only if Codex cannot prove safe concurrent semantics.
- A crash immediately before a provider write can conservatively suppress a compaction that did
  not actually occur.
- Settings, lifecycle, provider provenance, action coordination, and roster observability add
  more machinery than terminal-command injection.
- Unsupported harnesses receive no best-effort fallback.

## Amendment 1 (2026-09-01): the gates were run, both failed, and dispatch ships on the daemon's own serialization

D7's two Codex gates were put to a live provider (`codex-cli 0.151.0`, a second
app-server client on a real session's socket; evidence in
`docs/verification/context-guard.md`):

- a `thread/compact/start` sent mid-turn is accepted instantly and **cancels the
  running turn**;
- two concurrent compacts are both accepted, and the **second interrupts the
  first mid-compaction**.

The provider serializes nothing, and waiting for it to start would park the
feature indefinitely. The owner decision (2026-09-01) is to ship automatic
dispatch under the daemon's OWN enforcement, which D5/D6 already specified.
The daemon's serialization has two layers, because a compaction has two
windows: the WRITE (one request) and the EFFECT (the seconds the provider
spends compacting after the reply):

1. **The composer lane orders the write.** A dispatch enters the session's
   per-session semantic lane, FIFO with every daemon-driven composer send and
   Stop, and revalidates at the queue head: quiet status, no unresolved
   composer outcome, and — because veto evidence can arrive DURING the lane
   wait — nothing pending in the guard's own queue (a compaction item from a
   native or manual compact, a settings change, a trailing usage frame). The
   same evidence-and-revision check runs once more inside the write-boundary
   callback, where a refusal provably precedes any bytes.
2. **The effect window is gated, not laned.** From the durable `executing`
   record until the compaction is confirmed, held, or latched, the session
   refuses daemon-originated input: a `composer_send` returns the retryable
   `input_busy` (nothing written; the same words a moment later land), the
   typed-input choke point itself (`sendMessage`, serving `swarm send` and
   supervisor notifications alike) refuses before the first byte, and the
   supervisor additionally treats the session as unsafe and retries later. The
   lane ticket itself is released at the write boundary — holding it through
   the reply would stall every phone send behind a wedged provider. The
   supervisor's `SendInput` never used the lane at all, which is why the gate,
   not the lane, is the serialization of record for effects. The write
   boundary itself is atomic against every other frame on the connection: the
   provider client runs `beforeWrite` and the write under one write lock, so
   no approval response or notification can interpose between the durable
   executing record and the compact bytes it covers.
3. **The effect window is bounded.** The reply to `thread/compact/start`
   proves nothing; confirmation comes from the provider's own compaction
   lifecycle events. If they never arrive (the compaction was interrupted, or
   a provider patch changed the item shape), a confirmation deadline converts
   the wait into `outcome_unknown_hold` — an honest hold, never a resend, and
   the composer gate releases with it. A wedge cannot outlive the deadline.
4. **Attended sessions are never auto-compacted.** The one input the daemon
   cannot order is a human typing in the attached PTY — exactly the race the
   gate evidence prices at one destroyed turn. Dispatch requires the session
   to be UNATTENDED (no controller lease, no recent phone activity) as well as
   quiet: whoever holds the controls can `/compact` themselves; the guard
   exists for the unattended fleet. The residual races are (a) an attach plus
   a submitted prompt inside the milliseconds between the write-boundary check
   and the provider processing the write, and (b) a manual compact whose
   lifecycle notification has not yet crossed the socket when the write goes
   out — both accepted by the owner as negligible after the evidence veto
   above, which closes every window in which the daemon has the evidence.
5. **The write boundary is durable.** The `executing` record is persisted
   inside the provider client's write-boundary callback: a refused or
   unpersistable transition aborts with provably no bytes; once bytes may have
   left, every failure — timeout, transport loss, even a typed provider
   error — is an unknown outcome and a durable hold, never a resend (D5
   unchanged).
6. **`AutomaticDispatch` in the adapter is a capability claim only**, and it is
   granted solely to the EXACT versions live-gated against a real provider —
   an explicit allowlist, today `0.151.0` (2026-09-01). Compaction is
   destructive and non-idempotent, so even a patch release is not trusted
   sight-unseen: extending the allowlist requires regenerating and comparing
   that version's schema AND re-running the live gates. Every other version in
   a characterized family (0.150.x entirely, non-allowlisted 0.151.x patches)
   stays observe-only; an uncharacterized version downgrades the guard to
   unsupported. `Support` gains the value `automatic`; the observe-only view
   code `action_unverified` is NOT stamped on an automatic guard, whose phase
   and last result are the story.

The product contract (D1) is unchanged: daemon-global, owner-only, **opt-in and
disabled by default**, threshold 40–95, latch and re-arm as specified.

Known limitation, accepted for this milestone: a hold (`outcome_unknown_hold`,
`event_loss_hold`) or corrupt state is cleared only by a genuinely new session
instance. There is no owner-facing acknowledge-and-rearm operation yet; one
transient unknown outcome retires the guard for that session instance.

## Amendment 2 (2026-09-01): the compaction continues the task

An automatic compaction on an unattended working session must not strand the
flow: the owner's intent is context maintenance mid-task, not a stop. Two
mechanisms were live-verified (`docs/design/context-guard-continuation.md`).
Codex's native `thread/queue/add` defers behind a running compaction and
auto-starts afterwards — but a queued message is UNREVOKABLE while the
compaction runs, so a human attaching or stopping in that ~20-second window
would still receive the turn; the adversarial review rejected it. The shipped
mechanism is daemon-side:

1. **Arms a continuation only for its own compact**, at the write boundary. A
   compaction the daemon did not write never earns one.
2. **Sends an ordinary `turn/start` at `latched` only** — never while the
   compaction runs (a turn mid-compaction cancels it; gate evidence). The
   attempt is bounded by a two-minute freshness window from observing the
   completed compaction (stale maintenance is not a message for later) and
   rides the composer lane with the dispatch's own last-instant
   revalidation: unattended, quiet, Stop barrier unchanged, no uncertain
   composer outcome, backend identity current, worker not stopping. Any
   failed check forfeits.
3. **One shot, ever, per cycle.** The armed flag is consumed by the single
   attempt; a send failure or ambiguity forfeits (a stalled task the owner
   can nudge beats a duplicated surprise turn); every hold transition disarms
   immediately; and a crash forfeits structurally — recovery holds, and a
   held cycle can never re-observe the arming edge, so at-most-once needs no
   durable marker.
4. **The prompt is daemon-authored and fixed**: it names the compaction as
   automatic mid-task maintenance, instructs the agent to continue exactly
   where it left off, and tells an already-finished session to say so briefly
   rather than start new work.

Accepted residual: a manual/native compaction racing the daemon's own write
can merge into one latch and receive the continuation — the manual compactor
is attached or phone-active and therefore forfeits via the unattended rule in
every reachable case; a foreign API client on the same socket is outside the
product's threat model, as with the dispatch itself.

The continuation is integral to automatic dispatch in this version — no
separate setting — and rides the same exact-version allowlist. `compact_prompt`
(codex's own summarizer-prompt config) stays user-owned per C-4; owners who
want every compaction framed can set it at launch themselves.

## Alternatives Considered

- **Type `/compact` into every PTY.** Rejected: provider commands differ and input can be dirty,
  active, or awaiting approval.
- **Use harness-native auto-compaction settings.** Rejected: this would override user-owned
  harness policy rather than add a Swarm guardrail.
- **Infer usage from terminal percentages or token text.** Rejected: those values are not stable
  cross-provider active-context contracts.
- **Retry after transport ambiguity.** Rejected: compaction is non-idempotent and can destroy the
  context the retry is meant to protect.
- **Persist every usage sample.** Rejected: it creates disk and roster churn while persisted
  samples are intentionally ineligible after restart.
