# Live Discord text and file transport

On 26 November 2026 an operator-authorized test used the enrolled personal bot on its owner's actual guild. The installed connector was already connected to Discord. The Rust witness verified the bot and guild owner, checked channel permissions and created a new private thread. It posted one synthetic text message and one message carrying a 25-byte CSV. Both server-assigned IDs matched independent message-history and individual-message readbacks. The existing Rust attachment intake downloaded the CSV from Discord's CDN and matched the original bytes and SHA-256 `f97615d70689fcd6d813d1fa765408e238e55190b21898d002e0447cf47e54d4`. The witness archived the thread and verified the archive flag. The installed connector reported the same active installation throughout.

The private run receipt contains the guild, thread and message IDs. A read-only rerun uses that receipt. These results cover bot REST, multipart upload, Discord history and attachment download. Human-authored ingress, Discord's visual client, takeover of the live Gateway, and listener-observed audio have separate gates. Both Computer Use transports were unavailable when the archived thread was offered for visual inspection.

## Reproduce

Supply the enrolled bot and owner-verified guild names. Use a new private evidence directory for an authorized live run. The witness selects a guild with one ordinary text parent and verifies its private-thread permissions. It records each remote attempt before submission and can reconcile an interrupted run using that same directory.

```sh
export SWARM_LIVE_BOT_NAME SWARM_LIVE_GUILD_NAME SWARM_LIVE_EVIDENCE_DIR
cargo test --locked --bin swarm discord::rest::live_transport::personal_guild_text_and_attachment -- --exact --ignored --nocapture
```

Set `SWARM_LIVE_RECHECK_ONLY=1` and reuse the evidence directory to verify the recorded messages and archived thread without issuing a Discord write. This mode uses the exact saved guild and parent IDs. The enrolled application and current guild owner must still match. The November 26 recheck verified both original messages and the original CSV digest while the existing Gateway stayed connected. Its receipt bytes stayed unchanged. Voice requires consent from every participant at the time of the call.
