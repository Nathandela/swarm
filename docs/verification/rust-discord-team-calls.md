# Rust linked team dispatcher and call checkpoints

The ordinary guild route uses the existing Rust dispatcher and durable publication ledger. An enrolled teammate installation can select one existing guild thread using `team_discussion` in its private connector config.

```json
{
  "guild_id": "ENROLLED_GUILD_ID",
  "thread_id": "EXISTING_THREAD_ID"
}
```

The bot must be able to send messages and read history in the parent and linked thread. Both bot and human authors must satisfy the private membership check when the thread is private. A real mention opens the dispatcher. After it opens, verified follow-ups stay in the same thread. Explicit worker targets retain their existing ingress. Deleting a pending source withdraws it. A reply settles only on an exact receipt from the pinned original thread.

The offline fixture verifies exact linked-thread custody at native cutover for two distinct members and refuses a publication receipt from a neighboring channel. The control fixtures require an explicit fresh team call invitation, every live participant's consent and exact membership in the linked private thread.

Public provisioning of a new linked thread, live Discord qualification, and authorization recovery after Gateway gaps remain open. The configured thread is independent of the call configuration so removing media settings can preserve the written discussion.
