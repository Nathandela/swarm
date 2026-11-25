# Rust personal Discord call checkpoint

On 26 November the installed client and the integrated Rust candidate both reported the enrolled personal connector connected. The Rust doctor verified the bot identity and one-to-one owner channel through live Discord REST and negotiated the local manager protocol. The existing connector supplied the Gateway status. This read-only exercise sent no messages and captured no audio.

Under four-way concurrent startup, five of 24 Rust manager trials hit the four-second Codex private socket deadline. The reference permits 60 seconds. After aligning the startup and client deadlines, all 24 concurrent trials passed with a deterministic additional five-second delay before the fixture bound its socket. Every successful trial also verified one scoped tool publication against a synthetic Discord endpoint.

This checkpoint covers the enrolled personal Codex connector. It uses the original owner DM and existing scoped manager. Team calls, workers and a live rollout remain separate gates.

The media package is byte-identical to `services/discord-voice/` at Go call oracle `da7b9f2dfa7e2c6960bd860a72459c0448845c82`. The source and test fixtures inherit the repository license. The dependency lock pins Apache-2.0 Discord.js voice and prism-media, MIT DAVE, OpusScript and other dependencies, and 0BSD tslib. The reference archive packages `swarm-discord-voice`, `media.mjs`, `package.json` and `package-lock.json`. Keep these as one versioned bundle. Dependency binaries are installed by the lockfile and are outside the vendored source. Rust release artifact and update smoke tests remain open.

## Offline checks

| Input | Observation |
|---|---|
| Pinned Node 24.17.0 on macOS arm64 and Ubuntu x86_64 | Helper `--check` and 14 tests pass. Cases include authenticated native MLS membership, rekey, downgrade, both RTP AEAD modes, both Gateway halves, 20 ms Opus/PCM, RTP loss, replay, overflow and long contiguous silence. |
| Local WebSocket and real child process | Opt-in personal Identify adds guild and voice-state intents while keeping personal DM intent. The helper gets a scoped join and only the selected session material. An unexpected participant causes the Gateway owner to kill the child and leave while the native consumer is stalled. A separate text event remains queued. |
| Wrong speaker, oversized child output or child death | The Rust pipe runner rejects the output and reaps the helper. |
| Fresh roster, mute, role change, missing bot leave or expired generation | The room refuses authority or clears the current epoch. A prior epoch cannot forward. A new epoch requires the observed bot leave and new owner entry. |
| Synthetic PCM and provider event traces | All 480 click alignments preserve playback. Sustained speech interrupts. Overlap and unaddressed team text produce no admissible words. A readout with changed words produces zero playable bytes. |
| Voice record reopened from disk | Same turn and transcript return the original receipt. A modified body under the same request key and a different supervisor are refused. The store advances from format 2 to format 3 on the first persisted voice record. |

## Review regressions

- Fresh guild snapshots preserve exhausted owner consent while that owner remains in the room. A negative fixture expires the generation, observes the bot leave, refreshes the snapshot and rejects a new call until owner re-entry.
- The launcher verifies the shipped helper, media module, package metadata and lock against the binary's pinned artifacts. It also records and rechecks local executable and dependency identities and requires owner-controlled paths. It executes the embedded module sources from the held bundle directory with Node subprocess and worker permissions disabled. The compatible pinned native DAVE addon remains part of the trusted dependency bundle.
- A real helper fixture leaves a descendant with the inherited media pipe after its leader exits. The parent observes the unreaped leader and kills the group before reaping it. The pinned Node fixture uses the real helper to produce a private join and verifies that its process restrictions reject spawning another program.
- A normal disconnect ends media through the independent call-control socket before waiting for the text bridge. The loopback fixture holds the text shutdown open until it observes the outgoing Gateway leave.
- Native live authorization sends only the call ID, generation, turn ID and body digest to the call-control socket. The durable native verifier still checks the complete scoped origin before requesting that live decision.

On both hosts, the Rust suite passed 52 tests with two ignored by default. The ignored pinned-helper process test passed when run explicitly with Node 24.17. Linux used isolated RAM-backed scratch for all call fixtures and large files. The existing workspace trust fixture used a small disk-backed temporary directory for creation timestamps.

## Open gates

- Real call and hosted requests have not run from Rust. Validate with a designated private guild, isolated application, throwaway marker, personal room and authorized test window.
- Tested speech traffic uses bounded isolated finalized transcriptions and isolated verified readouts. The Go oracle's parallel GPT-Live delegation conversation has not been ported.
- Team consent controls require the admitted team discussion implementation. Reverse worker result playback, real device timings, historical supervisor migration and rollback across Rust-only effects remain open.
- An ambiguous native send is held by its original nonce. A definitive pre-effect live-authority refusal withdraws that cutover. Recovery from a crash after durable cutover and before any native acknowledgement still needs a proven negative native receipt before returning to input.
- Linux and macOS installed-release qualification remain open. The macOS Intel helper has not run, and the package has platform-specific native bindings.
