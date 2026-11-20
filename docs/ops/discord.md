# Connect a local Swarm to Discord

Each connection belongs to one local Swarm place and uses its own Discord app identity. Choose personal mode for an owner-only direct message or teammate mode for a bot that joins server discussions on demand. Existing configurations can still use legacy directory mode.

The main board groups managers under **DISPATCHER**. Dispatched workers get the **DSP** tag by default. Select a dispatcher and press **Ctrl+X**, then **Y**, to stop a running session or delete an ended one. The connector remains available for future messages.

## Connect a personal manager on Mac

Create a separate bot application in the [Discord Developer Portal](https://discord.com/developers/applications). Save its token in a private file outside the repository. Use Discord Developer Mode to copy your own Discord user ID. On the Mac where the manager and its workers should run, open the default checkout and run:

```sh
swarm discord connect --mode personal --owner-id OWNER_USER_ID --manager codex \
  --token-file /absolute/private/personal-bot-token --workdir "$PWD" \
  --state-dir /absolute/private/personal-discord --install
swarm discord doctor --state-dir /absolute/private/personal-discord
swarm discord status --state-dir /absolute/private/personal-discord
```

Setup obtains the bot identity and verifies the one-to-one DM with the specified owner. The command prints the channel ID. Open the DM with that bot and send an ordinary text message to start the manager. If you need a shared server to locate the bot or open the DM, use a private server and check Discord’s DM settings. The connector uses the direct-message Gateway intent. Discord supplies content in the bot’s own DMs without the privileged Message Content intent. [Discord Create DM](https://docs.discord.com/developers/resources/user#create-dm) · [Discord Gateway intents](https://docs.discord.com/developers/events/gateway#message-content-intent)

Only messages from the enrolled owner in that one-to-one DM can direct the personal manager. Replies and clarifications go to the same DM. The same connector and manager state survive restarts. Run `swarm discord disconnect --state-dir /absolute/private/personal-discord` to stop the connector. Changing the enrolled owner or app requires a new connector state directory.

To set standing instructions for this place, press `o` on the Swarm board, choose **Manager prompt**, and press Enter to save. The prompt is empty by default, accepts up to 1,024 UTF-8 bytes, and applies when a manager wakes.

Upgrade the connector first. For an active Codex discussion, let the outgoing supervisor park its manager before replacing that supervisor. Confirm `native.state` is `parked` and its retirement operation is recorded. A published acknowledgement or zero pending admissions alone is insufficient. After replacement the next message resumes the original logical discussion from the parked checkpoint. Workers keep their own shims.

A supervisor replacement while the Codex manager process is still live can lose native observation custody. The new supervisor refuses to adopt the previous observer's replay authority. Inspect the blocked manager identified in status. If it has exited through the local dashboard, the connector can reconstruct the discussion for pending input and disclose the continuity gap. Keep the original session history and do not replay an ambiguous action.

On upgrade, a running VM dispatcher can authenticate a new worker with its existing per-session hook credential. Some 0.29.5 Codex backends lack this credential. A delegation from such a dispatcher stops before launching a child and reports the missing credential. Let the manager finish and park, then resume the same Discord discussion on the upgraded supervisor. Keep existing workers and discussion history. An unfinished worker without a proven mailbox binding keeps the manager active. Follow its progress in the existing session and let it finish before parking.

`swarm discord status` includes up to 16 recently parked VM discussions and their worker notes for seven days after parking. Their recorded publication IDs and confirmed Discord message IDs remain available after the manager parks, without reopening its native session.

Run `swarm dispatcher-guide` for the worker notification and reply workflow. To expose the same guidance through native skill discovery, install the bundled skill in the dispatcher account. For Codex use `$HOME/.agents/skills`. For Claude use `${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills`.

```sh
skill_dir="$HOME/.agents/skills/swarm-dispatcher"
mkdir -p "$skill_dir"
swarm dispatcher-guide --skill > "$skill_dir/SKILL.md"
```

## Connect a teammate

On the machine where workers should run, open the default checkout and use the existing app credential:

```sh
swarm discord connect --mode teammate --manager codex \
  --token-file /absolute/private/bot-token --workdir "$PWD" --install
swarm discord doctor --channel CHANNEL_ID
swarm discord status
```

Open the main Swarm board to find Discord managers in **DISPATCHER**, including when grouping by tag or repository. Enter on a running manager opens its terminal. Its workers appear on their owning supervisor. A parked manager wakes on a new message in its thread.

New teammate and personal installations use the VM policy by default. Existing installations keep their enrolled-directory policy until explicitly changed. To give an existing installation's managers and delegated workers the same choice of working directory as local Swarm, including its native shell and ordinary CLI commands, set the VM policy and restart the connector:

```sh
swarm discord connect --state-dir "$connector" --project-access vm
```

In VM mode the manager uses its native shell and the installed `swarm` CLI. It can use the same commands, directories, agent types and VM-user access as an ordinary Swarm session. The Discord bridge carries conversation history and deliberate replies and wakes the manager for changes to its workers. Direct children appear as `DSP-` workers. Prior enrolled-mode installations keep their existing configuration until the operator selects VM mode. Named project aliases are optional. Each participant in an admitted teammate discussion can direct the manager.

### Bring an existing connector onto the home supervisor

Start new installations from the same Swarm supervisor you use in your terminal. For an installation that already has a separate supervisor, keep the connector directory, its database, and the original supervisor state in place. Each existing discussion has an endpoint-bound manager ledger and may have active workers. Migration starts new discussions on the home supervisor and continues existing discussions on their original supervisor. The main board can attach to those existing managers and workers while their discussions remain active.

Install the new Swarm executable at the path used by the existing user service. From the owner's usual terminal, start or verify the home supervisor and prepare the cutover. Set `connector` to the absolute connector state directory from the existing unit's `--state-dir` argument.

```sh
swarm ls --json
swarm discord migrate-supervisor --state-dir "$connector"
```

Add `--dry-run` to verify the two live supervisor identities and preview the cutover. For earlier connections that saved only a daemon socket, supply the four paths from the original service environment. The command checks its existing connector selection and verifies the original socket's identity with a live supervisor handshake.

```sh
swarm discord migrate-supervisor --state-dir "$connector" \
  --from-state-dir "$original_state" --from-socket "$original_socket" \
  --from-lock "$original_lock" --from-log "$original_log"
```

On Linux restart the connector after the configuration change. The saved supervisor selections take precedence over the service's startup environment.

```sh
systemctl --user restart swarm-discord.service
swarm discord doctor --state-dir "$connector"
swarm discord status --state-dir "$connector"
```

Check an existing discussion's manager and worker identities against its pre-migration status. A newly opened discussion uses the home supervisor. A parked historic thread still retains its original binding, so keep the previous state and its saved launch environment available as long as such threads can receive follow-ups. Repeating the migration to the same home completes an interrupted local selector write. A downgrade that predates the migration format must be deferred while either supervisor owns discussions.




For a previously configured installation, `connect --mode teammate --manager codex --install` reuses its identity and credentials. When the credential belongs to a bot installed in exactly one server, setup can discover the application ID, name and server. Use `--guild` to choose among servers. `--channel` can limit a rehearsal to a dedicated channel. To remove an old channel restriction, explicitly reconnect with `--all-channels --install`. Discord still controls which discussions the app can see. Keep private conversations in their intended audiences.

Enable Message Content on the app and grant View Channel, Read Message History, Send Messages, Send Messages in Threads, and Create Public Threads where you want to invite the teammate. Private threads require native access. Granting visibility to a parent channel is a Discord server administration action. Starting the local service alone cannot grant that permission.

Mention the app in a discussion. A regular channel mention starts a thread on that message. In an existing thread or forum post the teammate answers there. Every human participant can direct work and continue without another mention. The manager starts on demand and uses the selected native account on this place. A discussion can use its available history, including text written before the mention. Quotes, history and worker results remain attributed. If you supply several named workspaces, repeat `--project 'name=/absolute/checkout'` during setup. Each discussion remembers its chosen project; a later project switch affects new work.

For an investigation or a correction, the manager acknowledges the request in the thread before continuing. You can send follow-ups in the same thread while the manager is parked. After an early resume failure, Swarm checks the original launch and input receipts before retrying. If Swarm must start a fresh manager, the discussion retains its queued messages and tracked workers. A continuity notice identifies the change. The replacement can read attributed thread history, inspect action receipts and query the existing worker. Uncertain actions require reconciliation before another dispatch.

If a follow-up stalls, run `swarm discord status`. An early-exit diagnostic can include the exit code, signal and timestamps when the supervisor recorded them. The pending admission survives the failure. An exit code alone cannot tell you why the provider stopped.

Only deliberately published manager replies and clarifications appear in the thread. Workers appear in the local dashboard. Tool approvals stay on their authorized native surfaces. An inaccessible destination or uncertain send is visible through local status. An outage can leave a gap; a fresh instruction in the thread resumes work. The operator and all admitted participants should understand that discussion material can reach the configured native model provider, and that native workers use the machine's execution permissions.

Use `swarm discord disconnect` to stop the connector. To go back to the legacy directory, run `swarm discord connect --mode legacy --owner-id OWNER_USER_ID --install`. Check the saved channel/user scope before bringing the legacy behavior back online. The old commands below describe that mode.

## Set up a place

Create a bot application in the [Discord Developer Portal](https://discord.com/developers/applications). Choose its public bot name there and your local integration label below. Enable **Message Content Intent** on the Bot page: joined-thread context needs ordinary message text. Discord requires this setting for privileged message content; larger verified applications may also need Discord approval. See [Gateway intents](https://docs.discord.com/developers/events/gateway#message-content-intent).

Install it in the intended server with the `bot` and `applications.commands` scopes. Give it access only to the intended channels, with View Channel, Send Messages, Read Message History and Send Messages in Threads where required. Private threads also require native Discord membership/access. Administrator permission is unnecessary. Swarm uses an outbound Gateway connection, so it needs no public interaction endpoint, port forwarding or tunnel. See Discord's [bot setup](https://docs.discord.com/developers/quick-start/getting-started) and [interaction delivery](https://docs.discord.com/developers/interactions/receiving-and-responding).

Save the bot token in a private regular file outside the repository. Do not pass it inline as an argument. Obtain the application, owner-user, server and optional channel/user IDs using Discord's developer tools.

```bash
swarm discord connect \
  --name 'Workshop' \
  --application-id APPLICATION_ID \
  --owner-id OWNER_USER_ID \
  --token-file /absolute/private/bot-token \
  --guild SERVER_ID \
  --channel CHANNEL_ID \
  --install
```

Repeat `--guild`, `--channel` or `--allow-user` for multiple IDs. Omitting channels admits accessible channels in the configured servers; setting channels also admits verified threads under those channels. In servers, an empty user allowlist admits users in the allowed scope. Direct messages admit the owner and explicitly allowed users. Set `--allow-user` when only selected colleagues should have access.

`--install` writes and starts the native launchd/systemd user service. Without it, the command prints the foreground `swarm discord run` command. Linux user services need the user's normal session/service-manager configuration to run after logout. No provider session is restarted by connect or disconnect.

`--state-dir /absolute/path` selects a private connector state directory. Its selection is recorded in the local supervisor state, so later commands and native launch activation use the same place. Keep separate supervisor/connector state for genuinely separate local installations. The token file, config, database and local socket are private to the OS user; other programs running as that user are trusted.

```bash
swarm discord status
swarm discord connect --name 'A new label'
swarm discord disconnect
```

Rename preserves the installation identity and ledger. Discord's public bot nickname/name is managed through Discord. Disconnect stops the connector and disables new native ingress activation without ending coding agents. Reconnect uses the same private config and token. Bot token replacement uses `connect --token-file /new/private/file`; restart the connector to load changes.

Saved configuration changes take effect when the connector restarts; `connect … --install` replaces and restarts its installed service. Use `disconnect` when the active connection must stop immediately. Saving a new allowlist by itself does not change the running process.

## Talk and invite agents

- `/swarm agents` shows the current local discussions. `/swarm ask` and `/swarm join` offer agent autocomplete with stable IDs beside names.
- Mention the bot followed by a unique agent name or exact ID. Duplicate names produce choices rather than guessing an identity.
- Without a clear target, choose from the directory. The optional receptionist can propose suitable agents through the existing native CLI; choosing directly remains available.
- Use **Ask Swarm** from a message's Apps context menu to ask about that original message without copying it.
- `/swarm join agent:…` makes that agent follow the current discussion. It receives bounded attributed recent context with later addressed requests; ordinary chatter causes no model calls. `/swarm leave` ends that binding and drops cached context.
- DM the bot for an admitted private conversation. A reference from a server thread continues to publish only to that original thread.

All agent work uses existing native coding agents, tools and subagents. The bot does not act as another model or transcript viewer. It does not inspect unrelated channels or anyone's other personal messages.

The receptionist reuses one registered agent for each configured CLI/model in this place. Its default Codex model must be a discovered Luna model; if unavailable, the directory still works. Select Claude Haiku with `connect --receptionist-cli claude --receptionist-model haiku`. It uses the installed CLI's account. A stopped role can be explicitly reset by the owner with `/swarm receptionist-reset`; an uncertain launch is never blindly replaced.

## Owner actions

An external request is clearly attributed. The owner can press **Treat as my instruction** or use the message context command with that name. This endorses that selected message; it does not require a separate task plan and does not wait for the target to become idle. Edited or deleted original messages cannot silently reuse an earlier endorsement.

`/swarm new` creates a registered native coding agent with a chosen CLI, optional name/model/directory and initial prompt. `/swarm resume`, `/swarm stop`, `/swarm exclude` and `/swarm include` are owner controls. The local equivalents of include/exclude are:

```bash
swarm discord exclude EXACT_AGENT_ID
swarm discord include EXACT_AGENT_ID
```

Exclusion follows verified resume lineage. Newly created unrelated agents remain reachable by default. Acknowledged exclusion prevents subsequent admission/publication; it cannot retract an effect already dispatched.

Native tool approvals and questions are separate. `/swarm requests` and supported owner-DM controls act on an exact native request. Failed owner-DM delivery does not publish private approval details to the source channel. Unsupported native control shapes remain answerable in the native session. Endorsing a message never approves a tool, and approving a tool never endorses an external message.

## Deliberate replies and investigation

The target receives a compact attributed request reference and instructions to retrieve its exact source:

```bash
swarm discord context REQUEST_REF
swarm discord context REQUEST_REF --offset NEXT_OFFSET
swarm discord reply REQUEST_REF --body-file /path/to/answer.txt
swarm discord reply REQUEST_REF --publication-id progress-1 --body-file /path/to/progress.txt
```

The reference pins its original audience. The default reply ID is `final`; repeating it with the same body is safe, while changing that body conflicts. Additional messages need distinct publication IDs. Passing the reference and attribution to a native subagent lets that subagent investigate or deliberately reply through the same local interface. There is no automatic transcript or terminal forwarding.

Use the exact `--state-dir` shown in a request's instructions if it has one. Context is immutable, attributed, paged and expires after 24 hours. A revoked or expired reference exposes no cached source. Current joined-discussion context is limited to 50 messages and 64 KiB and is lost on connector restart. Message edits/deletions update that recent cache without creating native turns; already captured request snapshots remain immutable.

## Compatibility and recovery

New qualified Claude sessions use a session-owned native mod. New qualified agy sessions register their exact native message endpoint through a normal one-time native bootstrap command; its normal permission must be answered locally. Existing unregistered/old shims refuse unsafe ingress and require an explicit owner-selected refresh. No check of terminal appearance is used as permission to press Enter. Codex/OpenCode use their existing structured message paths. See [native evidence](../verification/discord-protocol/README.md) for exact qualification boundaries and modern Hermes details.

Busy-agent behavior belongs to the native provider. A `submitted` receipt proves handoff to its message API; `admitted` can prove native prompt start. Neither proves task completion. Claude's queued submission remains observed until native start or rejection, and a later rejection gets a generic status notice without replay. A timeout may mean the input landed. The connector reconciles the original receipt and never blindly submits it again. Similarly, an uncertain Discord publication is not blindly reposted; a nonce is not a permanent exactly-once guarantee. Do not create a new operation merely to suppress an uncertain status.

The ledger bounds active request/publication counts and logical bytes. It refuses new effects when full; unresolved guards are retained. This is not a physical disk quota. `status` reports current connection state; ordinary local Swarm operation continues during Discord outages.

This feature deliberately shares unrestricted personal agents with admitted colleagues. External-message framing and owner endorsement do not create a filesystem sandbox or guarantee confidentiality. Choose the Discord channel/user scope accordingly.

Modern Hermes keeps uncertain native work and its workspace after viewer loss until joined completion can be proven. Swarm does not bundle or patch Hermes's Node runtime: the pinned v0.22.5 qualification fixture has an upstream [Undici content-decoding advisory](https://github.com/advisories/GHSA-xjj6-f33q-929c). The isolated test used only synthetic loopback services; that qualification is not a blanket security audit of the provider's dependencies.

## Verification status

The owner authorized a local mock Discord server for this delivery. Its real Gateway/REST protocol tests cover connection, source context, endorsement, explicit publication, cancellation and fault recovery. [Integration evidence](../verification/discord-integration.md) distinguishes those tests, actual native CLI fixtures, independent reviews, release gates and installed-host checks. No live Discord server configuration is implied until actual bot credentials are configured.

### Teammate recovery and worker capability

The current native manager options are Codex and Claude on qualified versions. The teammate can obtain exact results from newly created Codex and qualified Claude workers. Use Codex for follow-up steering. A busy Codex worker can receive a queued instruction when its original attempt can safely accept a fresh native input. Unsupported providers or an ambiguous worker turn require operator attention. The legacy adapter roster is independent of this qualification.

A delegated worker can queue a progress note, blocker, or clarification for its own manager from the worker's shell.

```sh
swarm teammate message --text 'Which fixture should I use?'
```

The worker receives a message ID and can retry the same text with that ID if its receipt is uncertain. The manager wakes for the note and runs `swarm reply --id WAKE_MESSAGE_ID --text TEXT` with the ID from its wake. Swarm delivers the answer to the originating worker. Codex receives a follow-up after its current turn; a Claude worker retrieves the response with `swarm teammate reply --id MESSAGE_ID --wait 90s` using its original receipt.


If direct terminal input pauses a manager, inspect its instance and observed external-input count in local status, finish or detach the terminal intervention, and reconcile it locally:

```sh
swarm discord status
swarm discord resume-manual DISCUSSION_ID \
  --session EXACT_MANAGER_SESSION --instance EXACT_INSTANCE --external-input OBSERVED_COUNT
```

Find DISCUSSION_ID, the manager session and instance, and the numeric external input count in the discussion’s native status. A changed counter, replacement instance, lost custody or ambiguous native effect refuses reconciliation. Inspect the specific manager on the home dashboard before taking further action. For a native workspace trust prompt, use the provider's own terminal; a Discord instruction cannot grant trust. Installed connectors start the supervisor using its saved owner environment. Run the first teammate installation from the owner's login shell.

### Upgrade and troubleshoot dispatcher notifications

Upgrade the owning supervisor, connector and CLI together. Run `swarm discord doctor --state-dir "$connector"` against the installed place. Keep the original supervisor available for discussions bound there. A pre-upgrade worker keeps its history and ordinary CLI controls. A worker with no verifiable mailbox binding uses the existing status-based workflow. New delegated workers on the upgraded supervisor can send explicit notes.

Inside an eligible worker, inspect a note with `swarm teammate receipt --id MESSAGE_ID`. Retry a lost response with the original ID, kind and text. The supervisor keeps accepted notes across connector downtime. A queued note is waiting for ingestion. Use `swarm discord status --state-dir "$connector"` to inspect the worker attempt, manager admission, retirement operation and Discord publication receipts. Preserve the original receipt while an outcome is uncertain.

Before downgrading, finish active mailbox jobs and their replies with the compatible supervisor and connector. Check for queued notes, pending admissions, retirement claims and uncertain publications. Leave the compatible consumer running while any are unresolved. Keep the supervisor state directory, connector database and session shims in place during the change.
