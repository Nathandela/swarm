What this change does: Exact head `8e9cfa574e43fa450895ce5dba1046ebcba06f92` makes first-class supervised handoff use the source's actual checkout. It refuses missing sources and explicitly worktree-backed sources whose checkout was never captured, while preserving ordinary spawn and resume directory behavior.

The original must-fix findings are resolved. `coreAPI.Launch` now refuses a missing source before reservation, and `handoffDirectory` rejects worktree=true with empty AgentCwd. The corrected resume test uses `api.endpointID` and requires the intended missing-agent-binary error, so it now exercises the path it claims. No production resume change was needed.

Validation passed: native Go 1.26.9 `go test -race` over all handoff-focused tests plus the independent missing-source, uncaptured-worktree and corrected resume overlays. Result: `ok .../internal/skeleton 10.117s`. Environment stripped all SWARM_* variables and used the prescribed toolchain/cache/temp directory. Worktree stayed clean and HEAD was rechecked after tests. Evidence: `final-overlay.json`, `final_overlay_test.go`, `final-overlay-results.txt`.

Connected paths rechecked: general spawn with explicit --dir remains outside the supervised shape; hands-off clears supervision; linked/external resume excludes the correction; remote tier refuses supervision before launch; core validates the selected workspace before reservation and after pre-launch hooks. No new regression found within this change's scope.

Verdict: Ship this handoff correction.
Not checked: full-repository gates and an actual `swarm handoff` CLI smoke observing the child's process cwd. The tests use coreAPI plus actual shim launch configuration, not the complete CLI/protocol path.

## Separate P5 retention finding

Existing daemon idempotency TTL is 24 hours. `internal/idempotency/store.go:compactLocked` expires records by UpdatedAt regardless of phase, including executing and outcome_unknown. A deterministic injected-clock overlay reproduced an executing composer operation becoming a new prepared claim after 25 hours and Compact: `existed=false phase=prepared`. Evidence: `retention-overlay.json`, `retention_overlay_test.go`, `retention-overlay-results.txt`.

This is not, by itself, a new replay vulnerability in the current phone path: `handleComposerSend` authenticates the signed remote command first, and server command validity is capped at one hour. The original expired signed command cannot reach the claim after 24 hours. Phone publication retry keeps exact command custody; a deliberate new known-unsent retry has a new operation ID.

P5's proposed owner-tier semantic path does not automatically inherit that expiry proof. Moreover, current owner-tier composer_send bypasses the durable Claim/Begin/Commit block entirely because it is guarded by remoteTier. The new semantic owner handler must explicitly engage durable custody, and unresolved semantic operations must not age into fresh claims. Keep unresolved guards in the existing store until exact native reconciliation or explicit intervention; preserve the capacity refusal instead of evicting them. Terminal owner receipt expiry also needs a stated replay horizon and fail-closed stale-operation behavior. The permanent per-worker Initial marker solves initial-prompt replay only, not all later semantic messages.
