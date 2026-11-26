# Connect a local Swarm to Discord

## Enroll a personal dispatcher

Select a private place directory, a workspace, the owner's Discord user ID and an owner-readable bot token file. The account running the connector must own the file. Run these commands with the same per-user supervisor and state directory.

```sh
swarm discord connect --mode personal --manager codex --project-access enrolled \
    --owner-id "$owner" --token-file "$token_file" --workdir "$workspace" --state-dir "$place"
swarm discord doctor --state-dir "$place"
swarm discord run --state-dir "$place"
```

The example enrolls a personal Codex dispatcher with access to its registered workspaces. Select `--manager claude` for a qualified installed Claude Code. On a new placement, the historical default is `--project-access vm`; choosing `enrolled` restricts project operations to enrolled tools. The connect response reports the effective selection and the verified DM channel. Text the bot in that DM to start a discussion.

Use `--install` on `connect` to register the per-user service and `swarm discord status --state-dir "$place"` to inspect it. A failure during service activation can leave the place configured. Doctor checks the bot, selected discussion, supervisor and native provider. For a teammate in a guild, use `--mode teammate --guild "$guild"` and run the channel-specific check with `swarm discord doctor --state-dir "$place" --channel "$channel"`. [Service behavior](../verification/rust-discord-service.md) · [Retained Go state](../verification/rust-discord-retained-state.md)

## Personal voice room

A signed preview archive includes the pinned Node runtime and Discord media dependencies. Run `swarm release-check-media` from the installed generation before enabling voice. When running from source, install the pinned dependencies under `services/discord-voice` using `umask 077` and `npm ci --omit=dev`, run `./swarm-discord-voice --check`, and set `calls.helper_path` to that executable. The source override uses an installed Node 24.17 or later in the Node 24 series. [Packaged media and installation checks](../verification/rust-installed-pty.md)

Add a `calls` object to the private connector `config.json`, then restart the isolated foreground connector.

```json
{
  "mode": "personal",
  "guild_id": "GUILD_SNOWFLAKE",
  "room_id": "VOICE_ROOM_SNOWFLAKE",
  "discussion_id": "OWNER_DM_SNOWFLAKE",
  "speech_state_dir": "/absolute/path/to/private-speech-state"
}
```

Use the connector's exact `dm_channel_id` as `discussion_id`. The private speech directory holds the owner-only `openai.key` file. Give the bot access to view, connect to and speak in the room. Enter alone to consent to speech processing. The ready tone follows DAVE readiness. OpenAI processes finalized speech and exact readouts using the configured speech project. The verified publication remains in the owner DM.

Use `swarm discord stop-call` with the same state-dir selection as the connector to close media and suspend calls until restart. Hangup leaves accepted native work in the existing discussion. Remove the `calls` object before restarting to disable calls. Release status and remaining trial gates are recorded separately. [Personal call verification](../verification/rust-discord-personal-calls.md)


Each connection belongs to one local Swarm place and uses its own Discord app identity. Choose personal mode for an owner-only direct message or teammate mode for a bot that joins server discussions on demand. Existing configurations can still use legacy directory mode.

The main board groups managers under **DISPATCHER**. Dispatched workers get the **DSP** tag by default. Select a dispatcher and press **Ctrl+X**, then **Y**, to stop a running session or delete an ended one. The connector remains available for future messages.

## Native Rust connector

Use the [Discord migration contract](../specifications/rust-migration/discord.md) for the retained personal, guild and voice surfaces. The [qualification ledger](../verification/rust-migration.md) records which operations have been observed through a live account, installed provider, or private fixture. File pinning, manager delivery and live media have separate evidence.

The production Go setup, commands and recovery procedures remain available in the [pinned operator guide](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/docs/ops/discord.md).
