# Discord teammate continuity in v0.29.2

## Implementation and acceptance plan

1. The native manager writes a brief, task-specific acknowledgement before investigating, delegating, or steering a worker. It can answer a simple question immediately. Verify that an early publication reaches the thread before work starts, a later result reaches the same thread, and a delivery retry preserves publication identity.
2. For a parked discussion, reconcile the original resume receipt. Permit one additional attempt only when the exact successor exited before accepting the follow-up. Reuse the retired source's native history, enforce custody in the supervisor, and reconcile the same attempt across restart. Ambiguous process or input evidence must hold work.
3. If native history still cannot resume after verified exit, reconstruct one manager for the existing discussion. Keep the pending message, worker and attempt identities, open questions, and publication receipts. Recheck audience and project scope. A fresh manager can use attributed thread history, scoped action references, and verified worker output. It may ask a fast worker to digest selected evidence. Summarization availability must never gate recovery.
4. Distinguish launch from native readiness. Surface bounded diagnostics on verified startup failure, and report a continuity gap when reconstruction succeeds. Include an upgrade test for the persisted v0.29.1 failure state.
5. Run focused failure and replay tests, the native lifecycle rehearsal with a local Discord publication sink, independent review, and release-tag CI. Publish from the reviewed main commit once the acceptance gates pass.

## Findings from the VM

The original v0.29.1 discussion retained the pending follow-up and the original manager history. A recorded launch pointed to a successor that exited before accepting input. Reconciliation kept selecting that exited successor. The surviving records do not establish why the Codex frontend exited.

In a private rehearsal using Codex 0.170.0 and a local Discord publication sink, the native manager wrote a task-specific acknowledgement about six seconds after admission. Worker creation followed about nine seconds later. These timings describe one run. The connector never supplies the acknowledgement text.

Failure injection targets only the parked manager's resume frontend and leaves the worker running. The first rehearsal exposed another early-exit case: the shim had persisted its identity but the provider had not reached capability discovery. Recovery now checks the recorded incarnation, exit, stopped writers, original launch receipt and empty manager input ledger. A missing or mismatched proof keeps the follow-up pending.

Two independent unmodified Codex 0.170.0 rehearsals reproduced a specific failure: the remote frontend rejected Swarm's explicit resume directory inside the test VM's nested workspace trust configuration. A separate A/B with the same thread and trust settings isolated the redundant directory flag. For automatic Discord manager continuation on this exact version, Swarm checks the saved native thread ID and workspace before attachment, then checks the resumed workspace before making the backend available. The fixed natural rehearsal completed a cold response, parked the manager, and answered from the resumed thread in four seconds. This identifies a reproducible cause on the VM. The original Discord incident has no surviving frontend error text to compare.

Manager-native tool timestamps also establish that the manager published both the opening acknowledgement and the untagged correction acknowledgement before issuing the corresponding worker command. A busy turn can delay when the manager sees the next message; there is no fixed acknowledgement time.

With the final version check and observer cleanup together, the natural native rehearsal took 33.7 seconds. Two physical manager instances shared the original native conversation ID, and the final answer belonged to the new human admission. The observer regression also exercises a failed backend dial and a disconnect before registration. In each case the still-running supervisor released the exited manager's input ledger so recovery could verify it.
