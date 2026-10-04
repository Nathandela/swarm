# Account quota labels: v0.15.3

Status on 2026-10-04: **v0.15.3 published and deployed**. This release compacts known quota-window labels in the Accounts interface. The automatic read-only quota reader and its safeguards were introduced and verified in [v0.15.2](account-pools-v0152.md).

## Change and installed UI acceptance

Known Claude window labels display as **5h** and **weekly**; known Codex primary and secondary labels display as **Primary** and **Secondary**. Unknown-label sanitization is unchanged. The Android release identity is version name 0.15.3, version code 50.

The installed v0.15.3 Accounts interface was checked at 80 columns with two Claude accounts. Both list rows retained the 5h and weekly labels and percentages. Both details showed used and remaining percentages, reset times where provided, and reading age; the compact labels wrapped cleanly. The TUI closed cleanly, and a post-navigation capture/compare passed again on the same daemon PID 1449397. Navigation changed no authentication, settings or account lifecycle. The separate owner RPC probe requested usage refreshes. Usage values and account identities are omitted. No live Codex UI check was performed.

The focused regression also checks the selected account row at 80×24 with long representative account names, requiring both percentages to remain visible without guidance text masking truncation. Astra reviewed the exact source and found no blocker in this bounded change. Its independent focused race run passed nine tests across the Accounts quota UI and Android release identity gate. The aliases cover the reported two-Claude-window case; arbitrarily long labels, more windows, or narrower terminals may still require opening details.

## Source and repository verification

The reviewed source was commit `476342c8521a440fe3109cb11b3bbcdddb1a068e`, tree `5bf3e66cd0f4844018d163c77c2bb90e9bd2bb4c`. Its 1,650 Go files have canonical path-to-SHA256 inventory digest `c12261ff5cbeba16f6fef6e0aa9c772f4895547e2d37df288000041872e10238`. PR #44 merged normally as release source commit `89d58e68d0491896eaea6d9892c0211a76f5c3c2`, with the same tree and Go inventory. All 30 PR and branch checks passed. [Main CI](https://github.com/Nathandela/swarm/actions/runs/37205643974) and [push-gateway container checks](https://github.com/Nathandela/swarm/actions/runs/37205643981) also passed. The stable-source secret scan covered four changed implementation files and found zero issues.

Build (4.65s), vet (6.40s), lint (4.30s), and strict documentation checks (0.03s) passed. The complete race suite passed in 577.43s across 76 packages with tests and eight packages without tests; 64 tested packages were cached. The first complete normal run failed after 488.86s in `internal/protocol/TestIntegration_ChunkedOversizedSnapshot`: the reattach snapshot was 1,088,697 bytes, compared with 1,062,001 bytes for the first snapshot.

Astra triage identified a pre-existing test-readiness defect: exceeding one frame's size does not prove that styled painting has finished. The unchanged test passed ten isolated repetitions on both current and baseline source; a separate deterministic diagnostic crossed the threshold after 48 of 50 rows, before the full grid. The exact interleaving during the original failure is inferred because those original snapshot bytes were not retained. This does not establish transport corruption or a connection to the label change. A separate serial protocol run passed in 16.60s, followed by a complete serial normal-suite pass in 81.94s across 76 tested and eight no-test packages (68 tested packages cached). The initial failure remains part of the record; follow-up `swarm-9xv` tracks improving the readiness predicate.

## Release workflow

The first release-workflow attempt, run 37205698085, had one failure: remote-v2 job 111446311866 received HTTP 404 where `services/relay/test/rate-limit.mjs:33` expected 429 at 13:31:00.370 UTC. An early API retry attempt while the workflow was active returned HTTP 403 and did not start a rerun. After the initial workflow finished its remaining work, one bounded retry was accepted. Replacement job 111448734138 passed; release workflow attempt 2 completed with all 17 jobs successful. No source change was made for the retry.

Astra confirmed that the relay test, session script, worker, configuration, lockfile and CI files were unchanged from v0.15.2, and that the release Go inventory still matched the frozen v0.15.3 source. A local probe of the pinned Miniflare rate limiter returned 429 for request 61 within one 60-second window, and 404 for request 61 after 60 requests crossed into the next window. The limiter starts a new bucket at the window boundary. This supports a boundary-crossing test burst as the cause, but the exact boundary crossing in the failed job is inferred because per-request timestamps were not recorded. The evidence does not show a production per-window limit bypass. Open P2 `swarm-tx7` tracks constraining the test burst to one simulator window while retaining its rejection and header assertions.

[v0.15.3](https://github.com/Nathandela/swarm/releases/tag/v0.15.3) was published at **13:51:32 UTC on 2026-10-04** from release source commit `89d58e68d0491896eaea6d9892c0211a76f5c3c2`. [Release workflow 37205698085](https://github.com/Nathandela/swarm/actions/runs/37205698085) completed successfully on attempt 2. The release has ten assets, including checksums and a detached checksum signature.

## Deployment and acceptance limits

The installed CLI and responding daemon report v0.15.3; the daemon executable matches the installed binary, the pending-converge marker cleared, and all nine post-activation doctor checks passed. The continuity check retained all 155 session IDs and discussion identities/bindings and the eight prior live shim identities. It reported unchanged saved environment, account schema, provider rotation settings, and selected account identity/lifecycle/generation metadata. This helper does not establish full registry equivalence or quota-state continuity.

The first explicit daemon restart attempt exited 1 with a Hello/readiness timeout. Convergence, all nine doctor checks, and an owner-authorized quota snapshot subsequently succeeded on the same daemon PID 1449397. Astra classified the event as a startup-readiness timeout after releasing the previous singleton and spawning the replacement. The specific startup-delay phase was not identified, and the backend startup source was unchanged. No further restart was indicated; open P2 `swarm-gco` tracks the readiness question.

After activation, the installed reader probe passed for both enrolled Claude profiles: each returned ready with a fresh fetch attempt and populated usage observations. This is live Claude usage-read evidence. No live Codex profile or native quota-triggered discussion continuation/switch acceptance is established. Those behaviors remain follow-up work.
