# Dispatcher startup and visible replies

The baseline is v0.29.11. A private DM stayed queued after its Codex dispatcher waited for hook review. The original process exited, the replacement opened, and the supervisor stopped observing before the replacement created its first thread. Separately, an addressed request could complete a native turn with zero exported replies and become eligible to park.

## Release acceptance

Use an isolated workspace, bot route and supervisor. Record provider versions, which transport is real, the original request, the number of manager and worker launches, and Discord delivery receipts. Retain an active terminal draft and an outstanding worker as controls.

| Scenario | Required result |
|---|---|
| Recognized onboarding in an enrolled workspace | Apply only the identified startup choice. Verify readiness after the decision. |
| Changed hook or unknown modal | Match the current hook content and workspace for any automatic trust. Keep unknown input untouched and surface one specific next step. |
| CLI upgrade or permission prompt after startup | Recognize a supported version explicitly. Preserve ordinary tool and runtime approval decisions. |
| Dispatcher starts late | Observe the same process beyond both startup deadlines. Deliver the original request once when its native conversation becomes usable. |
| Observer restart during onboarding | Rejoin only with original launch and session evidence. Explain ambiguous ownership without overwriting a draft. |
| Native turn completes silently | Retain the obligation to answer. Check prior effects, obtain a report, and verify delivery or expose the specific unresolved outcome. |
| Long work, progress, question and answer | Keep an acknowledgement distinct from a final, and a delivered question distinct from an answer. Attribute the answer to the original request. |
| Worker completes while the manager is unavailable | Recover the original worker and receipts, including when the former manager returns. Preserve one authority to act. |
| Lost native or Discord acknowledgement | Recover the original operation using its receipt. Never repeat an uncertain side effect. |
| Provider limit or sign-in expires | Distinguish retryable limits, a known reset, missing sign-in and unavailable replacement. |
| New DM, shared thread or edited message | Preserve audience, source revision and order. Unaddressed shared conversation can remain quiet. |

## Provider qualification

The VM reported Codex 0.171.0, Claude Code 2.1.330, agy 1.5.13 and OpenCode 1.19.3. Hermes was deferred by the owner. A no-network agy 1.5.13 ACP initialization with a temporary profile advertised load, resume, close and list operations. That handshake does not prove reliable attachment to an ordinary installed TUI or attribution of a terminal turn to a Swarm admission.

## Independent plan review

Claude Fable 5.2 and Claude Opus 5.5 independently reviewed the design. Both required late and unknown-prompt cases, a bounded recovery attempt, authenticated delivery receipts, preserved drafts, and protection against repeated work after takeover. An elapsed deadline is grounds for inspection. It is insufficient evidence that an earlier process has stopped or that a worker action should be run again.

## Evidence from the initial candidate

- The late-first-thread test failed on the release baseline. With the candidate the same app-server connection admitted the late thread in 5.5 seconds using a shortened test deadline. The ordinary timeout, old-instance and resumed-session controls passed. This uses the Swarm supervisor and a fixture backend.
- An isolated Codex 0.171.0 app-server reported an untrusted test hook before the first thread. Writing the exact fingerprint changed its status to trusted. A later native config probe preserved an unrelated workspace and hook hash. The complete hook-review-to-real-TUI-to-Discord trial is pending.
- A fresh agy 1.5.13 CLI with synthetic credentials showed the folder trust prompt. Applying the exact workspace via session settings reached the composer. A fresh profile separately presented sign-in and a terms/data-sharing choice. An ordinary Swarm agy worker and a separate isolated ACP session both launched. Neither established the proof required for an agy dispatcher or an effect-free automatic doctor.
- Claude Code 2.1.330 displayed the known folder trust dialog. An unauthenticated offline startup failed with a connection error. OpenCode 1.19.3 started at its composer and offered provider connection. These are observations from isolated setups, not an exhaustive list for every provider or version.
- Focused tests reproduced and then covered silent native completion, one timeout notice, granted-source changes, and waiting for a delivered question. Full integration must still establish the correct terminal outcome after permanent Discord delivery failure, multiple workers, and interrupted report recovery.
- Fable 5.2 and Opus 5.5 requested changes to the first candidate. The follow-up limits workspace trust, distinguishes profile-wide hooks, preserves unrelated native settings, tolerates transient resume probes and distinguishes changing startup notices. Release approval remains open until the exact final revision passes independent review and acceptance.
- An intermediate build and vet completed. The full Go test attempt ran in a user namespace to preserve disk space. Permission and process-identity fixtures failed there. Native linking and test supervision also encountered ENOSPC on the VM root volume. The full suite, race, lint, real DM replay, and native cross-CLI fallback are pending; this evidence is insufficient to publish.

### Hosted startup and native qualification on Linux, 2026-11-22

The private rehearsals used installed CLI subscriptions, separate temporary profiles, and a dedicated supervisor. The real manager received one synthetic personal-DM admission. The connector harness required one publication containing that admission's random marker, no pending admissions, and a settled native turn. No live Discord channel was contacted.

| Scenario | Observed result |
| --- | --- |
| Codex 0.171.0, fresh workspace and an initially untrusted harmless profile hook | After explicit workspace and profile-hook opt-in, Codex recorded the folder and hook fingerprint as trusted. On the branch merged with v0.29.12, the manager published for input-301 and settled in 11 seconds. The test passed including teardown in 17.38 seconds. |
| Claude Code 2.1.330, fresh workspace | The manager published for input-301 and settled in 15 seconds. The test passed including teardown in 19.94 seconds. A separate run timed out during startup after 45 seconds and returned the saved-message notice. The successful run did not encounter that timeout. |
| Changed personal manager instructions | Before the clarification, the model received the changed instruction but answered from earlier Discord history. On 4c3154a5 one Codex manager answered both the original and changed instructions in 6 seconds each. The stock two-turn rehearsal passed in 21.78 seconds. |
| Native Claude proof with a loopback model | Installed 2.1.330, SHA-256 `f390eaa0814e6a1bff74cbb47acb74cb9c89d30ced809fe0af5af00c7bfe8ebb`, passed `TestNativeClaudeManagerProvenance` in 13.12 seconds and `TestNativeClaudeModalAdmission` in 19.44 seconds. Tests used a copied executable, private PTY and isolated network. |

Use `SWARM_TEAMMATE_TEST_STARTUP=codex` or `claude` with the existing `TestHostedTeammateLifecycle` private-account setup, `SWARM_TEAMMATE_TEST_PERSONAL=1`, a private trusted workspace and a dedicated candidate binary. Run each provider separately. Leave the startup option unset for the stock personal two-turn case.

### Two worker completions while the connector was offline

A private Linux rehearsal on c655d22a used an actual Codex manager and two ordinary Codex workers, one personal-DM source message, and a receipt-bearing simulated Discord sink. Both workers started in 19 seconds and signalled readiness through separate files. The test stopped the connector and restarted its dedicated supervisor. Both original worker incarnations retained their shim PID and process start time. After the supervisor restart and during the connector outage, each worker read its own random marker and issued an explicit completion, recording its queued CLI receipt.

After reconnection, each marker returned through its own authenticated worker wake. The north and south attempts ended in `bd89df69cc92` and `197e9953ea31`. The two results and original reply settled in 2 minutes 9 seconds after reopening. The full test passed in 161.44 seconds. A separate run that withheld transport receipts observed both native publications and retained an unresolved delivery check.

To repeat, use the existing private hosted test setup with `SWARM_TEAMMATE_TEST_PERSONAL=1`, `SWARM_TEAMMATE_TEST_VM=1`, `SWARM_TEAMMATE_TEST_DURABLE=codex`, and `SWARM_TEAMMATE_TEST_MULTI_RESTART=1`. The private PATH must include the installed Codex Linux sandbox launcher when using a disposable profile. Without the launcher, the first shell call failed before launching workers and Codex requested an approval.

## Integrated acceptance

The stricter test exposed two provisional job records lingering beside the two real worker records. After the repair, the test on `cafc8178` passed in 170.34 seconds with real Codex processes and a simulated Discord transport. It verified zero completion notes and visible results before releasing the workers, the same two shim identities after the supervisor restarted, and one confirmed publication for each original attempt after the connector reopened. The original request settled with zero outstanding or uncertain replies.

The real Codex launch through a symlink to a workspace containing quotes confirmed the physical workspace and exact hook fingerprint. An unrelated project and hook stayed intact. An independent loopback test lost a Discord POST acknowledgement, reopened the ledger and advanced the clock through 90 days. It recorded one POST and reconciled the original nonce with a late Gateway receipt.

The installed v0.29.11 connector and the patched binary exercised both downgrade directions on retained state. A prepared specific notice posted once. Re-upgrading recovered a previously missing binding pointer and preserved the notice’s original nonce. The old binary refused to open an active discussion with a new reply deadline. Re-upgrading recovered the same admission and deadline.

Claude Fable 5.2 and Opus 5.5 independently approved the integrated production code at `cafc8178`. A separate four-state regression confirmed that a generic startup notice preserves the original specific notice before send, after send, when uncertain and after compaction. The following lint-only change affected test response writes. Nathan deferred agy dispatcher takeover and cross-CLI diagnosis to `swarm-btd`. Ordinary Swarm model defaults continue to apply.
