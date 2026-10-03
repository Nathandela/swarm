# Personal account pools: implementation evidence

Status on 2026-10-03: **implemented in an isolated feature branch; v0.15.0 is unreleased and production acceptance is incomplete**. Neither a plan review nor synthetic account fixtures establish authenticated subscription capacity or native continuation.

The implementation extends the existing per-user daemon, private native profiles, immutable discussion bindings and authwatch recovery authority. The user flow is **Options → Accounts → Add Account**. Assignment is opt-in separately for Claude and Codex; healthy discussions retain their account. Recovery preserves the verified effective model, conversation, configuration and worktree and asks the owner to retry interrupted work without replaying a prompt or tool call.

## Source and supported native versions

The feature branch is `feat/account-pools-v0150`, based on main revision `f4c3c2fbc1c741d732453bf413936f7f2ff21896`. The supported Linux native binaries are Codex **0.160.0** and Claude Code **2.1.288**. Native-version and binary-identity checks apply to the actual launch, not only a daemon-startup probe.

Fresh isolated native sign-in is available for both providers. Cached imports remain unavailable: a cached account identifier is not authenticated proof that the accompanying credential belongs to that account. Claude's one-year environment token also remains unavailable; its token-only identity and effective policy have not been characterized, and this implementation does not provide a manual token launch route. Durable credential provenance prevents imported credentials from becoming automatic capacity through reverification or registry reload.

Managed discussion metadata uses schema 2. Optional owner-local capability `accounts.manage.v1` remains additive to wire version 1. Account registry, enrollment, recovery, worker, shim and configuration compatibility axes are checked during supported activation and rollback, including states with no discussion metadata.

The combined safety fixes introduce recovery, worker and shim compatibility version 2. Pending critical-event inbox files, per-discussion observation holds and detached native-check custody are inspected even when the registry is empty and no recovery journal exists. Missing writer-death proof after a supervisor crash holds history transfer; absence of a process group alone is insufficient.

## Verification boundaries

Local tests cover private path and credential custody, registry CAS and durable writes, enrollment writer containment and cancellation, immutable launch bindings, quota normalization, sticky selection, bounded fallback, persisted recovery, owner cancellation, input embargo, history transfer fixtures, configuration projection, reconnection and upgrade compatibility. Real child-process fixtures exercise detached enrollment and both CLI/backend launch paths with synthetic credentials.

Astra independently reviewed implementation iterations and reproduced defects with external Go overlays. Fixes include sparse quota evidence, stale registry reservations, restart claim redrive, native binary/configuration checks, credential provenance, retained generation isolation, enrollment death proof, native model precedence and recovery journal validation. The review verdict remains **hold for required acceptance**.

Subsequent committee findings are tracked explicitly. Bounded fixes preserve tried-account/history state after pre-visible write failure, refuse to adopt or stop an unrelated owner resume returned by launch deduplication, and prevent writing a journal beyond its readable inventory/size bounds. Initial fresh assignment supports an omitted native model while preserving unknown capacity and exact-model recovery requirements. Critical normalized account events are accepted durably before hook/spool acknowledgement and replay through the existing recovery authority. Failed native-event admission holds model authority across restart. Inbox evidence is retained until journal-parent durability is confirmed, including duplicate retries after a visible rename whose directory sync failed.

Recovery and manual resumes share exact stopped-writer verification. A manual resume checks every retained managed attempt for the same provider/conversation under the existing resume lock, after healthy-running deduplication. A clean startup failure can be resumed using an exact shim-generation proof published after all descendants are reaped; missing files after a crash do not establish safety.

The final frozen Go source is `42bcec4fa098c7635e33a11451be10fcaa260f7a`: 100 changed Go files and 1,624 tracked Go files. Its complete Go inventory hash, using canonical JSON of path-to-file-SHA256 entries, is `bed7d69de7906af97c2f6977667af05384b53e60d5cb29f9df03a7ed12c370b1`. Complete repository build, vet, lint, strict documentation checks, normal tests (**487.17 seconds**) and race tests (**560.65 seconds**) pass. Both full test runs pass all 75 tested packages; eight additional packages have no tests. Later evidence-only documentation edits preserve this Go inventory. Current branch CI is recorded on [draft PR #41](https://github.com/Nathandela/swarm/pull/41); CI and local source checks do not establish native acceptance.

Astra's final independent seven-package race selection and separate persistent-directory-sync-failure regression pass against that exact Go source. The bounded code review is accepted with no remaining reproduced defect from that pass; production release remains on hold. The external five-member committee reviewed the earlier public snapshot `c2944ea7fb5324950c9ec97c7f961cc90af75f7e`: one member returned findings, one exhausted quota and three timed out. Those failed review attempts are not approvals or consensus. Astra independently reproduced and verified the relevant findings and subsequent fixes.

The complete repository run also exposed a first-run `swarm refresh` regression: an absent state directory was treated as invalid account history. The anchored reader now preserves genuine absence while rejecting unsafe existing paths. A pre-existing CLI identity test rewrote a same-size fixture in place and relied on timestamp resolution; the fixture now uses an atomic rename, matching the documented observation contract without changing production fingerprint behavior.

Final combined checks also caught a rejected-hook diagnostic regression introduced by the authentication-only admission gate. The existing socket-level diagnostic/responsiveness test and both full suites pass after restoring the metadata-only rejection log. Production exported-symbol reachability passes with the ordinary strict configuration entry point and the authorized native-model entry point both used by their real launch paths.

All verification commands clear inherited `SWARM_*` variables to isolate tests from running user sessions. Go checks use a task-owned build cache and bounded package concurrency. Development preview state is separate from the installed daemon, credentials and binary.

The development preview was rebuilt from the frozen Go source and its empty private daemon restarted after verifying exact process ownership. The live TUI opens **Options → Accounts → Add Account**, displays the six-step wizard and offers Claude native sign-in and Codex device-code sign-in. The check stopped at method selection: no authentication job, discussion or model request was created. The preview reports `dev`; the installed VM binary remains v0.14.3.

## Required acceptance

Discovery found one distinct credential set for Claude and one for Codex. Standard and runtime locations alias or copy those credentials; this inventory does not prove current authenticated capacity. Historical Claude identity caches without matching credentials do not establish another usable login. No token values, account emails or native device codes belong in this evidence record.

The following requirements remain tracked in Beads and are not passing acceptance claims:

| Bead | Required evidence or behavior |
|---|---|
| `swarm-2xm.2` | Two distinct personal accounts per provider; actual post-switch model continuation; concurrent native refresh; effective configuration, trust, permissions, hooks and MCP isolation; complete native conversation assets; Claude safe-turn recovery. |
| `swarm-2xm.3` | Safe owner-reviewed migration of pre-pool discussions with proven writer ownership, configuration and history. Current unmanaged migration is explicitly refused. |
| `swarm-2xm.4` | Complete native credential cache inventory and writer/reference drainage before actual credential erasure. Retirement currently prevents new assignment and retains credentials. |
| `swarm-2xm.5` | Characterized token-only manual execution or authenticated import identity. Current methods are unavailable. |
| `swarm-2xm.15` | Reference-aware collection of completed native-check custody. Current bounded inventories retain proof and hold when exhausted; collection must not erase an uncertain writer or unlink a live credential lock. |

These requirements must be satisfied, or their scope explicitly changed by the owner, before a production-ready v0.15.0 claim, main merge, release tag or deployment. See [the operator guide](../operations/account-pools.md), [ADR-028](../adr/ADR-028-personal-account-pools.md) and [the protocol contract](../specifications/protocol.md) for the implemented behavior.
