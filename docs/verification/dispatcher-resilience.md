# Dispatcher resilience qualification

## Research and plan

Baseline fc7dbdf6 (v0.29.9). On the Mac an enrolled Codex manager recorded backend_unavailable around 04:00, then exited on SIGTERM at 07:51. A fresh manager started six seconds later. The marker alone cannot distinguish a current loss from a recovered backend. On Office-Swarm the owner reports recovery of queued input and a report, loss of native manager history and an uncertain job. The two report files present on disk do not establish the uncertain job's outcome.

1. Compare the baseline with a candidate under permanent and transient native loss. Require exact physical incarnation, current loss proof, draft and controller protection, one fenced replacement, and rejection of delayed old operations. Exercise AFK without work, reconnect, removal and repeated wakeups.
2. Carry bounded disposition evidence from the same discussion across replacement. Distinguish acknowledgement, scheduled final, confirmed Discord delivery and unknown effect. Keep new human instructions with a new message ID actionable. Compare responses to identical synthetic transcripts including edited/deleted messages, long prompts and an outstanding job.
3. Spike recovery of scoped history. Give a small read-only specialist only same-discussion, verified evidence. Compare latency, supported references, missed instructions and disclosure against the existing scoped reader. Keep private tool output and other discussions out of the prompt.
4. Shorten the hold notices and dispatcher instructions. Preserve the action that releases a draft hold and keep ordinary updates from waking other participants.
5. Run normal and degraded tests across DM and shared threads. Record native runtime versus synthetic transport distinctly. Run project quality gates. Check competing release claims and tag only after authorized Discord end-to-end qualification. Repeat that qualification against the published patch.

The failure model follows Chandra and Toueg's distinction between completeness and accuracy, Chubby's stale-owner fencing, Birrell and Nelson's call identity, AWS's explicit retry intent, Discord's bounded nonce guarantee and Anthropic's bounded research workers. [Failure detectors](https://hdl.handle.net/1813/7192) · [Chubby](https://research.google/pubs/the-chubby-lock-service-for-loosely-coupled-distributed-systems/) · [RPC](https://www.cs.cmu.edu/afs/cs/academic/class/15712-s19/www/papers/birrell84.pdf) · [AWS retries](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/) · [Discord messages](https://docs.discord.com/developers/resources/message) · [Research agents](https://www.anthropic.com/research/multi-agent-research-system)

## Notice and turn-taking checks on macOS

Across six fixed notice templates, v0.29.9 used 220 words and the candidate uses 111 (49.5% fewer). Counts use whitespace-separated words and one token for a discussion ID. The direct-input notice still asks the owner to save a draft before exiting. It directs the owner to inspect the outcome of previously submitted input before manual resume.

| Notice | v0.29.9 words | Candidate words |
| --- | ---: | ---: |
| Readiness unknown | 33 | 12 |
| Direct terminal input | 55 | 31 |
| Lost custody with terminal input | 41 | 21 |
| Earlier outcome uncertain | 35 | 19 |
| Startup unavailable | 27 | 12 |
| Replaced manager without native history | 29 | 16 |

The connector's mock Discord REST fixture exercised lost native admission replies, two queued DMs, a lost notice response, reconnect and repeated sync. Both admission variants produced one hold notice and two ordered replies. Separate cases passed for notice cooldown, revoked grants, uncertain posts, genuine mentions and a Gateway gap containing an old mention. The updated manager guidance fit the native prompt envelope at both tested human input sizes (990 and 2,000 bytes). These checks measure copy and transport behavior. Participant judgments of clarity and timing require the designated live discussion.

## Independent review and specialist experiment

Review added negative controls for a transient rejoin, a historical marker on a healthy instance, a late acknowledgement, an independent repeated request, a revoked audience, overlapping speakers and bot echoes. Keep the incoming human message intact when a full native prompt leaves insufficient space for receipt hints. Test the ability to query those receipts separately.

A synthetic transcript included two identically worded human requests, one published success, and an append with an unknown outcome. Three independent low-effort Codex trials per arm classified the state of each original request. With the Discord transcript alone, zero of nine classifications matched the underlying receipts. Adding exact synthetic publication, pending-job and uncertain-write references produced nine of nine matches. Median elapsed times including CLI startup were 3.44 s and 4.02 s respectively. This is one adversarial fixture repeated three times, with a known answer, not a production error-rate estimate. A Haiku comparison could not authenticate. The matched evidence arm used a confirmed publication fact supplied in the fixture; local scheduling alone cannot establish that fact. Repeated trials and inspection of the actual effect are consistent with [Anthropic’s agent evaluation guidance](https://www.anthropic.com/research/demystifying-evals-for-ai-agents).

A second synthetic set scored six decisions per trial (AFK, a new identical request, bystander chatter, a redelivered request, a held draft and a worker needing input). Both arms matched 18 of 18 expected actions over three trials each. Median CLI times were 4.60 s without explicit receipts and 4.07 s with them. No improvement in decision accuracy was observed on this set.

On the clean v0.29.9 baseline, the hosted Codex test with synthetic Discord recovered from an actual manager death in 33 s, with one worker attempt, one live replacement and one completion. A preliminary run passed the wrong fault selector and stopped before injecting a fault. The valid baseline run took 112.85 s. The baseline mock Discord package passed three complete repetitions in 114.17 s. Live read-only Office status confirmed one active discussion with exactly one uncertain native job. The Mac and Office configured connector inventory contained only their personal installations.

## Failure injection, native evidence and rollback

The supervisor regression used a real replay ledger with a conflicting native owner and a second failure validating the former login. The v0.29.9 fixture lost the refusal behind the login error. The candidate retained the refusal for the exact instance, replay ledger, discussion and native thread. Separate tests covered a locked ledger, a displaced observer, a returning healthy observer, a changed instance, a live controller lease and a durable draft. The focused race run passed in 18.136 s.

In the first native replay-refusal trial, an original worker remained while its supervisor restarted. The old manager retired and one successor launched 0.808 s later. The successor read the original worker’s evidence but treated the worker-to-manager mailbox acknowledgement as if Discord already had the result. It produced a terminal final without publishing the worker report. The 222.04 s rehearsal failed. This trial motivated making the completion wake distinguish the two deliveries.

In a separate rollback rehearsal the original worker and job attempt survived a candidate-to-v0.29.9 supervisor switch. The v0.29.9 test runtime correlated the completion to the same attempt and observed one child and one active dispatcher. Total time was 124.53 s with synthetic Discord. This exercised a binary rollback in scratch, independent of either production service.

In the first hosted shared-thread rehearsal, the dispatcher and original child exchanged a question and answer, then the manager parked. After wake, the native tool host twice timed out opening a session while the dispatcher tried to verify the result. The expected publication missed its deadline. The test retained its scratch evidence. Treat this as a failed trial even though the worker had produced the result.

## Discord input stalled before and after native admission

In the owner-reported Mac incident the connector posted a queued notice six seconds after the human request. The native Codex transcript recorded the request about 231 seconds after ingress. Both acknowledgement and final publication then received the bridge's unconfirmed-action error. A status check during that turn reported unresolved direct terminal input. The owner's later native input woke the dispatcher, but the subsequent publication attempt also failed. The later Discord complaint appeared in the native session only after reboot. The marker classified input as a draft during an active turn. Its contents and origin are unknown. Preserve that draft and any uncertain side effects. The older manual close, replacement and replay incident has a different timeline.

After restoring the worker-report changes, an isolated personal-mode rehearsal restarted the supervisor while the worker waited on a release file. The same attempt published its marker 40 seconds after reconnect, with one child and one active dispatcher. Total rehearsal time was 128.65 seconds. The rehearsal called the manager runtime with synthetic Discord input. The live Gateway, human sender and authenticated Discord result are separate gates. See the [shared qualification procedure](dispatcher-resilience-shared.md).

The recovered candidate at `6bbfdbcd` passed the same native replay-refusal rehearsal on macOS. It used a real Codex manager and child, a private supervisor and synthetic personal Discord transport. The worker's question appeared after 44 s and the held worker received the exact answer 5 s later. The supervisor restarted with that worker active. The former manager exited and the replacement published one report containing the worker's proof for the original job and attempt, 24 s after reconnect. The roster contained one child and one active manager at the end. The rehearsal passed in 93.38 s. The first failed trial above and this passing trial each represent one run.

## Reboot recovery and stronger restart controls

The durable owner ledger places about 212 seconds of the Mac incident between minting the launch operation and recording the owner launch. The original human admission later completed with zero stored publications. Neither rejected publish call produced a durable publication to send. The later complaint completed after reboot with one interim and one final. A completed native turn is insufficient evidence of a Discord reply. No preserved record identifies who or what generated the draft.

A hosted shared-mode supervisor restart answered a fresh synthetic Discord challenge in 29 seconds while the original worker remained active. The original attempt subsequently published its separate result in 13 seconds. Through native turn settlement the harness counted one challenge answer and one worker result. One child and one active manager remained. Total time was 124.55 seconds. A personal-mode trial answered its challenge in 24 seconds but failed the test's worker identity check. The strengthened test compares the worker's roster ID, attempt and incarnation before and after restart.

Two earlier personal-mode rehearsals timed out waiting for the worker result. One successor explained in its native transcript that the available history contained only an earlier message naming the fixture. The old fixture omitted the human's delegation request. The durable rehearsal now retains the synthetic human requests it admitted and verifies their IDs, revisions and authors. It returns no confirmed Discord message IDs. These changes let a successor recover its actual instructions while keeping the evidentiary limit of synthetic transport visible.

Independent review reproduced duplicate scheduled finals when the same enrolled worker's completion note and full native result reached the dispatcher in different turns. Both arrival orders produced two finals in the pre-fix store. The candidate reuses the scheduled publication only when the scoped worker evidence and final text agree. Changed evidence, changed text, steering and fresh human requests remain actionable. The regression group passed three times under the race detector in 15.876 seconds.

### Integrated candidate

The final hosted pair used binary revision 59f4e084. Its production code is identical to the staged Office build at 6d2b6fac. The intervening change strengthens the test’s before-and-after identity check. Both runs restarted their private supervisor, verified the original worker session, attempt and incarnation, admitted a new synthetic human challenge, then reopened the connector for the worker’s separate result. Neither run injected terminal input.

| Synthetic surface | Challenge reply | Worker result after reconnect | Substantive challenge / worker publications through settlement | Total test |
| --- | ---: | ---: | ---: | ---: |
| Personal DM | 30 s | 17 s | 1 / 1 | 134.91 s |
| Shared thread | 34 s | 18 s | 1 / 1 | 133.89 s |

Each final roster contained one child and one active dispatcher. The full Gateway and Discord POST path, real human author and independent Discord message IDs remain live acceptance criteria. Five race-detected repetitions of degraded connector, handoff and supervisor cases passed in 40.965, 22.650 and 23.825 seconds per package, respectively.

## Repository gates on the integrated candidate

The frozen production code at `6d2b6fac` passed `go build ./...`, `go vet ./...` and golangci-lint on macOS. Lint reported zero issues with a fresh analyzer cache. Both full test lanes remained red when the audit ended on November 21, 2026 at 10:03 UTC. The host ran Go 1.26.5 darwin/amd64 on Apple silicon with umask 022, two packages at a time and a 35-minute per-package timeout.

| Original macOS run | Passed | No test files | Failed | No result before stop | Total |
| --- | ---: | ---: | ---: | ---: | ---: |
| Normal | 92 | 5 | 3 | 4 | 104 |
| Race | 75 | 5 | 5 | 19 | 104 |

The normal run reported failures in `deploy/gcp`, `internal/skeleton` and `mobile`. The race run reported failures in `internal/persist`, `internal/shim`, `internal/skeleton`, `internal/testbin` and `internal/transcript`. Separate same-architecture retries passed the full GCP normal package in 0.585 s and the full persist race package in 5.186 s. Those retries are excluded from the original counts.

Several subprocesses consumed no measurable CPU for minutes before entering Go. Stack samples showed a single startup thread in Rosetta, and a separate native arm64 Go version probe stopped in dyld before main. The skeleton supervisors were stopped after helper processes accumulated. At audit close both gate process groups and their tracked test scratch roots contained zero running processes.

| Affected package | macOS normal | macOS race | Office Linux race |
| --- | --- | --- | --- |
| `cmd/swarm` | Passed, 219.245 s | Passed, 179.833 s | — |
| `internal/discord` | Passed, 58.137 s | Passed, 72.298 s | — |
| `internal/teammatemanager` | Passed, 1.504 s | Passed, 3.925 s | Passed, 1.039 s |
| `internal/voice` | Passed, 89.706 s | No result before stop | Passed, 41.841 s |
| `internal/skeleton` | Failed | Failed | Stopped at disk reserve before test execution |

Office fetched the public commit archive directly from GitHub and ran Go 1.26.9 linux/amd64 in a private memory-backed checkout. Its first complete shim race run failed one test in 211.924 s. The SSH environment had umask 0002, which permitted group writes on the temporary spool parent. The exact dead-producer identity test passed in 2.429 s with umask 022. That single-test rerun leaves the complete shim race gate open. The disk guard stopped the later skeleton build when the shared-cache volume reached 1,011 MiB free, before the planned mobile retry. The Office rehearsal left no process in its scratch directory and the directory was removed.

## Live personal DM on Office, 2026-11-21

The candidate connector at clean revision `d6953901` took over the original personal DM ledger and the existing 0.29.9 supervisor. A cold copy of the ledger preserved the pretrial evidence. The candidate reconciled the original DM boundary with zero pending messages, the same generation-12 manager, and the existing uncertain worker attempt.

At 10:51:22.797 UTC the enrolled owner asked for 37 times 14 and the word “mulberry.” The bot replied “37 times 14 is 518, and the word is mulberry.” at 10:51:31.630, a latency of 8.833 s. Across 48 samples through 10:53:34.746 UTC, only that human message and one substantive bot reply appeared beyond the original boundary. The native external input counter stayed at zero.

The original connector resumed the same advanced ledger. The production supervisor PID, manager identity, generation and earlier uncertain job were unchanged. The cold posttrial ledger retained all 47 historical publications with their original Discord IDs and dispositions. It had one additional publication with matching REST and Gateway confirmations. This was a pre-release personal connector trial on the installed supervisor.
