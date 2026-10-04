# Account pools

Account pools let Swarm keep separate personal Claude and Codex logins and assign each new discussion to one account. Automatic rotation is opt-in per provider. Adding an account does not enroll existing discussions or change their current credentials.

> **Release status (2026-10-04):** v0.15.0 release is authorized; publishing and deployment are pending. The authorized scope is native-login account pools for new managed discussions. Real two-account and full-flow acceptance will be tested after deployment. Token-only Claude execution, cached credential imports, and migration of older discussions remain unavailable follow-up work.

## Manage accounts

Open **Options → Accounts**. From Accounts, use the account list to inspect authentication state, observed quota and reset times, last observation, and the number of discussions bound to each account. Unknown quota is shown as unknown. Quota observations come from provider or discussion evidence; Swarm does not make background model requests to poll quota.

Use **Add Account** and follow the provider, method, authentication, verification, and label/admission steps. The daemon advertises methods and availability. Codex supports fresh native device-code sign-in; its browser method is shown unavailable on this VM. Claude supports fresh native personal OAuth in an isolated profile. Cached profile imports for both providers and the installed Claude environment's one-year token are unavailable pending characterized authenticated identity. Cached account identifiers and credentials are separate evidence; discovery or pairing does not establish that a credential authenticates the identified account or prove distinct pool capacity. Import, reauthentication, or reverification cannot promote a cached profile without durable provenance and native authenticated proof. The environment token cannot authenticate identity offline, and its effective remote policy is uncharacterized; it has no manual launch route. Review the returned account identity before admission. An account is available for automatic assignment only after Swarm has verified its provider identity. A credential without verified identity cannot be treated as a second subscription.

Account changes take effect when saved and are separate from pending Options changes. Pausing an account prevents future assignment; existing discussions keep their binding. Retiring an account also prevents new assignment. Swarm retains its credentials while discussions, native workers, enrollment or recovery still reference them. After those references drain, it fences execution, verifies exact stopped-writer proof, and removes the characterized credential files for the supported Linux native versions. Unknown cache routes, unsafe files or uncertain durability keep the credentials retained. Accounts shows **Retired · account credentials removed** only after every retained credential generation has been erased. Discussion history is kept, and retirement does not revoke the login at the provider.

## Enable or move discussions

Enable **automatic rotation for new sessions** separately for Claude and Codex. This changes assignments for future discussions only. Account details offer **Move discussion here** for discussions with a verified private binding. Busy discussions can be deferred until the existing recovery safeguards allow a move. Discussions launched before account pools do not have the required writer and configuration proofs and receive a clear refusal; safe migration remains follow-up work. Swarm does not change an account underneath a running process.

New discussions can use the provider's default model. Until the native CLI reports its effective model, capacity remains unknown and a known denial is not bypassed. Automatic recovery waits for verified model evidence.

When a bound account cannot serve a discussion's requested model, Swarm may restore that discussion on another eligible account from the same provider. A healthy discussion stays on its account even if another account has more quota. The requested model, permissions, worktree, and saved conversation are preserved. Swarm does not change providers or models to find capacity.

After a request fails during a switch, Swarm restores the conversation and asks you to retry. It does not resubmit the failed prompt, replay a tool call, or repeat an unresolved action. An account with unknown or stale quota is not assumed healthy; if no destination can be established safely, the discussion remains held for operator action.

## Sign-in and recovery

Enrollment is separate from the discussion board and transcript and can continue while you leave Accounts. Return to Accounts to inspect or review its result. Cancel is explicit; leaving the page does not cancel sign-in. If the daemon does not advertise `accounts.manage.v1`, account mutations are unavailable until Swarm is upgraded. If the daemon is unavailable, account state is read-only until it reconnects.

For Codex, fresh device-code sign-in is performed by the native Codex app-server. Cached profile import is unavailable pending native authenticated proof that the credential belongs to the identified account. Subscription API keys are not pool accounts.

For Claude, enrollment uses a fresh native personal OAuth sign-in in an isolated Swarm-managed profile. Cached profile import is unavailable pending native authenticated proof that the credential belongs to the identified account. The installed environment's one-year token is also unavailable because offline identity cannot be verified and its effective remote policy is unknown; no manual launch route is supported for it.

Managed launches freeze the selected native profile and the supported project configuration boundary. Claude's unselected global settings are excluded when the worktree is below the user's home; project MCP files remain checked. Codex uses its nearest supported Git root, or the current directory when there is no root, and also checks the main checkout configuration for an ordinary linked worktree. Changed home paths or Git markers, unfamiliar Git layouts and unsupported project settings hold the launch rather than silently changing its configuration.

## Post-deployment acceptance and follow-up

The v0.15.0 release scope is native-login account pools for new managed discussions. Real two-account operation and the full flow—including continuation, native credential refresh, effective configuration, history transfer, and a Claude safe-turn after account recovery—remain post-deployment acceptance work. Existing unmanaged discussions still refuse migration when original writer and configuration proofs are missing. Token-only Claude execution, cached imports, and legacy-discussion migration remain unavailable follow-up work.

The existing release and recovery procedures remain authoritative for binary upgrades and rollback: see [automatic upgrades](../ops/auto-upgrade.md) and [release signing](release-signing.md). Account storage adds local credentials and bindings; before using an older binary, follow the compatibility result produced by Swarm and do not manually remove account state to bypass a refusal.

If a native supervisor crashes before proving that all its writers have stopped, Swarm holds the discussion instead of transferring history. A stopped parent or missing process group does not prove that detached children have exited. Review the held recovery state before retrying.

Completed native-check proof is collected incrementally only after exact stopped-writer proof and release of all memory and durable references. Cleanup retains its deletion authority across crashes and preserves the credential lock inode. Live, referenced, malformed or uncertain records stay retained. If custody cannot be verified or the bounded inventory remains exhausted, Swarm holds further checks. Do not delete that state to bypass a hold.
