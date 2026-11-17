# v0.26 audit remediation

Baseline a1da607e. Scope covers the eight audit issues for the terminal dashboard, supervisor, and local integration fixtures. The intended deployment remains a per-user daemon on macOS and Linux with sessions surviving daemon replacement.

| Work | Acceptance and regression spike |
|---|---|
| Operation store (7ssa, 94v8) | Reads expose errors before opening a writable store. Repair only a provably incomplete final record, retaining valid unterminated records. Keep write and replay limits consistent, account for existing large records, and prove restart cannot lose later records or repeat a transition. |
| Live journal (sd7c) | Publish replacement segments and durably retire old segments before reporting success. Inject partial retirement and retry across both recovery and restart while preserving cursor ordering and subscriptions. |
| Native session identity (6h9n, xqdu) | Keep newer manual names through restarts, serialize timestamp checks with writes, retry failed persistence, honor disabled agy presentation, and reject conflicting Codex archive state before any batch launch. |
| Discord pruning (w039) | Visit only publications for each expired request, preserving guard retention, independent requests and unresolved deliveries. |
| Cleanup (cpnj) | Untrack the old executable, retire unused simulator helpers, correct requirement evidence and test the shipped board renderer. Preserve release builds, actual recovery tooling, and useful test fixtures. |
| Qualification (q44t) | Isolate inherited hook endpoints and use canonical fixture roots. Scope source gates to the checkout. Recheck race tests and relay approval through the real local Worker harness. |

Review the strategy and challenge failure paths before merging implementation. Cross-review changes with agents who did not author them. Run focused failures first, then build, vet, lint, full race tests and local relay integration. Record unresolved platform or release gates. Push reviewed source, with release publication a separate operation.
