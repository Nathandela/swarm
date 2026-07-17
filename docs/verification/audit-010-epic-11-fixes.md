# Audit 010 — Epic 11 fix round (FIX 1-7) re-review

**Date**: 2026-07-17
**Committee**: codex GPT-5.6 sol (cross-model, source/diff/doc-based — sandbox forbade running the Go suite) + Opus (independent, ran the FULL gate suite + -race, lock/sequence/dependency traces). agy quota-blocked.
**Verdict**: **FIX REQUIRED.** Productive divergence: codex returned 7 findings (1 CRITICAL, 4 HIGH); Opus returned APPROVE-conditional (all MEDIUM or below) with gates independently green. I adjudicated by reading the cited code — codex's concrete runtime defects are REAL (verified line-by-line); Opus's structural clearance is also correct. The round's spine is sound; its resume feature is broken and three correctness bugs remain.

## Confirmed by reading the code (codex right, Opus under-weighted)

- **CRITICAL — resume is non-functional AND mislabels fresh as resumed.**
  - `ExtractConversationID` has NO runtime call site (only `capability.go:56`, from a fixture). So `Meta.ConversationID` is never populated for organically launched sessions → resume has no id to carry.
  - `composeLaunchSpec` (api.go) sets `spec.ResumedFrom = local` unconditionally, then if `Resume` yields empty argv (empty ConversationID) `spec.Argv` stays empty and control falls through to the FRESH-launch path — a fresh session stamped `ResumedFrom`. Every organic resume is a mislabeled fresh launch.
  - TUI `resumeCmd` (general.go) omits `Env` from the `LaunchReq`, so real-adapter argv0 resolution fails for lack of PATH, and `_, _ = c.Launch(req)` discards the error — silent failure.
- **HIGH — RegisterSession ignores persisted status (S7 after restart).** `engine.RegisterSession` hardcodes `{running, unknown, unknown}`; `serve.go registerSession` never passes `m.Status`. After a daemon restart the engine believes `unknown`, and `Tick` only downgrades internally-active sessions, so a persisted `turn=active` can survive as stale-active until the next signal.
- **MEDIUM — terminal writers race each other (S1).** `markLost` and `handleShimExit` both read running-meta under `d.mu`, release, then `saveMeta` (which does NOT refuse non-running writes — FIX 1's guard is only in `SetStatus`). Concurrent, both pass the running check, later save wins: a `lost` can overwrite an authoritative `exited`+code. Pre-existing; adjacent to FIX 1's hardening.
- **MEDIUM — deriveDims default-to-permission is unsafe.** An unknown/missing Notification subtype leaves interaction at the `permission` default (engine.go deriveDims + claude descriptor) — a confidently-wrong permission state. Should degrade to `none`/`unknown`.
- **MEDIUM — Codex fixture not faithful JSON-RPC.** Method names right, but `turn/started`/`turn/completed` carry `params.turn` (nested), not flat `threadId`/`turnId`, and `item/commandExecution/requestApproval` is a server REQUEST with a JSON-RPC `id` the fixture omits (per the OpenAI app-server README).

## Consensus scope items (both reviewers) — deferral decisions

- **Codex typed-event runtime producer is dormant** (codex HIGH, Opus MEDIUM). Launch starts interactive `codex`, not `codex app-server`; nothing parses the JSON-RPC stream into authenticated callbacks. At runtime Codex status is grid-heuristic-only. Building a correct producer requires characterizing the real app-server — a live/billable run this project deliberately defers. **DECISION (consistent with the standing no-billable-runs decision): the Codex live typed-event path is deferred to Epic 14's flagged real-CLI smoke; Codex v1 runtime status driver = the grid heuristic; the typed mapping is fixture-proven pending live wiring.** The "working feature" framing must be softened for Codex in the evidence.
- **Exact real hook value names are unverified** (codex HIGH, Opus MEDIUM): Claude `PermissionRequest` event existence, the `notification_type` field name, and `permission_prompt`/`idle_prompt`/`auth_success` values; Codex `params.turn` shape. **DECISION: exact names are Epic 14 VERIFY (T-6); the descriptor stays drift-resilient. The Codex fixture is corrected now to the documented shape (cheap, doc-grounded); extraction stays green.**

## Cleared by both (and re-confirmed)

FIX 1 RMW correct + no deadlock (Opus traced every lock site: `writeMu` always outer, no `d.mu`->`writeMu` inversion). deriveDims runs only after token auth; derived dims still pass the per-dimension sequence high-water; unknown events are a benign no-op. Adapter purity (T-5) intact (`go list -deps`). Argv-array only (S4). Token confined to env + 0600 shim-launch.json (S6). No pre-existing test weakened. Whole module green under `-race` (27 packages), gofmt/build/vet clean.

## Disposition

One targeted fix round (blocking items B1-B5 + the Codex fixture correction), dispatched to the teammate that holds the round's context. Re-reviewed before close. Scope deferrals D1/D2 recorded here and carried to Epic 14 + the evidence file.
