# Account onboarding and terminal layout: v0.15.1

Status on 2026-10-04: **v0.15.1 published and deployed on the VM**. Independent UI review, all local repository gates, required release checks and installed navigation checks pass. Actual quota-triggered native continuation remains acceptance work.

## Behavior and source

After adding an account, Accounts distinguishes **Ready** authentication, assigned discussions and quota that has not yet been observed. Provider summaries explain opt-in assignment and whether a second ready account exists; a possible destination does not establish available quota or requested-model capacity. Previous failed sign-in attempts are labeled separately from saved accounts.

Wizard controls describe the action available at the current step. Losing readiness returns the wizard to the current sign-in state and blocks admission. Waiting and cancellation states do not offer a stale terminal attachment or an unavailable next step. Retry resets scrolling; unchanged terminal polling preserves the user's scroll position. The composed Accounts page uses its actual body viewport with margins, selection visibility and bounded scrolling for long details and sign-in text.

The final Go source is commit `b94259ca09fbd8700bf94cdd4fc6b4bf602bb315`, following UI commit `b096363d594397db204a2664f41907159141fb5f` and two behavior-preserving lint corrections. Its 1,640 tracked Go files have canonical JSON path-to-SHA256 inventory digest `5692e8853f90793e56207c8a3f49f989ed99e4ca906f1e24702c3bfcfce665b5`. Android release identity is 0.15.1/code 48. Account assignment, credential storage, backend recovery and compatibility axes are unchanged.

[PR #42](https://github.com/Nathandela/swarm/pull/42) merged normally to main commit `84f531a9b93acbabce588d22e9ac33ace9bb7d4a`. Its complete tree equals the verified source tree `294da083a5f65f2e1a22bc60bac45bc165bea3c1`. All four exact-head workflows passed: [push CI](https://github.com/Nathandela/swarm/actions/runs/37187688312), [pull-request CI](https://github.com/Nathandela/swarm/actions/runs/37187800958), [push container checks](https://github.com/Nathandela/swarm/actions/runs/37187688307) and [pull-request container checks](https://github.com/Nathandela/swarm/actions/runs/37187800963).

## Independent review and regressions

Sol implemented the Accounts changes and flow tests. Root added the composed-board viewport regression. Astra independently tested 48×14, 80×24, 120×32 and 25×7 terminals, skew notices, long URLs, Unicode, focus and paging, all six wizard steps across six enrollment states, readiness revocation, stale replies, cancellation, terminal detachment, clients without a native runner and side-effect-free rendering. The independent overlay race selection passed in 2.893 seconds. Astra subsequently checked both lint corrections for behavioral equivalence and reported no blockers.

The readiness regression failed against the earlier implementation by retaining a stale authentication address. The composed-board regression failed against the previous View sizing at 48×14 because the Accounts body lacked its left margin. Both are retained as failing-first evidence; neither expected counterfactual failure is a release-gate success.

Repository regressions cover truthful readiness and unknown usage, one-account versus two-account guidance, contextual controls, revoked readiness, retry targets, paging/focus and the complete board/footer layout. These exercise state changes and rendered bounds.

## Repository verification

Build, vet, lint and strict documentation checks pass. A secret scan of the eight changed implementation/documentation files at the frozen source reports zero findings. The complete normal suite passes in **141.88 seconds**, and the complete race suite passes in **249.06 seconds**. Both pass all 75 tested packages, with eight packages without tests, on the exact Go inventory above.

All Go checks clear inherited `SWARM_*` variables and use task-owned build caches with bounded concurrency. Initial full-suite attempts exhausted root disk space and do not establish success. Inactive task-owned investigation and committee checkouts were preserved on the data disk. A temporary data-disk `TMPDIR` then placed synthetic-HOME fixtures beneath the real owner's Claude settings; the existing ancestor guard correctly refused those unfamiliar project sources. Astra confirmed this through source and file metadata without reading credentials. Final verification uses ordinary `/tmp`; no configuration guard was weakened.

The separate [scheduled stress run](https://github.com/Nathandela/swarm/actions/runs/37189068958) failed before callback tests executed because its obsolete `mobile/conformance` package is absent. Astra confirmed the workflow and mobile tree are unchanged from v0.15.0. Current dispatcher overflow, panic and lifecycle tests are included in the full repository race suite, but this run does not establish elevated-count callback coverage or equivalence to the deleted slow-callback harness. Bead `swarm-qgl` tracks repairing both the package path and obsolete selectors. This failed nightly is separate from the successful required release gates.

## Authenticated acceptance boundary

The owner added two personal Claude accounts through the installed v0.15.0 native sign-in flow. Nonsecret registry metadata shows both enabled and authentication valid, with native-login provenance and Claude assignment enabled. At that observation, no managed discussion was bound and no quota scope had been observed. This establishes two native admissions, not actual model continuation or quota-triggered switching.

`swarm-2xm.2` remains open for actual continuation after switching, concurrent native refresh, effective configuration and complete native history, Claude safe-turn recovery and two-account Codex acceptance. Token-only execution, cached imports and migration of older unmanaged discussions remain deferred as documented in the [operator guide](../operations/account-pools.md).

## Published release and VM activation

[v0.15.1](https://github.com/Nathandela/swarm/releases/tag/v0.15.1) was published at 08:35 UTC on 2026-10-04 from main commit `84f531a9b93acbabce588d22e9ac33ace9bb7d4a`. [Release run 37188493669](https://github.com/Nathandela/swarm/actions/runs/37188493669) passed all 17 jobs, including reused CI, container publication and signed binary publication. [Main CI](https://github.com/Nathandela/swarm/actions/runs/37188450864) and [main container checks](https://github.com/Nathandela/swarm/actions/runs/37188450841) also passed. The separate failed nightly is recorded above.

The installed upgrader verified the official archive's signed checksums; both staged binaries' build metadata matches the release commit. The compatibility card retains account schema/jobs/inventory 1, recovery/worker/shim/configuration 2, session schema 2, shim wire 1 and protocol 1. Activation retained a v0.15.0 backup. Unattended restart deferred for active discussions; the authorized supervisor-only restart used its original saved environment, and convergence used the verified owner systemd bus. The pending marker is absent.

At 08:39 UTC, CLI and responding daemon both report 0.15.1 and the loaded daemon matches the installed verified binary. All nine doctor checks pass. All 155 prior saved discussion IDs, schemas, agent types, conversation identities and account bindings/projection references are retained. All eight previously live shims retain PID and start ticks, match their saved process identity, and remain non-zombie processes. The saved daemon environment is unchanged.

Both owner Claude accounts retain their schema, generation metadata, identity and label digests. Provider settings remain Claude enabled and Codex disabled. These are nonsecret metadata comparisons; credentials were not read for these checks. Installed binary SHA-256 values are `5bb6a19be5e6f47b6fbb08aaf5c03e7a9497ecf1f4166d870e3ef5f0d8ece412` for swarm and `a0c206da0d220dadda3b0e1bbf9f6deb1f265907715f8aa3580eaaebecb8e5e0` for swarm-remote.

The installed TUI opens Options → Accounts → Add account and shows the six-step provider/method flow for native Claude and Codex device-code sign-in. Its account page displays Ready, unknown usage, a contextual next step and the explanation for two ready accounts with unconfirmed backup capacity. Navigation stopped before authentication, exited cleanly, and preserved owner account metadata and provider settings. The final navigation check again passed all nine doctor checks. No model request or discussion replay was initiated by this verification.
