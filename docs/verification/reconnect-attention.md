# Reconnection and attention verification

Date: 2026-10-10. Bead: `agents-tracker-2hhp`. Baseline: main `686383ebb96433e549cec5ea35d9375a26635580`. Release target: v0.17.2 after existing v0.17.1 tag, rechecked before publication.

Implementation follows [the detailed plan](../specifications/reconnect-attention-plan.md) and [ADR-030](../adr/ADR-030-reconnection-and-attention.md). Only automatic terminal recovery and structured attention are in scope.

## Review

Astra plan review required authoritative roster refresh instead of queued-event replay, request-ID correlation, shared cancellation/upgrade ownership, stale-action guards, deterministic concurrent waits and explicit failure lifetime. Each requirement is reflected in the plan and regression tests.

A separate Astra implementation reviewer found and verified fixes for original-envelope admission, wait snapshots erasing live request correlation, form stale visibility, discarded banner timers, settings revision reload, Accounts-to-Options return and authoritative restoration of temporarily absent rows and failed initial subscription signaling. Its final verdict: **Approved for merge after required Linux CI passes.** Independent focused race checks passed, including three repeated runs of final recovery regressions; `git diff --check` passed.

## Failing-first evidence

[Captured red transcripts](reconnect-attention-red.md) show absent question/error normalization, failed status derivation, premature/incomplete recovery, retry exhaustion, leaked candidates, stale payload regression, overlapping native request resolution, ambiguous transport envelopes, invisible stale forms, old Lists undoing completed rename, nonexpiring hydrated banners and stale settings. Follow-up tests cover the corresponding corrected behavior.

Sticky-error lifetime and additive compatibility tests were added after implementation and started green; they are regression coverage, not claimed failing-first evidence. Existing exact board/ANSI goldens now pin the requested `approval` label and banner. The Accounts reconnect test expects a new coalesced channel rather than the provider's raw event channel.

## Local checks

Pinned native Go 1.25.0 and golangci-lint 2.12.2 ran on Darwin arm64. Pinned gomobile/gobind came from android/toolchain.env. Tool caches and test temporary roots were confined to /private/tmp.

| Check | Result |
|---|---|
| `go build ./...` | Pass |
| `go vet ./...` | Pass after portable test-helper extraction |
| `golangci-lint run` | Pass |
| Full `-race` suites for TUI, app-server, Codex adapter, status and engine | Pass |
| Backend attention plus feed/pump/lifecycle regressions, repeated `-race -count=3` | Pass |
| Real owner UDS disappearance/replacement recovery, repeated race runs | Pass |
| Ambiguous/original-envelope WebSocket admission, repeated race runs | Pass |
| Persistence→fresh reload→version-1 owner hello/List/Subscribe | Pass, three race repetitions |
| Encrypted phone mailbox/status→durable restart | Pass, three race repetitions |
| Android release identity gate, versionName 0.17.2/code 60 | Pass |
| Linux arm64 skeleton/accountcheck test cross-compilation | Pass |
| Existing production autostart/idempotency, real daemon handoff, live-shim/backend adoption and typed-status SIGKILL recovery | Pass, two native race repetitions |

The socket test replaces a real protocol server at the same endpoint and preserves session/selection identity. A delayed old producer snapshot cannot overwrite hydration. Subsequent error status arrives through the replacement List connection. Candidate cleanup and superseded mutation replies are tested separately. Existing daemon singleton-startup, real restart and durable-shim survival tests passed twice natively and remain required CI coverage; recovery never launches/resumes/kills sessions. Initial Subscribe errors/nil streams now enter the same recovery path.

## Native baseline limits

An attempted native `go test -race ./...` exposed unrelated baseline failures: Linux-only test helpers referenced by portable tests, macOS account policy paths rejected through the `/etc` symlink, non-Linux processcontain.Running reporting a live owner stopped, inherited Swarm hook variables and Linux-specific process probes. Portable helper bodies were extracted without removing Linux-only fixtures or changing production validation. Test state paths were canonicalized.

A clean archive of main `686383e`, using only the portable helper overlays, reproduces `TestWritersStoppedForBindingRefusesUnknownRetainedCustody` (live check owner permitted transfer) and native Claude/Codex account configuration-source-changed failures. These production behaviors were not changed or assertions weakened. Follow-up issues: `agents-tracker-ovc9` (policy path) and `agents-tracker-swml` (custody liveness). The required Linux full-race and platform CI lanes gate merging and release. Native results are not represented as a clean full-suite pass.

## CI and publication

[PR #50](https://github.com/Nathandela/swarm/pull/50) records required branch CI and the protected-main merge. Tag-triggered release CI, signed assets, compatibility manifest, Homebrew update and host activation are recorded in the release notes after publication. No gate may be bypassed to publish this patch.
