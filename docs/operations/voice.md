# Swarm voice manager

The manager runs on the machine that owns your Swarm sessions. Choose Codex or Claude using its native login. The desktop panel accepts typed requests and push-to-talk audio. The controller records each request before dispatch, and the panel shows the verified publications tied to its native turn. GPT-Transcribe finalizes spoken input. GPT-Live provides optional speech output. [Implementation plan](../specifications/voice-manager-plan.md) · [Integration evidence](../verification/voice-manager/controller-integration.md)

The manager's **Read aloud** action requests a bounded preview of one selected, retained publication after its native manager turn completes. It uses the place's OpenAI key and sends that publication's canonical text as GPT-Live commentary. The preview is limited to six seconds of audio and can omit or paraphrase text. The canonical text remains visible. Playback requires an explicit request each time, including for a publication from a turn whose speech was stopped. Cancel stops the request and clears local audio. Automatic speech when a manager publishes remains a separate integration gate.

## Start a local manager

From a workspace you intend the selected native manager to operate in, configure the manager and its per-user service.

```sh
swarm voice setup --manager codex --dir "$PWD"
swarm voice start
swarm voice status
swarm voice
```

Choose `--manager claude` for Claude Code. Text works without an OpenAI project key. Configure speech separately with `swarm voice setup` when you want push-to-talk or Read aloud. The local panel starts on an ephemeral loopback port. `swarm voice panel --print-url` prints a private URL for manual opening. Its fragment authorizes this panel and should stay with your browser.

`swarm voice serve` runs the controller in the foreground. Run the first `swarm voice start` from the owner’s shell so it can initialize the native supervisor with the owner’s environment. An installed service can attach to an already running supervisor or start one with its saved environment. `swarm voice stop` stops the installed voice unit. Existing native sessions and workers retain their shim lifetime.

To select a different manager or workspace, stop the service and run `swarm voice switch --from <conversation-id> --manager claude` (or `codex`). Add `--dir <workspace>` to change the workspace. Use the exact conversation ID from the setup result or manager status. If the command reports unresolved native effects, restart the current service and reconcile them before switching. Start the service after the switch to launch the new conversation. Previously selected model settings are cleared, and devices must enroll again. Prior native sessions, workers and ledger records remain available in their original scopes. The ordinary voice receipt command targets the newly selected conversation. Repeating the original setup preserves its conversation identity.

## Submit, reconcile and read a reply

```sh
printf '%s' 'List the work in progress and publish a short update.' | swarm voice text --id daily-review
swarm voice receipt --id daily-review
swarm voice publications --id daily-review
```

Use a fresh ID for a new request. Repeat the original ID and exact text after a lost response. A completed admission records the end of the native turn. Published phrases are the canonical manager reply. If there are none, inspect the native session for authentication, permissions or model errors.

`swarm voice ptt --id ID` reads one utterance of mono 24 kHz PCM16LE from stdin and submits the final transcript to the manager. It reserves the ID before capture and pins the submitted audio digest before sending it to OpenAI. Reusing the same ID with different audio fails. If a paid request may have been sent but its result was lost, the service reports uncertainty. `swarm voice read --id ID --publication PUBLICATION_ID` returns a bounded PCM preview of a verified phrase.

Use `swarm voice question` to inspect pending manager clarifications. `voice answer` accepts a reply on stdin and requires the displayed request ID, hash, stable answer ID, manager instance and generation. `voice resume`, `voice ack`, and `voice replace` act only against the expected manager binding. Check each command's `--help` for its coordinates. `voice replacement` reads the durable replacement receipt. Replacement requires positive evidence the previous session has exited. Native permission prompts retain their own provider workflow.

To interrupt the manager, save `swarm voice status` while the intended turn is active. Inspect its session, input and turn. Pass that same JSON to `swarm voice interrupt-manager` on stdin and use `swarm voice interrupt-status` with the same file to reconcile the outcome. A native acknowledgement can be pending until a terminal observation arrives. Stopping local audio has a different effect and can be done immediately with the playback controls.

Only one microphone capture can be active on a place. Other clients may continue sending text and reading their own receipts. On the host, `swarm voice capture` shows the current source, lease ID and expiry. Use `swarm voice capture --end LEASE_ID` to end that exact capture, or `swarm voice capture --take LEASE_ID --id NEW_ID` followed by `swarm voice ptt --id NEW_ID` with PCM on stdin to take over locally. A superseded recording cannot submit a new manager turn. An abandoned lease expires after three minutes. Keep the original request ID to look up an already admitted recording.

Signed device status requests resume from a cursor scoped to the grant. The reply includes the current manager generation, pause and custody state, microphone availability, and that device’s latest admission sequence and phase. A service restart or expired history returns a fresh snapshot. Use the original request ID to retrieve a specific receipt.

## Connect over SSH

On the manager machine, repeat its original setup command with `--device-listen 127.0.0.1:0`, then restart the voice service to load the new setting. `swarm voice device-endpoint` reports the bound loopback address. Remote access is opt-in. On your desktop, use an OpenSSH alias whose host key you have verified.

```sh
swarm voice --host my-workstation --enroll
swarm voice --host my-workstation
```

The first command enrolls this desktop and opens the local browser. Subsequent launches reuse its private grant. The remote CLI must be available to the SSH session. The client supports verified direct and jump-host routes. It checks the selected destination before reusing a grant. Device requests are signed and scoped to their own receipt and publication history. Read aloud is also available over SSH for a publication belonging to the enrolled device. Revocation cancels any provider request that is still generating the preview.

For phones, use a fixed nonzero loopback port such as `127.0.0.1:32100`. The phone retains the pinned profile between connections. See the [Android setup guide](../../android/voice-device.md). For a phone with its own SSH key and device signing key, install its SSH public key through a trusted terminal. On the manager machine run `swarm voice enroll --device-key PUBLIC_KEY` with the phone's base64url Ed25519 **device** public key. The returned invitation expires after ten minutes and can be consumed once. `swarm voice devices` lists retained device IDs, public signing-key fingerprints, and grant status. Use it to identify and revoke a lost phone. A device can reconnect with its grant until `swarm voice revoke --id DEVICE_ID`. Revocation blocks further voice operations. An SSH account key installed separately must be managed through the account's own SSH controls.

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
