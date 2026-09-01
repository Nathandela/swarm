# ContextGuard implementation evidence

**Date:** 2026-08-31

**Decision:** [ADR-023](../adr/ADR-023-context-guard-auto-compaction.md)

**Status:** settings, exact Codex observation, lifecycle safety, TUI, and sanitized owner observability implemented; automatic provider dispatch intentionally gated off.

## Shipped contract

- Daemon-global, owner-only opt-in; disabled by default; remembered default threshold 80; accepted range 40–95 inclusive.
- Versioned CAS settings document and instance-bound lifecycle sidecar use atomic `0600` writes and directory sync. Telemetry is never persisted.
- Codex occupancy is only `tokenUsage.last.totalTokens / modelContextWindow`; cumulative `tokenUsage.total` is forbidden as the numerator.
- Every sample is fenced by session id, session instance, backend epoch, provider thread, settings revision, source sequence, and capture time. Callbacks only bound/copy/queue and never parse, fsync, or call the provider.
- Owner rosters receive only integer percentage, support, phase, stable last result, and stable error code. Remote rosters and unsupported harnesses receive no field.
- The reducer contains the future at-most-once write lifecycle, but production ignores `RequestDispatch` and Codex advertises `AutomaticDispatch=false` / `observe_only`.

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
the guard fires. Production dispatch stays disabled; these results are the recorded
evidence the gates demanded, and they came back negative.
