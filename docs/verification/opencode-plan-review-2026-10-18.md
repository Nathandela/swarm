# OpenCode implementation strategy reviews

DeepSeek V4.1 Flash and GLM-5.3 Flash reviewed the proposed OpenCode maturity strategy through **actual Swarm-managed OpenCode sessions**, then received a second critique request through `swarm send`. Their native provider/model fields, request/reply parent IDs, terminal completion and errors were checked independently of Swarm's currently incomplete OpenCode status model.

The result is the [implementation plan](../specifications/opencode-maturity-plan.md). Both reviews improved it, but several recommendations required correction. The live API experiments changed the preferred first production slice from an assumed durable-v2 integration to qualified legacy HTTP/SSE observation with v2 actions gated separately.

## What was run

| Reviewer | Requested and native-recorded model | Round 1 | Round 2 | Native-reported total cost |
| --- | --- | --- | --- | --- |
| DeepSeek V4.1 Flash | `openrouter/deepseek/deepseek-v4.1-flash` | 24.866 s | 10.101 s | $0.004168162 |
| GLM-5.3 Flash | `openrouter/z-ai/glm-5.3-flash` | 87.662 s | 40.017 s | $0.002059922 |

These are observations from one task, not a model benchmark or invoice. Round 1 used the same strategy packet with different job labels. Round 2 supplied new measured facts and different peer objections. Provider routing, caching, load and output length were not controlled. The total above excludes initial blocked attempts and the separate native API qualification prompts.

Workers used an isolated Swarm daemon, workspace and XDG profile under `/private/tmp/swarm-oc-plan-20261018`, the existing OpenRouter credential supplied only in process memory, and test-only 8,192-output-token limits. They were given the strategy, not unrelated project contents. Reviewer tasks had no edit or shell authority. No production native integration was implemented by the review workers.

The initial file-based packet failed: DeepSeek's read calls were blocked by the disposable permission configuration. It reported `REVIEW_BLOCKED` and did not fabricate a review. That attempt was replaced with a complete inline packet for both models; the inline runs are the measured results. The first GLM session was stopped without a usable review. DeepSeek's second answer used the earlier `REVIEW_COMPLETE` marker rather than the requested `ROUND2_COMPLETE`; native request/reply identity and terminal completion, rather than the marker alone, established that the second reply was received.

## Inspect the actual material

- [Shared strategy packet](opencode-plan-review-2026-10-18/strategy-packet.md)
- [DeepSeek initial critique](opencode-plan-review-2026-10-18/deepseek-round1.md) and [revised critique](opencode-plan-review-2026-10-18/deepseek-round2.md)
- [GLM initial critique](opencode-plan-review-2026-10-18/glm-round1.md) and [revised critique](opencode-plan-review-2026-10-18/glm-round2.md)
- [Correlated model metadata and API qualification evidence](opencode-plan-review-2026-10-18/evidence.json)

The raw critiques are model-generated proposals, not verified specifications. Final assistant text is retained; hidden reasoning and credential/profile contents are excluded.

## What each model contributed

DeepSeek concentrated on identity before admission, same-runtime qualification, precise completion correlation and failure interleavings. It argued that a queue/history API cannot be the authoritative spine until it covers the actual TUI. It also moved concurrent request handling earlier and identified compaction/reconnect/port-reuse cases that need to be covered by the first lifecycle slices.

GLM concentrated on the smallest deployable slice. It argued for shipping status and request visibility through the already exercised legacy API while qualifying the newer runtime in parallel. It also emphasized that multi-pending request storage must precede concurrent supervision, and that permission preservation needs a serializer/contract test rather than a convention. Its second review more clearly separated the two runtime surfaces.

Both endorsed shim-owned process lifetime, early native identity, an explicit managed-startup failure instead of a second hidden conversation, and bounded native-history access. Both opposed adding another broker or full transcript archive.

## Accepted, corrected and rejected recommendations

| Recommendation | Disposition in the plan |
| --- | --- |
| Native identity and backend ownership before initial work | Accepted in P2, including create-response and persistence crash windows |
| Correlate completion with inputs, not idle/tool-step messages | Accepted in P3/P4; failed, declined and cancelled operations need not produce a successful assistant answer |
| Observe all pending requests before concurrent supervision | Accepted in P3; decision writes and phone UI remain later |
| Legacy observation can deliver value before v2 runtime qualification | Accepted after live experiments showed the v2 projection split |
| Preserve permission policy through explicit serialization tests | Accepted; no prompt tools override or blanket policy replacement |
| Every admission is uncertain until a reply | Corrected: a proven admission is admitted; uncertainty describes an ambiguous effect, not ordinary waiting |
| Different-content ID conflict should become uncertain | Rejected: a definite conflict is refusal; do not reuse that ID or blur it with a lost-response case |
| Error followed by assistant completion should end as success | Rejected: completion metadata alone does not erase a native error; preserve the matching actual terminal outcome |
| Permission grouping explains the old `Interaction=none` modal | Corrected: missing native capture explains none; attention precedence is an additional issue once the fields are wired |
| Move to a daemon-owned shared server under resource pressure | Rejected for this plan: it changes the proven lifetime boundary. Measure per-worker cost before considering another owned topology |
| Stop an old turn with an unconditioned session interrupt | Not enabled; strict Stop remains false until newer work is protected by a real native precondition |
| Fix generic grid staleness as the central OpenCode solution | Not selected: native authority is the primary fix; unrelated generic heuristic changes need their own regression evidence |

## Additional live API qualification

The installed OpenCode 1.19.3 local `/doc` advertises a newer `/api/session` family beyond the legacy routes listed in the [earlier audit](opencode-parity-2026-10-18.md). Tests used only disposable sessions and authenticated local servers.

| Probe | Measured result |
| --- | --- |
| No Basic credential versus correct credential | Health returned 401 versus 200 |
| Concurrent `serve --port 0` | First bound 4096; second bound 53199; use actual native endpoint, not an assumed fixed/ephemeral port |
| Caller-ID v2 session create | Same ID retries returned the existing session. Changed-agent retry also returned existing settings rather than conflict |
| Legacy/v2 session reads | Each API could read a row created through the other |
| V2 queued input, caller message ID, `resume:false` | Exact retry returned the same `admittedSeq=1`; different content returned 409 |
| Server restart | Exact retry/conflict and one durable admission event survived |
| History and event replay | The admitted event replayed; `after=1` excluded sequence 1; this does not qualify all replay-to-live races or retention policies |
| Ordinary reviewer TUI sessions | Successful final replies existed in legacy history, while v2 history returned empty |
| Attached TUI on v2-created session | Native TUI showed the selected DeepSeek model and produced `LEGACY_ATTACH_OK`; v2 history/projection did not gain that legacy exchange |
| V2 resume of queued input | Admission/promotion and a projected user appeared; a successful assistant response was not established in this probe |

The final two rows are decisive. Readable shared session rows do not establish one execution/history pipeline. The plan therefore uses legacy HTTP/SSE for the first observation slice and requires complete same-runtime qualification before enabling v2 semantic actions. The probe does not establish that every newer-API execution path is broken or that future versions cannot supply the desired guarantees.

The [official server documentation](https://docs.opencode.ai/docs/server/) supports the managed server/attached-client arrangement. The pinned local schema and runtime responses, rather than the legacy documentation alone, determined the newer API distinctions. Request handling must also preserve the [native permission policy](https://docs.opencode.ai/docs/permissions/).

## Model selector implementation

The requested selector now reuses Swarm's existing editable suggestions. Native `opencode models --verbose` supplies exact IDs from built-in/configured providers, with bounded capture, timeout, deduplication and deprecated filtering. It performs no model generation. Unavailable discovery falls back to editable text.

Independent source review caught two issues before delivery: promoting the first discovered model during handoff, and reading a default from Swarm's cwd then forcing it in another target project. The final implementation is catalogue-only and leaves the native default untouched until explicit selection. Existing explicit Claude/Codex defaults remain honored. Tests exercise both untouched and chosen OpenCode handoffs.

The installed Homebrew release is not replaced by these local commits. Distribution still requires the repository's normal release and update procedure. Target-cwd-specific catalogue refresh remains a documented later improvement; the patch does not pretend the detected catalogue belongs to every possible launch directory.

## Verification boundary

Final code checks and retained output are recorded in the [picker evidence](opencode-plan-review-2026-10-18/evidence.json): full detect/TUI/command packages, focused race checks, full build, changed-package vet and CI-target Linux lint passed. Early package failures were traced to inherited Swarm hook environment variables and an overly long temp path clipping an existing TUI directory-completion assertion; subsequent runs remove all inherited `SWARM_*` values and use a short canonical private temp directory. The system-installed Go/lint binaries are x86_64 on an arm64 host; validation uses the existing native Go toolchain and a checksum-verified official native lint binary pinned to CI's version/toolchain policy. Whole-repository Darwin lint reports eleven unused Linux-only account helpers; CI-target Linux lint is clean, and unrelated account code was not changed. Whole-repository Go tests and full-repository race runs were not repeated for this picker change.

No overnight operation, full native integration, actual phone release or model-quality benchmark is claimed. The plan defines those acceptance gates. All disposable review workers and servers are stopped after the evidence is saved.
