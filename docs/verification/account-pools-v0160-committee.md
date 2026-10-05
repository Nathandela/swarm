# Account pools v0.16.0 Audit Committee synthesis

Date: 2026-10-05. Target: native configuration preservation, shared history, existing-discussion enrollment, model/quota recovery and credential custody on base `b3bd45d7ae56d1bd43804949cc748e523cf97273`.

## Consensus

Sonnet and Opus independently identified that original project/configuration changes and routine private native writes could strand resume or recovery. The implementation now provides an explicit stopped-owner refresh with durable old/new intent, ignores only characterized private Claude trust booleans, and continues to hold unproved permission, MCP and private Codex edits. Real native trust writes are characterized separately from permission grants.

Both reviews required actual native behavior and complete release gates. Synthetic native probes establish settings, history and failed-model mechanics; authenticated two-account continuation and production deployment require separate receipts.

## Divergence and validated fixes

- Sonnet's refresh crash-atomicity objection led to durable, fsynced update intents for both providers, interrupted-write regressions, pending-intent fences and retirement holds.
- Opus's credential-worker contention objection led to a separate configuration fence and exact installed-cohort read-only reuse, tested while the credential lock is held.
- Opus's deleted-owner-asset objection led to removal of only the recorded private alias, preserving the original target and history.
- Stale Claude model observations were replaced with current authenticated prompt/transcript/request diagnostics. Restart, delayed flush, superseding prompt and candidate acknowledgement regressions prevent borrowing another turn's model.
- Astra independently found newly introduced native routing/policy selectors; all five now refuse native admission and are scrubbed from final account resolution. Path selectors with literal values `false` and `0` also refuse.
- Final assembly tracing found that environment filtering dropped original profile origins and Swarm's diagnostic/fallback flags. Narrow transient source capture and saved-environment replay now preserve original configuration, while fixed owned child flags are inserted after filtering. Actual shim/child regressions failed before the correction and pass afterward.

Broad settings acceptance, automatic private trust transfer and companion history portability were not introduced to silence review objections. Native version support remains pinned and documented; unknown updates hold managed execution. Native asset directory contents stay live, with root custody checked, as ordinary owner configuration expects.

## Blind spots and evidence limits

An argument-level test does not prove the assembled child receives it; the final regression crosses the actual shim boundary. A cached launch model does not prove the failed request model; the current prompt/request evidence is mandatory. A resume acknowledgement does not prove authenticated continuation; a real next turn is a separate acceptance check. A green release does not prove live-discussion continuity; deployment compares process start identities and retained discussion/account metadata.

## Per-member signal

| Member | Result |
|---|---|
| Claude Sonnet, native print mode | Completed read-only source review; strongest contributions were refresh crash recovery and routine native writes. |
| Claude Opus, native print mode | Completed read-only source review; strongest contributions were owner-refresh eligibility, credential lock contention and deleted assets. |
| Claude Fable, native print mode | Timed out after 300 seconds with no final output. No approval inferred. |
| Gemini through agy | Failed because native authentication/model availability was unavailable. No review inferred. |
| Codex Sol CLI | Retry timed out after 600 seconds without a final output. Actual Sol collaboration agents implemented and tested; these are distinct roles. |
| Codex Astra CLI | Retry timed out after 300 seconds without a final output. An independent Astra collaboration agent provided the subsequent full feature and regression review. |

## Verdict

The initial verdict was **revise**. Validated objections produced the fixes and experiments above. At the v0.16.0 source freeze, Astra identified no remaining code blocker; all 1,698 reviewed Go files matched the final source, and full local build/vet/lint/normal/race gates passed. Official release gates and signed deployment subsequently passed. Installed managed-launch acceptance exposed further protocol-ingress and native enrolled-profile layout defects. These are recorded separately in the [v0.16.1 hotfix verification](account-pools-v0161.md); the earlier review did not establish that acceptance. This record does not claim unanimous committee approval or authenticated quota exhaustion.

Raw independent brief and outputs: `/tmp/swarm-native-context-committee-ki32d4q7xjjaggcr`. Extended Astra and Sol correction receipts remain in the session's `machine-account-rotation-impact` evidence directory.
