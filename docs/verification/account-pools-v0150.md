# Personal account pools: implementation evidence

Status on 2026-10-03: **implemented in an isolated feature branch; v0.15.0 is unreleased and production acceptance is incomplete**. Neither a plan review nor synthetic account fixtures establish authenticated subscription capacity or native continuation.

The implementation extends the existing per-user daemon, private native profiles, immutable discussion bindings and authwatch recovery authority. The user flow is **Options → Accounts → Add Account**. Assignment is opt-in separately for Claude and Codex; healthy discussions retain their account. Recovery preserves the verified effective model, conversation, configuration and worktree and asks the owner to retry interrupted work without replaying a prompt or tool call.

## Source and supported native versions

The feature branch is `feat/account-pools-v0150`, based on main revision `f4c3c2fbc1c741d732453bf413936f7f2ff21896`. The supported Linux native binaries are Codex **0.160.0** and Claude Code **2.1.288**. Native-version and binary-identity checks apply to the actual launch, not only a daemon-startup probe.

Fresh isolated native sign-in is available for both providers. Cached imports remain unavailable: a cached account identifier is not authenticated proof that the accompanying credential belongs to that account. Claude's one-year environment token also remains unavailable; its token-only identity and effective policy have not been characterized, and this implementation does not provide a manual token launch route. Durable credential provenance prevents imported credentials from becoming automatic capacity through reverification or registry reload.

Managed discussion metadata uses schema 2. Optional owner-local capability `accounts.manage.v1` remains additive to wire version 1. Account registry, enrollment, recovery, worker, shim and configuration compatibility axes are checked during supported activation and rollback, including states with no discussion metadata.

## Verification boundaries

Local tests cover private path and credential custody, registry CAS and durable writes, enrollment writer containment and cancellation, immutable launch bindings, quota normalization, sticky selection, bounded fallback, persisted recovery, owner cancellation, input embargo, history transfer fixtures, configuration projection, reconnection and upgrade compatibility. Real child-process fixtures exercise detached enrollment and both CLI/backend launch paths with synthetic credentials.

Astra independently reviewed implementation iterations and reproduced defects with external Go overlays. Fixes include sparse quota evidence, stale registry reservations, restart claim redrive, native binary/configuration checks, credential provenance, retained generation isolation, enrollment death proof, native model precedence and recovery journal validation. The review verdict remains **hold for required acceptance**.

The complete repository run also exposed a first-run `swarm refresh` regression: an absent state directory was treated as invalid account history. The anchored reader now preserves genuine absence while rejecting unsafe existing paths. A pre-existing CLI identity test rewrote a same-size fixture in place and relied on timestamp resolution; the fixture now uses an atomic rename, matching the documented observation contract without changing production fingerprint behavior.

All verification commands clear inherited `SWARM_*` variables to isolate tests from running user sessions. Go checks use a task-owned build cache and bounded package concurrency. Development preview state is separate from the installed daemon, credentials and binary.

## Required acceptance

Discovery found one usable Claude profile and one usable Codex profile. Standard and runtime locations alias the same credential files. Historical Claude identity caches without matching credentials do not establish another usable login. No token values, account emails or native device codes belong in this evidence record.

The following requirements remain tracked in Beads and are not passing acceptance claims:

| Bead | Required evidence or behavior |
|---|---|
| `swarm-2xm.2` | Two distinct personal accounts per provider; actual post-switch model continuation; concurrent native refresh; effective configuration, trust, permissions, hooks and MCP isolation; complete native conversation assets; Claude safe-turn recovery. |
| `swarm-2xm.3` | Safe owner-reviewed migration of pre-pool discussions with proven writer ownership, configuration and history. Current unmanaged migration is explicitly refused. |
| `swarm-2xm.4` | Complete native credential cache inventory and writer/reference drainage before actual credential erasure. Retirement currently prevents new assignment and retains credentials. |
| `swarm-2xm.5` | Characterized token-only manual execution or authenticated import identity. Current methods are unavailable. |

These requirements must be satisfied, or their scope explicitly changed by the owner, before a production-ready v0.15.0 claim, main merge, release tag or deployment. See [the operator guide](../operations/account-pools.md), [ADR-028](../adr/ADR-028-personal-account-pools.md) and [the protocol contract](../specifications/protocol.md) for the implemented behavior.
