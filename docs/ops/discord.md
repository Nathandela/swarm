# Connect a local Swarm to Discord

Each local Swarm gets its own Discord application, token, installation ID and user-chosen name. Connecting a VM does not connect or control another computer. Existing and future registered Swarm discussions appear automatically; exclude individual discussions when needed.

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

## Verification status

The owner authorized a local mock Discord server for this delivery. Its real Gateway/REST protocol tests cover connection, source context, endorsement, explicit publication, cancellation and fault recovery. [Integration evidence](../verification/discord-integration.md) distinguishes those tests, actual native CLI fixtures, independent reviews, release gates and installed-host checks. No live Discord server configuration is implied until actual bot credentials are configured.
