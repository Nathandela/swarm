# ADR-028: Personal subscription account pools

**Status:** Implementation in progress for planned, unreleased v0.15.0; authenticated release gates remain open
**Date:** 2026-10-03

## Context

Swarm currently inherits one provider login for a discussion. A provider login that reaches quota or loses authentication can strand that discussion even when the owner has another subscription for the same provider. Replacing credentials under a running process would also change the account identity without the discussion lifecycle knowing about it.

The existing auth-recycle lifecycle in [ADR-024](ADR-024-auth-recycle-on-relogin.md) owns guarded recovery. Account pools must extend that lifecycle and preserve its owner-operation and process fences.

## Decision

Store multiple owner-local Claude and Codex subscription accounts in the stdlib-backed private registry owned by `internal/accounts`. Each managed discussion has an immutable `Binding` to a provider, logical account, and profile generation before launch. Resolve credentials from that frozen binding for both the CLI and its backend. Never persist bearer credentials in session metadata, launch environments, argv, or account job progress. A bound session uses managed metadata schema 2; ordinary unmanaged records remain schema 1. The binding freezes provider, account id, credential generation, identity digest, and configuration generation. The managed schema distinction ensures an older build cannot silently run a session whose account binding it would ignore.

Automatic assignment and rotation are opt-in per provider. Adding an account does not move existing discussions. Moving selected discussions uses guarded recovery and never edits the identity of a running process. Keep a healthy discussion on its assigned account. When that account cannot serve the requested model, select an eligible account from the same provider, preserving the model, permissions, worktree, and conversation. If no safe destination is known, hold the discussion for operator action; do not switch providers or models.

Account enrollment uses fresh native provider authentication in an isolated private profile. Fresh native Claude personal OAuth and Codex device-code sign-in are implemented. Cached profile imports for both providers are unavailable pending native authenticated proof that the credential belongs to the identified account; cached account identifiers and credentials are separate evidence, and pairing them cannot prove authentication or distinct pool capacity. Import, reauthentication, or reverification cannot promote a cached profile without durable provenance and native authenticated proof. The environment-provided one-year Claude token is also unavailable because it cannot authenticate identity offline and its effective remote policy is uncharacterized; it provides no manual launch route. The owner-facing protocol surface is `protocol.Client.Accounts(AccountsReq)`, implemented through the optional `AccountsBackend.Accounts` seam, with `AccountsReq` / `AccountsReply` carried by operation `account_manage` in `internal/protocol/server.go`. Clients negotiate capability `accounts.manage.v1` before enabling the page actions. A detached worker in `internal/enrollment`, started through `swarm internal account-enroll`, may prepare a candidate but cannot admit it; the daemon verifies identity and commits admission. Supported provider/method flags are advertised by the daemon. Keep ownership narrow: `internal/accounts` stores and validates private profiles and registry state; native adapters normalize provider evidence; the UI presents state but does not select eligible accounts; the shim resolves its frozen binding but does not choose a replacement; the worker never admits an account. Cancel and removal are durable state transitions. Retiring accounts receive no new assignments. Credential deletion waits until bound processes, reservations, and enrollment writers release their references and a complete native cache inventory is available; retiring credentials remain retained until then. Removing an account does not erase discussion history or revoke the provider login.

Quota and authentication are observation inputs, not reasons for background model calls. Unknown or stale quota remains unknown. Native credential refresh with the same verified identity is not account rotation. An identity change quarantines the profile for new launches and routes its bound discussions through guarded recovery.

Restoring a conversation does not retry the failed request. The user must resubmit it; Swarm never replays a prompt, tool call, or unresolved provider effect as part of account recovery.

The installed Claude environment's one-year token is unavailable: it cannot authenticate identity offline, and its effective remote policy is uncharacterized. It provides no manual launch route. Cached profile imports for Claude and Codex are also unavailable until native authenticated evidence proves the credential belongs to the identified account. Cached identity fields and access/refresh credentials are separate evidence; pairing them or discovering a profile does not establish authentication or distinct pool capacity. Any other credential mode without verified account identity stays ineligible for automatic pools. Any optional access check is explicit, uses the selected model without tools, and is not a quota polling mechanism.

Implementation does not close the release gates. Authenticated acceptance remains outstanding for two accounts per provider; continuation, native credential refresh, effective configuration, and history transfer; and a Claude safe-turn after account recovery. Existing unmanaged discussions still refuse migration when original writer and configuration proofs are missing. A planned v0.15.0 release requires these behaviors to be accepted and recorded; a plan review or transport-only test does not satisfy the gates.

## Compatibility and rollback

The `accounts.manage.v1` capability is optional, owner-local, and negotiated only when the daemon has an account backend. A client without it has no account mutation surface. Preserve the existing unmanaged launch path and ambient login behavior when a provider pool is disabled. Account bindings are durable launch metadata, and account state is not disposable: activation and rollback refuse a binary transition that cannot safely interpret live bindings, account records, enrollment workers, or running account-bound shims. Do not bypass a refusal by deleting the registry.

The signed `compat.json` card now carries account schema, enrollment-job, recovery, worker, shim, and account-configuration versions (`account_schema`, `account_jobs`, `account_recovery`, `account_worker`, `account_shim`, and `account_config`). Each is version 1 in this release contract; managed session metadata uses the separate `schema` value 2. The activation and rollback guards inspect persisted account state and refuse when the target card cannot preserve it. The client/daemon protocol and daemon/shim wire versions remain 1 because these additions are capability-gated and additive. Do not bump `shimwire.Version`: it is shared with running shims and a bump makes them appear lost during daemon upgrade.

## Consequences

**Positive:** Each discussion has an explicit credential owner; healthy sessions remain stable; quota failure can use another verified account while preserving conversation state; enrollment and credential removal have clear ownership.

**Negative:** The daemon now owns private account profiles and a registry whose compatibility must be checked during upgrades and rollback. Provider-native quota and identity evidence is incomplete for some credential methods, so those methods remain unavailable unless verified. Real authenticated acceptance is required before enabling automatic rotation broadly.

## Alternatives considered

- Switch a shared provider auth file globally: rejected because live processes can observe another account without a durable per-discussion binding.
- Add a second restart loop beside the discussion recycler: rejected because two lifecycle owners could race with user operations and history writers.
- Retry the interrupted prompt automatically: rejected because the provider may already have applied an effect before the connection failed.
