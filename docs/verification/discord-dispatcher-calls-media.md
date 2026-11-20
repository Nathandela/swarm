# Discord dispatcher call media verification

Local evidence recorded on 2026-11-20. The integration helper requires Node 24.17+ within the Node 24 series. Its lock pins `@discordjs/voice@1.0.0-dev.1795176133-cd7400323`, `@snazzah/davey@0.2.0`, `opusscript@0.1.1` and `@noble/ciphers@2.0.1`.

| Gate | Input and environment | Observation | Disposition |
| --- | --- | --- | --- |
| S1 macOS dependencies | Clean `npm ci` on Darwin arm64, official Node 24.17.0 binary verified against the release SHA-256 manifest. The older Node 22.16 fixture returned a nonzero probe with a runtime requirement. | All 12 helper tests passed. The executable loaded the native DAVE binding, generated an MLS key package, ran the codec and XChaCha checks, and exited successfully. | Local dependency and codec check passed. |
| S1 Linux arm64 dependencies | Disposable Debian Bookworm container, Node 24.17.0 and bundled npm 11.13.0. The package lock includes the optional WASM peer entries required by this npm version. | Clean `npm ci`, all 12 helper tests and `--check` passed. Node loaded the actual `davey-linux-arm64-gnu.node` binding. | GNU Linux arm64 runtime passed. |
| S2 helper pipe | Discord Gateway state and server dispatch fixtures in both orders, exact duplicates, a changed server credential and an incorrect room. | One scoped join and readiness event with exact duplicates. Changed or out-of-scope material faulted. Go owns the final leave. | Helper contract passed with a stubbed Discord voice transport. |
| S3 PCM and RTP | A 440 Hz tone, silent dependency probe, synthetic RTP sequence states and in-process transport encryption. | 20 ms Opus round-trip between 48 kHz and 24 kHz mono. Both modern RTP modes authenticated their payloads and rejected tampering. Unapproved senders, replay, late duplicates and passthrough were rejected. The helper retired on an authenticated SSRC change or an active receive discontinuity and ignored the Discord silence marker. | Local codec and RTP controls passed. |
| S3 native DAVE | Offline MLS external sender and clients using the locked native addon. An independent OpenMLS fixture signed the synthetic Add and Remove proposals. | Native commit/welcome established the bot and owner epoch. Both clients listed themselves and their peer. Protected 20 ms Opus decoded in both directions. Adding and removing a guest advanced the group to epochs 2 and 3. The removed guest could not decode epoch-3 media. The configured room snowflake was accepted in its big-endian group representation. | Offline native positive path passed on Darwin arm64 and GNU Linux arm64. Hosted activation and rekey recovery remain open. |
| S9 helper containment | Overlength NDJSON, invalid identities and a burst over the microphone queue limit. | Malformed commands and microphone overflow faulted. The helper bounds each microphone queue to three frames and the playback queue to six frames. A queued microphone frame over 120 ms or blocked output closes the generation. Playback drops stale frames. A monotonic timer runs while playback or receive work is pending. | Local helper protocol controls passed. Text continuity belongs to the connector integration suite. |

Run the dependency and media tests from `services/discord-voice`.

```bash
npm ci
npm test
./swarm-discord-voice --check
cargo build --locked --manifest-path fixtures/mls-proposals/Cargo.toml
node fixtures/native-mls.mjs fixtures/mls-proposals/target/debug/swarm-dave-fixture
```

The Rust executable is used only by the offline fixture. It generates an external sender signing key and proposals from the fixture clients' public key packages. The media helper and its npm test suite do not require Rust. The fixture removes its synthetic keys and packets on exit.

A sole client obtains an unestablished local group with no active encryption keys. Bot plus one human can establish the shared group through the voice gateway's proposals and commit/welcome exchange. The helper waits for DAVE v1, native encryption readiness and the exact Go-admitted roster plus the bot. It retires the active child on rekey to require a new audience check. It relies on Discord's authenticated Gateway and voice gateway for routing and on the pinned native library to validate the received MLS group. [Discord DAVE group creation and reset](https://daveprotocol.com/#initial-group-creation), [native group validation](https://github.com/Snazzah/davey/blob/js-0.2.0/davey/src/session.rs).

The previous stable voice package used davey 0.1.x. In an offline negative test using a deliberately shared external signer, davey 0.1.12 accepted a Welcome whose group differed from the constructor channel while continuing to report the constructor value. Davey 0.2.0 adds the actual group-ID and recognized-user checks. The pinned voice snapshot uses the corresponding native transition API and passes its recognized clients into Welcome processing. [Pinned Discord.js voice source](https://github.com/discordjs/discord.js/tree/cd7400323/packages/voice), [Davey 0.2 migration](https://github.com/Snazzah/davey/wiki/Migrating-to-0.2.0).

Interior sequence gaps observed before the current speech window closes fail the call instead of exporting the next frame. RTP cannot establish whether media was lost before the first authenticated packet, after the last one, or across a long quiet interval. Late duplicates and ordinary silent gaps do not fault. Discord roster events also have delivery latency. [Discord voice connections](https://docs.discord.com/developers/topics/voice-connections).

No Discord accounts, hosted voice gateways, production credentials, human speech or physical audio devices were used. Hosted join, hosted rekey recovery, intelligibility, and interactive latency remain release gates.
