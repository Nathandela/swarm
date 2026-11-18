# OpenAI speech in Swarm

The voice-manager feature branch includes a standalone speech client. `swarm voice live` carries a two-way audio conversation through GPT-Live-1. `swarm voice transcribe` returns the final text for one recorded utterance using GPT-Transcribe. The native Codex and Claude managers still own worker actions. Connecting these speech commands to their scheduler and to the desktop and phone interfaces is a separate integration gate. [Implementation plan](../specifications/voice-manager-plan.md)

## Configure a machine

Run `swarm voice setup` on the machine that will send audio. Enter your OpenAI project API key at the hidden prompt. Setup also accepts a raw key on standard input. The CLI stores it in `voice/openai.key` under the Swarm state directory with mode 0600 and an owner-only parent directory. Running setup again rotates the saved key for future calls. The key authorizes requests to `api.openai.com`. OpenAI account pricing and data controls apply. [OpenAI data controls](https://developers.openai.com/api/docs/guides/your-data)

On a machine with a v1 Gradium configuration, setup preserves the place and manager identifiers and switches the voice to Marin. The global OpenAI endpoint replaces the old region setting. Existing Gradium credentials stay at their original path. Run setup explicitly on each machine you intend to migrate.

## Stream audio

Both commands accept raw mono, signed 16-bit little-endian PCM at 24,000 samples per second. The Live output has the same format. Feed real-time audio to stdin and connect stdout to your player. For example, this command replays an ordinary recording on its capture clock and writes the reply to a WAV file. The trailing silence gives the model time to answer before capture ends.

```sh
ffmpeg -loglevel error -i question.wav -af apad=pad_dur=7 -ar 24000 -ac 1 -f s16le - |
  swarm voice live |
  ffmpeg -loglevel error -f s16le -ar 24000 -ac 1 -i - reply.wav
```

Swarm paces piped and recorded input at the capture rate. EOF requests session closure. An interrupt aborts the session. Closing a capture can cut off a pending reply. Live captions are incremental, and GPT-Live can paraphrase spoken commentary. A request that needs the native manager ends the standalone command with a message. [OpenAI Live conversations](https://developers.openai.com/api/docs/guides/live-conversations)

## Transcribe a completed utterance

```sh
ffmpeg -loglevel error -i question.wav -ar 24000 -ac 1 -f s16le - | swarm voice transcribe
```

EOF finalizes the input. The CLI accepts up to two minutes, submits one transcription request, and writes the returned text to stdout. It does not dispatch that text as a Swarm command. Input audio and responses stay in memory unless you direct them to a file yourself. See the [authenticated spike and audit](../verification/voice-manager/openai-live.md).
