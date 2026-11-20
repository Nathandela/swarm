# Discord dispatcher call media verification

Local evidence recorded on 2026-11-20 for the integration branch. The helper was introduced in `bce0ef3`. This report accompanies the media hardening commit.

| Gate | Input and environment | Observation | Disposition |
| --- | --- | --- | --- |
| S1 macOS dependencies | Clean `npm ci` on Darwin arm64, Node 22.16.0, npm 11.6.2. Locked voice 0.19.2, davey 0.1.12, OpusScript 0.1.1 and Noble Ciphers 2.0.1. | The executable loaded, generated a native DAVE key package, ran its codec and XChaCha checks and exited successfully. | Local dependency and codec check passed. |
| S1 Linux packages | One scratch cross-install per target from the same lock using npm `--os=linux`, x64 and arm64, targeting glibc. | Both installations succeeded. The selected DAVE packages contained ELF native add-ons for the requested architecture, with GNU and musl artifacts. | Package selection passed. Linux execution remains open because the Docker daemon was unavailable. |
| S2 helper pipe | Local child-process fixtures with matched Gateway state and server events in both orders, repeated exact events, a changed server credential and incorrect room. | One scoped join, one readiness notification and no second negotiation for exact duplicates. Changed or out-of-scope material faulted. The parent owns the final leave. | Helper contract passed with a stubbed Discord voice transport. |
| S3 PCM and RTP | Ten local helper tests, including a 440 Hz tone, silent dependency probe, synthetic RTP packets and mock MLS state. | 20 ms Opus round-trip between 48 kHz and 24 kHz mono. Both modern RTP modes authenticated the payload and rejected tampering. The receive gate rejected unapproved senders, duplicate and late sequence numbers and passthrough, accepted a wrap with loss and retired on authenticated SSRC rebind. | Local codec and RTP controls passed. The tone and synthetic payload establish format and encryption behavior. |
| S3 DAVE activation | Native session created with a synthetic user and room, and controlled voice networking state for two expected members. | A new native session generated its key package while `ready` remained false and `epoch` null. The gate required an established epoch and exact Go-provided member IDs plus the bot. A wrong member, downgrade or changed epoch closed the generation. | Negative controls passed. Discord-hosted MLS creation, rekey, and human audio are open. |
| S9 helper containment | Invalid speaker list, wrong voice-state scope and oversized NDJSON. Inspect timer and queue behavior. | All invalid pipe fixtures faulted. The helper caps receive backlog at three frames per admitted speaker and playback backlog at six frames. Frames older than 120 ms are discarded. Pacing uses one monotonic timer while frames are pending and no pacing timer during idle. Unwritable output drops audio before unbounded buffering. | Local protocol controls passed. Cross-process fault and text continuity evidence belongs to the integration suite. |

Run the helper checks from `services/discord-voice`.

```bash
npm ci
npm test
./swarm-discord-voice --check
```

Discord starts a sole remaining client in an unestablished local group. A bot and one other client can establish a shared MLS group when the voice gateway coordinates the member proposal and first commit or welcome. A successful voice socket or RTP session description alone can therefore precede MLS readiness. [Discord DAVE initial group creation and sole member reset](https://daveprotocol.com/#initial-group-creation).

The helper checks the negotiated DAVE version, native readiness, epoch, and admitted member IDs before emitting readiness. The pinned voice package exposes a DAVE wrapper but permits media fallback while MLS is inactive. Its per-user receive stream also omits RTP sequence metadata. The helper guards the pinned packet parser before permitting authenticated per-speaker audio and binds an active child to one MLS epoch. A rekey retires the child so the coordinator can revalidate audience before creating another generation. OpusScript outputs decoded samples directly at 24 kHz rather than applying an additional resampler to 48 kHz output. [Voice package](https://discord.js.org/docs/packages/voice/0.19.2), [native DAVE session](https://github.com/Snazzah/davey/blob/js-0.1.12/davey/src/session.rs).

No Discord accounts, real participants, hosted voice gateway, production credentials, speech samples or physical audio devices were used. Hosted join, rekey recovery, intelligibility, Linux runtime, and interactive latency remain release gates.
