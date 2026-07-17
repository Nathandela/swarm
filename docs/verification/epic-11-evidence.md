# Epic 11 — Evidence

**Epic**: Claude Code + Codex adapters + the engine→daemon status feature (`agents-tracker-bf1`)
**Commits**: 483c21b (adapters + registry) · 8b2e4ac (engine→daemon wiring) · ceabc44 (FIX 1 SetStatus race) · 8c9ae67 (FIX 2-7) · b933bee (audit-010 fixes) · 381cc6a (audit-011 C1-C4) · plus the C1 test-name precision fix.

## What Epic 11 delivers

Two real-CLI adapters (Claude Code hook-style, Codex typed-event style) as PURE strategy objects, wired into the running daemon so status detection FUNCTIONS end-to-end: launch resolves an agent through the registry to the adapter's real argv + per-invocation hook injection; a hook posted with the session's minted token authenticates (S6), is normalized event+payload→status via the session's registered SignalSources (the mapping bridge), and is persisted through the sole meta writer (G6) + fanned out to the TUI (V-2). Conversation ids are captured from the transcript so ended/lost sessions resume as a NEW session linked by `resumed_from` (R-2). This completes the engine→daemon status carry-forward Epic 8b/10 deferred.

## Criterion walk (E11.1 – E11.8)

| Criterion | Evidence |
|---|---|
| E11.1 Claude characterization first | claude adapter fixtures + capability matrix under adapter/claude/testdata; descriptors (Binary/VersionArgs/ParseVersion) pure |
| E11.2 Claude adapter | hook events incl. dedicated PermissionRequest + Notification (subtype-refined, safe default); inline `--settings` per-invocation injection (json.Marshal, single argv, S4-safe); `--resume` argv from a captured conversation id; conformance + fixtures green |
| E11.3 Codex characterization first | codex fixtures (testdata/codex.json) in the real JSON-RPC app-server shape (turn/started, turn/completed, item/commandExecution/requestApproval; params.turn nesting; request id) + capability matrix |
| E11.4 Codex adapter | typed turn/approval descriptors per the app-server interface; conformance + fixtures green. **Runtime typed-event PRODUCER deferred to Epic 14** (see Deferrals) — Codex v1 runtime status driver is the grid heuristic |
| E11.5 Resume-as-new-session (R-2) | reserved `resume_from` option → daemon validates the source (id/endpoint/exists/ended/agent-match) → composes the adapter resume argv from the captured conversation id → new session stamped `resumed_from`; refuses (clear error) when no id was captured, never a fresh launch mislabeled as a resume; TUI `r` affordance on ended/lost rows carries Env + surfaces errors |
| E11.6 Grid-heuristic fallback + L-2 greying | generic grid heuristic is the T-3 fallback; out-of-version fixture greys the picker (L-2) in the launch form |
| E11.7 Scenarios 4, 5, 12 | engine-wiring e2e (authenticated hook changes status + foreign-token no-op, S6), persisted-status e2e, resume-as-new-session e2e, attach-independent capture e2e — all green |
| E11.8 no core edits (T-5) | `go list -deps` confirms adapter/claude + adapter/codex depend only on `internal/adapter` + `internal/vt` (+ stdlib); committee re-confirmed |

## Correctness hardening (audit committee, three rounds)

The adapters passed on green tests but the FEATURE was initially simulation-only; three committee rounds (codex cross-model + independent Opus) drove it to a working, race-safe feature. Key fixes, each verified:

- **SetStatus resurrection race (CRITICAL)** — atomic read-modify-write under `writeMu`; a late engine emit can never resurrect an exited/lost session (F1).
- **Typed status survives restart** — reconcile re-registers reconnected sessions (token re-read from the 0600 shim-launch.json) and seeds the persisted status into the engine atomically, so a stale active is downgradable after restart (S7); markLost retires the engine entry.
- **Mapping bridge** — `swarm hook` parses the stdin JSON payload; the engine normalizes via registered SignalSources; an unknown/missing Notification subtype degrades to a conservative interaction, never a confident false permission.
- **Terminal writers atomic (S1)** — markLost + handleShimExit finalize under `writeMu` with a rank re-check (exited > lost > running); an authoritative exit can never be clobbered to lost.
- **Resume is real, not a label** — conversation id captured from the transcript file independent of the live attach (poll cadence + a session-end net), write-once, session-bound, terminator-guarded against a partial mid-write id; resume refuses rather than mislabeling.

Full audit trail: docs/verification/audit-009/010/011-epic-11*.md. Final verdict: **codex CONFIRM + Opus CONFIRM** (both independent reviewers agree Epic 11 is ready to close; no blocking residual).

## Deferrals (recorded, carried to Epic 14)

- **D1 — Codex live typed-event producer** (a `codex app-server` subprocess + JSON-RPC bridge converting events to authenticated callbacks): deferred to Epic 14's flagged real-CLI smoke, consistent with the standing no-billable-runs decision. Codex v1 runtime status is driven by the grid heuristic; the typed mapping is fixture-proven pending live wiring.
- **D2 — exact real hook value names** (Claude PermissionRequest existence, the `notification_type` field name, permission_prompt/idle_prompt/auth_success; the Codex app-server field placement): Epic 14 VERIFY (T-6). The descriptors are drift-resilient and B5's safe default guards the unknown-name case in the meantime.

## Quality gates (GG-4)

gofmt · `go build ./...` · `go vet ./...` · `GOOS=linux go build ./...` clean. Whole module green under `-race` (27 packages); daemon/engine/skeleton/claude/e2e race + capture/extract tests at `-count=5`. TDD (GG-5): each round wrote failing tests first; both independent reviewers confirmed the tests assert real behavior and no pre-existing (frozen) test was weakened.
