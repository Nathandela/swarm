# Audit 011 — Epic 11 fix round CONFIRMATION (commit b933bee)

**Date**: 2026-07-17
**Committee**: codex GPT-5.6 sol (cross-model, source-based; first run hung and was killed, retried under a 720s cap) + Opus (independent, ran the FULL gate suite + -race). agy quota-blocked.
**Verdict**: **FIX REQUIRED (narrow).** Divergence: Opus CONFIRM (all 5 + fixture resolved, gates green under -race, only a LOW stale doc comment); codex FIX REQUIRED (3 residual edges in B2/B3). I adjudicated by reading the code — all 3 codex residuals are REAL edges Opus's happy-path verification did not probe. Both agree B1, B4, B5, and the Codex fixture are fully resolved.

## Fully resolved (both reviewers + re-confirmed in code)

- **B1** resume no longer lies/fails silently — ResumedFrom stamped only after a real resume argv (api.go); TUI carries Env + surfaces the launch error (general.go/tui.go). Sole non-test ResumedFrom writer.
- **B4** terminal writers atomic — finalizeTerminal re-reads + rank-checks (exited>lost>running) + writes in one writeMu section; no TOCTOU, no lock inversion; race test exercises both writers concurrently, green under -race.
- **B5** deriveDims safe default — unknown/missing Notification subtype -> InteractionNone; known subtype overrides; auth/replay/high-water ordering intact.
- **Codex fixture** — nested params.turn + JSON-RPC request id; threadId extraction is nesting-agnostic, still green.
- Gates: gofmt/build/vet clean, GOOS=linux clean, whole module green under -race (Opus re-ran independently; I ran it at -count=1 + daemon/engine race tests at -count=5).

## Residual edges to fix (codex-found, code-confirmed)

- **C1 (B2-a, ~HIGH) — conversation capture is unreachable for an attached-until-exit session.** captureConversationID runs ONLY from sampleGrid -> d.api.Attach; the shim serves serially, so while a client holds the attach no grid tap runs, and once the session exits it is no longer sampled. A session attached immediately at launch and held until exit ends with an empty ConversationID -> non-resumable. Fix: capture the id independent of the live attach — drive extraction from the transcript FILE tail (bounded) on the poll cadence AND at session end (finalizeTerminal), persisting via the existing write-once SetConversationID. Mind the writeMu nesting (do the SetConversationID BEFORE finalizeTerminal, not nested inside it).
- **C2 (B3, MEDIUM) — RegisterSession/SeedStatus is non-atomic on fresh launch.** registerSession calls RegisterSession then a separate SeedStatus (two e.mu acquisitions). On fresh launch the agent runs before OnSessionStart, so an authenticated callback landing in the gap is overwritten by SeedStatus (which keeps the advanced high-water). Fix: fold the initial status into RegisterSession so registration + status install is ONE atomic locked op; drop the separate seed on the register path. Fresh launch seeds the baseline (harmless), reconcile seeds persisted status, no emit on seed.
- **C3 (B2-b, MEDIUM) — write-once capture can commit a partial Claude id.** sessionIDFrom accepts the token after "Session " through EOF with no terminator, so a value truncated at the current transcript EOF (mid-write) can be saved write-once. Fix: require a terminator (newline/whitespace / complete line) after the id before accepting it.
- **C4 (LOW) — stale doc comment** at skeleton/api.go ~88-90: it still says the reserved fake agent "relaunches fresh, still linked," but fake resume is now rejected. One-line comment fix.

## Deferrals (unchanged, not re-raised as blocking)

D1 Codex live typed-event producer -> Epic 14 (grid heuristic is Codex v1 runtime driver). D2 exact real hook value names -> Epic 14 VERIFY (T-6). Both recorded in codex.go + audit-010.

## Disposition

One final tight round (C1-C4). Re-confirm with codex (the finding model) before close. Round-over-round the findings are strictly narrowing (round 1: 1 CRITICAL + 4 HIGH; round 2: 3 narrow edges + 1 LOW) — this is convergence. After C1-C4, if only cosmetic residuals remain, close Epic 11 and record any accepted v1 limitation.
