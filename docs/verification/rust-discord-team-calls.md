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

Additional linked discussions within one place, live Discord qualification, and recovery of unobserved guild mentions outside the linked thread remain open. The configured thread is independent of the call configuration so removing media settings can preserve the written discussion.

## Guild continuity and automatic linking

A configured guild discussion repairs its own history after the sole Gateway owner exits. A new history cursor starts at a read-back boundary. Existing pending sources are rechecked and late replies settle only through their original nonce and audience. The repair never treats historical call controls as live consent. A gap beyond the four-page evidence bound holds cutover. Connector status preserves the observed gap and states that mentions outside the known thread can be missing.

An unbound enrolled teammate connector can bind one ordinary public thread from an explicit parent-channel mention. It verifies the author, app permissions and source revision, records the possible create before POST, then attaches only after an exact thread, parent and guild readback. Discord's thread-from-message coordinate is the source message ID. A lost create response leaves a recovery record and permits readback without a second write. The first native admission keeps the verified starter rather than borrowing text from an unrelated thread message. The configured and automatic routes use the same dispatcher.

A supervisor keeps independently enrolled Discord places in a private registry. Choosing a different default place leaves prior identities enrolled. Native manager effects resolve the exact enrolled bridge, and worker input verifies its explicit installation. An independent application lease prevents two Gateway owners for one app under the same supervisor.

Direct received attachments in a verified personal DM or linked team discussion are restricted to the original Discord CDN channel and attachment coordinates. The connector sends no bot credentials to the CDN. It pins the complete bytes under the original message and attachment identity, checks the digest immediately before native admission, and retains the exact snapshot across signed URL changes. The fixture rejects foreign CDN authorities, unrelated source coordinates, modified bytes, and writable snapshots. New prompts carry attachment identifiers, digests and sizes. The qualified Codex manager reads a bounded chunk through the authenticated active-turn tool, and older receipts retain their original prepared text.

## Guild review regressions

Reconnect collects timeline pages only to the saved history cursor. Older pending admissions use individual verified message reads, so an old request beyond the four-page timeline bound cannot block a recent repair.

An explicit HTTP 429 can defer one exact opener, with no more than three create attempts and a recorded cooldown measured from the response. Retry verifies the same opener, current permissions, and the absence of the expected thread. Once deployed work might have crossed the network, recovery uses thread readback only. A local HTTPS fixture exercises the actual HTTP classification, a dropped response and unauthenticated attachment download.

A competing same-app registration must prove that the earlier place is disabled. Missing or altered earlier custody holds enrollment. Received-file admission verifies the rendered native prompt size before CDN traffic or filesystem staging. It reclaims only bounded, private abandoned staging directories under the sole writer lease. Complete snapshots continue to count toward the storage limit and malformed storage fails closed.

Visible Discord forwards can carry clipped quotes and attachments under the verified outer message. The connector uses the copied snapshot, including the attachment provenance in its receipt, and verifies each download against the outer channel. The fixture refuses the original channel's CDN coordinate and a snapshot without a typed forward reference.

The scoped attachment read requires an observed native Codex tool call for the exact active accepted source and argument digest. It takes an attachment ID, SHA256 and byte offset, and returns at most 8192 verified bytes in base64. It cannot select arbitrary local paths, another turn, a worker wake, or an attachment that failed custody checks. The received-file fixture verifies the active read and wrong ID, digest, turn, and retired-input refusals.

One enrolled place can also configure up to sixteen additional linked threads in the private `team_discussions` array. Entries use the same guild and thread fields as `team_discussion`. Each has separate native manager custody, source history, attachment storage and publication receipts. One Gateway handles all linked audiences. The single configured voice room resolves only to the exact linked discussion. The fixture resolves two sibling native scopes and rejects cross-thread native input and an unenrolled directory.
