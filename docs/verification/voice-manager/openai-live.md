# Voice manager audit and OpenAI speech verification

2026-11-17. The audit covered the seven voice-manager commits through `1aff7fae` and the later local controller and proof-retirement work recovered from the interrupted implementation. The expected load is one person using a machine with multiple agent sessions. The speech migration starts at the pushed checkpoint.

## Audit judgment

The session and action boundaries make sense. Swarm already owns the native CLI session, and its durable operation IDs let a caller reconcile uncertain outcomes. The voice provider can change independently. The previous branch had a qualified Gradium client and native proof tests but exposed only `swarm voice mcp`. Neither a user-facing conversation nor a locked-phone workflow had been demonstrated. [Previous provider qualification](provider-go-clients.md) · [Native review](native-review.md)

| Priority | What the audit found | Consequence and required correction |
|---|---|---|
| Release blocker | The pushed common proof registry caps records at 1,024, and Claude caps admissions at 128. | Long-running managers can exhaust capacity. Retire settled entries while retaining durable replay guards. |
| Release blocker | In the recovered retirement code, the custody worker releases waiters before publishing a failed storage write. A queued manager-custody error can also lose a race with owner disconnect. | A caller can briefly see a valid proof after storage failure, or receive ordinary unavailability instead of a quarantine signal. Publish failure before waking readers and preserve a received reply at connection close. |
| Release blocker | Claude's recovered queue accounts for text and projection plus a fixed estimate. The earlier reviewer measured 393,272 bytes accepted under a 256 KiB ceiling. | Account for the complete retained request before admission. |
| Release blocker | A missing Claude manager setup can start the ordinary broker. Retiring a completed interrupt can discard its accepted disposition. | Require the expected role at startup and keep an exact, bounded receipt for interrupt retries. |
| Upgrade blocker | The recovered controller rejects an existing v2 store after its v3 format bump. | Migrate or provide an explicit recovery path before using this code with existing state. |
| Integration gap | Both managers, device enrollment, SSH, mobile audio and the voice scheduler have separate outstanding gates. | Verify one real spoken request, native action and spoken receipt before release. Hardware and long-run trials are still needed. |

The recovery audit ran the complete native proof and Claude input race suites successfully. The controller race test `TestProtocolControllerPreservesTypedCustodyLostStatus` failed on the received-reply case above. The four earlier retirement regressions were independently reproduced in the prior session and located again in the recovered source. Those changes remain in a separate worktree. This speech PR contains none of the retirement or v3 storage changes.

## Model and protocol

OpenAI's model catalog and the project's authenticated model list both included `gpt-live-1`, `gpt-realtime-2.1`, `gpt-realtime-1.5`, and `gpt-transcribe`. The Live WebSocket confirmed `gpt-live-1`, client delegation, Marin and mono 24 kHz PCM during startup. Captions arrive as fragments. Explicitly finalized inputs use one transcription request. The CLI runs no native actions from either stream. [GPT-Live-1](https://developers.openai.com/api/docs/models/gpt-live-1) · [Live WebSocket](https://developers.openai.com/api/reference/resources/live/primary-websocket) · [GPT-Transcribe](https://developers.openai.com/api/docs/models/gpt-transcribe)

The project key is stored through `swarm voice setup`. The key was absent from argv, logs, and these artifacts. Production requests go only to fixed OpenAI URLs, with redirects disabled. The application does not request a stored Live recording. The project's OpenAI data controls still apply.

## Authenticated observations

The production CLI sent locally synthesized English speech and returned audio over the same connection. Each result below comes from a second transcription request on the returned PCM. Timing is the amount of returned PCM, including silence. It is not interaction latency.

| Input as transcribed | Reply as transcribed | Returned PCM | Peak amplitude |
|---|---|---:|---:|
| Hello, please say the words blue apple. | Splue apple. | 530,880 bytes (11.06 s) | 13,184 |
| Please repeat the words purple umbrella. | Purple umbrella. | 407,040 bytes (8.48 s) | 10,048 |

Both connections closed successfully. The first discrepancy could originate in synthesis, transcription, or both. These samples verify authenticated audio in both directions and illustrate why written manager receipts must remain authoritative. Neither test used a microphone or played audio to a physical speaker.

To repeat the billed second check on macOS with a configured Swarm state directory and FFmpeg installed, run the following from the repository root. It uses a fixed synthetic phrase and temporary audio files. [Spike source](../../../scripts/verification/openai_live_voice.py)

```sh
go build -o /tmp/swarm-voice-check ./cmd/swarm
python3 scripts/verification/openai_live_voice.py /tmp/swarm-voice-check
```
