# Rust linked team dispatcher and call checkpoints

The ordinary guild route uses the existing Rust dispatcher and durable publication ledger. An enrolled teammate installation using the current Codex native manager can select one existing guild thread using `team_discussion` in its private connector config.

```json
{
  "guild_id": "ENROLLED_GUILD_ID",
  "thread_id": "EXISTING_THREAD_ID"
}
```

The bot must be able to send messages and read history in the parent and linked thread. Both bot and human authors must satisfy the private membership check when the thread is private. A real mention opens the dispatcher. After it opens, verified follow-ups stay in the same thread. Explicit worker targets retain their existing ingress. Deleting a pending source withdraws it. A reply settles only on an exact receipt from the pinned original thread.

Add the `calls` object only to offer media. Use `mode: "team"`, the same `guild_id`, an ordinary `room_id`, the exact thread as `discussion_id`, and an explicit `user_ids` allowlist. The helper and speech key follow the personal setup. In the linked thread, each current room participant must type `@Bot voice consent`, while one types `@Bot voice join`. Any participant can type `@Bot voice leave`. The current Discord roster and a fresh readback of permissions gate media for every participant. Calls require guild membership events so the active room can react to thread or role changes. Discord's privileged-intent refusal disables team media and permits one reconnect for the ordinary text connector. Team speech must address “Dispatcher,” or “Hey Dispatcher” to enter native work. All consenting speakers should expect isolated transcription for address detection.

The offline fixture verifies exact linked-thread custody at native cutover for two distinct members and refuses a publication receipt from a neighboring channel. The control fixtures require an explicit fresh team call invitation, every live participant's consent and exact membership in the linked private thread.

The composed loopback fixture exercises one Gateway connection and a real fixture child with synthetic PCM and injected offline inference. It confirms that unaddressed conversation and call-control text generate no dispatcher input, two addressed turns preserve distinct speaker identities, and deleting a consent source closes the room.

Worker wake links use the original accepted call turn and native parent-input digest. A persisted note digest covers the immutable note body, kind, attempt and file identities. Final written delivery stays bound to the original audience after hangup. Spoken delivery uses that original voice coordinate only while its call generation remains eligible. The offline reverse-order fixture rejects a different native parent and distinguishes a changed note body.

The GPT-Live sidecar starts one session per accepted room generation. It sends completed isolated utterances, with team audio forwarded only after explicit address recognition. A bounded sample cursor matches a client delegation to the original turn. Only verified admission and canonical publication commentary can come back through that delegation. The loopback Live fixture confirms startup, reversed and duplicate delegation handling, exact commentary routing, and expired-session closure. Live-generated output cannot drive work or room playback.

A recognized terminal DAVE rekey retires the old helper and generation. One bounded attempt waits for the old bot leave before joining again. It inherits the previous inactivity and hard cutoffs and requires fresh team permission verification. A membership change, withdrawal, or repeated rekey leaves the previous grant closed. The Rust fixtures include a terminal helper fault delivered immediately before process exit.

On macOS arm64 and Ubuntu x86_64, the pinned offline DAVE signer and native addon formed a three-member group, exchanged encrypted Opus, rekeyed, and refused decryption by the removed member. The final focused Discord suite passed 41 tests on each platform, with three guarded fixtures ignored by default. The pinned helper process fixture passed when run explicitly. The pinned Node suite passed all 14 tests. The macOS package suite passed 134 tests with five guarded fixtures ignored by default, and strict Clippy passed. The separate pinned Go reference run passed its call roster, Gateway and rejoin cases.

Public provisioning of a new linked thread, live Discord qualification, and authorization recovery after Gateway gaps remain open. The configured thread is independent of the call configuration so removing media settings can preserve the written discussion.

## Guild continuity and automatic linking

A configured guild discussion repairs its own history after the sole Gateway owner exits. A new history cursor starts at a read-back boundary. Existing pending sources are rechecked and late replies settle only through their original nonce and audience. The repair never treats historical call controls as live consent. A gap beyond the four-page evidence bound holds cutover.

An unbound enrolled teammate connector can bind one ordinary public thread from an explicit parent-channel mention. It verifies the author, app permissions and source revision, records the possible create before POST, then attaches only after an exact thread, parent and guild readback. Discord's thread-from-message coordinate is the source message ID. A lost create response leaves a recovery record and permits readback without a second write. The first native admission keeps the verified starter rather than borrowing text from an unrelated thread message. The configured and automatic routes use the same dispatcher.
