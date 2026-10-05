# Account pools

Account pools let Swarm keep separate personal Claude and Codex logins and assign each new discussion to one account. Automatic rotation is opt-in per provider. Enabling a provider covers compatible new discussions and explicit resumes of ended discussions on this VM. Running discussions keep their current credentials until they end or their existing managed recovery runs.

> **Release candidate (2026-10-05):** v0.16.0 adds native configuration preservation, shared native conversation history, existing-discussion enrollment on explicit owner resume, and actual coverage counts. Supported native contracts are Codex 0.160.0 and Claude 2.1.289; historical managed 2.1.288 discussions retain their previous compatibility contract. See the [v0.16.0 verification record](../verification/account-pools-v0160.md) for release, deployment and acceptance status.

## Manage accounts

Open **Options → Accounts**. From Accounts, use the account list to inspect authentication state, observed quota and reset times, last observation, and the number of discussions bound to each account. Unknown quota is shown as unknown. Quota observations come from provider or discussion evidence; Swarm does not make background model requests to poll quota.

To add an account:

1. Press **a** (**Add account**), select Claude or Codex, then choose an available sign-in method.
2. Complete the provider sign-in. You can leave Accounts while sign-in runs; return to review its status.
3. Check the displayed identity to confirm it is the subscription you intend to add, choose a local label, and add it. Swarm makes the account available for assignment only after it verifies the provider identity.

**Unavailable methods:** Codex supports fresh native device-code sign-in; its browser method is unavailable on this VM. Claude supports fresh native personal OAuth in an isolated profile. Cached profile imports for both providers and the installed Claude environment's one-year token are unavailable. A cached account identifier does not prove its credential authenticates that account or represents another subscription. Importing, signing in again, or reverifying cannot promote a cached profile without durable provenance and native authenticated proof. The environment token cannot verify identity offline, its effective remote policy is uncharacterized, and it has no manual launch route. Subscription API keys are not pool accounts.

## After adding an account

**Ready** means sign-in succeeded and the verified account was saved. It does not mean a discussion is already assigned to it or that Swarm has observed its usage. Quota can remain unknown until provider or discussion evidence is available; Swarm does not make background model requests to poll it.

For verified native Claude and Codex logins, Swarm reads usage after account admission and at daemon startup when a reading is due. It refreshes readings about every five minutes. These read-only checks use the current verified account profile. They do not start a provider CLI, request a model response, refresh OAuth credentials, or write credentials. Checks run whether provider rotation is on or off and continue for paused accounts; retiring an account stops its checks.

Usage checks update reported usage only. **Unknown** means no usable usage observation is available; it never means 0%. While a check runs or after one fails, the last successful reading remains visible with its age. Readings older than five minutes, or with a reset time that has passed, are marked stale. A usage refresh does not clear a recorded denial or prove that a requested model is available.

To request a refresh, open account details and press **r**. Requests coalesce while a refresh is running, and manual refreshes are limited to one every 30 seconds. After an error, Swarm retries with a delay; provider rate limits can delay the next attempt. If the provider refuses usage access, retry once with **r**. If it still requires sign-in, choose **Sign in again** in Accounts and complete native sign-in. Usage checks do not renew the login token.

Enable provider rotation to use configured accounts for **compatible new discussions and resumed ended discussions** on this VM. With one ready account, Swarm can assign new discussions to it, but has no other account from that provider to switch to. A second verified account gives Swarm a possible destination; whether it can serve a discussion still depends on the requested model and available quota evidence. A healthy discussion stays bound to its current account, even when another account is available.

If recovery interrupts a request while switching accounts, Swarm restores the conversation and asks you to retry. It does not resubmit the request, replay a tool call, or repeat an unresolved action.

Account changes take effect when saved and are separate from pending Options changes. Pausing an account prevents future assignment; existing discussions keep their binding. Retiring an account also prevents new assignment. Swarm retains its credentials while discussions, native workers, enrollment or recovery still reference them. After those references drain, it fences execution, verifies exact stopped-writer proof, and removes the characterized credential files for the supported Linux native versions. Unknown cache routes, unsafe files or uncertain durability keep the credentials retained. Accounts shows **Retired · account credentials removed** only after every retained credential generation has been erased. Discussion history is kept, and retirement does not revoke the login at the provider.

## Enable or move discussions

Enable **automatic rotation** separately for Claude and Codex. Accounts shows how many visible discussions are managed, how many remain unmanaged, and how many unmanaged discussions are still running. An enabled switch does not mean those running processes have changed credentials.

To enroll an existing discussion, end its current process normally and use **Resume**. Swarm prepares a new managed child, retains the old discussion record and original conversation history, and selects a verified account. Codex resumes its latest proved native model; Claude lets native session restore select the model until an authenticated failed-request proof establishes recovery authority. Missing history or uncertain stopped writers hold the resume with a clear refusal. Account details also offer **Move discussion here** for discussions already carrying a verified private binding. Busy discussions wait for the existing recovery safeguards.

New discussions can use the provider's default model. Until the native CLI reports its effective model, capacity remains unknown and a known denial is not bypassed. Automatic recovery waits for verified model evidence.

When a bound account cannot serve a discussion's requested model, Swarm may restore that discussion on another eligible account from the same provider. The requested model, permissions, worktree, and saved conversation are preserved. Swarm does not change providers or models to find capacity.

An account with unknown or stale quota is not assumed healthy; if no destination can be established safely, the discussion remains held for operator action.

## Sign-in and recovery

Enrollment is separate from the discussion board and transcript and can continue while you leave Accounts. Return to Accounts to inspect or review its result. Prior sign-in attempts are hidden initially; press **h** to show history. Inside the wizard, press **Ctrl+X** to cancel the current attempt or **Esc** to return to Accounts while sign-in continues. From the Accounts list, **Esc** returns to Options. If the daemon does not advertise `accounts.manage.v1`, account mutations are unavailable until Swarm is upgraded. If the daemon is unavailable, account state is read-only until it reconnects.

For Codex, fresh device-code sign-in is performed by the native Codex app-server. Cached profile import is unavailable pending native authenticated proof that the credential belongs to the identified account. Subscription API keys are not pool accounts.

For Claude, enrollment uses a fresh native personal OAuth sign-in in an isolated Swarm-managed profile. Cached profile import is unavailable pending native authenticated proof that the credential belongs to the identified account. The installed environment's one-year token is also unavailable because offline identity cannot be verified and its effective remote policy is unknown; no manual launch route is supported for it.

Managed launches retain ordinary native user settings at their user tier and leave project, local and invocation settings at their native tiers. Codex TOML paths and hook trust keys retain their original meaning. Claude ordinary preferences, project settings and hooks retain their native precedence. Only the selected private account supplies provider credentials. Source credential files, source account identity, and ambient MCP OAuth credentials are not copied into managed profiles.

Conversation history stays in the original native store through recorded history aliases; switching accounts does not copy or replace the primary conversation. Native companion files outside the characterized conversation store, such as every possible rewind or up-arrow cache, are not claimed portable. Original hook, skill and plugin directories remain live native assets, with their root custody checked.

Automatic recovery uses frozen settings and holds if policy or project settings change. After all affected writers stop, an explicit owner resume can capture changed owner settings with a crash-resumable update. A private native edit that differs from the recorded owner configuration stays held and is not overwritten. In particular, Codex's first visit to a new project may write private trust settings; that trust decision is not automatically copied to another account. Set the intended ordinary trust/configuration in the original native settings before preparing a fresh managed cohort; do not delete managed proof files to bypass a divergence hold.

The supported native contracts are pinned. An uncharacterized CLI update holds managed launch/recovery until Swarm supports that version. Standard trailing directory separators from autocomplete are normalized; interior traversal, unsafe symlinks, foreign ownership and uncharacterized alternate account routing still refuse. Codex network access follows native project/user policy. When that policy disables network access, its sandbox can also block tool-side Swarm socket commands; owner TUI controls remain available.

## Post-deployment acceptance and follow-up

Native-login accounts are supported for compatible new discussions and explicit resumes of ended discussions. Authenticated continuation and real native quota exhaustion are distinct acceptance checks; the verification record lists exactly which ran. Token-only Claude execution, cached imports, automatic transfer of private trust edits, and ambient MCP OAuth transfer remain unavailable. Existing live unmanaged discussions are not rebound in place.

The existing release and recovery procedures remain authoritative for binary upgrades and rollback: see [automatic upgrades](../ops/auto-upgrade.md) and [release signing](release-signing.md). Account storage adds local credentials and bindings; before using an older binary, follow the compatibility result produced by Swarm and do not manually remove account state to bypass a refusal.

If a native supervisor crashes before proving that all its writers have stopped, Swarm holds the discussion instead of transferring history. A stopped parent or missing process group does not prove that detached children have exited. Review the held recovery state before retrying.

Completed native-check proof is collected incrementally only after exact stopped-writer proof and release of all memory and durable references. Cleanup retains its deletion authority across crashes and preserves the credential lock inode. Live, referenced, malformed or uncertain records stay retained. If custody cannot be verified or the bounded inventory remains exhausted, Swarm holds further checks. Do not delete that state to bypass a hold.
