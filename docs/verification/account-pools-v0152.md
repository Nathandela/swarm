# Account usage reads: v0.15.2

Status on 2026-10-04: **v0.15.2 published and deployed**. This record covers quota and usage reads for verified native account profiles. It does not claim quota-triggered discussion switching or provider acceptance for Codex.

## Behavior and safeguards

Swarm reads reported usage for verified native Claude and Codex accounts after admission and at daemon startup when a reading is due, then about every five minutes. The read uses the current verified account profile and a fixed HTTPS GET. It does not start a provider CLI, make a model request, refresh OAuth credentials, or write credentials. Usage reads continue while rotation is off or an account is paused; retirement cancels its read.

Usage is telemetry only. Recording it does not advance native feed sequence, change permissions, clear a recorded denial, or prove that a requested model is available. An unknown value remains unknown rather than being shown as zero. The UI retains the last successful reading while a refresh runs or after one fails, shows its age, and marks readings stale after five minutes or when the reported reset time has passed.

The Accounts list and details show fetch progress and errors, the last attempt, and the next scheduled refresh when available. Manual refreshes coalesce with an in-progress read and are limited to one every 30 seconds. Failed reads use bounded backoff and honor provider retry timing. An authentication refusal may require the owner to complete native sign-in again; this worker does not renew login tokens.

## Source and repository verification

The independently reviewed and verified source is commit `724879b2d5c8935eb666cd728edec06df87c06dd`, tree `019e81fbe08f7131c8f95918aedd014950387518`. Its 1,650 tracked Go files have canonical path-to-SHA256 inventory digest `578e1ec93c79978d5dfe801947d6de7802d88202d185e7ee4d1167f9f7494c75`. The release source is commit `a1237132797d764f8451f22eb99a62991e3a21b0`, with the same complete tree and Go inventory.

Build, vet, lint and strict documentation checks pass. The complete normal suite passes in **557.15 seconds** and the race suite passes in **684.75 seconds**; both cover 76 packages with tests and eight packages without tests. Pull-request CI passed on the reviewed source head. The merged source also passed [main CI](https://github.com/Nathandela/swarm/actions/runs/37202785464) and [main container checks](https://github.com/Nathandela/swarm/actions/runs/37202785452).

A secret scan of the 19 changed implementation files reports zero findings. Existing compatibility axes are unchanged: account schema, enrollment-job schema, and inventory version 1; recovery, worker, shim, and configuration version 2; session schema 2; shim wire version 1; and protocol version 1. Quota fetch state and scheduling details are additive transient response fields; they do not add a persisted account-state migration.

Astra independently reviewed the frozen source and synthetic regressions. Its six focused race regressions passed; the review found no remaining blocker in the bounded source and synthetic cases. The review covered protection against stale usage overwriting newer native evidence or clearing a latched denial, generation checks before credential reads and usage merges, and retirement cancellation that retains credential custody until the worker drains. It did not perform provider requests or native, authentication, or model actions.

## Provider evidence and acceptance limits

The production reader received HTTP 200 for two enrolled Claude profiles. This confirms those live usage reads at the recorded observation, not native model continuation or quota-triggered account switching. Codex parsing and the pinned request contract pass synthetic fixtures; no live enrolled Codex profile was available for acceptance.

Bead `swarm-2xm.2` remains open for authenticated two-account-per-provider continuation, quota-triggered switching, concurrent native refresh, effective configuration, complete conversation history, and Claude safe-turn acceptance. These repository and reader checks do not establish those behaviors.

## Publication and deployment

[v0.15.2](https://github.com/Nathandela/swarm/releases/tag/v0.15.2) was published at **12:56:21 UTC on 2026-10-04** from release source commit `a1237132797d764f8451f22eb99a62991e3a21b0`, whose tree matches the reviewed source above. [Release workflow 37202839039](https://github.com/Nathandela/swarm/actions/runs/37202839039) completed successfully with all 17 jobs, including reused CI, container publication and signed release assets. The release contains ten assets, including checksums and a detached checksum signature.

The verified staged pair was activated. The installed CLI and responding daemon both report v0.15.2, the daemon executable matches the installed binary, and the pending-converge marker cleared. All nine post-activation doctor checks passed.

The continuity check retained all 155 session IDs and discussion identities/bindings, along with the eight prior live shim identities. It also reported preserved provider rotation settings, account schema, and selected account identity/lifecycle/generation metadata. The check does not establish full registry equivalence or quota-state continuity. The installed-reader probe passed after activation: both enrolled Claude profiles had a fresh usage attempt and populated usage observations. No live Codex profile was available.

## Installed terminal acceptance

Read-only navigation through Options, Accounts and both Claude account details confirmed used and remaining percentages, provider-supplied reset times and observation age on the installed binary. An 80-column terminal revealed that the account list could truncate the weekly percentage: HTTP provider labels did not match the compact label aliases exercised by UI fixtures. Details retained both readings. A separate follow-up patch will add exact known-label aliases and regressions with production-shaped labels; v0.15.2 is immutable and is not being retagged. No settings, account state or sign-in action was performed by this UI check.
