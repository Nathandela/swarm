# Dispatcher worker note qualification

The release candidate uses ordinary Swarm model defaults. The plan was reviewed independently by Sol and Astra at `2d59e59f` in [PR #86](https://github.com/Nathandela/swarm/pull/86).

## Isolated native rehearsals on 20 November 2026

Each run used a real native manager, provider accounts and supervisor, a temporary project, and a synthetic Discord sink. The worker alone read a random question marker and result. It retrieved the manager's exact answer by note ID inside its running turn. The manager parked while the worker remained live. The connector stopped, the worker recorded its queued completion receipt, and the connector restarted. The result reached the original discussion with the original job and attempt without a second human message.

| Worker | Question publication | Answer observed in worker | Manager parked | Completion after reopening |
| --- | --- | --- | --- | --- |
| Codex | 43 s | 13 s later | 2 min 22 s later | 14 s |
| Claude | 63 s | 27 s later | 2 min 8 s later | 26 s |

The Codex run produced publication `publication:5:81b24ddb8948403fa3409d1c20b8f6bd484808d2559efa28d21093eb1831fbbc`. The Claude run produced `publication:4:e755141183ba55cab5b416720b0da4a5c15c5d71f89e1b65fa6ad170923916f0`. These are connector publication IDs. Discord assigns its own message IDs on actual delivery.

A separate upgrade trial started a Codex worker on v0.29.5, replaced the supervisor, retained shim PID 2514881, and issued the first semantic send with a cold identity cache. Its exact native completion turn was `01a1fa59-31ee-7b80-be3f-7708775b80e4`. The fresh-session and expired-cache cases completed in turns `01a1fa59-68cb-7083-b397-a3d2fbc88086` and `01a1fa59-7dc4-72b0-be45-a27fc6084631`. Replaying each operation ID produced one native completion for its random marker. The generic receipt still reported submitted, so the native transcript supplied the execution evidence.

A fresh Codex session discovered the guide through top-level CLI help. A fresh Claude session discovered the bundled skill in a clean workspace and followed its reference to the command. Both named the two notification kinds and the reply command without receiving a skill path or subcommand in the discovery prompt.

A second native run for each worker required the publication to reference an actual worker-message admission. It checked the note IDs, worker incarnation, job and attempt in the status ledger and verified that the manager exported an answer. Both runs passed.

| Worker | Needs-input admission | Completion admission | Worker incarnation | Result after restart |
| --- | --- | --- | --- | --- |
| Codex | 3 | 5 | `e0d82e9303f1730eaf392778282ea5c3` | 14 s |
| Claude | 4 | 5 | `5e5745e335b457c026449d74b1130274` | 14 s |

To repeat with a trusted scratch workspace and a CLI profile authorized for the candidate, put the candidate `swarm` first on PATH. Set `SWARM_TEAMMATE_TEST_BINARY` to its absolute path, `SWARM_TEAMMATE_TEST_MANAGER_DIR` to the workspace, `SWARM_TEAMMATE_TEST_VM=1`, and `SWARM_TEAMMATE_TEST_DURABLE` to `codex` or `claude`. Run `go test -count=1 -v ./internal/teammatemanager -run '^TestHostedTeammateLifecycle$'`.

A live v0.29.5 Codex dispatcher also exposed a migration boundary. Even after its published turn had settled, replacing its supervisor while the dispatcher process was still live left the earlier observation owner unproven. The supported upgrade trial parked the original manager on v0.29.5 in 2 min 1 s before replacing the supervisor. The candidate resumed the recorded provider conversation under the same logical discussion. The worker question reached the dispatcher in 33 s, the worker read the answer 7 s later, and the completion was published 14 s after a further park and connector restart. The worker incarnation was `222f2bce7830561992830123385d4910`. The question and completion were admissions 4 and 6.

Personal DM regression from macOS v0.29.5. TestDiscussionPersistsHumanIngressBeforeFencingRetirement reopens the discussion and verifies that fresh owner ingress cancels an unclaimed retirement before native admission. A claimed retirement keeps its fence. TestDiscordDraftSurvivesReconnectAndFencesNativeAdmission restores an unresolved terminal draft and checks that native admission cannot overwrite it when proof is unavailable. TestPersonalInputHoldReconnectAndOrderedRecovery starts with one connector row sending and the next prepared. It covers both a missing admission and a lost admission acknowledgement, reopens the store, verifies one private actionable delivery notice, then releases the hold and verifies both original messages arrive in order once. These tests run on Linux. The original macOS incident remains a field report.

In a candidate-to-candidate active supervisor restart, the original native manager entered a visible custody hold. The operator exited that exact isolated manager. The successor retained the logical discussion and emitted one continuity notice when the native history changed from `01a1fa6e-d6a1-7453-a278-d61a066f5bf2` to `01a1fa6f-0504-7173-b04d-5390610bbeb4`. Its early worker question arrived in 54 s and the worker received the exact answer 10 s later. The manager parked while the same worker remained active. Completion arrived 16 s after reconnect. The two note admissions were 4 and 6 for worker incarnation `2ca11e0e5ce96287c851fed62bbfdad2`.

TestDiscussionLateDraftRefusalPreservesExactFIFO covers a draft appearing between preflight and submission. The original rejection receipt proves no effect, allowing the same admission to obtain a new operation ID. TestDiscussionUnconfirmedEffectReportsFirstAdmissionWithoutResending covers the opposite case. A missing effect receipt holds the original admission and its successor, identifies the first message as uncertain, and prevents blind replay. The release workflow also runs the personal DM, draft and uncertainty regressions on its native macOS runner.
