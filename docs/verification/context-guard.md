# ContextGuard implementation evidence

**Date:** 2026-08-31

**Decision:** [ADR-023](../adr/ADR-023-context-guard-auto-compaction.md)

**Status:** settings, exact Codex observation, lifecycle safety, TUI, sanitized owner observability, AND the automatic dispatch lane (ADR-023 amendment 1) implemented. Dispatch is enforced by the daemon: composer-lane serialization, queue-head quiet revalidation, the unattended rule, and the durable executing record at the provider write boundary. Opt-in and disabled by default, unchanged.

## Shipped contract

- Daemon-global, owner-only opt-in; disabled by default; remembered default threshold 80; accepted range 40–95 inclusive.
- Versioned CAS settings document and instance-bound lifecycle sidecar use atomic `0600` writes and directory sync. Telemetry is never persisted.
- Codex occupancy is only `tokenUsage.last.totalTokens / modelContextWindow`; cumulative `tokenUsage.total` is forbidden as the numerator.
- Every sample is fenced by session id, session instance, backend epoch, provider thread, settings revision, source sequence, and capture time. Callbacks only bound/copy/queue and never parse, fsync, or call the provider.
- Owner rosters receive only integer percentage, support, phase, stable last result, and stable error code. Remote rosters and unsupported harnesses receive no field.
- The reducer's at-most-once write lifecycle is DRIVEN in production since ADR-023 amendment 1: `RequestDispatch` promotes through the composer lane, and Codex advertises `AutomaticDispatch=true` / `automatic` for the characterized 0.150.x/0.151.x families as a capability claim (the daemon owns every concurrency guarantee -- see the live gate results below).

## Provider characterization

The installed binary reported `codex-cli 0.150.1`. The experimental protocol schema was generated with:

```text
codex app-server generate-json-schema --out /private/tmp/swarm-contextguard-schema --experimental
```

It characterizes `thread/tokenUsage/updated`, `item/started` and `item/completed` with `contextCompaction`, and `thread/compact/start`. Token counts and the model window are signed `int64` schema values; negative and above-`MaxInt64` values are refused.

A real local app-server initialize handshake, run against an isolated temporary `CODEX_HOME` and without starting a model turn, returned the sanitized shape checked in at `internal/skeleton/testdata/contextguard-initialize-0.150.1.json`. The running server identifies its version in `initialize.result.userAgent`; ContextGuard uses that spawn-bound value, not a later PATH probe. Adapter fixtures are sanitized schema shapes rather than claims about a live automatic compaction response.

## Implementation map

| Concern | Location |
|---|---|
| settings/CAS | `internal/skeleton/contextguard_settings.go` |
| owner protocol/client | `internal/protocol/` |
| options menu | `internal/tui/options.go` |
| pure policy reducer | `internal/contextguard/` |
| optional adapter contract | `internal/adapter/contextguard.go` |
| strict Codex parser/action descriptor | `internal/adapter/codex/contextguard.go` |
| feed provenance, worker, recovery, view | `internal/skeleton/contextguard.go` |
| architectural contract | `docs/adr/ADR-023-context-guard-auto-compaction.md` |

## TDD and adversarial review

Focused tests cover bounds 39/40/95/96; exact threshold arithmetic and overflow; duplicate/malformed/oversized provider JSON; cumulative-total rejection; stale backend/session/settings provenance; pre-registration lifecycle buffering and overflow hold; blocked persistence; recovery and no-telemetry sidecars; replacement during fsync; late old-backend closure; remote omission; capability skew; CAS conflicts; and transactional menu apply.

Three independent review roles audited architecture, policy/concurrency/durability, and protocol/TUI compatibility. Findings drove feed buffering, capture-time settings revisions, close-before-restore replacement, instance-publication serialization, exact watcher fencing, stable last-result observability, and transactional layout/settings apply.

## Release gates

The exact final candidate produced these results on 2026-08-31. Swarm hook/session
environment variables were unset so the suite could not feed the session running the
verification itself.

| Gate | Result |
|---|---|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `/Users/Nathan/go/bin/golangci-lint run ./...` | PASS (`0 issues`) |
| focused ContextGuard race suite | PASS |
| `go test -race -count=1 ./internal/skeleton ./internal/protocol ./internal/tui ./internal/adapter/codex ./internal/contextguard` | PASS |
| `go test -count=1 ./...` | BLOCKED outside the feature diff; every ContextGuard/touched package passed |

The repository-wide run has three pre-existing failures: Android gates
`TestR4D3R3_ASecondAddWhileOneIsRunningIsRefusedOutLoud` and
`TestS25_EveryWaitingVerbIsDispatchedOrLedgered`, plus the stale
`internal/skeleton.Daemon.SocketPath` allowance reported by
`TestB94_EveryExportedSymbolIsReachableFromProduction`. This branch changes neither
`android/` nor `internal/verify/` relative to base `2f69bc89`; it does not waive or modify
those blockers. A repository-wide race result is therefore not claimed. The complete
feature-owned surface is green under the race detector.

## Deliberately unproven / unavailable

No automatic compaction is published yet. Two Codex gates still require real-provider evidence: a concurrent turn must not be cancelled/replaced by compaction, and a concurrent native/manual compaction must not combine with Swarm into two compactions. Until both pass, enabling the setting observes/latches pressure but emits no provider request. Claude, OpenCode, AGY, and Hermes remain unavailable pending their own exact telemetry, action, completion, and concurrency contracts.

## 2026-09-01 live gate results: BOTH GATES FAIL on codex 0.151.0

Method: a throwaway swarm codex session (thread `01a05ce6-7524-7642-9833-beae667c99a7`,
app-server `codex-cli 0.151.0`), a second WebSocket client on its per-session socket
(the `internal/appserver` dial + recorded initialize/initialized handshake), and raw
`thread/compact/start {threadId}` requests observed against the notification stream
and the TUI screen.

- **Gate 1 (concurrent turn) FAILS.** With a turn ACTIVE (a running 45-second exec
  tool call), `thread/compact/start` was accepted immediately (`{}` in 1ms — no
  refusal, no queueing) and CANCELLED the turn: the TUI printed "Conversation
  interrupted", the sandboxed command was killed mid-run and never completed, then
  "Context compacted". The provider offers no turn-vs-compaction serialization.
- **Gate 2 (double compaction) FAILS.** Two concurrent `thread/compact/start` at
  idle were BOTH accepted (`{}`, `{}`; the second returned no error) and the second
  INTERRUPTED THE FIRST MID-COMPACTION: the screen reads "Context compacted" /
  "Conversation interrupted" / "Context compacted", and `thread/status/changed`
  shows two back-to-back active/idle compaction cycles. There is no provider-side
  dedup, refusal, or serialization of compaction against compaction.

Consequence: D7's caution is not conservatism but fact. Any automatic dispatch must
rely ENTIRELY on Swarm-side enforced serialization (D5/D6), and even a perfect lane
retains a check-to-write residue in which a just-started turn — or a just-started
manual `/compact` — is destroyed, precisely in the near-full-context sessions where
the guard fires. ADR-023 amendment 1 records the owner decision that followed: the
lane enforces the serialization for everything the daemon originates, ATTENDED
sessions are never auto-compacted (the attach-typing race is the one the lane cannot
order), and the millisecond attach-plus-submit residue is accepted. These results
remain the recorded evidence the gates demanded.

## 2026-09-01 adversarial audit round (dispatch milestone)

Two independent adversarial reviews of the dispatch implementation (a
cross-model reviewer and a same-model auditor briefed to break the reducer
contract and the lane story) converged on one structural finding: the composer
lane orders WRITES, but a compaction's EFFECT outlives its write by seconds,
and nothing gated daemon-originated input during that window — a queued phone
send could land seconds into a live compaction (uncharacterized turn-vs-compact
territory), and the supervisor's `SendInput` never used the lane at all. The
fix round that followed, all verified in code before implementation:

- **Effect-window gate**: `composer_send` returns the retryable `input_busy`
  and the supervisor defers while the guard is in
  executing/awaiting_confirmation/provider_compacting.
- **Confirmation deadline**: awaiting/provider_compacting without lifecycle
  completion becomes `outcome_unknown_hold` after 5 minutes (reducer gained the
  provider_compacting exit) — no silent wedge, and the composer gate is
  bounded by it.
- **Evidence veto**: the dispatch re-checks the guard's own queue at the lane
  head and again inside the write-boundary callback; a compaction item, a
  settings change, or a trailing usage frame that arrived during the lane wait
  refuses the write with provably no bytes. A queued disable also refuses via
  a settings-revision comparison at the boundary.
- **Fence narrowed**: automatic dispatch only for 0.151.x (the live-gated
  family); 0.150.x is schema-characterized only and stays observe-only.
- **Lifecycle hygiene**: the dispatch context dies with the worker (close never
  waits out a provider reply), the reconcile window no longer permanently
  deafens a feed, an overflow loss no longer discards a queued settings edge,
  the pending bound is sized for a parked worker (256), and the observe-only
  view code `action_unverified` is not stamped on automatic guards.

Each item is pinned by a test in `internal/skeleton/contextguard_dispatch_test.go`
(evidence veto, queued disable, revision mismatch at the boundary, deadline
hold, in-flight gate lifecycle, restore-from-executing sidecar, stop-while-
queued ticket handback, prompt close during reply wait, loss-retains-config,
reconcile-window feed survival) or `internal/contextguard` (the
provider_compacting exit and its rejection everywhere else).

The cross-model reviewer's round (received after the first fix round) added and
the second fix round closed:

- **Typed-input choke point gated**: the effect-window gate moved into
  `sendMessage` itself via `Server.SetInputGateFunc`, covering `swarm send` and
  supervisor notifications in one place, refusing before the first byte
  (pinned at the protocol layer).
- **Write-boundary atomicity**: the appserver client now runs `beforeWrite`
  and the request write under one write lock — no approval response or
  notification on the same connection can interpose between the durable
  executing record and the compact bytes; a closed connection is detected
  before `beforeWrite`, making it a clean retryable refusal.
- **Stop-barrier and identity revalidation**: the dispatch captures its lane
  ticket's admitted barrier (a Stop admitted while queued vetoes), re-checks
  `s.stop` after winning the ticket (the select tie), and re-proves the
  registration's backend identity (`isCurrent`) at the queue head and inside
  the write boundary.
- **Stalled reply cannot discard a conclusive completion**: the reply-failure
  path drains queued lifecycle evidence first; only a machine still waiting on
  the write's outcome holds (a queued completion latches).
- **Exact-version allowlist**: automatic dispatch requires the exact live-gated
  version (today 0.151.0); every other characterized version is observe-only.
  Extending the allowlist requires schema regeneration AND a live-gate rerun.
- **Normative docs reconciled**: system-spec C-5, invariant S13, and CG.5 now
  record the negative gates and the daemon-side enforcement instead of the
  stale observe-only constraint.

Accepted residuals, recorded rather than closed: attached-PTY typing (attended
sessions never dispatch; a human acting inside the notification-latency window
is the owner-accepted race), Stop during a compaction (human intent; bounded by
the confirmation deadline), and approval resolution (requires a pending
approval, which already blocks dispatch at the queue head).

## Continuation (ADR-023 amendment 2)

Implemented against the live-verified provider semantics in
`docs/design/context-guard-continuation.md`: the guard's own compact arms a
one-shot continuation at the write boundary; the worker enqueues codex's
native `thread/queue/add` on reaching `provider_compacting` (or `latched`,
where the idle thread starts it directly); attended sessions are skipped;
failure or ambiguity forfeits rather than retries; native/manual compactions
never continue. Pinned by
`TestContextGuardContinuationRidesTheProviderQueue` (shape, prompt,
once-per-cycle, second cycle), `...AtLatchWhenCompletionArrivesFirst`,
`...NeverContinuesACompactionItDidNotDispatch`,
`...SkipsAttendedSessions`, `...FailureIsForfeitNotRetry`,
`...ForfeitsOnHold` (skeleton) and
`TestContextGuardContinuationShapeAndFence` (codex adapter, exact-version
fence + degenerate identity).
