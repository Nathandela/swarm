# Connect a local Swarm to Discord

## Personal voice room on the Rust migration branch

The enrolled personal Codex connector can opt in to one ordinary guild voice room. Establish the dispatcher by texting the owner DM first. Prepare the pinned helper with Node 24.17 or later in the Node 24 series, run `npm ci --omit=dev` in `services/discord-voice`, then run `./swarm-discord-voice --check`. The connector looks for Node in `/usr/local/bin`, `/opt/homebrew/bin`, then `/usr/bin`. Use npm 11 with `umask 077` to install the pinned lockfile. Keep the bundle and dependencies owned by the connector user or root and free of group or other write access. Restart the connector after installing a matching bundle.

Add a `calls` object to the private connector `config.json`, then restart the isolated foreground connector.

```json
{
  "mode": "personal",
  "guild_id": "GUILD_SNOWFLAKE",
  "room_id": "VOICE_ROOM_SNOWFLAKE",
  "discussion_id": "OWNER_DM_SNOWFLAKE",
  "helper_path": "/absolute/path/to/swarm-discord-voice",
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
