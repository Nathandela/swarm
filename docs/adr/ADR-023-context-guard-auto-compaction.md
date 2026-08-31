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
