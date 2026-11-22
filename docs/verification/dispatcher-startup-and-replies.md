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

## Candidate evidence and open gates

- The late-first-thread test failed on the release baseline. With the candidate the same app-server connection admitted the late thread in 5.5 seconds using a shortened test deadline. The ordinary timeout, old-instance and resumed-session controls passed. This uses the Swarm supervisor and a fixture backend.
- An isolated Codex 0.171.0 app-server reported an untrusted test hook before the first thread. Writing the exact fingerprint changed its status to trusted. A later native config probe preserved an unrelated workspace and hook hash. The complete hook-review-to-real-TUI-to-Discord trial is pending.
- A fresh agy 1.5.13 CLI with synthetic credentials showed the folder trust prompt. Applying the exact workspace via session settings reached the composer. A fresh profile separately presented sign-in and a terms/data-sharing choice. An ordinary Swarm agy worker and a separate isolated ACP session both launched. Neither established the proof required for an agy dispatcher or an effect-free automatic doctor.
- Claude Code 2.1.330 displayed the known folder trust dialog. An unauthenticated offline startup failed with a connection error. OpenCode 1.19.3 started at its composer and offered provider connection. These are observations from isolated setups, not an exhaustive list for every provider or version.
- Focused tests reproduced and then covered silent native completion, one timeout notice, granted-source changes, and waiting for a delivered question. Full integration must still establish the correct terminal outcome after permanent Discord delivery failure, multiple workers, and interrupted report recovery.
- Fable 5.2 and Opus 5.5 requested changes to the first candidate. The follow-up limits workspace trust, distinguishes profile-wide hooks, preserves unrelated native settings, tolerates transient resume probes and distinguishes changing startup notices. Release approval remains open until the exact final revision passes independent review and acceptance.
- An intermediate build and vet completed. The full Go test attempt ran in a user namespace to preserve disk space. Permission and process-identity fixtures failed there. Native linking and test supervision also encountered ENOSPC on the VM root volume. The full suite, race, lint, real DM replay, and native cross-CLI fallback are pending; this evidence is insufficient to publish.
