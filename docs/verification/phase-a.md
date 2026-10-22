---
title: Phase A verification — Codex discovery, backend/shim resilience and journal retention
entity: verification-phase-a
type: verification
tags: [codex, journal, verification, adr-014, adr-010]
related: [adr-014, adr-010]
---

# Phase A verification

Date: 2026-10-14. Branch `fix/2pk-phase-a`, from main `e538d1e9`. Evidence logs are kept in the
lab directory `swarm-lab/impl/` (`2pk.3`, `2pk.4`, and independent final-review fixes).

## Scope

- **2pk.3 (A5 to A7).** The Codex model scan skips oversized non-`turn_context` lines. Every
  Codex app-server runs in the session cwd (ADR-010 amendment 5). A refused socket dial names the
  sandbox.
- **2pk.4.** Journal retention is bounded (ADR-014 A12). Backend readiness has a budget. The shim
  reaps orphans. Resume history degrades when the paginated rollout is missing. Test binaries
  sweep leaked helpers (B94 allowlist).
- **Independent final-review fixes.** `journal.Open` unlinks retained empty segments except the
  newest. Only a launch-dial failure writes the `backend_never_served` degraded marker, and only
  that marker widens the Codex resume windows.

## Test-first evidence

Each item has a test commit before its fix commit. The red and green logs are retained under
`swarm-lab/impl/`.

| Item | Red tests (abridged) | Evidence |
|------|----------------------|----------|
| 2pk.3 | `TestReadCodexTranscriptModel_SkipsAToolOutputLineLongerThanTheLineCap`, `TestNativeProjectionKeepsTheOwnerSandboxNetworkOverride`, `TestBackendLaunch_*`, `TestDispatchAgentVerb_APermissionRefusedDialDoesNotBlameAMissingDaemon` | `impl/2pk.3/{red,green}.txt` |
| 2pk.4 | `TestOpen_MaxTotalBytesDropsTheOldestSegmentsBeforeLoading`, `TestBackendReady_*`, `TestManagedShimReapsAnAdoptedOrphanWhileTheSessionRuns`, `TestResumeHistory_CodexMatchesADegradedLaunchStartedAfterTheBackendBudget`, `TestB94_EveryExportedSymbolIsReachableFromProduction` | `impl/2pk.4/{red,green}.txt` |
| Journal/backend review | `TestOpen_UnlinksRetainedEmptySegmentsExceptTheNewest`, `TestBackendNeverServed_OnlyTheLaunchDialFailureIsDegradedStartEvidence` | `impl/phase-a/{red,green}.txt` |

## Final gates at the historical checkout

The gates ran on the integrated tree with `GOFLAGS=-buildvcs=false` and inherited `SWARM_*` session
variables unset. Results are recorded in `impl/phase-a/gates.txt` and `gates-final.txt`.

- `go build ./...` and `go vet ./...` passed. `golangci-lint run` reported 0 issues.
- `go test ./...` passed in every package.
- Race runs reported an intermittent `Serve`/`newCoreAPI` field-assignment race in skeleton; it
  was unchanged from that checkout's main branch. A load-sensitive skeleton test and a daemon
  survival test passed their repeated standalone runs.
- `cmd/swarm` and hook-client tests failed when live `SWARM_*` session variables were inherited.
  A TUI launch-field test also fails when the checkout path contains "agent".

The original evidence also recorded a live VT emulator panic; it was unrelated to these changes.
