# Account pools

Account pools let Swarm keep separate personal Claude and Codex logins and assign each new discussion to one account. Automatic rotation is opt-in per provider. Enabling a provider covers compatible new discussions and explicit resumes. Onboarding of running discussions has its own switch, off until the cross-account resume acceptance passes (ADR-032). Onboarding stops the original process and creates one managed continuation with the same conversation; it never changes credentials inside a running process.

> **Verification records (2026-10-13):** the [Codex minimum-version record](../verification/codex-minimum-version.md) describes the v0.17.7 policy, actual native 0.162.1 sign-in/cancellation checks and migration holds. The [v0.17.6 record](../verification/account-pools-v0176.md) retains the previous signed deployment and successful live Claude creation/onboarding evidence. The [v0.16.2 record](../verification/account-pools-v0162.md) retains earlier native configuration evidence; historical managed Claude 2.1.288 discussions retain their previous compatibility contract.

## Manage accounts

Open **Options → Accounts**. From Accounts, use the account list to inspect authentication state, observed quota and reset times, last observation, and the number of discussions bound to each account. Unknown quota is shown as unknown. Quota observations come from provider or discussion evidence; Swarm does not make background model requests to poll quota.

To add an account:

1. Press **a** (**Add account**), select Claude or Codex, then choose an available sign-in method.
2. Complete the provider sign-in. You can leave Accounts while sign-in runs; return to review its status.
3. Check the displayed identity to confirm it is the subscription you intend to add, choose a local label, and add it. Swarm makes the account available for assignment only after it verifies the provider identity.

**Unavailable methods:** Codex supports fresh native device-code sign-in; its browser method is unavailable on this VM. Claude supports fresh native personal OAuth in an isolated profile. Cached profile imports for both providers and the installed Claude environment's one-year token are unavailable. A cached account identifier does not prove its credential authenticates that account or represents another subscription. Importing, signing in again, or reverifying cannot promote a cached profile without durable provenance and native authenticated proof. The environment token cannot verify identity offline, its effective remote policy is uncharacterized, and it has no manual launch route. Subscription API keys are not pool accounts.

## After adding an account

**Ready** means sign-in succeeded and the verified account was saved. It does not mean a discussion is already assigned to it or that Swarm has observed its usage. Quota can remain unknown until provider or discussion evidence is available; Swarm does not make background model requests to poll it.

A new Claude account can still need native first-use setup. Open its first discussion and follow the native prompts before expecting a model response. The installed check reaches the theme selector, then **Select login method**; the characterized native flow also includes **Security notes** confirmation. This setup belongs to that account's private profile, so another account can need it independently. A verified login alone does not prove that first-use setup is complete, and Swarm does not mark it complete or skip native confirmations on the owner's behalf.

Use a fresh managed Claude discussion for this setup: enable Claude in Accounts, create a new discussion and complete its native prompts in the attached terminal. Inspect the discussion's assigned account. **Move discussion here** is not a setup route for another account: a recovery destination can remain input-held until native readiness is established. A dedicated per-account setup flow is still pending.

For verified native Claude and Codex logins, Swarm reads usage after account admission and at daemon startup when a reading is due. It refreshes readings about every five minutes. These read-only checks use the current verified account profile. They do not start a provider CLI, request a model response, refresh OAuth credentials, or write credentials. Checks run whether provider rotation is on or off and continue for paused accounts; retiring an account stops its checks.

Usage checks update reported usage only. **Unknown** means no usable usage observation is available; it never means 0%. While a check runs or after one fails, the last successful reading remains visible with its age. Readings older than five minutes, or with a reset time that has passed, are marked stale. A usage refresh does not clear a recorded denial or prove that a requested model is available.

To request a refresh, open account details and press **r**. Requests coalesce while a refresh is running, and manual refreshes are limited to one every 30 seconds. After an error, Swarm retries with a delay; provider rate limits can delay the next attempt. If the provider refuses usage access, retry once with **r**. If it still requires sign-in, choose **Sign in again** in Accounts and complete native sign-in. Usage checks do not renew the login token.

Enable provider rotation to use configured accounts for **compatible new discussions and resumed ended discussions** on this VM. With one ready account, Swarm can assign new discussions to it, but has no other account from that provider to switch to. A second verified account gives Swarm a possible destination; whether it can serve a discussion still depends on the requested model and available quota evidence. A healthy discussion stays bound to its current account, even when another account is available.

Other discussions on the same account never block a launch. When the owner configuration changed, the launch refreshes the account's native profile even while other discussions run on it (ADR-032). If the selected account refuses a launch for an account-specific reason, such as a busy configuration check, Swarm tries the next eligible account itself; when none can serve the discussion, the refusal names each account with its reason and a remedy.

If recovery interrupts a request while switching accounts, Swarm restores the conversation and asks you to retry. It does not resubmit the request, replay a tool call, or repeat an unresolved action.

Account changes take effect when saved and are separate from pending Options changes. Pausing an account prevents future assignment; existing discussions keep their binding. Retiring an account also prevents new assignment. Swarm retains its credentials while discussions, native workers, enrollment or recovery still reference them. After those references drain, it fences execution, verifies exact stopped-writer proof, and removes the characterized credential files for the supported Linux native versions. Unknown cache routes, unsafe files or uncertain durability keep the credentials retained. Accounts shows **Retired · account credentials removed** only after every retained credential generation has been erased. Discussion history is kept, and retirement does not revoke the login at the provider.

## Codex version compatibility

Codex 0.160.0 is the minimum stable version. Managed launches, account enrollment, recovery and automatic onboarding accept later stable versions and follow the installed CLI on PATH. Running discussions keep their existing process until a guarded restart; Swarm does not downgrade a discussion to an old retained executable. Missing, unsafe, ambiguous or prerelease installations hold managed execution.

Old private 0.160.0 copies are preserved for existing processes and compatibility inspection. They no longer override the installed Codex version or serve as an automatic fallback. Claude continues to use its retained qualified runtime.

Automatic onboarding accepts Codex sources at or above that minimum. It still needs matching post-spawn CLI observation, native conversation/history proof, an eligible account, and compatible settings. Removing the version pin does not bypass those checks. Credential erasure retains its separately characterized inventory; unrecognized later-version artifacts remain retained rather than being deleted blindly.

## Enable or move discussions

Enable **automatic rotation** separately for Claude and Codex. Accounts shows how many visible discussions are managed, how many remain unmanaged, and how many unmanaged discussions are still running. An enabled switch does not mean those running processes have changed credentials. When onboarding is also on for the provider, compatible running discussions are queued automatically when idle. Busy turns, permission dialogs, attached controllers, missing history, unqualified source CLI versions, and configuration conflicts defer onboarding without stopping the source. The existing lifecycle coordinator validates the native executable, model, history and account configuration before claiming the stop, then retains the selected account and original conversation across daemon restart. This enrollment follows the provider pool switch independently of the CLI-refresh switch.

To enroll an existing discussion, end its current process normally and use **Resume**. Swarm prepares a new managed child, retains the old discussion record and original conversation history, and selects a verified account. Codex resumes its latest proved native model; Claude lets native session restore select the model until an authenticated failed-request proof establishes recovery authority. Missing history or a still-running writer of the conversation holds the resume with a clear refusal. Account details also offer **Move discussion here** for discussions already carrying a verified private binding. Busy discussions wait for the existing recovery safeguards.

New discussions can use the provider's default model. Until the native CLI reports its effective model, capacity remains unknown and a known denial is not bypassed. Automatic recovery waits for verified model evidence.

When a bound account cannot serve a discussion's requested model, Swarm may restore that discussion on another eligible account from the same provider. The requested model, permissions, worktree, and saved conversation are preserved. Swarm does not change providers or models to find capacity.

An account with unknown or stale quota is not assumed healthy; if no destination can be established safely, the discussion remains held for operator action.

## Sign-in and recovery

Enrollment is separate from the discussion board and transcript and can continue while you leave Accounts. Return to Accounts to inspect or review its result. Prior sign-in attempts are hidden initially; press **h** to show history. Inside the wizard, press **Ctrl+X** to cancel the current attempt or **Esc** to return to Accounts while sign-in continues. From the Accounts list, **Esc** returns to Options. If the daemon does not advertise `accounts.manage.v1`, account mutations are unavailable until Swarm is upgraded. If the daemon is unavailable, account state is read-only until it reconnects.

For Codex, fresh device-code sign-in is performed by the native Codex app-server. Cached profile import is unavailable pending native authenticated proof that the credential belongs to the identified account. Subscription API keys are not pool accounts.

For Claude, enrollment uses a fresh native personal OAuth sign-in in an isolated Swarm-managed profile. Cached profile import is unavailable pending native authenticated proof that the credential belongs to the identified account. The installed environment's one-year token is also unavailable because offline identity cannot be verified and its effective remote policy is unknown; no manual launch route is supported for it.

Managed launches retain ordinary native user settings at their user tier and leave project, local and invocation settings at their native tiers. Codex TOML paths and hook trust keys retain their original meaning. Claude ordinary preferences, project settings and hooks retain their native precedence. Only the selected private account supplies provider credentials. Source credential files, source account identity, and ambient MCP OAuth credentials are not copied into managed profiles.

Conversation history stays in the original native store through recorded history aliases; switching accounts does not copy or replace the primary conversation. Native companion files outside the characterized conversation store, such as every possible rewind or up-arrow cache, are not claimed portable. Original hook, skill and plugin directories remain live native assets, with their root custody checked.

Configuration drift never refuses a launch, resume, recovery or onboarding (ADR-032). When Claude or Codex writes the managed profile (`/fast`, `/model`, `/config`, `mcp add`, project trust), the next launch uses the profile as it is and logs `accepted native configuration drift` with the profile and file. When the owner configuration changes, the next launch refreshes the profile from it and the owner wins: private edits in the profile are overwritten, and an interrupted refresh completes toward the current owner configuration. Set lasting choices, including trust, in the original native settings. Owner settings that route credentials or providers (`apiKeyHelper`, AWS auth helpers, forced login, and `env` entries such as `ANTHROPIC_API_KEY` or `CLAUDE_CODE_USE_BEDROCK`) still refuse with `credential-or-provider-settings`. A quota or auth rotation started after an owner change is still refused until rotation is rebuilt (Phase C).

Claude's native contract remains pinned. On the characterized Linux portable installation, Swarm retains and verifies Claude 2.1.289 for managed operations while ordinary launches continue using the owner's installed CLI. Accounts shows the qualified managed version and the last observed installed version. A missing or changed retained executable holds managed Claude operations. Its original vendor directory must be owned by the user without group or other write access; the installed VM's directory was repaired from 0775 to 0755 while preserving owner updater access. Custom launchers and alternate layouts do not gain this retention guarantee. Other uncharacterized Claude updates hold managed launch/recovery until Swarm supports that version.

### Migrate existing discussions

Onboarding running discussions needs a second per-provider switch besides rotation, off by default until the cross-account resume acceptance passes (ADR-032). There is no TUI control yet: the daemon reads `account-onboarding.json` in the Swarm state directory, for example `{"codex":true}`, and an absent or unreadable file means off. With both switches on, this is the bulk onboarding route for the provider's eligible running discussions. Swarm queues them when idle and creates one managed continuation for each, keeping its original conversation. Working turns, permission prompts and attached controllers defer the switch. Ended history entries are not automatically restarted in bulk; use **Resume** for each discussion you want to continue.

For a running WIP discussion, let its current turn finish, then either allow automatic onboarding or end the process normally and use **Resume**. Do not delete an ended history entry. A missing conversation ID or CLI observation can prevent automatic onboarding even when native history exists; do not stop such an exception until its identity and recovery route are established.

Standard trailing directory separators from autocomplete are normalized; interior traversal, unsafe symlinks, foreign ownership and uncharacterized alternate account routing still refuse. Managed Codex keeps the owner's sandbox network setting: Swarm's `sandbox_workspace_write.network_access=true` override is carried to the CLI and its backend (ADR-028 amendment of 2026-10-14). A managed workspace-write agent can therefore reach the Swarm daemon socket, and it may open network connections without an approval prompt. Owner TUI controls remain available.

## Post-deployment acceptance and follow-up

Native-login accounts are supported for compatible new discussions and explicit resumes of ended discussions. Authenticated continuation and real native quota exhaustion are distinct acceptance checks; the verification record lists exactly which ran. Token-only Claude execution, cached imports, automatic transfer of private trust edits, and ambient MCP OAuth transfer remain unavailable. Existing live unmanaged discussions are not rebound in place.

The existing release and recovery procedures remain authoritative for binary upgrades and rollback: see [automatic upgrades](../ops/auto-upgrade.md) and [release signing](release-signing.md). Account storage adds local credentials and bindings; before using an older binary, follow the compatibility result produced by Swarm and do not manually remove account state to bypass a refusal.

Resume, recovery and onboarding wait only while a recorded writer of the discussion is alive: its shim, native CLI, backend, or any member of their process groups. A supervisor that crashed or was killed without writing a stopped proof does not hold the discussion once those processes are gone. Retirement still requires the exact stopped-writer proof before removing credentials.

Completed native-check proof is collected incrementally only after exact stopped-writer proof and release of all memory and durable references. Cleanup retains its deletion authority across crashes and preserves the credential lock inode. Live, referenced, malformed or uncertain records stay retained. If custody cannot be verified or the bounded inventory remains exhausted, Swarm holds further checks. Do not delete that state to bypass a hold.
